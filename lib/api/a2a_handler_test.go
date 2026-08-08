package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/lib/agents"
	"github.com/AgentDrasil/asgard/lib/config"
	"github.com/AgentDrasil/asgard/lib/db"
	"github.com/AgentDrasil/asgard/lib/dbmodels"
)

func TestNewAgentHandler(t *testing.T) {
	testDB := db.NewDBForTest(t)
	repo := dbmodels.NewSessionRepository(testDB)

	agent := &agents.Agent{
		Config: agents.AgentConfig{
			ID:          "test-agent",
			Name:        "Test Agent",
			Description: "A test agent for testing A2A integration",
		},
		Path: "/dummy/path",
	}

	conf := &config.Config{Host: "http://localhost:8080"}
	handler, card := NewAgentHandler(agent, conf, repo, nil)
	assert.NotNil(t, handler)
	assert.NotNil(t, card)
	assert.Equal(t, "Test Agent", card.Name)
	assert.Equal(t, "A test agent for testing A2A integration", card.Description)
	assert.Equal(t, "1.0.0", card.Version)

	// Test request to A2A REST handler
	req := httptest.NewRequest(http.MethodGet, "/tasks", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	// A2A REST handler should return JSON content type
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
}

func TestHandleAgents(t *testing.T) {
	srv := &Server{
		agents: []*agents.Agent{
			{
				Config: agents.AgentConfig{
					Name: "Agent Alpha",
				},
			},
			{
				Config: agents.AgentConfig{
					Name: "Agent Beta",
				},
			},
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/agents", nil)
	w := httptest.NewRecorder()

	srv.handleAgents(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

	var res []AgentInfo
	err := json.Unmarshal(w.Body.Bytes(), &res)
	assert.NoError(t, err)
	assert.Len(t, res, 2)
	assert.Equal(t, "Agent Alpha", res[0].Name)
	assert.Equal(t, "Agent Beta", res[1].Name)
}

func TestAgentCardInternalHandler(t *testing.T) {
	srv := &Server{
		conf: &config.Config{
			Host: "https://public.example.com",
			Port: 8080,
		},
		agents: []*agents.Agent{
			{
				Config: agents.AgentConfig{
					ID:   "test-agent",
					Name: "Test Agent",
				},
			},
		},
	}

	mux := srv.buildMuxLocked()

	// Default request returns public host URL
	reqPublic := httptest.NewRequest(http.MethodGet, "/agents/test-agent/.well-known/agent-card.json", nil)
	wPublic := httptest.NewRecorder()
	mux.ServeHTTP(wPublic, reqPublic)
	assert.Equal(t, http.StatusOK, wPublic.Code)
	assert.Contains(t, wPublic.Body.String(), "https://public.example.com/agents/test-agent")

	// Internal query parameter request returns internal localhost URL
	reqInternalQuery := httptest.NewRequest(http.MethodGet, "/agents/test-agent/.well-known/agent-card.json?internal=true", nil)
	wInternalQuery := httptest.NewRecorder()
	mux.ServeHTTP(wInternalQuery, reqInternalQuery)
	assert.Equal(t, http.StatusOK, wInternalQuery.Code)
	assert.Contains(t, wInternalQuery.Body.String(), "http://127.0.0.1:8080/agents/test-agent")

	// Internal header request returns internal localhost URL
	reqInternalHeader := httptest.NewRequest(http.MethodGet, "/agents/test-agent/.well-known/agent-card.json", nil)
	reqInternalHeader.Header.Set("X-Internal", "true")
	wInternalHeader := httptest.NewRecorder()
	mux.ServeHTTP(wInternalHeader, reqInternalHeader)
	assert.Equal(t, http.StatusOK, wInternalHeader.Code)
	assert.Contains(t, wInternalHeader.Body.String(), "http://127.0.0.1:8080/agents/test-agent")
}

func TestExecuteValidation(t *testing.T) {
	agent := &agents.Agent{
		Config: agents.AgentConfig{
			ID:      "test-agent",
			Name:    "Test Agent",
			RunDirs: []string{"/tmp"},
		},
	}
	executor := &agentExecutor{agent: agent}

	// Test invalid chatID fails validation
	execCtxInvalidChat := &a2asrv.ExecutorContext{
		ContextID: "invalid id !@#$",
	}
	seq := executor.Execute(t.Context(), execCtxInvalidChat)
	for _, err := range seq {
		if err != nil {
			assert.Contains(t, err.Error(), "invalid chatID format")
		}
	}
}

func TestRecordStatusUpdateArtifactFiltering(t *testing.T) {
	testDB := db.NewDBForTest(t)
	err := dbmodels.AutoMigrate(testDB)
	require.NoError(t, err)

	repo := dbmodels.NewSessionRepository(testDB)

	chatID := "test-chat-artifact-filtering"
	err = repo.SaveSession(&dbmodels.Session{
		ChatID:       chatID,
		CurrentAgent: "test-agent",
	})
	require.NoError(t, err)

	workspaceDir, err := os.MkdirTemp("", "asgard-a2a-test-*")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(workspaceDir) })

	// Create git repo in workspaceDir with .gitignore
	cmd := exec.Command("git", "init", workspaceDir)
	require.NoError(t, cmd.Run())
	err = os.WriteFile(filepath.Join(workspaceDir, ".gitignore"), []byte("scratch/\n*.tmp\n"), 0644)
	require.NoError(t, err)

	agentConfig := &agents.AgentConfig{
		Name: "TestAgent",
		MountDirs: agents.MountConfig{
			ReadWrite: []string{"/tmp/custom_rw_dir"},
		},
	}

	update := AgentStatusUpdate{
		Content:   "Updated target files",
		EntryType: "activity",
		Metadata: map[string]any{
			"target_files": []string{
				"src/main.go",                  // unignored source file -> not artifact
				"scratch/demo.py",              // ignored in gitignore -> artifact
				"/tmp/custom_rw_dir/data.json", // in agent rw path -> artifact
			},
		},
	}

	recordStatusUpdate(repo, chatID, update, agentConfig, workspaceDir)

	sess, err := repo.GetSession(chatID)
	require.NoError(t, err)
	assert.Contains(t, sess.Artifacts, "scratch/demo.py")
	assert.Contains(t, sess.Artifacts, "/tmp/custom_rw_dir/data.json")
	assert.NotContains(t, sess.Artifacts, "src/main.go")
}
