package api

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/backend/lib/db"
	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
	"github.com/AgentDrasil/asgard/pkg/agentspec"
	"github.com/AgentDrasil/asgard/pkg/runnerpb"
)

func TestWorkflowRunDirDefaults(t *testing.T) {
	testDB := db.NewDBForTest(t)
	require.NoError(t, dbmodels.AutoMigrate(testDB))
	repo := dbmodels.NewSessionRepository(testDB)

	agentDir, sessDir := "/srv/agent-work", "/srv/agent-work/session-dir"
	agent := &agentspec.Agent{Config: agentspec.AgentConfig{ID: "wf", Type: "workflow", RunDirs: []string{agentDir}}}
	s := &Server{repo: repo, agents: []*agentspec.Agent{agent}}
	s.runnerClient = func(string) (runnerpb.RunnerServiceClient, error) { return nil, nil }

	require.NoError(t, repo.SaveSession(&dbmodels.Session{ChatID: "c-none", Title: "t", RunnerNodeID: "r1"}))
	require.NoError(t, repo.SaveSession(&dbmodels.Session{ChatID: "c-sess", Title: "t", RunnerNodeID: "r1", RunDir: sessDir}))
	require.NoError(t, repo.SaveSession(&dbmodels.Session{ChatID: "c-bad", Title: "t", RunnerNodeID: "r1", RunDir: "/etc"}))

	// Explicit request dir wins and is left to the runner to validate.
	got, err := s.workflowRunDir("c-sess", agent, TriggerMessageRequest{RunDir: "/srv/agent-work/x"})
	require.NoError(t, err)
	assert.Equal(t, "/srv/agent-work/x", got)

	// Session run dir, then the agent's first run_dir.
	got, err = s.workflowRunDir("c-sess", agent, TriggerMessageRequest{})
	require.NoError(t, err)
	assert.Equal(t, sessDir, got)
	got, err = s.workflowRunDir("c-none", agent, TriggerMessageRequest{})
	require.NoError(t, err)
	assert.Equal(t, agentDir, got)

	// A stored dir outside the runner's allowed dirs is refused.
	_, err = s.workflowRunDir("c-bad", agent, TriggerMessageRequest{})
	assert.ErrorContains(t, err, "refused")

	// Nothing anywhere: a clear error, never the backend cwd.
	bare := &agentspec.Agent{Config: agentspec.AgentConfig{ID: "wf2", Type: "workflow"}}
	_, err = s.workflowRunDir("c-none", bare, TriggerMessageRequest{})
	assert.ErrorContains(t, err, "no run dir")

	// Without runners nothing changes: an empty dir stays empty (cwd default).
	local := &Server{repo: repo}
	got, err = local.workflowRunDir("c-none", bare, TriggerMessageRequest{})
	require.NoError(t, err)
	assert.Empty(t, got)
}
