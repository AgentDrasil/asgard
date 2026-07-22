package agy

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/lib/agentwrapper/types"

	"github.com/AgentDrasil/asgard/lib/term"
)

// Prompt launches agy in a headless terminal with --conversation=<sessionID>
// and --dangerously-skip-permissions, sends the given prompt text, waits for
// the agent to return to idle (with a double-check), then reads the transcript
// and returns structured metadata.
//
// The sequence is:
//  1. Open a headless PTY-backed terminal (220×50).
//  2. Launch `agy --conversation=<sessionID> --dangerously-skip-permissions`.
//  3. Wait until the statusbar reports idle (idle #1 – startup idle).
//  4. Send the prompt text followed by Enter.
//  5. Wait until idle again (idle #2 – post-response idle).
//  6. Sleep 200 ms, then wait until idle once more (idle #3 – double-check).
//  7. Exit agy cleanly (Esc, Ctrl-D, Ctrl-D).
//  8. Read the last line of ~/.gemini/antigravity-cli/brain/<sessionID>/.system_generated/logs/transcript.jsonl.
//  9. Parse the statusline for token metadata and return a PromptResult.
func Prompt(ctx context.Context, prompt string, opts types.PromptOptions) (*types.PromptResult, error) {
	runDir := opts.Dir
	if runDir == "" {
		var err error
		runDir, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("getting current working directory: %w", err)
		}
	}
	if err := ensureWorkspaceTrusted(runDir); err != nil {
		return nil, fmt.Errorf("ensuring workspace is trusted: %w", err)
	}

	sessionID := opts.SessionID
	isNewSession := sessionID == ""

	t := term.NewTerm(termCols, termRows)
	defer t.Close()

	argv := []string{"agy"}
	if !isNewSession {
		argv = append(argv, "--conversation="+sessionID)
	}
	if opts.Model != "" {
		argv = append(argv, "--model", opts.Model)
	}
	argv = append(argv, "--dangerously-skip-permissions")

	log.Debug().Interface("argv", argv).Msg("agy/prompt: starting")

	awSessionID := opts.SessionID
	if awSessionID == "" {
		awSessionID = uuid.NewString()
	}

	// ── resume diff: count existing lines before launching ────────────────────
	// We need the transcript path early; we use awSessionID as a best-guess
	// (it is the same as sessionID when resuming).
	startOffset := 0
	if !isNewSession {
		startOffset = countTranscriptLines(awSessionID)
		log.Debug().Int("start_offset", startOffset).Msg("agy/prompt: transcript resume offset")
	}

	log.Debug().Str("session_id", awSessionID).Msg("agy/prompt: launching agy")
	done, err := t.RunCommandInDir(context.Background(), argv, runDir, []string{"AW_SESSION_ID=" + awSessionID})
	if err != nil {
		return nil, fmt.Errorf("launching agy: %w", err)
	}

	handleErr := func(err error) error {
		if ctx.Err() != nil {
			GratefulShutdown(t, done)
			return ctx.Err()
		}
		return err
	}

	// ── idle #1: wait for startup idle ────────────────────────────────────────
	log.Debug().Msg("agy/prompt: waiting for startup idle (#1)")
	if timedOut, err := pollUntilIdle(ctx, awSessionID, done, opts.StartupDelayOrDefault()); err != nil {
		return nil, handleErr(err)
	} else if timedOut {
		log.Warn().Msg("agy/prompt: startup idle (#1) timed out")
	} else {
		log.Debug().Msg("agy/prompt: startup idle reached (#1)")
	}

	// ── send the prompt ───────────────────────────────────────────────────────
	if err := t.SendString(prompt); err != nil {
		return nil, handleErr(fmt.Errorf("sending prompt: %w", err))
	}
	if err := t.SendKeys(term.KeyEnter); err != nil {
		return nil, handleErr(fmt.Errorf("sending Enter: %w", err))
	}
	log.Debug().Msg("agy/prompt: prompt sent")

	// ── background transcript watcher ─────────────────────────────────────────
	watcherDone := make(chan struct{})
	if opts.ReportCallback != nil {
		go watchTranscript(ctx, awSessionID, opts.SessionID, startOffset, opts.ReportCallback, watcherDone)
	} else {
		close(watcherDone)
	}

	// ── idle #2: wait for the agent to finish responding ──────────────────────
	log.Debug().Msg("agy/prompt: waiting for post-response idle (#2)")
	if timedOut, err := pollUntilIdle(ctx, awSessionID, done, opts.ResponseDelayOrDefault()); err != nil {
		if opts.ReportCallback != nil {
			close(watcherDone)
		}
		return nil, handleErr(err)
	} else if timedOut {
		log.Warn().Msg("agy/prompt: post-response idle (#2) timed out")
	} else {
		log.Debug().Msg("agy/prompt: post-response idle reached (#2)")
	}

	// ── idle #3: sleep 200 ms, then double-check idle ────────────────────────
	select {
	case <-time.After(200 * time.Millisecond):
	case <-ctx.Done():
		if opts.ReportCallback != nil {
			close(watcherDone)
		}
		return nil, handleErr(ctx.Err())
	}

	log.Debug().Msg("agy/prompt: waiting for double-check idle (#3)")
	if timedOut, err := pollUntilIdle(ctx, awSessionID, done, opts.ResponseDelayOrDefault()); err != nil {
		if opts.ReportCallback != nil {
			close(watcherDone)
		}
		return nil, handleErr(err)
	} else if timedOut {
		log.Warn().Msg("agy/prompt: double-check idle (#3) timed out")
	} else {
		log.Debug().Msg("agy/prompt: double-check idle reached (#3)")
	}

	// ── stop the watcher before exiting ──────────────────────────────────────
	if opts.ReportCallback != nil {
		close(watcherDone)
	}

	// ── exit agy cleanly ─────────────────────────────────────────────────────
	CleanExit(t, done)

	// ── parse statusline for token metadata ───────────────────────────────────
	parsedSessionID, inputTokens, maxTokens, remaining := parseStatusLineFromSession(awSessionID)
	if parsedSessionID != "" {
		sessionID = parsedSessionID
	}

	// ── read the last transcript line ─────────────────────────────────────────
	lastContent, err := readLastTranscriptContent(sessionID)
	if err != nil {
		// Non-fatal: log and continue with an empty string.
		log.Warn().Err(err).Str("session_id", sessionID).Msg("agy/prompt: could not read transcript")
		lastContent = ""
	}

	return &types.PromptResult{
		SessionID:   sessionID,
		InputTokens: inputTokens,
		MaxTokens:   maxTokens,
		Remaining:   remaining,
		LastContent: lastContent,
	}, nil
}

