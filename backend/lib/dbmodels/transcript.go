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

// AppendMessage appends or atomically replaces a message in messages.jsonl.
// If msg.ID is empty, it appends the message as a single line (O(1)) and returns appended = true.
// If msg.ID is non-empty:
//   - If a message with the same ID already exists, it is replaced in-place atomically.
//     If the existing message had Replied == true and the incoming message has !msg.Replied,
//     the Replied and ReplyText states are inherited from the existing message.
//     Returns appended = false, err = nil.
//   - If no message with the same ID exists, it is appended to the end of the file.
//     Returns appended = true, err = nil.
func AppendMessage(sessionDir string, msg ChatMessage) (bool, error) {
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		return false, fmt.Errorf("create session dir: %w", err)
	}

	path := TranscriptFilePath(sessionDir)

	// Fast path: empty ID always appends directly
	if msg.ID == "" {
		data, err := json.Marshal(msg)
		if err != nil {
			return false, fmt.Errorf("marshal message: %w", err)
		}
		data = append(data, '\n')

		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			return false, fmt.Errorf("open transcript for append: %w", err)
		}
		defer f.Close()

		if _, err := f.Write(data); err != nil {
			return false, fmt.Errorf("write message line: %w", err)
		}
		if err := f.Sync(); err != nil {
			return false, fmt.Errorf("sync transcript file: %w", err)
		}
		return true, nil
	}

	// Non-empty ID: check if transcript file exists
	existingBytes, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// File does not exist yet, write as first line
			data, err := json.Marshal(msg)
			if err != nil {
				return false, fmt.Errorf("marshal message: %w", err)
			}
			data = append(data, '\n')
			if err := writeAtomic(sessionDir, path, data); err != nil {
				return false, err
			}
			return true, nil
		}
		return false, fmt.Errorf("read transcript: %w", err)
	}

	// Scan lines to see if ID exists
	scanner := bufio.NewScanner(bytes.NewReader(existingBytes))
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, 10*1024*1024)

	var lines []ChatMessage
	foundIdx := -1

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
		if m.ID == msg.ID && foundIdx == -1 {
			foundIdx = len(lines)
		}
		lines = append(lines, m)
	}

	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		log.Warn().Err(err).Str("path", path).Msg("scanner error reading transcript")
	}

	if foundIdx != -1 {
		// Replace in-place
		oldMsg := lines[foundIdx]
		if oldMsg.Replied && !msg.Replied {
			msg.Replied = true
			msg.ReplyText = oldMsg.ReplyText
		}
		lines[foundIdx] = msg

		if err := writeAllMessagesAtomic(sessionDir, path, lines); err != nil {
			return false, err
		}
		return false, nil
	}

	// Not found -> append
	data, err := json.Marshal(msg)
	if err != nil {
		return false, fmt.Errorf("marshal message: %w", err)
	}
	data = append(data, '\n')

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return false, fmt.Errorf("open transcript for append: %w", err)
	}
	defer f.Close()

	if _, err := f.Write(data); err != nil {
		return false, fmt.Errorf("write message line: %w", err)
	}
	if err := f.Sync(); err != nil {
		return false, fmt.Errorf("sync transcript file: %w", err)
	}
	return true, nil
}

// ReadMessages reads and parses all valid JSON lines from messages.jsonl.
// Torn or unparseable lines (e.g. from crash) are skipped with a warning log,
// and all preceding and succeeding valid records are preserved.
// If the file does not exist, an empty Messages slice is returned with nil error.
func ReadMessages(sessionDir string) (Messages, error) {
	path := TranscriptFilePath(sessionDir)
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Messages{}, nil
		}
		return nil, fmt.Errorf("open transcript: %w", err)
	}
	defer f.Close()

	var messages Messages
	scanner := bufio.NewScanner(f)
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, 10*1024*1024)

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

	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		log.Warn().Err(err).Str("path", path).Msg("scanner error reading transcript")
	}

	if messages == nil {
		messages = Messages{}
	}
	return messages, nil
}

// MarkAskUserReplied marks an ask_user message as replied in messages.jsonl.
// Follows three-level fallback matching:
// 1. If messageID != "", exact match by ID (role/replied doesn't restrict match);
// 2. If messageID == "", match the first message with Role == "ask_user" and !Replied;
// 3. If neither matched, fallback to match the last message with Role == "ask_user" and !Replied.
func MarkAskUserReplied(sessionDir string, messageID string, replyText string) (*ChatMessage, bool, error) {
	path := TranscriptFilePath(sessionDir)
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("open transcript: %w", err)
	}

	var messages []ChatMessage
	scanner := bufio.NewScanner(f)
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, 10*1024*1024)

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
	_ = f.Close()

	foundIdx := -1
	// Priority 1 & 2
	for i, m := range messages {
		if (messageID != "" && m.ID == messageID) || (messageID == "" && m.Role == "ask_user" && !m.Replied) {
			foundIdx = i
			break
		}
	}

	// Priority 3 (fallback)
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

		if err := writeAllMessagesAtomic(sessionDir, path, messages); err != nil {
			return nil, false, err
		}
	}

	hasUnreplied := false
	for _, m := range messages {
		if m.Role == "ask_user" && !m.Replied {
			hasUnreplied = true
			break
		}
	}

	return updatedMsg, hasUnreplied, nil
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

func writeAtomic(dir string, targetPath string, data []byte) error {
	tmpFile, err := os.CreateTemp(dir, "transcript-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp transcript file: %w", err)
	}
	tmpName := tmpFile.Name()
	defer func() {
		_ = tmpFile.Close()
		_ = os.Remove(tmpName)
	}()

	if _, err := tmpFile.Write(data); err != nil {
		return fmt.Errorf("write temp transcript file: %w", err)
	}
	if err := tmpFile.Sync(); err != nil {
		return fmt.Errorf("sync temp transcript file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("close temp transcript file: %w", err)
	}

	if err := os.Rename(tmpName, targetPath); err != nil {
		return fmt.Errorf("rename temp transcript file: %w", err)
	}
	return nil
}

func writeAllMessagesAtomic(dir string, targetPath string, messages []ChatMessage) error {
	var buf bytes.Buffer
	for _, m := range messages {
		data, err := json.Marshal(m)
		if err != nil {
			return fmt.Errorf("marshal message: %w", err)
		}
		buf.Write(data)
		buf.WriteByte('\n')
	}
	return writeAtomic(dir, targetPath, buf.Bytes())
}
