package trigger

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-co-op/gocron/v2"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/backend/lib/agents"
	"github.com/AgentDrasil/asgard/backend/lib/db"
	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
)

func TestCleanCronSessionID(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		workflow string
	}{
		{name: "plain name", workflow: "build-and-fix"},
		{name: "spaces", workflow: "my workflow"},
		{name: "dots", workflow: "nightly.build.v1"},
		{name: "chinese", workflow: "夜间构建"},
		{name: "mixed", workflow: "Report 生成.final!"},
		{name: "very long", workflow: strings.Repeat("a", 200)},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			id := CleanCronSessionID(tc.workflow)
			assert.True(t, isValidChatIDFormat(id), "id %q should match ^[a-zA-Z0-9_-]{1,64}$", id)
			assert.LessOrEqual(t, len(id), 64)
			assert.True(t, len(id) >= len(cronSessionIDPrefix))
			assert.Equal(t, "wf-cron-", id[:len(cronSessionIDPrefix)])
		})
	}

	assert.Equal(t, "wf-cron-build-and-fix", CleanCronSessionID("build-and-fix"))
	assert.Equal(t, "wf-cron-my-workflow", CleanCronSessionID("my workflow"))
	assert.Equal(t, "wf-cron-nightly-build-v1", CleanCronSessionID("nightly.build.v1"))
	assert.Equal(t, "wf-cron-----", CleanCronSessionID("夜间构建"))
}

// isValidChatIDFormat mirrors api.IsValidChatID without an import cycle.
func isValidChatIDFormat(chatID string) bool {
	if len(chatID) == 0 || len(chatID) > 64 {
		return false
	}
	for _, r := range chatID {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// newTestWorkflowAgent writes a minimal scheduled workflow definition and wraps
// it in a workflow agent.
func newTestWorkflowAgent(t *testing.T, id string, schedule string) *agents.Agent {
	t.Helper()
	spec := fmt.Sprintf(`
name: %s
no_human: true
schedule: %q
nodes:
  - id: cmd1
    type: command
    command: "true"
`, id, schedule)
	dir := t.TempDir()
	path := filepath.Join(dir, "workflow.yaml")
	require.NoError(t, os.WriteFile(path, []byte(spec), 0o644))
	return &agents.Agent{
		Config:       agents.AgentConfig{ID: id, Name: id, Type: "workflow"},
		Path:         dir,
		WorkflowPath: path,
	}
}

func newTestCronManager(t *testing.T, repo *dbmodels.SessionRepository, trigger TriggerFunc) *WorkflowCronManager {
	t.Helper()
	m, err := NewWorkflowCronManager(repo, trigger)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = m.Shutdown()
	})
	return m
}

func TestWorkflowCronManager_ScheduleTrigger(t *testing.T) {
	dbConn := db.NewDBForTest(t)
	require.NoError(t, dbmodels.AutoMigrate(dbConn))
	repo := dbmodels.NewSessionRepository(dbConn)

	var mu sync.Mutex
	var calls []struct {
		agent    *agents.Agent
		chatID   string
		prompt   string
		headless bool
	}
	m := newTestCronManager(t, repo, func(ctx context.Context, agent *agents.Agent, chatID string, prompt string, headless bool) error {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, struct {
			agent    *agents.Agent
			chatID   string
			prompt   string
			headless bool
		}{agent, chatID, prompt, headless})
		return nil
	})

	agent := newTestWorkflowAgent(t, "sched-trigger-wf", "")
	m.mu.Lock()
	jobID, err := m.addJobLocked(agent, gocron.DurationJob(50*time.Millisecond))
	m.mu.Unlock()
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, jobID)

	assert.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(calls) > 0
	}, 3*time.Second, 20*time.Millisecond, "cron trigger should fire")

	mu.Lock()
	call := calls[0]
	mu.Unlock()

	assert.True(t, call.headless)
	assert.Empty(t, call.prompt)
	assert.Equal(t, agent.Config.ID, call.agent.Config.ID)
	assert.Equal(t, CleanCronSessionID(agent.Config.Name), call.chatID)

	session, err := repo.GetSession(call.chatID)
	require.NoError(t, err)
	require.NotNil(t, session)
	assert.Equal(t, fmt.Sprintf("Scheduled: %s", agent.Config.Name), session.Title)
}

