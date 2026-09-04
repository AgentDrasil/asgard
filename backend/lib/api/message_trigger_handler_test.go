package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/backend/lib/config"
	"github.com/AgentDrasil/asgard/backend/lib/db"
	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
	"github.com/AgentDrasil/asgard/backend/lib/workflow"
	"github.com/AgentDrasil/asgard/pkg/agentspec"
)

func TestMessageTriggerHandler(t *testing.T) {
	// Isolate HOME so executor tmp/session dirs land in a test-owned directory
	t.Setenv("HOME", t.TempDir())

	testDB := db.NewDBForTest(t)
	err := dbmodels.AutoMigrate(testDB)
	require.NoError(t, err)

	repo := dbmodels.NewSessionRepository(testDB)
	tempDir := t.TempDir()
	repo.SetSessionDirFunc(func(chatID string) string {
		return filepath.Join(tempDir, chatID)
	})
	hub := NewSessionEventHubWithCapacity(10)
	t.Cleanup(hub.Close)

	agentConfig := agentspec.AgentConfig{
		ID:          "test-agent",
		Name:        "Test Agent",
		Description: "A test agent",
		Type:        "agent",
	}
	agent := &agentspec.Agent{
		Config: agentConfig,
	}

	registry := workflow.NewNodeRunnerRegistry()
	registry.Register(workflow.NewCommandRunner(false))
	engine := workflow.NewEngine(registry)

	wfFile := filepath.Join(tempDir, "workflow.yaml")
	require.NoError(t, os.WriteFile(wfFile, []byte(fmt.Sprintf(`
name: test-sync-wf
tmp_dir: "%s/tmp/${session_id}"
nodes:
  - id: step1
    type: command
    command: "echo test-sync-done"
`, tempDir)), 0644))

	wfAgent := &agentspec.Agent{
		Config: agentspec.AgentConfig{
			ID:   "test-wf-agent",
			Name: "Test Workflow Agent",
			Type: "workflow",
		},
		WorkflowPath: wfFile,
	}

	server := &Server{
		conf:           &config.Config{},
		repo:           repo,
		eventHub:       hub,
		workflowEngine: engine,
		agents:         []*agentspec.Agent{agent, wfAgent},
	}
	server.mux = server.buildMuxLocked()

	t.Run("agent not found", func(t *testing.T) {
		t.Parallel()

		body := bytes.NewBufferString(`{"prompt":"hello"}`)
		req := httptest.NewRequest(http.MethodPost, "/api/agents/unknown-agent/message", body)
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()

		server.ServeHTTP(rr, req)
		assert.Equal(t, http.StatusNotFound, rr.Code)
	})

	t.Run("empty prompt rejected", func(t *testing.T) {
		t.Parallel()

		body := bytes.NewBufferString(`{"prompt":"   "}`)
		req := httptest.NewRequest(http.MethodPost, "/api/agents/test-agent/message", body)
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()

		server.ServeHTTP(rr, req)
		assert.Equal(t, http.StatusBadRequest, rr.Code)
	})

	t.Run("trigger message accepted and published", func(t *testing.T) {
		t.Parallel()

		chatID := "chat-trigger-1"
		subCh, _, cancel := hub.Subscribe(chatID, 0)
		t.Cleanup(cancel)

		payload := TriggerMessageRequest{
			Prompt: "trigger prompt",
			ChatID: chatID,
		}
		data, err := json.Marshal(payload)
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodPost, "/api/agents/test-agent/message", bytes.NewReader(data))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()

		server.ServeHTTP(rr, req)
		assert.Equal(t, http.StatusAccepted, rr.Code)

		var resp map[string]any
		err = json.Unmarshal(rr.Body.Bytes(), &resp)
		require.NoError(t, err)
		assert.Equal(t, "accepted", resp["status"])
		assert.Equal(t, chatID, resp["chatId"])

		// The executor will run in background and append user message, publishing it to EventHub
		select {
		case ev := <-subCh:
			assert.Equal(t, "message", ev.Type)
			require.NotNil(t, ev.Message)
			assert.Equal(t, "trigger prompt", ev.Message.Content)
		case <-time.After(1 * time.Second):
			t.Fatal("timed out waiting for triggered user message event")
		}
	})

	t.Run("concurrent trigger on same chat rejected with conflict when wait is true", func(t *testing.T) {
		t.Parallel()

		chatID := "chat-conflict-1"
		server.activeExecutions.Store(chatID, struct{}{})
		t.Cleanup(func() { server.activeExecutions.Delete(chatID) })

		payload := TriggerMessageRequest{
			Prompt: "another prompt",
			ChatID: chatID,
			Wait:   true,
		}
		data, err := json.Marshal(payload)
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodPost, "/api/agents/test-agent/message", bytes.NewReader(data))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()

		server.ServeHTTP(rr, req)
		assert.Equal(t, http.StatusConflict, rr.Code)
	})

	t.Run("workflow sync wait mode returns 200 with result", func(t *testing.T) {
		t.Parallel()

		chatID := "chat-sync-wait-1"
		payload := TriggerMessageRequest{
			Prompt: "sync prompt",
			ChatID: chatID,
			Wait:   true,
		}
		data, err := json.Marshal(payload)
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodPost, "/api/agents/test-wf-agent/message", bytes.NewReader(data))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()

		server.ServeHTTP(rr, req)
		assert.Equal(t, http.StatusOK, rr.Code)

		var resp map[string]any
		err = json.Unmarshal(rr.Body.Bytes(), &resp)
		require.NoError(t, err)
		assert.Equal(t, "completed", resp["status"])
		assert.Equal(t, chatID, resp["chatId"])
	})

	t.Run("single agent sync wait returns error on execution failure", func(t *testing.T) {
		t.Parallel()

		chatID := "chat-single-agent-wait-fail"
		payload := TriggerMessageRequest{
			Prompt: "single agent fail prompt",
			ChatID: chatID,
			Wait:   true,
		}
		data, err := json.Marshal(payload)
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodPost, "/api/agents/test-agent/message", bytes.NewReader(data))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()

		server.ServeHTTP(rr, req)
		assert.Equal(t, http.StatusInternalServerError, rr.Code)

		var resp map[string]any
		err = json.Unmarshal(rr.Body.Bytes(), &resp)
		require.NoError(t, err)
		assert.Equal(t, "failed", resp["status"])
		assert.NotEmpty(t, resp["error"])
		assert.Equal(t, chatID, resp["chatId"])
	})

	t.Run("trigger message with attachments parses and accepts", func(t *testing.T) {
		t.Parallel()

		chatID := "chat-trigger-attachments-1"
		subCh, _, cancel := hub.Subscribe(chatID, 0)
		t.Cleanup(cancel)

		payload := TriggerMessageRequest{
			Prompt: "analyze files",
			ChatID: chatID,
			Attachments: []dbmodels.Attachment{
				{
					Name:     "data.csv",
					Path:     "/malicious/client/path/data.csv",
					Size:     1024,
					MimeType: "text/csv",
				},
			},
		}
		data, err := json.Marshal(payload)
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodPost, "/api/agents/test-agent/message", bytes.NewReader(data))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()

		server.ServeHTTP(rr, req)
		assert.Equal(t, http.StatusAccepted, rr.Code)

		var resp map[string]any
		err = json.Unmarshal(rr.Body.Bytes(), &resp)
		require.NoError(t, err)
		assert.Equal(t, "accepted", resp["status"])
		assert.Equal(t, chatID, resp["chatId"])

		// The executor will persist the message and publish to event hub
		select {
		case ev := <-subCh:
			assert.Equal(t, "message", ev.Type)
			require.NotNil(t, ev.Message)
			assert.Equal(t, "analyze files", ev.Message.Content)
			require.Len(t, ev.Message.Attachments, 1)
			assert.Equal(t, "data.csv", ev.Message.Attachments[0].Name)
			assert.Equal(t, int64(1024), ev.Message.Attachments[0].Size)
		case <-time.After(1 * time.Second):
			t.Fatal("timed out waiting for triggered user message event with attachments")
		}
	})
}

