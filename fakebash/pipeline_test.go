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

func TestPipeline_FastPath_TinyOutput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		totalBytes int64
		expected   bool
	}{
		{
			name:       "empty output",
			totalBytes: 0,
			expected:   true,
		},
		{
			name:       "short success command e.g. pwd",
			totalBytes: 50,
			expected:   true,
		},
		{
			name:       "short error command e.g. ls /nonexistent",
			totalBytes: 80,
			expected:   true,
		},
		{
			name:       "many lines but still under threshold",
			totalBytes: 100,
			expected:   true,
		},
		{
			name:       "boundary 255 bytes",
			totalBytes: 255,
			expected:   true,
		},
		{
			name:       "boundary 256 bytes - should not short-circuit",
			totalBytes: 256,
			expected:   false,
		},
		{
			name:       "large output 1000 bytes",
			totalBytes: 1000,
			expected:   false,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ShouldShortCircuit(tc.totalBytes)
			assert.Equal(t, tc.expected, got)
		})
	}
}

func TestPipeline_FormatFooter(t *testing.T) {
	t.Parallel()

	footer := FormatFooter("c-12345-1-abc")
	assert.Contains(t, footer, "output compressed")
	assert.Contains(t, footer, "show-output c-12345-1-abc")
	assert.NotContains(t, footer, "for the latest")

	// Test FormatDropOnSuccess
	dropMsg := FormatDropOnSuccess("c-12345-1-abc")
	assert.Contains(t, dropMsg, "[fakebash: command succeeded with exit code 0. Verbose output truncated]")
	assert.Contains(t, dropMsg, "show-output c-12345-1-abc")
	assert.NotContains(t, dropMsg, "by sandbox")
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
	out, err := pipeline.Process(context.Background(), cmdStr, sf.ID(), totalSize, 0)
	require.NoError(t, err)

	assert.True(t, out.Truncated)
	assert.True(t, out.Compressed, "truncation is a lossy result the agent may want to retrieve")
	assert.Contains(t, out.Output, "output too large")
	assert.Contains(t, out.Output, fmt.Sprintf("show-output %s", sf.ID()))
	assert.NotContains(t, out.Output, "# CMD:")
	assert.NotContains(t, out.Output, "# EXIT:")
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

	out, err := pipeline.Process(context.Background(), cmdStr, sf.ID(), int64(len(content)), 1)
	require.NoError(t, err)

	// In fallback degradation, it must return pure payload without # CMD: header, # EXIT: footer, or compressed footer
	assert.Equal(t, content, out.Output)
	assert.NotContains(t, out.Output, "# CMD:")
	assert.NotContains(t, out.Output, "# EXIT:")
	assert.NotContains(t, out.Output, "output compressed")
}

func TestPipeline_KeepsCompassUsageWhenSummaryIsDiscarded(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	storage, err := NewStorage(tmpDir)
	require.NoError(t, err)

	var sb strings.Builder
	for i := 0; i < 50; i++ {
		fmt.Fprintf(&sb, "compiling target %d...\n", i)
	}
	content := sb.String()
	require.Greater(t, len(content), ShortCircuitMaxBytes)

	sf, err := storage.CreateFile("go build ./...")
	require.NoError(t, err)
	require.NoError(t, sf.Append([]byte(content)))
	require.NoError(t, sf.Finish(0))

	// The summarizer echoes the payload verbatim, so the pipeline discards the
	// summary and falls back to raw output, but the call was still billed.
	evaluator := &fakeEvaluator{strategy: StrategySummarize, usage: ModelUsage{Issued: true, Tokens: 10}}
	summarizer := &fakeSummarizer{summary: content, usage: ModelUsage{Issued: true, Tokens: 33}}
	pipeline := NewPipeline(storage, evaluator, summarizer)

	out, err := pipeline.Process(context.Background(), "go build ./...", sf.ID(), int64(len(content)), 0)
	require.NoError(t, err)

	assert.Equal(t, content, out.Output)
	assert.True(t, out.Jev.Issued)
	assert.Equal(t, int64(10), out.Jev.Tokens)
	assert.True(t, out.Compass.Issued, "a discarded summary was still a billed call")
	assert.Equal(t, int64(33), out.Compass.Tokens)
}

