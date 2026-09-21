package fakebash

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/genai"
)

// fakeRoundTripper intercepts HTTP requests to mock Gemini GenerateContent API.
type fakeRoundTripper struct {
	roundTripFunc func(req *http.Request) (*http.Response, error)
}

func (f *fakeRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f.roundTripFunc(req)
}

func TestSummarizer_SmallLog_DirectExtract(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	storage, err := NewStorage(tmpDir)
	require.NoError(t, err)

	// Create 5KB error log (< 10KB)
	var sb strings.Builder
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&sb, "build line %d: checking packages...\n", i)
	}
	sb.WriteString("FAIL: TestUserAuth (0.05s)\n    auth_test.go:42: passwords do not match\n")
	sb.WriteString("FAIL\nexit status 1\n")
	content := sb.String()
	require.Less(t, len(content), TenKBThreshold)
	require.Greater(t, len(content), 3000)

	sf, err := storage.CreateFile("go test ./...")
	require.NoError(t, err)
	require.NoError(t, sf.Append([]byte(content)))
	require.NoError(t, sf.Finish(1))

	var mu sync.Mutex
	var receivedBody string
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		mu.Lock()
		receivedBody = string(buf)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"candidates": [
				{
					"content": {
						"parts": [
							{"text": "Failed test: TestUserAuth at auth_test.go:42: passwords do not match"}
						]
					}
				}
			]
		}`))
	}))
	t.Cleanup(mockServer.Close)

	httpClient := &http.Client{
		Transport: &fakeRoundTripper{
			roundTripFunc: func(req *http.Request) (*http.Response, error) {
				req.URL.Scheme = "http"
				req.URL.Host = mockServer.Listener.Addr().String()
				return http.DefaultTransport.RoundTrip(req)
			},
		},
	}

	genaiClient, err := genai.NewClient(context.Background(), &genai.ClientConfig{
		APIKey:     "test-gemini-api-key",
		Backend:    genai.BackendGeminiAPI,
		HTTPClient: httpClient,
	})
	require.NoError(t, err)

	summarizer := NewGenAISummarizer(storage, WithGenAIClient(genaiClient))
	res, err := summarizer.Summarize(context.Background(), "go test ./...", 1, StrategyExtractFailure, sf.ID(), int64(len(content)))
	require.NoError(t, err)

	mu.Lock()
	body := receivedBody
	mu.Unlock()

	assert.Contains(t, res, "Failed test: TestUserAuth at auth_test.go:42")
	assert.Contains(t, body, "Inspector Mode: false")
	assert.Contains(t, body, "auth_test.go:42: passwords do not match")
}

func TestSummarizer_LargeLog_InspectorRetrieval(t *testing.T) {
	t.Parallel()

	testSizes := []struct {
		name      string
		lineCount int
	}{
		{name: "50KB log", lineCount: 1500},
		{name: "500KB log", lineCount: 15000},
	}

	for _, tc := range testSizes {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tmpDir := t.TempDir()
			storage, err := NewStorage(tmpDir)
			require.NoError(t, err)

			var sb strings.Builder
			// Header lines
			for i := 0; i < 60; i++ {
				fmt.Fprintf(&sb, "initialization step %d: setup toolchain environment\n", i)
			}
			// Middle noisy lines
			for i := 0; i < tc.lineCount; i++ {
				if i == tc.lineCount/2 {
					sb.WriteString("panic: runtime error: invalid memory address or nil pointer dereference\n")
					sb.WriteString("[signal SIGSEGV: segmentation violation code=0x1 addr=0x0 pc=0x123456]\n")
					sb.WriteString("goroutine 1 [running]:\nmain.processWork(0x0, 0x10)\n\t/app/server/worker.go:188 +0x3a\n")
				} else {
					fmt.Fprintf(&sb, "compiling pkg/module_%d/file_%d.go: progress [=====>    ] ok\n", i%20, i)
				}
			}
			// Tail lines
			for i := 0; i < 60; i++ {
				fmt.Fprintf(&sb, "cleanup routine %d: closing descriptor\n", i)
			}
			sb.WriteString("exit status 2\n")

			content := sb.String()
			require.GreaterOrEqual(t, int64(len(content)), int64(TenKBThreshold))

			sf, err := storage.CreateFile("go test ./...")
			require.NoError(t, err)
			require.NoError(t, sf.Append([]byte(content)))
			require.NoError(t, sf.Finish(2))

			var mu sync.Mutex
			var receivedPrompt string
			mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				buf, _ := io.ReadAll(r.Body)
				mu.Lock()
				receivedPrompt = string(buf)
				mu.Unlock()

				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{
					"candidates": [
						{
							"content": {
								"parts": [
									{"text": "Panic in main.processWork at /app/server/worker.go:188: nil pointer dereference"}
								]
							}
						}
					]
				}`))
			}))
			t.Cleanup(mockServer.Close)

			httpClient := &http.Client{
				Transport: &fakeRoundTripper{
					roundTripFunc: func(req *http.Request) (*http.Response, error) {
						req.URL.Scheme = "http"
						req.URL.Host = mockServer.Listener.Addr().String()
						return http.DefaultTransport.RoundTrip(req)
					},
				},
			}

			genaiClient, err := genai.NewClient(context.Background(), &genai.ClientConfig{
				APIKey:     "test-gemini-api-key",
				Backend:    genai.BackendGeminiAPI,
				HTTPClient: httpClient,
			})
			require.NoError(t, err)

			summarizer := NewGenAISummarizer(storage, WithGenAIClient(genaiClient))
			res, err := summarizer.Summarize(context.Background(), "go test ./...", 2, StrategyExtractFailure, sf.ID(), int64(len(content)))
			require.NoError(t, err)

			mu.Lock()
			prompt := receivedPrompt
			mu.Unlock()

			assert.Contains(t, res, "Panic in main.processWork at /app/server/worker.go:188")
			// Verify that Inspector mode was activated and prompt did not send the full 50KB/500KB content
			assert.Contains(t, prompt, "Inspector Mode: true")
			assert.Contains(t, prompt, "Inspector truncated noisy lines")
			assert.Contains(t, prompt, "/app/server/worker.go:188")
			assert.Less(t, len(prompt), len(content))
		})
	}
}

