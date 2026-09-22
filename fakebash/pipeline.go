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

	// ShortCircuitMaxBytes is the output size below which the raw output is always
	// returned verbatim, independent of exit code and line count.
	ShortCircuitMaxBytes = 256
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
// Tiny output bypasses evaluation entirely, whatever the exit code: when the result
// is this small, the raw bytes carry more information than any summary of them and
// spending an LLM call to restate them is pure cost.
func ShouldShortCircuit(totalBytes int64) bool {
	return totalBytes < ShortCircuitMaxBytes
}

// FormatFooter formats the standard fakebash compressed output footer.
func FormatFooter(cmdID string) string {
	return fmt.Sprintf("[fakebash: output compressed. To view raw output, run: show-output %s]", cmdID)
}

// FormatDropOnSuccess formats the summary prompt and footer for commands whose verbose output was dropped on success.
func FormatDropOnSuccess(cmdID string) string {
	lead := "[fakebash: command succeeded with exit code 0. Verbose output truncated]"
	return lead + "\n" + FormatFooter(cmdID)
}

// FormatOverflowFooter formats the footer when output exceeds MaxBufferedOutput.
func FormatOverflowFooter(cmdID string) string {
	return fmt.Sprintf("[fakebash: output too large, truncated. To view full raw output, run: show-output %s]", cmdID)
}

// Outcome is the result of running a command's output through the compression
// pipeline: the text handed back to the agent, plus the model usage incurred
// while deciding what to do with it.
type Outcome struct {
	Output  string
	Jev     ModelUsage
	Compass ModelUsage
	// Compressed reports that the agent received less than the full raw output,
	// whether by summarization, drop-on-success, or truncation. It is false when
	// the raw payload was returned intact, so it is exactly the set of results the
	// agent might want to pull back with show-output.
	Compressed bool
	// Truncated narrows Compressed to the overflow path specifically.
	Truncated bool
}

// Process coordinates the compression pipeline:
// 1. Bypass check (ASGARD_BASH_COMPACT=0 or ASGARD_BASH_COMPACT_RAW=1)
// 2. Fast-path check (< ShortCircuitMaxBytes)
// 3. Overflow check (> 16MB -> truncate first 1MB of payload + footer)
// 4. Level 1 Jev evaluation
// 5. Level 2 Summarization (drop_on_success, extract_failure, summarize)
func (p *Pipeline) Process(ctx context.Context, cmd string, cmdID string, totalBytes int64, exitCode int) (Outcome, error) {
	// 1. Bypass check
	if os.Getenv("ASGARD_BASH_COMPACT") == "0" || os.Getenv("ASGARD_BASH_COMPACT_RAW") == "1" {
		raw, err := p.storage.ReadPayload(cmdID)
		if err != nil {
			return Outcome{}, err
		}
		return Outcome{Output: string(raw)}, nil
	}

	// 2. Fast-path check
	if ShouldShortCircuit(totalBytes) {
		raw, err := p.storage.ReadPayload(cmdID)
		if err != nil {
			return Outcome{}, err
		}
		return Outcome{Output: string(raw)}, nil
	}

	// 3. Overflow check (> 16MB)
	if totalBytes > MaxBufferedOutput {
		// Read raw chunk starting at offset 0 up to 1MB + 4KB to locate and strip header line
		chunk, err := p.storage.ReadRange(cmdID, 0, oneMB+4096)
		if err != nil {
			return Outcome{}, err
		}
		if bytes.HasPrefix(chunk, []byte("# CMD: ")) {
			if idx := bytes.IndexByte(chunk, '\n'); idx != -1 {
				chunk = chunk[idx+1:]
			}
		}
		if len(chunk) > oneMB {
			chunk = chunk[:oneMB]
		}
		footer := FormatOverflowFooter(cmdID)
		return Outcome{Output: string(chunk) + "\n" + footer, Compressed: true, Truncated: true}, nil
	}

	// 4. Level 1 Jev decision
	strategy := StrategyKeepRaw
	var jevUsage ModelUsage
	if p.evaluator != nil {
		var err error
		strategy, jevUsage, err = p.evaluator.Classify(ctx, cmd, exitCode, cmdID)
		if err != nil {
			log.Debug().Err(err).Msg("evaluator error; falling back to keep_raw")
			strategy = StrategyKeepRaw
		}
	}

	if strategy == StrategyKeepRaw {
		raw, err := p.storage.ReadPayload(cmdID)
		if err != nil {
			return Outcome{}, err
		}
		return Outcome{Output: string(raw), Jev: jevUsage}, nil
	}

	// 5. Level 2 Processing
	if strategy == StrategyDropOnSuccess {
		return Outcome{Output: FormatDropOnSuccess(cmdID), Jev: jevUsage, Compressed: true}, nil
	}

	// extract_failure or summarize
	var compassUsage ModelUsage
	if p.summarizer != nil {
		var summary string
		var err error
		summary, compassUsage, err = p.summarizer.Summarize(ctx, cmd, exitCode, strategy, cmdID, totalBytes)
		if err == nil && summary != "" {
			raw, rErr := p.storage.ReadPayload(cmdID)
			if rErr == nil && summary != string(raw) {
				footer := FormatFooter(cmdID)
				return Outcome{Output: summary + "\n" + footer, Jev: jevUsage, Compass: compassUsage, Compressed: true}, nil
			}
		}
	}

	// Fallback to raw output. The compass call still happened and was billed, so
	// its usage is carried through even though its summary was discarded.
	raw, err := p.storage.ReadPayload(cmdID)
	if err != nil {
		return Outcome{}, err
	}
	return Outcome{Output: string(raw), Jev: jevUsage, Compass: compassUsage}, nil
}
