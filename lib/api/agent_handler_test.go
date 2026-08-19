package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/lib/agents"
	"github.com/AgentDrasil/asgard/lib/db"
	"github.com/AgentDrasil/asgard/lib/dbmodels"
)

func TestHandleAgents(t *testing.T) {
	srv := &Server{
		agents: []*agents.Agent{
			{
				Config: agents.AgentConfig{
					ID:   "agent-alpha",
					Name: "Agent Alpha",
				},
			},
			{
				Config: agents.AgentConfig{
					ID:   "agent-beta",
					Type: "agent",
					Name: "Agent Beta",
				},
			},
			{
				Config: agents.AgentConfig{
					ID:   "workflow-gamma",
					Type: "workflow",
					Name: "Workflow Gamma",
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
	assert.Len(t, res, 3)
	assert.Equal(t, "Agent Alpha", res[0].Name)
	assert.Equal(t, "agent", res[0].Type)
	assert.Equal(t, "Agent Beta", res[1].Name)
	assert.Equal(t, "agent", res[1].Type)
	assert.Equal(t, "Workflow Gamma", res[2].Name)
	assert.Equal(t, "workflow", res[2].Type)

	assert.Contains(t, w.Body.String(), "\"type\":\"workflow\"")
	assert.Contains(t, w.Body.String(), "\"type\":\"agent\"")
}

func TestExecuteValidation(t *testing.T) {
	agent := &agents.Agent{
		Config: agents.AgentConfig{
			ID:      "test-agent",
			Name:    "Test Agent",
			RunDirs: []string{"/tmp"},
		},
	}
	executor := NewSingleAgentExecutor(agent, nil, nil, nil, nil)

	// Test invalid chatID fails validation
	_, err := executor.Execute(t.Context(), SingleAgentRunParams{
		ChatID: "invalid id !@#$",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid chatID format")
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

	workspaceDir, err := os.MkdirTemp("", "asgard-agent-test-*")
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

	recordStatusUpdate(nil, repo, chatID, update, agentConfig, workspaceDir)

	sess, err := repo.GetSession(chatID)
	require.NoError(t, err)
	assert.Contains(t, sess.Artifacts, "scratch/demo.py")
	assert.Contains(t, sess.Artifacts, "/tmp/custom_rw_dir/data.json")
	assert.NotContains(t, sess.Artifacts, "src/main.go")

	require.Len(t, sess.Messages, 1)
	msg := sess.Messages[0]
	assert.Contains(t, msg.ArtifactFiles, "scratch/demo.py")
	assert.Contains(t, msg.ArtifactFiles, "/tmp/custom_rw_dir/data.json")
	assert.NotContains(t, msg.ArtifactFiles, "src/main.go")
	assert.Contains(t, msg.TargetFiles, "src/main.go")
}
