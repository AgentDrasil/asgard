package fakebash

import (
	"bytes"
	"context"
	"fmt"
	"os"

	"github.com/rs/zerolog/log"
)

const (
	MaxBufferedOutput = 16 * 1024 * 1024 // 16MB overflow threshold
	oneMB             = 1024 * 1024      // 1MB truncate slice
)

// Pipeline coordinates command output storage, classification, and summarization.
type Pipeline struct {
	storage    Storage
	evaluator  Evaluator
	summarizer Summarizer
}

// NewPipeline creates a new Pipeline.
func NewPipeline(storage Storage, evaluator Evaluator, summarizer Summarizer) *Pipeline {
	return &Pipeline{
		storage:    storage,
		evaluator:  evaluator,
		summarizer: summarizer,
	}
}

// ShouldShortCircuit determines whether the command output qualifies for immediate
// fast-path pass-through without any compression or evaluation.
// Both successful and failed commands (any exitCode) short-circuit if totalBytes < 256 and lineCount <= 5.
func ShouldShortCircuit(totalBytes int64, lineCount int, exitCode int) bool {
	return totalBytes < 256 && lineCount <= 5
}

// formatBytes returns a human-readable byte string such as "420B", "15KB", "2MB".
func formatBytes(b int64) string {
	const unit = 1000
	if b < unit {
		return fmt.Sprintf("%dB", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	units := []string{"KB", "MB", "GB", "TB"}
	val := float64(b) / float64(div)
	if val == float64(int64(val)) {
		return fmt.Sprintf("%.0f%s", val, units[exp])
	}
	return fmt.Sprintf("%.1f%s", val, units[exp])
}

// FormatFooter formats the standard fakebash compressed output footer.
func FormatFooter(origBytes, newBytes int64, cmdID string) string {
	return fmt.Sprintf("[fakebash: output compressed from %s -> %s. To view raw output, run: show-output %s (or 'show-output' for the latest)]",
		formatBytes(origBytes), formatBytes(newBytes), cmdID)
}

// FormatDropOnSuccess formats the summary prompt and footer for commands whose verbose output was dropped on success.
func FormatDropOnSuccess(origBytes int64, cmdID string) string {
	lead := "[fakebash: command succeeded with exit code 0. Verbose output truncated by sandbox]"
	footer := FormatFooter(origBytes, int64(len(lead)), cmdID)
	return lead + "\n" + footer
}

// FormatOverflowFooter formats the footer when output exceeds MaxBufferedOutput.
func FormatOverflowFooter(origBytes, truncatedBytes int64, cmdID string) string {
	return fmt.Sprintf("[fakebash: output too large (%s > 16MB), truncated to %s. To view full raw output, run: show-output %s (or 'show-output' for the latest)]",
		formatBytes(origBytes), formatBytes(truncatedBytes), cmdID)
}

// Process coordinates the compression pipeline:
// 1. Bypass check (ASGARD_BASH_COMPACT=0 or ASGARD_BASH_COMPACT_RAW=1)
// 2. Fast-path check (< 256B and <= 5 lines)
// 3. Overflow check (> 16MB -> truncate first 1MB of payload + footer)
// 4. Level 1 Jev evaluation
// 5. Level 2 Summarization (drop_on_success, extract_failure, summarize)
func (p *Pipeline) Process(ctx context.Context, cmd string, cmdID string, totalBytes int64, lineCount int, exitCode int) (string, error) {
	// 1. Bypass check
	if os.Getenv("ASGARD_BASH_COMPACT") == "0" || os.Getenv("ASGARD_BASH_COMPACT_RAW") == "1" {
		raw, err := p.storage.ReadPayload(cmdID)
		if err != nil {
			return "", err
		}
		return string(raw), nil
	}

	// 2. Fast-path check
	if ShouldShortCircuit(totalBytes, lineCount, exitCode) {
		raw, err := p.storage.ReadPayload(cmdID)
		if err != nil {
			return "", err
		}
		return string(raw), nil
	}

	// 3. Overflow check (> 16MB)
	if totalBytes > MaxBufferedOutput {
		// Read raw chunk starting at offset 0 up to 1MB + 4KB to locate and strip header line
		chunk, err := p.storage.ReadRange(cmdID, 0, oneMB+4096)
		if err != nil {
			return "", err
		}
		if bytes.HasPrefix(chunk, []byte("# CMD: ")) {
			if idx := bytes.IndexByte(chunk, '\n'); idx != -1 {
				chunk = chunk[idx+1:]
			}
		}
		if len(chunk) > oneMB {
			chunk = chunk[:oneMB]
		}
		footer := FormatOverflowFooter(totalBytes, int64(len(chunk)), cmdID)
		return string(chunk) + "\n" + footer, nil
	}

	// 4. Level 1 Jev decision
	strategy := StrategyKeepRaw
	if p.evaluator != nil {
		var err error
		strategy, err = p.evaluator.Classify(ctx, cmd, exitCode, cmdID)
		if err != nil {
			log.Debug().Err(err).Msg("evaluator error; falling back to keep_raw")
			strategy = StrategyKeepRaw
		}
	}

	if strategy == StrategyKeepRaw {
		raw, err := p.storage.ReadPayload(cmdID)
		if err != nil {
			return "", err
		}
		return string(raw), nil
	}

	// 5. Level 2 Processing
	if strategy == StrategyDropOnSuccess {
		return FormatDropOnSuccess(totalBytes, cmdID), nil
	}

	// extract_failure or summarize
	if p.summarizer != nil {
		summary, err := p.summarizer.Summarize(ctx, cmd, exitCode, strategy, cmdID, totalBytes)
		if err == nil && summary != "" {
			raw, rErr := p.storage.ReadPayload(cmdID)
			if rErr == nil && summary != string(raw) {
				footer := FormatFooter(totalBytes, int64(len(summary)), cmdID)
				return summary + "\n" + footer, nil
			}
		}
	}

	// Fallback to raw output
	raw, err := p.storage.ReadPayload(cmdID)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}
