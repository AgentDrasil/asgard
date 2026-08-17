package api

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/AgentDrasil/asgard/lib/db"
	"github.com/AgentDrasil/asgard/lib/dbmodels"
	"github.com/AgentDrasil/asgard/lib/workflow"
)

const humanNodeStreamYAML = `
name: human-stream
tmp_dir: "tmp/${session_id}"
nodes:
  - id: entry_question
    type: human
    prompt: "please approve the plan"
  - id: final
    type: command
    depends:
      - node: entry_question
    command: "echo done > ${tmp_dir}/final.txt"
`

// streamWorkflowReply posts a /message:stream request mirroring the webui
// client and returns the raw SSE data lines.
func streamWorkflowReply(t *testing.T, handler http.Handler, prompt, contextID string) []string {
	t.Helper()
	body := map[string]any{
		"message": map[string]any{
			"messageId": "msg-stream-1",
			"contextId": contextID,
			"role":      "user",
			"parts": []map[string]any{
				{"text": prompt},
			},
		},
		"configuration": map[string]any{
			"acceptedOutputModes": []string{"text"},
		},
	}
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/message:stream", bytes.NewReader(raw))
	req.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	var lines []string
	sc := bufio.NewScanner(rec.Body)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "data:") {
			lines = append(lines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	return lines
}

func TestWorkflowHumanNodeEmitsAskUserOnStream(t *testing.T) {
	testDB := db.NewDBForTest(t)
	sqlDB, err := testDB.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, dbmodels.AutoMigrate(testDB))
	repo := dbmodels.NewSessionRepository(testDB)
	wfRepo := dbmodels.NewWorkflowRunRepository(testDB)

	registry := workflow.NewNodeRunnerRegistry()
	registry.Register(workflow.NewCommandRunner(false))
	engine := workflow.NewEngine(registry)
	engine.SetRunStore(newWorkflowRunStore(wfRepo))

	s := &Server{repo: repo, workflowEngine: engine}
	engine.SetHumanSuspender(s.suspendWorkflowHuman)

	parsed, err := workflow.ParseDefinition([]byte(humanNodeStreamYAML))
	require.NoError(t, err)

	executor := workflow.NewWorkflowExecutor(engine, parsed)
	executor.AgentName = "wf-stream-agent"
	executor.OnEvent = s.handleWorkflowEvent
	restHandler := a2asrv.NewRESTHandler(a2asrv.NewHandler(executor))

	chatID := "chat-wf-stream"
	require.NoError(t, repo.SaveSession(&dbmodels.Session{ChatID: chatID, CurrentAgent: "wf-stream-agent"}))

	lines := streamWorkflowReply(t, restHandler, "start the flow", chatID)
	require.NotEmpty(t, lines, "SSE stream produced no data events")

	var askMessageID string
	var sawInputRequired, sawAskUserMeta, sawMessageID bool
	for _, line := range lines {
		var payload struct {
			StatusUpdate *struct {
				Status struct {
					State   string `json:"state"`
					Message *struct {
						Parts []struct {
							Text string `json:"text"`
						} `json:"parts"`
					} `json:"message"`
				} `json:"status"`
				Metadata map[string]any `json:"metadata"`
			} `json:"statusUpdate"`
		}
		if err := json.Unmarshal([]byte(line), &payload); err != nil {
			continue
		}
		su := payload.StatusUpdate
		if su == nil || !strings.HasSuffix(su.Status.State, "INPUT_REQUIRED") {
			continue
		}
		sawInputRequired = true
		if su.Metadata["entry_type"] == "ask_user" {
			sawAskUserMeta = true
		}
		if id, ok := su.Metadata["message_id"].(string); ok {
			sawMessageID = true
			askMessageID = id
		}
	}
	assert.True(t, sawInputRequired, "no INPUT_REQUIRED statusUpdate in stream; events: %v", lines)
	assert.True(t, sawAskUserMeta, "INPUT_REQUIRED event missing entry_type=ask_user metadata; events: %v", lines)
	assert.True(t, sawMessageID, "INPUT_REQUIRED event missing message_id metadata; events: %v", lines)
	require.NotEmpty(t, askMessageID, "could not capture ask_user message_id")

	// The run must stay WAITING_HUMAN (not be killed by the A2A final-event
	// context cancellation) so the user's reply can resume it.
	waitForRunStatus(t, testDB, chatID, workflow.PersistStatusWaitingHuman)

	// The user replies through the ask-user REST endpoint, like the webui.
	rec := postAskUserReply(t, s, chatID, askMessageID, "Approved")
	assert.Equal(t, http.StatusOK, rec.Code)

	// The resumed run must complete.
	waitForRunStatus(t, testDB, chatID, workflow.PersistStatusCompleted)

	// The resumed run's summary must be persisted for the session transcript.
	require.Eventually(t, func() bool {
		session, err := repo.GetSession(chatID)
		require.NoError(t, err)
		for _, m := range session.Messages {
			if m.Role == "ask_user" && m.ID == askMessageID && m.Replied {
				return true
			}
		}
		return false
	}, 5*time.Second, 50*time.Millisecond, "ask_user message not marked replied in DB")
}

func waitForRunStatus(t *testing.T, gdb *gorm.DB, chatID, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var runs []dbmodels.WorkflowRun
		if err := gdb.Where("session_id = ?", chatID).Order("updated_at DESC").Find(&runs).Error; err != nil {
			t.Fatalf("querying workflow runs: %v", err)
		}
		for _, run := range runs {
			if run.Status == want {
				return
			}
		}
		if time.Now().After(deadline) {
			statuses := make([]string, 0, len(runs))
			for _, run := range runs {
				statuses = append(statuses, fmt.Sprintf("%s=%s", run.RunID, run.Status))
			}
			t.Fatalf("workflow run did not reach status %s; runs: %v", want, statuses)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
