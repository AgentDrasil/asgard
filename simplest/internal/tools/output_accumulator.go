package tools

import (
	"bytes"
	"os"
	"strings"
	"sync"
)

// outputAccumulator is a bounded-memory io.Writer for combined command
// output (pi 6b18cdbac, fixed in f95306781). Instead of buffering the whole
// stream, it retains only:
//   - a rolling decoded tail of at most 2*maxBytes bytes, used to build the
//     displayed (truncated) output, and
//   - raw chunk copies only until truncation is first detected, at which
//     point they are spilled to a temp file and subsequent writes stream
//     straight into it.
//
// Global byte/line totals are tracked without retaining the full output, so
// a chatty build can no longer OOM before truncation. Temp-file failures are
// non-fatal: truncation still works and FullOutputPath stays empty.
type outputAccumulator struct {
	mu sync.Mutex

	maxLines int
	maxBytes int

	tail        []byte // rolling last <= 2*maxBytes bytes of output
	tailMidLine bool   // true when tail[0] is not at a line boundary

	totalBytes   int
	newlineCount int
	lastByte     byte
	sawByte      bool

	// currentLineBytes counts bytes written since the last newline, i.e.
	// the byte size of the in-progress (last) line.
	currentLineBytes int

	chunks [][]byte // raw copies held only until the spill file starts

	spillFile   *os.File
	spillPath   string
	spillFailed bool
}

func newOutputAccumulator(opts TruncationOptions) *outputAccumulator {
	maxLines := opts.MaxLines
	if maxLines == 0 {
		maxLines = DefaultMaxLines
	}
	maxBytes := opts.MaxBytes
	if maxBytes == 0 {
		maxBytes = DefaultMaxBytes
	}
	return &outputAccumulator{maxLines: maxLines, maxBytes: maxBytes}
}

// Write implements io.Writer. It is safe for concurrent use.
func (a *outputAccumulator) Write(p []byte) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	n := len(p)
	if n == 0 {
		return 0, nil
	}

	a.totalBytes += n
	a.newlineCount += bytes.Count(p, []byte{'\n'})
	a.lastByte = p[n-1]
	a.sawByte = true

	// Track the size of the current (last) line: bytes since the last
	// newline. The bytes are sanitized UTF-8, so byte length is correct.
	if idx := bytes.LastIndexByte(p, '\n'); idx >= 0 {
		a.currentLineBytes = n - idx - 1
	} else {
		a.currentLineBytes += n
	}

	// Keep raw chunks only until truncation is first detected.
	if a.spillFile == nil && !a.spillFailed {
		chunk := make([]byte, n)
		copy(chunk, p)
		a.chunks = append(a.chunks, chunk)
	}

	// Retain a rolling tail of at most 2*maxBytes bytes, without cutting a
	// UTF-8 rune in half.
	a.tail = append(a.tail, p...)
	if limit := 2 * a.maxBytes; len(a.tail) > limit {
		start := len(a.tail) - limit
		for start < len(a.tail) && a.tail[start]&0xC0 == 0x80 {
			start++ // continuation byte: advance past the split rune
		}
		a.tailMidLine = start > 0 && a.tail[start-1] != '\n'
		a.tail = append(a.tail[:0], a.tail[start:]...)
	}

	a.maybeStartSpillLocked(p)
	return n, nil
}

// maybeStartSpillLocked lazily creates the spill file once the output
// exceeds either limit, flushing the buffered chunks into it. Failures are
// non-fatal.
func (a *outputAccumulator) maybeStartSpillLocked(p []byte) {
	if a.spillFile != nil {
		if _, err := a.spillFile.Write(p); err != nil {
			// Non-fatal: keep accepting output; the spill file may be
			// incomplete but the snapshot is unaffected.
			a.dropSpillLocked()
			a.spillFailed = true
		}
		return
	}
	if a.spillFailed {
		return
	}
	lineCount := a.newlineCount
	if a.lastByte != '\n' {
		lineCount++
	}
	if a.totalBytes <= a.maxBytes && lineCount <= a.maxLines {
		return
	}

	f, err := os.CreateTemp("", "simplest-bash-*.txt")
	if err != nil {
		a.spillFailed = true
		a.chunks = nil
		return
	}
	for _, chunk := range a.chunks {
		if _, err := f.Write(chunk); err != nil {
			_ = f.Close()
			_ = os.Remove(f.Name())
			a.spillFailed = true
			a.chunks = nil
			return
		}
	}
	a.chunks = nil
	a.spillFile = f
	a.spillPath = f.Name()
}

func (a *outputAccumulator) dropSpillLocked() {
	if a.spillFile != nil {
		_ = a.spillFile.Close()
		_ = os.Remove(a.spillPath)
		a.spillFile = nil
		a.spillPath = ""
	}
}

// Snapshot returns the truncation metadata for the output seen so far. The
// content is computed from the retained tail via TruncateTail, then its
// global totals are corrected with the exact counters.
func (a *outputAccumulator) Snapshot() TruncationResult {
	a.mu.Lock()
	defer a.mu.Unlock()

	tail := a.tail
	if a.tailMidLine {
		// Drop the partial first line so TruncateTail sees line-aligned
		// input, except when there is no newline at all (a single huge
		// line): keeping it preserves LastLinePartial and the last
		// maxBytes of that line. If the only newline is the final byte,
		// the whole tail is one huge line: strip just the newline so
		// TruncateTail still sees (and byte-truncates) that line.
		if idx := bytes.IndexByte(tail, '\n'); idx >= 0 {
			if idx == len(tail)-1 {
				tail = tail[:idx]
			} else {
				tail = tail[idx+1:]
			}
		}
	}
	tr := TruncateTail(string(tail), TruncationOptions{MaxLines: a.maxLines, MaxBytes: a.maxBytes})

	totalLines := a.newlineCount
	if a.sawByte && a.lastByte != '\n' {
		totalLines++
	}
	tr.TotalBytes = a.totalBytes
	tr.TotalLines = totalLines
	tailTruncated := tr.Truncated
	tr.Truncated = a.totalBytes > a.maxBytes || totalLines > a.maxLines
	if tr.Truncated {
		if tr.TruncatedBy == "" {
			// TruncateTail saw only the small line-aligned remainder of
			// the tail; recover the cause from the global counters.
			if a.totalBytes > a.maxBytes {
				tr.TruncatedBy = "bytes"
			} else {
				tr.TruncatedBy = "lines"
			}
		}
		if !tailTruncated && strings.HasSuffix(tr.Content, "\n") {
			// The tail was within limits and came back verbatim with its
			// trailing newline; truncated results use line-joined content
			// without one.
			tr.Content = strings.TrimSuffix(tr.Content, "\n")
			tr.OutputBytes = len(tr.Content)
		}
	}
	return tr
}

// LastLineBytes returns the byte size of the last (possibly partial) line
// written so far. It is safe for concurrent use.
func (a *outputAccumulator) LastLineBytes() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.currentLineBytes
}

// SpillPath returns the temp file holding the full output, or "" when no
// spill file exists (limits never crossed, or temp-file creation failed).
func (a *outputAccumulator) SpillPath() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.spillPath
}

// Close closes the spill file, removing it when the output was not
// truncated. It must be called once the command has finished.
func (a *outputAccumulator) Close(truncated bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.spillFile == nil {
		return
	}
	_ = a.spillFile.Close()
	if !truncated {
		_ = os.Remove(a.spillPath)
	}
	a.spillFile = nil
}
