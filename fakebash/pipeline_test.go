package fakebash

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPipeline_FastPath_BothExitCodes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		totalBytes int64
		lineCount  int
		exitCode   int
		expected   bool
	}{
		{
			name:       "short success command e.g. pwd",
			totalBytes: 50,
			lineCount:  1,
			exitCode:   0,
			expected:   true,
		},
		{
			name:       "short error command e.g. ls /nonexistent",
			totalBytes: 80,
			lineCount:  2,
			exitCode:   2,
			expected:   true,
		},
		{
			name:       "boundary 255 bytes and 5 lines",
			totalBytes: 255,
			lineCount:  5,
			exitCode:   1,
			expected:   true,
		},
		{
			name:       "boundary 256 bytes - should not short-circuit",
			totalBytes: 256,
			lineCount:  3,
			exitCode:   0,
			expected:   false,
		},
		{
			name:       "exceeds line count 6 lines - should not short-circuit",
			totalBytes: 100,
			lineCount:  6,
			exitCode:   0,
			expected:   false,
		},
		{
			name:       "large output 1000 bytes",
			totalBytes: 1000,
			lineCount:  20,
			exitCode:   0,
			expected:   false,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ShouldShortCircuit(tc.totalBytes, tc.lineCount, tc.exitCode)
			assert.Equal(t, tc.expected, got)
		})
	}
}

func TestPipeline_FormatFooter_HumanReadable(t *testing.T) {
	t.Parallel()

	footer := FormatFooter(15000, 420, "c-12345-1-abc")
	assert.Contains(t, footer, "15KB -> 420B")
	assert.Contains(t, footer, "show-output c-12345-1-abc")
	assert.Contains(t, footer, "(or 'show-output' for the latest)")

	// Test FormatDropOnSuccess
	dropMsg := FormatDropOnSuccess(15000, "c-12345-1-abc")
	assert.Contains(t, dropMsg, "[fakebash: command succeeded with exit code 0. Verbose output truncated by sandbox]")
	assert.Contains(t, dropMsg, "15KB ->")
}

func TestPipeline_OverflowHandling(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	storage, err := NewStorage(tmpDir)
	require.NoError(t, err)

	cmdStr := "cat /dev/urandom"
	sf, err := storage.CreateFile(cmdStr)
	require.NoError(t, err)

	// Simulate 20MB file
	totalSize := int64(20 * 1000 * 1000)
	chunk := make([]byte, 1024*1024) // 1MB chunk
	for i := range chunk {
		chunk[i] = 'A'
	}
	// Append 2 chunks (2MB) on disk, but report totalBytes as 20MB
	require.NoError(t, sf.Append(chunk))
	require.NoError(t, sf.Append(chunk))
	require.NoError(t, sf.Finish(0))

	pipeline := NewPipeline(storage, nil, nil)
	processed, err := pipeline.Process(context.Background(), cmdStr, sf.ID(), totalSize, 1000, 0)
	require.NoError(t, err)

	assert.Contains(t, processed, "output too large")
	assert.Contains(t, processed, "20MB > 16MB")
	assert.Contains(t, processed, fmt.Sprintf("show-output %s", sf.ID()))
	assert.NotContains(t, processed, "# CMD:")
	assert.NotContains(t, processed, "# EXIT:")
}