func TestWorkflowCronManager_SingletonExecution(t *testing.T) {
	dbConn := db.NewDBForTest(t)
	require.NoError(t, dbmodels.AutoMigrate(dbConn))
	repo := dbmodels.NewSessionRepository(dbConn)

	release := make(chan struct{})
	var started sync.WaitGroup
	started.Add(1)
	var concurrent int32
	var maxConcurrent int32
	var mu sync.Mutex

	m := newTestCronManager(t, repo, func(ctx context.Context, agent *agents.Agent, chatID string, prompt string, headless bool) error {
		mu.Lock()
		concurrent++
		if concurrent > maxConcurrent {
			maxConcurrent = concurrent
		}
		first := concurrent == 1
		mu.Unlock()
		if first {
			started.Done()
		}
		<-release
		mu.Lock()
		concurrent--
		mu.Unlock()
		return nil
	})

	agent := newTestWorkflowAgent(t, "singleton-wf", "")
	m.mu.Lock()
	_, err := m.addJobLocked(agent, gocron.DurationJob(100*time.Millisecond))
	m.mu.Unlock()
	require.NoError(t, err)

	started.Wait()
	time.Sleep(500 * time.Millisecond)
	close(release)

	mu.Lock()
	defer mu.Unlock()
	assert.LessOrEqual(t, maxConcurrent, int32(1), "overlapping singleton runs must be skipped")
}

func TestWorkflowCronManager_SyntheticSession_UpsertPreservesMessages(t *testing.T) {
	dbConn := db.NewDBForTest(t)
	require.NoError(t, dbmodels.AutoMigrate(dbConn))
	repo := dbmodels.NewSessionRepository(dbConn)

	triggered := make(chan string, 4)
	m := newTestCronManager(t, repo, func(ctx context.Context, agent *agents.Agent, chatID string, prompt string, headless bool) error {
		select {
		case triggered <- chatID:
		default:
		}
		return nil
	})

	agent := newTestWorkflowAgent(t, "upsert-wf", "")
	sessionID := CleanCronSessionID(agent.Config.Name)

	// Pre-existing session with messages and artifacts.
	existing := &dbmodels.Session{
		ChatID:    sessionID,
		Title:     "old title",
		Messages:  dbmodels.Messages{{ID: "m1", Role: "user", Content: "hello"}},
		Artifacts: dbmodels.Artifacts{"/tmp/artifact.txt"},
	}
	require.NoError(t, repo.SaveSession(existing))

	m.mu.Lock()
	_, err := m.addJobLocked(agent, gocron.DurationJob(50*time.Millisecond))
	m.mu.Unlock()
	require.NoError(t, err)

	select {
	case got := <-triggered:
		assert.Equal(t, sessionID, got)
	case <-time.After(3 * time.Second):
		t.Fatal("cron trigger did not fire")
	}

	session, err := repo.GetSession(sessionID)
	require.NoError(t, err)
	require.NotNil(t, session)
	assert.Len(t, session.Messages, 1, "existing messages must be preserved")
	assert.Len(t, session.Artifacts, 1, "existing artifacts must be preserved")
	assert.Equal(t, fmt.Sprintf("Scheduled: %s", agent.Config.Name), session.Title)
}

func TestWorkflowCronManager_NilRepo_Tolerance(t *testing.T) {
	triggered := make(chan string, 4)
	m := newTestCronManager(t, nil, func(ctx context.Context, agent *agents.Agent, chatID string, prompt string, headless bool) error {
		select {
		case triggered <- chatID:
		default:
		}
		return nil
	})

	agent := newTestWorkflowAgent(t, "nil-repo-wf", "")
	m.mu.Lock()
	_, err := m.addJobLocked(agent, gocron.DurationJob(50*time.Millisecond))
	m.mu.Unlock()
	require.NoError(t, err)

	select {
	case <-triggered:
	case <-time.After(3 * time.Second):
		t.Fatal("cron trigger did not fire with nil repo")
	}
}

func TestWorkflowCronManager_DynamicReload(t *testing.T) {
	m := newTestCronManager(t, nil, func(ctx context.Context, agent *agents.Agent, chatID string, prompt string, headless bool) error {
		return nil
	})

	wfA := newTestWorkflowAgent(t, "reload-a", "0 2 * * *")
	wfB := newTestWorkflowAgent(t, "reload-b", "*/5 * * * *")
	noSchedule := newTestWorkflowAgent(t, "reload-nosched", "")

	// Initial registration.
	m.Reload([]*agents.Agent{wfA, wfB})
	assert.Eventually(t, func() bool {
		return len(m.scheduler.Jobs()) == 2
	}, time.Second, 10*time.Millisecond)

	// Remove one, drop schedule from another.
	m.Reload([]*agents.Agent{wfA, noSchedule})
	assert.Eventually(t, func() bool {
		return len(m.scheduler.Jobs()) == 1
	}, time.Second, 10*time.Millisecond)

	// Add a new one back.
	m.Reload([]*agents.Agent{wfA, wfB})
	assert.Eventually(t, func() bool {
		return len(m.scheduler.Jobs()) == 2
	}, time.Second, 10*time.Millisecond)

	// Empty list removes everything.
	m.Reload(nil)
	assert.Eventually(t, func() bool {
		return len(m.scheduler.Jobs()) == 0
	}, time.Second, 10*time.Millisecond)
}