func TestFormatPromptWithAttachments(t *testing.T) {
	t.Parallel()

	t.Run("empty attachments returns original prompt", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, "Hello World", formatPromptWithAttachments("Hello World", nil))
		assert.Equal(t, "Hello World", formatPromptWithAttachments("Hello World", []dbmodels.Attachment{}))
	})

	t.Run("single and multiple attachments injects sandbox path", func(t *testing.T) {
		t.Parallel()
		atts := []dbmodels.Attachment{
			{Name: "report.pdf", Size: 2048, Path: "/ignored/path"},
			{Name: "data.csv", Size: 512},
		}
		res := formatPromptWithAttachments("Analyze data", atts)
		expected := "Analyze data\n\n[Attached Files]\n- report.pdf (/tmp/attachments/report.pdf, 2048 bytes)\n- data.csv (/tmp/attachments/data.csv, 512 bytes)\nPlease inspect and process these attachments directly from the sandbox filesystem."
		assert.Equal(t, expected, res)
	})

	t.Run("ignores client fake path and protects against path traversal", func(t *testing.T) {
		t.Parallel()
		atts := []dbmodels.Attachment{
			{Name: "../../../etc/passwd", Size: 100, Path: "/etc/passwd"},
			{Name: "..\\..\\windows\\system32\\calc.exe", Size: 200, Path: "C:\\calc.exe"},
			{Name: "normal.txt", Size: 50, Path: "/malicious/path"},
		}
		res := formatPromptWithAttachments("Check files", atts)
		// Directory traversals in Name are filtered out by base != rawName check
		assert.Contains(t, res, "- normal.txt (/tmp/attachments/normal.txt, 50 bytes)")
		assert.NotContains(t, res, "passwd")
		assert.NotContains(t, res, "calc.exe")
		assert.NotContains(t, res, "/malicious/path")
	})

	t.Run("enforces max 20 attachments limit", func(t *testing.T) {
		t.Parallel()
		atts := make([]dbmodels.Attachment, 25)
		for i := 0; i < 25; i++ {
			atts[i] = dbmodels.Attachment{
				Name: fmt.Sprintf("file_%d.txt", i),
				Size: int64(i),
			}
		}
		res := formatPromptWithAttachments("Many files", atts)
		assert.Contains(t, res, "file_0.txt")
		assert.Contains(t, res, "file_19.txt")
		assert.NotContains(t, res, "file_20.txt")
	})

	t.Run("filters out invalid names and names exceeding 255 chars", func(t *testing.T) {
		t.Parallel()
		tooLongName := strings.Repeat("a", 256) + ".txt"
		atts := []dbmodels.Attachment{
			{Name: tooLongName, Size: 100},
			{Name: "bad\x00name.txt", Size: 100},
			{Name: "bad\nname.txt", Size: 100},
			{Name: ".", Size: 100},
			{Name: "..", Size: 100},
			{Name: "/", Size: 100},
			{Name: "valid.txt", Size: 100},
		}
		res := formatPromptWithAttachments("Check invalid", atts)
		assert.Contains(t, res, "- valid.txt (/tmp/attachments/valid.txt, 100 bytes)")
		assert.NotContains(t, res, tooLongName)
		assert.NotContains(t, res, "bad")
	})
}