// TestPipeline_CompressedFlag pins the denominator the retrieval rate is computed
// against: it must be true exactly when the agent received a lossy result, since
// only then would it have any reason to call show-output.
func TestPipeline_CompressedFlag(t *testing.T) {
	t.Parallel()

	longContent := strings.Repeat("compiling module...\n", 40)
	require.Greater(t, len(longContent), ShortCircuitMaxBytes)

	tests := []struct {
		name      string
		strategy  Strategy
		summary   string
		content   string
		wantLossy bool
	}{
		{
			name:      "keep_raw hands the full output back",
			strategy:  StrategyKeepRaw,
			content:   longContent,
			wantLossy: false,
		},
		{
			name:      "drop_on_success hides the output",
			strategy:  StrategyDropOnSuccess,
			content:   longContent,
			wantLossy: true,
		},
		{
			name:      "summarize replaces the output",
			strategy:  StrategySummarize,
			summary:   "condensed diagnosis",
			content:   longContent,
			wantLossy: true,
		},
		{
			name:      "summary identical to the payload falls back to raw",
			strategy:  StrategySummarize,
			summary:   longContent,
			content:   longContent,
			wantLossy: false,
		},
		{
			name:      "short circuit hands the full output back",
			strategy:  StrategySummarize,
			summary:   "condensed diagnosis",
			content:   "tiny\n",
			wantLossy: false,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			storage, err := NewStorage(t.TempDir())
			require.NoError(t, err)

			sf, err := storage.CreateFile("go build ./...")
			require.NoError(t, err)
			require.NoError(t, sf.Append([]byte(tc.content)))
			require.NoError(t, sf.Finish(0))

			pipeline := NewPipeline(storage,
				&fakeEvaluator{strategy: tc.strategy},
				&fakeSummarizer{summary: tc.summary},
			)

			out, err := pipeline.Process(context.Background(), "go build ./...", sf.ID(), int64(len(tc.content)), 0)
			require.NoError(t, err)
			assert.Equal(t, tc.wantLossy, out.Compressed)
		})
	}
}

// fakeEvaluator implements Evaluator for testing
type fakeEvaluator struct {
	strategy Strategy
	usage    ModelUsage
	err      error
}

func (f *fakeEvaluator) Classify(ctx context.Context, cmd string, exitCode int, cmdID string) (Strategy, ModelUsage, error) {
	return f.strategy, f.usage, f.err
}

// fakeSummarizer implements Summarizer for testing
type fakeSummarizer struct {
	summary string
	usage   ModelUsage
	err     error
}

func (f *fakeSummarizer) Summarize(ctx context.Context, cmd string, exitCode int, strategy Strategy, cmdID string, totalBytes int64) (string, ModelUsage, error) {
	return f.summary, f.usage, f.err
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
	res, err := pipeline.Process(context.Background(), "npm run build", sf.ID(), int64(len(rawContent)), 0)
	require.NoError(t, err)
	assert.Contains(t, res.Output, "[fakebash: command succeeded with exit code 0. Verbose output truncated]")
	assert.Contains(t, res.Output, fmt.Sprintf("show-output %s", sf.ID()))

	// 2. Bypass with ASGARD_BASH_COMPACT=0
	t.Setenv("ASGARD_BASH_COMPACT", "0")
	bypassRes, err := pipeline.Process(context.Background(), "npm run build", sf.ID(), int64(len(rawContent)), 0)
	require.NoError(t, err)
	assert.Equal(t, rawContent, bypassRes.Output)
	assert.NotContains(t, bypassRes.Output, "# CMD:")
	assert.NotContains(t, bypassRes.Output, "# EXIT:")

	// 3. Fast-path check
	_ = os.Unsetenv("ASGARD_BASH_COMPACT")
	sfShort, err := storage.CreateFile("pwd")
	require.NoError(t, err)
	shortContent := "/home/user/project\n"
	require.NoError(t, sfShort.Append([]byte(shortContent)))
	require.NoError(t, sfShort.Finish(0))

	fastRes, err := pipeline.Process(context.Background(), "pwd", sfShort.ID(), int64(len(shortContent)), 0)
	require.NoError(t, err)
	assert.Equal(t, shortContent, fastRes.Output)
	assert.NotContains(t, fastRes.Output, "# CMD:")
	assert.NotContains(t, fastRes.Output, "# EXIT:")
}
