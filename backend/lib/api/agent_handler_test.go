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

	"github.com/AgentDrasil/asgard/backend/lib/config"
	"github.com/AgentDrasil/asgard/backend/lib/db"
	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
	"github.com/AgentDrasil/asgard/pkg/agentspec"
)

func TestHandleAgents(t *testing.T) {
	srv := &Server{
		agents: []*agentspec.Agent{
			{
				Config: agentspec.AgentConfig{
					ID:   "agent-alpha",
					Name: "Agent Alpha",
				},
			},
			{
				Config: agentspec.AgentConfig{
					ID:   "agent-beta",
					Type: "agent",
					Name: "Agent Beta",
				},
			},
			{
				Config: agentspec.AgentConfig{
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
	agent := &agentspec.Agent{
		Config: agentspec.AgentConfig{
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

	workspaceDir := t.TempDir()

	// Create git repo in workspaceDir with .gitignore
	cmd := exec.Command("git", "init", workspaceDir)
	require.NoError(t, cmd.Run())
	err = os.WriteFile(filepath.Join(workspaceDir, ".gitignore"), []byte("scratch/\n*.tmp\n"), 0644)
	require.NoError(t, err)

	agentConfig := &agentspec.AgentConfig{
		Name: "TestAgent",
		MountDirs: agentspec.MountConfig{
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

func TestRecordStatusUpdate_TmpPathDisambiguation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	home, err := os.UserHomeDir()
	require.NoError(t, err)

	testDB := db.NewDBForTest(t)
	err = dbmodels.AutoMigrate(testDB)
	require.NoError(t, err)

	repo := dbmodels.NewSessionRepository(testDB)
	chatID := "test-chat-tmp-disambiguation"
	require.NoError(t, repo.SaveSession(&dbmodels.Session{
		ChatID:       chatID,
		CurrentAgent: "test-agent",
	}))

	workspaceDir := t.TempDir()

	// Initialize git repo with .gitignore containing tmp/
	cmd := exec.Command("git", "init", workspaceDir)
	require.NoError(t, cmd.Run())
	err = os.WriteFile(filepath.Join(workspaceDir, ".gitignore"), []byte("tmp/\nscratch/\n*.tmp\n"), 0644)
	require.NoError(t, err)

	// 1. Session tmp file exists
	sessionTmpDir := filepath.Join(home, "tmp", chatID)
	require.NoError(t, os.MkdirAll(sessionTmpDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(sessionTmpDir, "code_review.md"), []byte("# Review"), 0644))

	// 2. Workspace tmp file exists and gitignored
	wsTmpDir := filepath.Join(workspaceDir, "tmp")
	require.NoError(t, os.MkdirAll(wsTmpDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(wsTmpDir, "local.txt"), []byte("ws local"), 0644))

	agentConfig := &agentspec.AgentConfig{
		Name: "TestAgent",
	}

	update := AgentStatusUpdate{
		Content:   "Updated target files",
		EntryType: "activity",
		Metadata: map[string]any{
			"target_files": []string{
				"tmp/code_review.md", // exists in session tmp only -> normalized to /tmp/code_review.md
				"tmp/local.txt",      // exists in workspace (gitignored) -> kept as tmp/local.txt
				"tmp/unknown.txt",    // neither exists -> kept as tmp/unknown.txt
			},
		},
	}

	recordStatusUpdate(nil, repo, chatID, update, agentConfig, workspaceDir)

	sess, err := repo.GetSession(chatID)
	require.NoError(t, err)
	assert.Contains(t, sess.Artifacts, "/tmp/code_review.md")
	assert.Contains(t, sess.Artifacts, "tmp/local.txt")
	assert.NotContains(t, sess.Artifacts, "/tmp/local.txt")
	assert.Contains(t, sess.Artifacts, "tmp/unknown.txt")

	require.Len(t, sess.Messages, 1)
	msg := sess.Messages[0]
	assert.Contains(t, msg.ArtifactFiles, "/tmp/code_review.md")
	assert.Contains(t, msg.ArtifactFiles, "tmp/local.txt")
	// TargetFiles must preserve original reported values for auditing
	assert.Equal(t, []string{"tmp/code_review.md", "tmp/local.txt", "tmp/unknown.txt"}, msg.TargetFiles)
}

func TestAgentHandler_ModelsFilteredByEnabledProviders(t *testing.T) {
	srv := &Server{
		conf: &config.Config{
			Providers: []string{"agy"},
		},
		agents: []*agentspec.Agent{
			{
				Config: agentspec.AgentConfig{
					ID:   "test-agent",
					Name: "Test Agent",
					CLI: []agentspec.CLITarget{
						{CLI: "agy", Model: "agy-model-1"},
						{CLI: "opencode", Model: "opencode-model-1"},
					},
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
	require.NoError(t, err)
	require.Len(t, res, 1)
	assert.Equal(t, []string{"agy-model-1"}, res[0].Models)
}