func TestTriggerMessage_QueueWhenRunning(t *testing.T) {
	t.Parallel()
	server, repo, hub := setupQueueTestServer(t)

	chatID := "018f3a5b-0000-7000-8000-000000000010"
	require.NoError(t, repo.UpdateAgentSession(chatID, "test-agent", "", "", nil))

	// Mark session as running by taking the guard
	server.activeExecutions.Store(chatID, struct{}{})
	t.Cleanup(func() { server.activeExecutions.Delete(chatID) })

	subCh, _, cancel := hub.Subscribe(chatID, 0)
	t.Cleanup(cancel)

	// POST /api/agents/test-agent/message while session is running -> should queue and return 202
	payload := TriggerMessageRequest{
		Prompt: "queued message prompt",
		ChatID: chatID,
		Model:  "gemini-2.0-flash",
	}
	data, err := json.Marshal(payload)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/agents/test-agent/message", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	server.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusAccepted, rr.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Equal(t, "queued", resp["status"])
	assert.Equal(t, chatID, resp["chatId"])
	assert.NotEmpty(t, resp["messageId"])

	// Check DB
	queued, err := repo.GetQueuedMessages(chatID)
	require.NoError(t, err)
	require.Len(t, queued, 1)
	assert.Equal(t, "queued message prompt", queued[0].Prompt)

	// Check SSE broadcast
	select {
	case ev := <-subCh:
		assert.Equal(t, EventTypeQueue, ev.Type)
		qPayload, ok := ev.Payload["queue"].([]dbmodels.QueuedMessage)
		require.True(t, ok)
		require.Len(t, qPayload, 1)
		assert.Equal(t, "queued message prompt", qPayload[0].Prompt)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for queue SSE event")
	}
}

