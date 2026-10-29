package dbmodels

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/rs/zerolog/log"
)

const transcriptFileName = "messages.jsonl"

// TranscriptFilePath returns the path to messages.jsonl under the given session directory.
func TranscriptFilePath(sessionDir string) string {
	return filepath.Join(sessionDir, transcriptFileName)
}

// AppendMessage appends or atomically replaces a message in messages.jsonl
// under a local session directory (tests and runner-less development; the
// backend reaches a runner-bound chat's transcript through sessionStore).
func AppendMessage(sessionDir string, msg ChatMessage) (bool, error) {
	appended, _, err := appendMessage(dirStore{sessionDir}, msg)
	return appended, err
}

// ReadMessages reads all valid messages from a local messages.jsonl.
func ReadMessages(sessionDir string) (Messages, error) {
	return readMessages(dirStore{sessionDir})
}

// MarkAskUserReplied marks an ask_user message as replied in a local messages.jsonl.
func MarkAskUserReplied(sessionDir string, messageID string, replyText string) (*ChatMessage, bool, error) {
	return markAskUserReplied(dirStore{sessionDir}, messageID, replyText)
}

func transcriptPath(s sessionStore) string { return storePath(s, transcriptFileName) }

func hasUnrepliedAskUser(msgs []ChatMessage) bool {
	for _, m := range msgs {
		if m.Role == "ask_user" && !m.Replied {
			return true
		}
	}
	return false
}

// parseTranscript parses the valid JSON lines of a transcript; torn or
// unparseable lines (e.g. from a crash) are skipped with a warning.
func parseTranscript(data []byte, path string) []ChatMessage {
	msgs, err := scanTranscript(data, path)
	if err != nil && !errors.Is(err, io.EOF) {
		log.Warn().Err(err).Str("path", path).Msg("scanner error reading transcript")
	}
	return msgs
}

// scanTranscript is parseTranscript that also reports the scanner error.
func scanTranscript(data []byte, path string) ([]ChatMessage, error) {
	var messages []ChatMessage
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64*1024), 10*1024*1024)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var m ChatMessage
		if err := json.Unmarshal(line, &m); err != nil {
			log.Warn().Err(err).Int("line", lineNum).Str("path", path).Msg("skipping torn/corrupted jsonl line")
			continue
		}
		messages = append(messages, m)
	}
	return messages, scanner.Err()
}

func marshalLine(msg ChatMessage) ([]byte, error) {
	data, err := json.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("marshal message: %w", err)
	}
	return append(data, '\n'), nil
}

// appendMessage appends or atomically replaces a message in the transcript.
// If msg.ID is empty, it appends the message as a single line (O(1), no read
// of the existing transcript) and returns appended = true. If msg.ID is
// non-empty:
//   - If a message with the same ID already exists, it is replaced in-place atomically.
//     If the existing message had Replied == true and the incoming message has !msg.Replied,
//     the Replied and ReplyText states are inherited from the existing message.
//     Returns appended = false.
//   - If no message with the same ID exists, it is appended to the end of the file.
//     Returns appended = true.
//
// The returned replyState is set when the transcript was read anyway.
func appendMessage(s sessionStore, msg ChatMessage) (appended bool, state replyState, err error) {
	path := transcriptPath(s)

	if msg.ID == "" {
		data, err := marshalLine(msg)
		if err != nil {
			return false, replyState{}, err
		}
		if err := s.Append(path, data); err != nil {
			return false, replyState{}, fmt.Errorf("append transcript: %w", err)
		}
		return true, replyState{}, nil
	}

	existing, err := s.Read(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return false, replyState{}, fmt.Errorf("read transcript: %w", err)
		}
		data, err := marshalLine(msg)
		if err != nil {
			return false, replyState{}, err
		}
		if err := s.WriteAtomic(path, data, false); err != nil {
			return false, replyState{}, fmt.Errorf("write transcript: %w", err)
		}
		return true, replyState{known: true, unreplied: hasUnrepliedAskUser([]ChatMessage{msg})}, nil
	}

	lines := parseTranscript(existing, path)
	foundIdx := -1
	for i, m := range lines {
		if m.ID == msg.ID {
			foundIdx = i
			break
		}
	}

	if foundIdx != -1 {
		oldMsg := lines[foundIdx]
		if oldMsg.Replied && !msg.Replied {
			msg.Replied = true
			msg.ReplyText = oldMsg.ReplyText
		}
		lines[foundIdx] = msg
		if err := writeAllMessages(s, lines); err != nil {
			return false, replyState{}, err
		}
		return false, replyState{known: true, unreplied: hasUnrepliedAskUser(lines)}, nil
	}

	data, err := marshalLine(msg)
	if err != nil {
		return false, replyState{}, err
	}
	if err := s.Append(path, data); err != nil {
		return false, replyState{}, fmt.Errorf("append transcript: %w", err)
	}
	return true, replyState{known: true, unreplied: hasUnrepliedAskUser(append(lines, msg))}, nil
}

