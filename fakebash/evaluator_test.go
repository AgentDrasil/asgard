package fakebash

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/pkg/jev"
)

func TestEvaluator_Classify_MockJevChoices(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		choice    string
		expected  Strategy
		cmd       string
		exitCode  int
		logOutput string
	}{
		{
			name:      "drop on success for verbose build",
			choice:    "drop_on_success",
			expected:  StrategyDropOnSuccess,
			cmd:       "npm install",
			exitCode:  0,
			logOutput: "added 1200 packages in 10s\n",
		},
		{
			name:      "extract failure for compile error",
			choice:    "extract_failure",
			expected:  StrategyExtractFailure,
			cmd:       "go build ./...",
			exitCode:  1,
			logOutput: "main.go:10: undefined: foo\n",
		},
		{
			name:      "summarize for long informational output",
			choice:    "summarize",
			expected:  StrategySummarize,
			cmd:       "docker ps -a",
			exitCode:  0,
			logOutput: "CONTAINER ID IMAGE COMMAND CREATED STATUS PORTS NAMES\n",
		},
		{
			name:      "keep raw when output should stay unmodified",
			choice:    "keep_raw",
			expected:  StrategyKeepRaw,
			cmd:       "git status",
			exitCode:  0,
			logOutput: "On branch main\nnothing to commit, working tree clean\n",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// Mock server returning the desired choice
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/v1/systemone", r.URL.Path)
				assert.Equal(t, "Bearer test-api-key", r.Header.Get("Authorization"))

				var req jev.EvaluateRequest
				err := json.NewDecoder(r.Body).Decode(&req)
				assert.NoError(t, err)

				w.Header().Set("Content-Type", "application/json")
				resp := jev.EvaluateResponse{
					Model: req.Model,
					Answers: map[string]jev.Answer{
						"compression_strategy": {
							Type:   jev.QuestionTypeChoice,
							Choice: tc.choice,
						},
					},
				}
				_ = json.NewEncoder(w).Encode(resp)
			}))
			defer server.Close()

			tmpDir := t.TempDir()
			storage, err := NewStorage(tmpDir)
			require.NoError(t, err)

			sf, err := storage.CreateFile(tc.cmd)
			require.NoError(t, err)
			require.NoError(t, sf.Append([]byte(tc.logOutput)))
			require.NoError(t, sf.Finish(tc.exitCode))

			client, err := jev.NewClient("test-api-key", jev.WithBaseURL(server.URL))
			require.NoError(t, err)

			evaluator := NewJevEvaluator(storage, WithJevClient(client))
			strategy, err := evaluator.Classify(context.Background(), tc.cmd, tc.exitCode, sf.ID())
			require.NoError(t, err)
			assert.Equal(t, tc.expected, strategy)
		})
	}
}

func TestEvaluator_Classify_TimeoutFallback(t *testing.T) {
	t.Parallel()

	// Mock server that sleeps longer than timeout
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	storage, err := NewStorage(tmpDir)
	require.NoError(t, err)

	sf, err := storage.CreateFile("go test ./...")
	require.NoError(t, err)
	require.NoError(t, sf.Append([]byte("running tests...\n")))
	require.NoError(t, sf.Finish(1))

	client, err := jev.NewClient("test-api-key", jev.WithBaseURL(server.URL))
	require.NoError(t, err)

	evaluator := NewJevEvaluator(storage, WithJevClient(client), WithTimeout(20*time.Millisecond))
	strategy, err := evaluator.Classify(context.Background(), "go test ./...", 1, sf.ID())
	require.NoError(t, err)
	assert.Equal(t, StrategyKeepRaw, strategy)
}

func TestEvaluator_Classify_NoAPIKey(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	storage, err := NewStorage(tmpDir)
	require.NoError(t, err)

	sf, err := storage.CreateFile("ls -la")
	require.NoError(t, err)
	require.NoError(t, sf.Append([]byte("total 0\n")))
	require.NoError(t, sf.Finish(0))

	// Clear TYPESAFE_API_KEY for this test (or pass no client)
	require.NoError(t, os.Unsetenv("TYPESAFE_API_KEY"))

	evaluator := NewJevEvaluator(storage)
	strategy, err := evaluator.Classify(context.Background(), "ls -la", 0, sf.ID())
	require.NoError(t, err)
	assert.Equal(t, StrategyKeepRaw, strategy)
}