// fullTranscriptEntry is a richer transcript line shape used for classification.
type fullTranscriptEntry struct {
	StepIndex int              `json:"step_index"`
	Source    string           `json:"source"`
	Type      string           `json:"type"`
	Content   string           `json:"content"`
	ToolCalls []map[string]any `json:"tool_calls"`
}

// classifyEntry returns the entryType string for a transcript entry.
func classifyEntry(e *fullTranscriptEntry) string {
	switch e.Type {
	case "PLANNER_RESPONSE":
		if len(e.ToolCalls) > 0 {
			return "tool_call"
		}
		return "agent_response"
	case "TOOL_CALL", "VIEW_FILE", "RUN_COMMAND":
		return "tool_call"
	default:
		return "other"
	}
}

// transcriptPath returns the path to the transcript.jsonl file for the given sessionID.
func transcriptPath(sessionID string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("determining home directory: %w", err)
	}
	return filepath.Join(home, ".gemini", "antigravity-cli", "brain", sessionID,
		".system_generated", "logs", "transcript.jsonl"), nil
}

// countTranscriptLines returns the number of non-empty lines currently in the
// transcript file. Used to establish a resume offset before launching agy.
func countTranscriptLines(sessionID string) int {
	path, err := transcriptPath(sessionID)
	if err != nil {
		return 0
	}
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer func() { _ = f.Close() }()

	count := 0
	scanner := bufio.NewScanner(f)
	buf := make([]byte, 4*1024*1024)
	scanner.Buffer(buf, len(buf))
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) != "" {
			count++
		}
	}
	return count
}

// watchTranscript polls the transcript file every 500 ms and calls report for
// each new non-empty line found after startOffset. It stops when done is closed.
func watchTranscript(ctx context.Context, awSessionID string, initialSessionID string, startOffset int, report types.ReportFunc, done <-chan struct{}) {
	realSessionID := initialSessionID

	currentLine := 0
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-done:
			// One final drain before returning.
			if realSessionID == "" {
				realSessionID, _, _, _ = parseStatusLineFromSession(awSessionID)
			}
			if realSessionID != "" {
				if path, err := transcriptPath(realSessionID); err == nil {
					readNewLines(path, &currentLine, startOffset, report)
				}
			}
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			if realSessionID == "" {
				realSessionID, _, _, _ = parseStatusLineFromSession(awSessionID)
			}
			if realSessionID != "" {
				if path, err := transcriptPath(realSessionID); err == nil {
					readNewLines(path, &currentLine, startOffset, report)
				}
			}
		}
	}
}