func TestPipeline_DegradeOnNoKey_NoMetadataNoFooter(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	storage, err := NewStorage(tmpDir)
	require.NoError(t, err)

	cmdStr := "make"
	var sb strings.Builder
	for i := 0; i < 30; i++ {
		fmt.Fprintf(&sb, "compiling target %d...\n", i)
	}
	sb.WriteString("fatal error: gcc cannot find crt1.o\nexit status 1\n")
	content := sb.String()

	sf, err := storage.CreateFile(cmdStr)
	require.NoError(t, err)
	require.NoError(t, sf.Append([]byte(content)))
	require.NoError(t, sf.Finish(1))

	// Real GenAISummarizer without API key (falls back gracefully to raw payload)
	summarizer := NewGenAISummarizer(storage, WithGenAIClient(nil))
	evaluator := &fakeEvaluator{strategy: StrategyExtractFailure}
	pipeline := NewPipeline(storage, evaluator, summarizer)

	processed, err := pipeline.Process(context.Background(), cmdStr, sf.ID(), int64(len(content)), 32, 1)
	require.NoError(t, err)

	// In fallback degradation, it must return pure payload without # CMD: header, # EXIT: footer, or compressed footer
	assert.Equal(t, content, processed)
	assert.NotContains(t, processed, "# CMD:")
	assert.NotContains(t, processed, "# EXIT:")
	assert.NotContains(t, processed, "output compressed from")
}

// fakeEvaluator implements Evaluator for testing
type fakeEvaluator struct {
	strategy Strategy
	err      error
}

func (f *fakeEvaluator) Classify(ctx context.Context, cmd string, exitCode int, cmdID string) (Strategy, error) {
	return f.strategy, f.err
}

// fakeSummarizer implements Summarizer for testing
type fakeSummarizer struct {
	summary string
	err     error
}

func (f *fakeSummarizer) Summarize(ctx context.Context, cmd string, exitCode int, strategy Strategy, cmdID string, totalBytes int64) (string, error) {
	return f.summary, f.err
}

func TestPipeline_BypassAndDropOnSuccess(t *testing.T) {
	tmpDir := t.TempDir()
	storage, err := NewStorage(tmpDir)
	require.NoError(t, err)

	var sb strings.Builder
	for i := 0; i < 20; i++ {
		fmt.Fprintf(&sb, "building module %d: compiling components...\n", i)
	}
	sb.WriteString("build successful\n")
	rawContent := sb.String()
	require.Greater(t, len(rawContent), 256)

	sf, err := storage.CreateFile("npm run build")
	require.NoError(t, err)
	require.NoError(t, sf.Append([]byte(rawContent)))
	require.NoError(t, sf.Finish(0))

	evaluator := &fakeEvaluator{strategy: StrategyDropOnSuccess}
	summarizer := &fakeSummarizer{summary: "concise summary"}
	pipeline := NewPipeline(storage, evaluator, summarizer)

	// 1. Drop on success
	res, err := pipeline.Process(context.Background(), "npm run build", sf.ID(), int64(len(rawContent)), 21, 0)
	require.NoError(t, err)
	assert.Contains(t, res, "[fakebash: command succeeded with exit code 0. Verbose output truncated by sandbox]")
	assert.Contains(t, res, fmt.Sprintf("show-output %s", sf.ID()))

	// 2. Bypass with ASGARD_BASH_COMPACT=0
	t.Setenv("ASGARD_BASH_COMPACT", "0")
	bypassRes, err := pipeline.Process(context.Background(), "npm run build", sf.ID(), int64(len(rawContent)), 21, 0)
	require.NoError(t, err)
	assert.Equal(t, rawContent, bypassRes)
	assert.NotContains(t, bypassRes, "# CMD:")
	assert.NotContains(t, bypassRes, "# EXIT:")

	// 3. Fast-path check
	_ = os.Unsetenv("ASGARD_BASH_COMPACT")
	sfShort, err := storage.CreateFile("pwd")
	require.NoError(t, err)
	shortContent := "/home/user/project\n"
	require.NoError(t, sfShort.Append([]byte(shortContent)))
	require.NoError(t, sfShort.Finish(0))

	fastRes, err := pipeline.Process(context.Background(), "pwd", sfShort.ID(), int64(len(shortContent)), 1, 0)
	require.NoError(t, err)
	assert.Equal(t, shortContent, fastRes)
	assert.NotContains(t, fastRes, "# CMD:")
	assert.NotContains(t, fastRes, "# EXIT:")
}