func TestTriggerMessage_WaitTrueConflict(t *testing.T) {
	t.Parallel()
	server, repo, _ := setupQueueTestServer(t)

	chatID := "018f3a5b-0000-7000-8000-000000000011"
	require.NoError(t, repo.UpdateAgentSession(chatID, "test-agent", "", "", nil))

	// Mark running
	server.activeExecutions.Store(chatID, struct{}{})
	t.Cleanup(func() { server.activeExecutions.Delete(chatID) })

	payload := TriggerMessageRequest{
		Prompt: "wait true conflict",
		ChatID: chatID,
		Wait:   true,
	}
	data, err := json.Marshal(payload)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/agents/test-agent/message", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	server.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusConflict, rr.Code)
}

func TestTriggerMessage_RunningRejectAttachments(t *testing.T) {
	t.Parallel()
	server, repo, _ := setupQueueTestServer(t)

	chatID := "018f3a5b-0000-7000-8000-000000000012"
	require.NoError(t, repo.UpdateAgentSession(chatID, "test-agent", "", "", nil))

	// Mark running
	server.activeExecutions.Store(chatID, struct{}{})
	t.Cleanup(func() { server.activeExecutions.Delete(chatID) })

	payload := TriggerMessageRequest{
		Prompt: "message with attachments while running",
		ChatID: chatID,
		Attachments: []dbmodels.Attachment{
			{Name: "data.csv", Size: 100},
		},
	}
	data, err := json.Marshal(payload)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/agents/test-agent/message", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	server.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "queued messages only support plain text; attachments are not allowed")
}

func TestTriggerMessage_FIFOExecution(t *testing.T) {
	t.Parallel()
	server, repo, _ := setupQueueTestServer(t)

	executedPrompts := make([]string, 0)
	var mu sync.Mutex

	// Mock execution function to record execution order
	server.runSingleAgentFn = func(ctx context.Context, agent *agentspec.Agent, chatID string, req TriggerMessageRequest) (string, string, error) {
		mu.Lock()
		executedPrompts = append(executedPrompts, req.Prompt)
		mu.Unlock()
		return "completed", "ok", nil
	}

	chatID := "018f3a5b-0000-7000-8000-000000000013"
	require.NoError(t, repo.UpdateAgentSession(chatID, "test-agent", "", "", nil))

	// Enqueue 2 queued messages
	_, err := repo.EnqueueMessage(chatID, "queue 1", "")
	require.NoError(t, err)
	_, err = repo.EnqueueMessage(chatID, "queue 2", "")
	require.NoError(t, err)

	// Acquire guard and run real queue consumer loop
	server.activeExecutions.Store(chatID, struct{}{})
	go server.runQueueConsumerLoop(context.Background(), server.agents[0], chatID, "")

	// Wait until drained
	assert.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(executedPrompts) == 2
	}, 3*time.Second, 20*time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"queue 1", "queue 2"}, executedPrompts)

	// Verify queue is completely empty in repo
	remaining, err := repo.GetQueuedMessages(chatID)
	require.NoError(t, err)
	assert.Empty(t, remaining)

	// Guard should be released
	_, running := server.activeExecutions.Load(chatID)
	assert.False(t, running)
}