func TestSummarizer_DegradeOnFailure(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	storage, err := NewStorage(tmpDir)
	require.NoError(t, err)

	rawOutput := "compilation error: undefined symbol\nexit status 1\n"
	sf, err := storage.CreateFile("make")
	require.NoError(t, err)
	require.NoError(t, sf.Append([]byte(rawOutput)))
	require.NoError(t, sf.Finish(1))

	// Mock server returning HTTP 429 quota error
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error": {"code": 429, "message": "RESOURCE_EXHAUSTED"}}`))
	}))
	t.Cleanup(mockServer.Close)

	httpClient := &http.Client{
		Transport: &fakeRoundTripper{
			roundTripFunc: func(req *http.Request) (*http.Response, error) {
				req.URL.Scheme = "http"
				req.URL.Host = mockServer.Listener.Addr().String()
				return http.DefaultTransport.RoundTrip(req)
			},
		},
	}

	genaiClient, err := genai.NewClient(context.Background(), &genai.ClientConfig{
		APIKey:     "test-gemini-api-key",
		Backend:    genai.BackendGeminiAPI,
		HTTPClient: httpClient,
	})
	require.NoError(t, err)

	summarizer := NewGenAISummarizer(storage, WithGenAIClient(genaiClient), WithSummarizerTimeout(100*time.Millisecond))
	res, err := summarizer.Summarize(context.Background(), "make", 1, StrategyExtractFailure, sf.ID(), int64(len(rawOutput)))
	require.NoError(t, err)

	// Should gracefully degrade to raw output without panic or error
	assert.Contains(t, res, "compilation error: undefined symbol")
	// Ensure no # CMD: or # EXIT: leaked in degrade output
	assert.NotContains(t, res, "# CMD:")
	assert.NotContains(t, res, "# EXIT:")
}

func TestExtractInspectorLines_SingleLineLargeLog(t *testing.T) {
	t.Parallel()

	// 20KB single-line input (minified JSON / long line)
	var sb strings.Builder
	for i := 0; i < 200; i++ {
		sb.WriteString(`{"event":"step","status":"ok","fatal_error":"unexpected EOF in payload"},`)
	}
	content := sb.String()
	require.GreaterOrEqual(t, len(content), TenKBThreshold)

	extracted := ExtractInspectorLines(content)
	// Must be processed by inspector rather than returned verbatim
	assert.LessOrEqual(t, len(extracted), len(content))
	assert.Contains(t, extracted, "fatal_error")
}

func TestExtractInspectorLines_DenseKeywordsBounded(t *testing.T) {
	t.Parallel()

	// 140KB / 4000 lines log with failure keyword every 8 lines
	var sb strings.Builder
	for i := 0; i < 4000; i++ {
		if i%8 == 0 {
			fmt.Fprintf(&sb, "line %d: FAILED test_subcase_%d\n", i, i)
		} else {
			fmt.Fprintf(&sb, "line %d: processing step info\n", i)
		}
	}
	content := sb.String()
	require.Greater(t, len(content), 100*1024)

	extracted := ExtractInspectorLines(content)
	// Must be strictly bounded and significantly smaller than input (under 50% of input size)
	assert.Less(t, len(extracted), len(content)/2)
	assert.LessOrEqual(t, len(extracted), 35*1024) // within maxInspectorOutputBytes margin
}

func TestExtractInspectorLines_SingleHugeLineWithKeyword(t *testing.T) {
	t.Parallel()

	// totalLines > 5 to bypass <=5 lines heuristic
	var sb strings.Builder
	for i := 0; i < 10; i++ {
		fmt.Fprintf(&sb, "preamble line %d\n", i)
	}
	// Massive line of 2MB containing error:
	sb.WriteString("error: ")
	hugeData := strings.Repeat("A", 2*1024*1024)
	sb.WriteString(hugeData)
	sb.WriteString("\n")
	for i := 0; i < 10; i++ {
		fmt.Fprintf(&sb, "postamble line %d\n", i)
	}

	content := sb.String()
	require.Greater(t, len(content), 2*1024*1024)

	extracted := ExtractInspectorLines(content)
	// Total extracted output must strictly respect maxInspectorOutputBytes (<= 35KB with margin)
	assert.LessOrEqual(t, len(extracted), 35*1024)
	// Must contain per-line truncation marker
	assert.Contains(t, extracted, "...[truncated long line]")
}