// replyState is the unreplied-ask_user state appendMessage learned for free
// while it had the transcript in hand.
type replyState struct {
	known     bool
	unreplied bool
}

// readMessages reads and parses all valid JSON lines from the transcript.
// Torn or unparseable lines (e.g. from crash) are skipped with a warning log,
// and all preceding and succeeding valid records are preserved.
// If the file does not exist, an empty Messages slice is returned with nil error.
func readMessages(s sessionStore) (Messages, error) {
	path := transcriptPath(s)
	data, err := s.Read(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Messages{}, nil
		}
		return nil, fmt.Errorf("open transcript: %w", err)
	}
	messages := Messages(parseTranscript(data, path))
	if messages == nil {
		messages = Messages{}
	}
	return messages, nil
}

// markAskUserReplied marks an ask_user message as replied in the transcript.
// Follows three-level fallback matching:
// 1. If messageID != "", exact match by ID (role/replied doesn't restrict match);
// 2. If messageID == "", match the first message with Role == "ask_user" and !Replied;
// 3. If neither matched, fallback to match the last message with Role == "ask_user" and !Replied.
func markAskUserReplied(s sessionStore, messageID string, replyText string) (*ChatMessage, bool, error) {
	path := transcriptPath(s)
	data, err := s.Read(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("open transcript: %w", err)
	}
	messages, err := scanTranscript(data, path)
	if err != nil {
		return nil, false, fmt.Errorf("scan transcript: %w", err)
	}

	foundIdx := -1
	for i, m := range messages {
		if (messageID != "" && m.ID == messageID) || (messageID == "" && m.Role == "ask_user" && !m.Replied) {
			foundIdx = i
			break
		}
	}
	if foundIdx == -1 {
		for i := len(messages) - 1; i >= 0; i-- {
			if messages[i].Role == "ask_user" && !messages[i].Replied {
				foundIdx = i
				break
			}
		}
	}

	var updatedMsg *ChatMessage
	if foundIdx != -1 {
		messages[foundIdx].Replied = true
		messages[foundIdx].ReplyText = replyText
		msgCopy := messages[foundIdx]
		updatedMsg = &msgCopy
		if err := writeAllMessages(s, messages); err != nil {
			return nil, false, err
		}
	}
	return updatedMsg, hasUnrepliedAskUser(messages), nil
}

// TruncateSummary sanitizes and truncates message content for metadata summary (max maxLen characters).
func TruncateSummary(content string, maxLen int) string {
	if maxLen <= 0 {
		maxLen = 200
	}

	var b strings.Builder
	b.Grow(len(content))
	prevSpace := false

	for _, r := range content {
		if unicode.IsSpace(r) {
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
		} else {
			b.WriteRune(r)
			prevSpace = false
		}
	}

	trimmed := strings.TrimSpace(b.String())
	runes := []rune(trimmed)
	if len(runes) > maxLen {
		return string(runes[:maxLen]) + "..."
	}
	return trimmed
}

// writeAllMessages atomically replaces the whole transcript.
func writeAllMessages(s sessionStore, messages []ChatMessage) error {
	var buf bytes.Buffer
	for _, m := range messages {
		line, err := marshalLine(m)
		if err != nil {
			return err
		}
		buf.Write(line)
	}
	if err := s.WriteAtomic(transcriptPath(s), buf.Bytes(), false); err != nil {
		return fmt.Errorf("write transcript: %w", err)
	}
	return nil
}