func TestTriggerMessage_InitialFailurePurgesQueue(t *testing.T) {
	t.Parallel()
	server, repo, hub := setupQueueTestServer(t)

	chatID := "018f3a5b-0000-7000-8000-000000000014"
	require.NoError(t, repo.UpdateAgentSession(chatID, "test-agent", "", "", nil))

	// Pre-enqueue 2 messages
	_, err := repo.EnqueueMessage(chatID, "msg in queue 1", "")
	require.NoError(t, err)
	_, err = repo.EnqueueMessage(chatID, "msg in queue 2", "")
	require.NoError(t, err)

	subCh, _, cancel := hub.Subscribe(chatID, 0)
	t.Cleanup(cancel)

	// Mock runSingleAgentFn to fail with sandbox failure (agentRunError)
	simulatedErr := &agentRunError{Err: errors.New("sandbox process crashed")}
	server.runSingleAgentFn = func(ctx context.Context, agent *agentspec.Agent, cid string, req TriggerMessageRequest) (string, string, error) {
		return "failed", "", simulatedErr
	}

	// Hold guard and drive real runSingleAgentWithQueue
	server.activeExecutions.Store(chatID, struct{}{})
	server.runSingleAgentWithQueue(context.Background(), server.agents[0], chatID, TriggerMessageRequest{Prompt: "initial prompt"})

	// Verify queued messages were cleared
	remaining, err := repo.GetQueuedMessages(chatID)
	require.NoError(t, err)
	assert.Empty(t, remaining)

	// Verify guard was deleted
	_, running := server.activeExecutions.Load(chatID)
	assert.False(t, running)

	// Verify events received: error message event and queue cleared event
	var receivedErrorMsg, receivedEmptyQueue bool
	for i := 0; i < 2; i++ {
		select {
		case ev := <-subCh:
			if ev.Type == EventTypeMessage && ev.Message != nil && ev.Message.Role == "error" {
				receivedErrorMsg = true
				assert.Contains(t, ev.Message.Content, "已自动清空该会话所有排队消息")
			}
			if ev.Type == EventTypeQueue {
				receivedEmptyQueue = true
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for events")
		}
	}
	assert.True(t, receivedErrorMsg)
	assert.True(t, receivedEmptyQueue)
}

func TestQueueConsumer_NoLostWakeupAtDrainBoundary(t *testing.T) {
	t.Parallel()
	server, repo, _ := setupQueueTestServer(t)

	chatID := "018f3a5b-0000-7000-8000-000000000015"
	require.NoError(t, repo.UpdateAgentSession(chatID, "test-agent", "", "", nil))

	var processedCount int64
	var mu sync.Mutex

	// Mock runSingleAgentFn to record processed count with a tiny delay
	// simulating realistic execution work while producer continues enqueuing
	server.runSingleAgentFn = func(ctx context.Context, agent *agentspec.Agent, cid string, req TriggerMessageRequest) (string, string, error) {
		time.Sleep(5 * time.Millisecond)
		mu.Lock()
		processedCount++
		mu.Unlock()
		return "completed", "ok", nil
	}

	totalSubmitted := 10
	var enqueueWg sync.WaitGroup
	enqueueWg.Add(1)

	go func() {
		defer enqueueWg.Done()
		for cycle := 0; cycle < totalSubmitted; cycle++ {
			// Enqueue message
			prompt := fmt.Sprintf("boundary-task-%d", cycle)
			for {
				_, err := repo.EnqueueMessage(chatID, prompt, "")
				if err == nil {
					server.startQueueConsumerIfIdle(chatID, server.agents[0], "")
					break
				}
				// If queue full (capacity 3), back off briefly until consumer frees capacity
				time.Sleep(5 * time.Millisecond)
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()

	// Wait for producer to finish submitting
	enqueueWg.Wait()

	// Assert that all submitted items are eventually processed without lost wakeups
	assert.Eventually(t, func() bool {
		mu.Lock()
		count := processedCount
		mu.Unlock()
		return count == int64(totalSubmitted)
	}, 5*time.Second, 20*time.Millisecond, "expected all %d submitted items to be processed", totalSubmitted)

	// Verify the queue is completely drained
	msgs, err := repo.GetQueuedMessages(chatID)
	require.NoError(t, err)
	assert.Empty(t, msgs)

	// Verify consumer loop has terminated and released guard
	assert.Eventually(t, func() bool {
		_, running := server.activeExecutions.Load(chatID)
		return !running
	}, 2*time.Second, 20*time.Millisecond, "expected execution guard to be released after queue drained")
}

func TestTriggerMessage_PurgeOnlyOnAgentFailure(t *testing.T) {
	t.Parallel()
	server, repo, hub := setupQueueTestServer(t)

	chatID := "018f3a5b-0000-7000-8000-000000000016"
	require.NoError(t, repo.UpdateAgentSession(chatID, "test-agent", "", "", nil))

	subCh, _, cancel := hub.Subscribe(chatID, 0)
	t.Cleanup(cancel)

	// 1. Non-sandbox failure (e.g. invalid run_dir / pre-update failure):
	// Does NOT purge queue, appends visible role: error message
	_, err := repo.EnqueueMessage(chatID, "task to preserve 1", "")
	require.NoError(t, err)

	server.runSingleAgentFn = func(ctx context.Context, agent *agentspec.Agent, cid string, req TriggerMessageRequest) (string, string, error) {
		return "failed", "", errors.New("run_dir /invalid does not exist")
	}
	server.activeExecutions.Store(chatID, struct{}{})
	server.runSingleAgentWithQueue(context.Background(), server.agents[0], chatID, TriggerMessageRequest{Prompt: "failing non-sandbox"})

	// Check queue is preserved (1 message still queued)
	msgs, err := repo.GetQueuedMessages(chatID)
	require.NoError(t, err)
	assert.Len(t, msgs, 1)
	assert.Equal(t, "task to preserve 1", msgs[0].Prompt)

	// Check that a visible error message was published
	select {
	case ev := <-subCh:
		assert.Equal(t, EventTypeMessage, ev.Type)
		require.NotNil(t, ev.Message)
		assert.Equal(t, "error", ev.Message.Role)
		assert.Contains(t, ev.Message.Content, "任务调度失败（该消息未执行）")
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for non-sandbox error message")
	}

	// 2. Context canceled: does NOT purge queue and does NOT append error message
	_, err = repo.EnqueueMessage(chatID, "task to preserve 2", "")
	require.NoError(t, err)

	server.runSingleAgentFn = func(ctx context.Context, agent *agentspec.Agent, cid string, req TriggerMessageRequest) (string, string, error) {
		return "failed", "", context.Canceled
	}
	server.activeExecutions.Store(chatID, struct{}{})
	server.runSingleAgentWithQueue(context.Background(), server.agents[0], chatID, TriggerMessageRequest{Prompt: "canceled task"})

	msgs, err = repo.GetQueuedMessages(chatID)
	require.NoError(t, err)
	assert.Len(t, msgs, 2)

	// Verify no error message was appended
	select {
	case ev := <-subCh:
		t.Fatalf("unexpected event on context canceled: %+v", ev)
	case <-time.After(50 * time.Millisecond):
		// Expected: no event
	}

	// 3. Sandbox failure (agentRunError): DOES purge queue and publishes error + empty queue events
	server.runSingleAgentFn = func(ctx context.Context, agent *agentspec.Agent, cid string, req TriggerMessageRequest) (string, string, error) {
		return "failed", "", &agentRunError{Err: errors.New("agent exit code 1")}
	}
	server.activeExecutions.Store(chatID, struct{}{})
	server.runSingleAgentWithQueue(context.Background(), server.agents[0], chatID, TriggerMessageRequest{Prompt: "failing sandbox"})

	msgs, err = repo.GetQueuedMessages(chatID)
	require.NoError(t, err)
	assert.Empty(t, msgs)

	// Verify error message and queue cleared events
	var receivedPurgeError, receivedPurgeQueue bool
	for i := 0; i < 2; i++ {
		select {
		case ev := <-subCh:
			if ev.Type == EventTypeMessage && ev.Message != nil && ev.Message.Role == "error" {
				receivedPurgeError = true
				assert.Contains(t, ev.Message.Content, "已自动清空该会话所有排队消息")
			}
			if ev.Type == EventTypeQueue {
				receivedPurgeQueue = true
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for purge events")
		}
	}
	assert.True(t, receivedPurgeError)
	assert.True(t, receivedPurgeQueue)
}

func TestQueue_SurvivesServerRestart(t *testing.T) {
	t.Parallel()
	testDB := db.NewDBForTest(t)
	require.NoError(t, dbmodels.AutoMigrate(testDB))

	repo := dbmodels.NewSessionRepository(testDB)
	chatID := "018f3a5b-0000-7000-8000-000000000017"
	require.NoError(t, repo.UpdateAgentSession(chatID, "test-agent", "", "", nil))

	// Pre-enqueue 2 messages before server starts
	_, err := repo.EnqueueMessage(chatID, "preserved 1", "")
	require.NoError(t, err)
	_, err = repo.EnqueueMessage(chatID, "preserved 2", "")
	require.NoError(t, err)

	agent := &agentspec.Agent{
		Config: agentspec.AgentConfig{
			ID:   "test-agent",
			Name: "Test Agent",
			Type: "agent",
		},
	}

	server := &Server{
		conf:     &config.Config{},
		repo:     repo,
		eventHub: NewSessionEventHubWithCapacity(10),
		agents:   []*agentspec.Agent{agent},
	}
	t.Cleanup(server.eventHub.Close)
	server.mux = server.buildMuxLocked()

	// Call recoverOrphanQueuedSessions
	server.recoverOrphanQueuedSessions()

	// Verify that guard was acquired or execution started for chatID
	_, running := server.activeExecutions.Load(chatID)
	assert.True(t, running, "expected session to be picked up by orphan recovery")
}