// readNewLines reads all lines in path after the resume offset + currentLine
// and advances currentLine. Each parsed entry is classified and reported.
func readNewLines(path string, currentLine *int, startOffset int, report types.ReportFunc) {
	f, err := os.Open(path)
	if err != nil {
		return // file may not exist yet
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	buf := make([]byte, 4*1024*1024)
	scanner.Buffer(buf, len(buf))

	lineIdx := 0
	for scanner.Scan() {
		raw := scanner.Text()
		if strings.TrimSpace(raw) == "" {
			lineIdx++
			continue
		}
		// Skip lines we already know about (pre-existing + already reported)
		if lineIdx < startOffset+*currentLine {
			lineIdx++
			continue
		}

		var entry fullTranscriptEntry
		if err := json.Unmarshal([]byte(raw), &entry); err != nil {
			lineIdx++
			*currentLine++
			continue
		}

		// Skip checkpoints — they carry no useful content.
		if entry.Type == "CHECKPOINT" {
			lineIdx++
			*currentLine++
			continue
		}

		entryType := classifyEntry(&entry)
		var metadata map[string]any
		if entry.Type != "" {
			metadata = map[string]any{"raw_type": entry.Type}
		}
		report(startOffset+*currentLine, entry.Source, entryType, entry.Content, metadata)

		lineIdx++
		*currentLine++
	}
}

// transcriptLine is the minimal shape we need from each JSONL line.
type transcriptLine struct {
	Type    string `json:"type"`
	Content string `json:"content"`
}

// readLastTranscriptContent reads the last line of
// ~/.gemini/antigravity-cli/brain/<sessionID>/.system_generated/logs/transcript.jsonl
// that is not of type CHECKPOINT, and returns its "content" field.
func readLastTranscriptContent(sessionID string) (string, error) {
	path, err := transcriptPath(sessionID)
	if err != nil {
		return "", fmt.Errorf("determining transcript path: %w", err)
	}

	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("opening transcript %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	var lastContent string
	scanner := bufio.NewScanner(f)
	// Use a large buffer for potentially long lines.
	buf := make([]byte, 4*1024*1024)
	scanner.Buffer(buf, len(buf))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		var entry transcriptLine
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			return "", fmt.Errorf("parsing transcript JSON: %w", err)
		}
		if entry.Type != "CHECKPOINT" {
			lastContent = entry.Content
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("reading transcript: %w", err)
	}
	return lastContent, nil
}

func ensureWorkspaceTrusted(dir string) error {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("resolving absolute path for %q: %w", dir, err)
	}
	absDir = filepath.Clean(absDir)

	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("determining home directory: %w", err)
	}
	settingsPath := filepath.Join(home, ".gemini", "antigravity-cli", "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return fmt.Errorf("reading settings file %s: %w", settingsPath, err)
	}

	var config struct {
		TrustedWorkspaces []string `json:"trustedWorkspaces"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return fmt.Errorf("parsing settings JSON: %w", err)
	}

	for _, ws := range config.TrustedWorkspaces {
		if filepath.Clean(ws) == absDir {
			return nil
		}
	}

	// Read settings as map to preserve other keys.
	var settingsMap map[string]any
	if err := json.Unmarshal(data, &settingsMap); err != nil {
		return fmt.Errorf("parsing settings JSON for update: %w", err)
	}

	var trustedWorkspaces []any
	if tw, ok := settingsMap["trustedWorkspaces"]; ok {
		if arr, ok := tw.([]any); ok {
			trustedWorkspaces = arr
		}
	}

	trustedWorkspaces = append(trustedWorkspaces, absDir)
	settingsMap["trustedWorkspaces"] = trustedWorkspaces

	newData, err := json.MarshalIndent(settingsMap, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling updated settings: %w", err)
	}

	log.Info().Str("path", absDir).Msg("Adding directory to trusted workspaces in settings.json")
	if err := os.WriteFile(settingsPath, newData, 0644); err != nil {
		return fmt.Errorf("writing updated settings file: %w", err)
	}

	return nil
}
