package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/AgentDrasil/asgard/backend/lib/config"
	"github.com/AgentDrasil/asgard/backend/lib/db"
	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
	"github.com/AgentDrasil/asgard/backend/lib/dirlock"
	"github.com/AgentDrasil/asgard/backend/lib/runnerfs"
	"github.com/AgentDrasil/asgard/pkg/agentspec"
)

type stubGitLogFS struct {
	runnerfs.FS
	gitLogFn func(ctx context.Context, runDir string, limit int) (runnerfs.GitLogResult, error)
}

func (s stubGitLogFS) GitLog(ctx context.Context, runDir string, limit int) (runnerfs.GitLogResult, error) {
	if s.gitLogFn != nil {
		return s.gitLogFn(ctx, runDir, limit)
	}
	return runnerfs.GitLogResult{}, nil
}

func setupDirLockTestServer(t *testing.T) (*Server, *dbmodels.SessionRepository, *gorm.DB) {
	t.Helper()
	testDB := db.NewDBForTest(t)
	err := dbmodels.AutoMigrate(testDB)
	require.NoError(t, err)

	repo := dbmodels.NewSessionRepository(testDB)
	tempDir := t.TempDir()
	repo.SetSessionDirFunc(func(chatID string) string {
		return filepath.Join(tempDir, chatID)
	})
	conf := &config.Config{
		Host: "http://localhost:8080",
	}

	server := &Server{
		conf:     conf,
		repo:     repo,
		eventHub: NewSessionEventHub(),
	}
	server.dirLocks = dirlock.NewManager(func(chatID string) {
		server.PublishSessionEvent(chatID, SessionEvent{
			Type: EventTypeStatus,
			Payload: map[string]any{
				"dirLock": server.dirLocks.StatusForChat(chatID),
			},
		})
	})
	server.mux = server.buildMuxLocked()
	return server, repo, testDB
}

func TestGetSessions_DirLockStatus(t *testing.T) {
	t.Parallel()
	server, repo, _ := setupDirLockTestServer(t)

	// Create two sessions in repo
	chat1 := "chat-1111-1111-1111-111111111111"
	chat2 := "chat-2222-2222-2222-222222222222"
	require.NoError(t, repo.UpdateAgentSession(chat1, "test-agent", "Chat 1", "", nil))
	require.NoError(t, repo.UpdateAgentSession(chat2, "test-agent", "Chat 2", "", nil))

	// Chat 1 acquires lock on /data/repo
	ctx := context.Background()
	rel1, err := server.dirLocks.Acquire(ctx, dirlock.AcquireRequest{
		ChatID:   chat1,
		Title:    "Chat 1",
		RunnerID: "runner-1",
		Dirs:     []string{"/data/repo"},
		Mode:     dirlock.ModeExclusive,
	})
	require.NoError(t, err)
	t.Cleanup(func() { rel1(false) })

	// Test GET /api/sessions/{id} for chat1
	req1 := httptest.NewRequest(http.MethodGet, "/api/sessions/"+chat1, nil)
	rr1 := httptest.NewRecorder()
	server.ServeHTTP(rr1, req1)
	assert.Equal(t, http.StatusOK, rr1.Code)

	var sess1 ChatSession
	require.NoError(t, json.Unmarshal(rr1.Body.Bytes(), &sess1))
	require.NotNil(t, sess1.DirLock)
	assert.True(t, sess1.DirLock.IsLocked)
	assert.Equal(t, dirlock.ReasonRunning, sess1.DirLock.LockReason)
	assert.Equal(t, dirlock.ModeExclusive, sess1.DirLock.LockMode)
	assert.Equal(t, "/data/repo", sess1.DirLock.LockedDir)
	assert.False(t, sess1.DirLock.CanUnlock)

	// Test GET /api/sessions/{id} for chat2 (no lock held)
	req2 := httptest.NewRequest(http.MethodGet, "/api/sessions/"+chat2, nil)
	rr2 := httptest.NewRecorder()
	server.ServeHTTP(rr2, req2)
	assert.Equal(t, http.StatusOK, rr2.Code)

	var sess2 ChatSession
	require.NoError(t, json.Unmarshal(rr2.Body.Bytes(), &sess2))
	assert.Nil(t, sess2.DirLock)

	// Test GET /api/sessions lists dirLock status
	reqList := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	rrList := httptest.NewRecorder()
	server.ServeHTTP(rrList, reqList)
	assert.Equal(t, http.StatusOK, rrList.Code)

	var list []ChatSession
	require.NoError(t, json.Unmarshal(rrList.Body.Bytes(), &list))
	require.Len(t, list, 2)
	found1 := false
	for _, s := range list {
		if s.ChatID == chat1 {
			found1 = true
			require.NotNil(t, s.DirLock)
			assert.True(t, s.DirLock.IsLocked)
		}
	}
	assert.True(t, found1)
}

func TestUnlockSessionHandler(t *testing.T) {
	t.Parallel()
	server, repo, _ := setupDirLockTestServer(t)

	ctx := context.Background()

	t.Run("invalid chat id", func(t *testing.T) {
		t.Parallel()
		req := httptest.NewRequest(http.MethodPost, "/api/sessions/invalid@id/unlock", nil)
		rr := httptest.NewRecorder()
		server.ServeHTTP(rr, req)
		assert.Equal(t, http.StatusBadRequest, rr.Code)
	})

	t.Run("session not found", func(t *testing.T) {
		t.Parallel()
		req := httptest.NewRequest(http.MethodPost, "/api/sessions/018f6f00-0000-7000-8000-000000000000/unlock", nil)
		rr := httptest.NewRecorder()
		server.ServeHTTP(rr, req)
		assert.Equal(t, http.StatusNotFound, rr.Code)
	})

	t.Run("unlock when no lock held returns unlocked false", func(t *testing.T) {
		t.Parallel()
		chatID := "chat-nolock-1111-1111-111111111111"
		require.NoError(t, repo.UpdateAgentSession(chatID, "test-agent", "No Lock Chat", "", nil))

		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/sessions/%s/unlock", chatID), nil)
		rr := httptest.NewRecorder()
		server.ServeHTTP(rr, req)
		assert.Equal(t, http.StatusOK, rr.Code)

		var resp map[string]any
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
		assert.Equal(t, "unlocked", resp["status"])
		assert.Equal(t, chatID, resp["chatId"])
		assert.Equal(t, false, resp["unlocked"])
	})

	t.Run("unlock when actively running returns 409 conflict", func(t *testing.T) {
		t.Parallel()
		chatID := "chat-running-1111-1111-111111111111"
		require.NoError(t, repo.UpdateAgentSession(chatID, "test-agent", "Running Chat", "", nil))

		rel, err := server.dirLocks.Acquire(ctx, dirlock.AcquireRequest{
			ChatID:   chatID,
			Title:    "Running Chat",
			RunnerID: "runner-1",
			Dirs:     []string{"/repo-running"},
			Mode:     dirlock.ModeExclusive,
		})
		require.NoError(t, err)
		t.Cleanup(func() { rel(false) })

		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/sessions/%s/unlock", chatID), nil)
		rr := httptest.NewRecorder()
		server.ServeHTTP(rr, req)
		assert.Equal(t, http.StatusConflict, rr.Code)
	})

	t.Run("unlock when idle uncommitted returns 200 unlocked true", func(t *testing.T) {
		t.Parallel()
		chatID := "chat-idle-1111-1111-111111111111"
		require.NoError(t, repo.UpdateAgentSession(chatID, "test-agent", "Idle Chat", "", nil))

		rel, err := server.dirLocks.Acquire(ctx, dirlock.AcquireRequest{
			ChatID:   chatID,
			Title:    "Idle Chat",
			RunnerID: "runner-1",
			Dirs:     []string{"/repo-idle"},
			Mode:     dirlock.ModeExclusive,
		})
		require.NoError(t, err)

		// Release and retain uncommitted lock
		rel(true)

		st := server.dirLocks.StatusForChat(chatID)
		require.NotNil(t, st)
		assert.True(t, st.CanUnlock)

		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/sessions/%s/unlock", chatID), nil)
		rr := httptest.NewRecorder()
		server.ServeHTTP(rr, req)
		assert.Equal(t, http.StatusOK, rr.Code)

		var resp map[string]any
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
		assert.Equal(t, "unlocked", resp["status"])
		assert.Equal(t, chatID, resp["chatId"])
		assert.Equal(t, true, resp["unlocked"])

		assert.Nil(t, server.dirLocks.StatusForChat(chatID))
	})
}

func TestArchiveAndDeletedSession_ReleasesLockAndUnblocksQueued(t *testing.T) {
	t.Parallel()

	t.Run("archive releases lock and unblocks queued session", func(t *testing.T) {
		t.Parallel()
		server, repo, _ := setupDirLockTestServer(t)

		chat1 := "chat-1111-1111-1111-111111111111"
		chat2 := "chat-2222-2222-2222-222222222222"
		require.NoError(t, repo.UpdateAgentSession(chat1, "test-agent", "Chat 1", "", nil))
		require.NoError(t, repo.UpdateAgentSession(chat2, "test-agent", "Chat 2", "", nil))

		ctx := context.Background()

		// Chat 1 acquires lock
		_, err := server.dirLocks.Acquire(ctx, dirlock.AcquireRequest{
			ChatID:   chat1,
			RunnerID: "runner-1",
			Dirs:     []string{"/repo"},
			Mode:     dirlock.ModeExclusive,
		})
		require.NoError(t, err)

		// Chat 2 attempts to acquire lock and queues
		chat2Acquired := make(chan func(bool), 1)
		go func() {
			rel2, err2 := server.dirLocks.Acquire(ctx, dirlock.AcquireRequest{
				ChatID:   chat2,
				RunnerID: "runner-1",
				Dirs:     []string{"/repo"},
				Mode:     dirlock.ModeExclusive,
			})
			if err2 == nil {
				chat2Acquired <- rel2
			}
		}()

		assert.Eventually(t, func() bool {
			st := server.dirLocks.StatusForChat(chat2)
			return st != nil && st.IsWaitingForLock
		}, 2*time.Second, 10*time.Millisecond)

		// Archive Chat 1 via API
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/sessions/%s/archive", chat1), nil)
		rr := httptest.NewRecorder()
		server.ServeHTTP(rr, req)
		assert.Equal(t, http.StatusOK, rr.Code)

		// Chat 2 must now be granted
		select {
		case rel2 := <-chat2Acquired:
			st2 := server.dirLocks.StatusForChat(chat2)
			require.NotNil(t, st2)
			assert.True(t, st2.IsLocked)
			rel2(false)
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for chat 2 after chat 1 archived")
		}
	})

	t.Run("delete session releases lock and unblocks queued session", func(t *testing.T) {
		t.Parallel()
		server, repo, _ := setupDirLockTestServer(t)

		chat1 := "chat-3333-3333-3333-333333333333"
		chat2 := "chat-4444-4444-4444-444444444444"
		require.NoError(t, repo.UpdateAgentSession(chat1, "test-agent", "Chat 3", "", nil))
		require.NoError(t, repo.UpdateAgentSession(chat2, "test-agent", "Chat 4", "", nil))

		ctx := context.Background()

		// Chat 1 acquires lock
		_, err := server.dirLocks.Acquire(ctx, dirlock.AcquireRequest{
			ChatID:   chat1,
			RunnerID: "runner-1",
			Dirs:     []string{"/repo"},
			Mode:     dirlock.ModeExclusive,
		})
		require.NoError(t, err)

		// Chat 2 attempts to acquire lock and queues
		chat2Acquired := make(chan func(bool), 1)
		go func() {
			rel2, err2 := server.dirLocks.Acquire(ctx, dirlock.AcquireRequest{
				ChatID:   chat2,
				RunnerID: "runner-1",
				Dirs:     []string{"/repo"},
				Mode:     dirlock.ModeExclusive,
			})
			if err2 == nil {
				chat2Acquired <- rel2
			}
		}()

		assert.Eventually(t, func() bool {
			st := server.dirLocks.StatusForChat(chat2)
			return st != nil && st.IsWaitingForLock
		}, 2*time.Second, 10*time.Millisecond)

		// Delete Chat 1 via API
		req := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/sessions?chat_id=%s", chat1), nil)
		rr := httptest.NewRecorder()
		server.ServeHTTP(rr, req)
		assert.Equal(t, http.StatusOK, rr.Code)

		// Chat 2 must now be granted
		select {
		case rel2 := <-chat2Acquired:
			st2 := server.dirLocks.StatusForChat(chat2)
			require.NotNil(t, st2)
			assert.True(t, st2.IsLocked)
			rel2(false)
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for chat 2 after chat 1 deleted")
		}
	})
}

func TestAcquireChatDirLock_FailClosedOnDBError(t *testing.T) {
	t.Parallel()
	server, _, testDB := setupDirLockTestServer(t)

	// Close the underlying SQL DB so subsequent DB calls fail
	sqlDB, err := testDB.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	ctx := context.Background()
	rel, err := server.acquireChatDirLock(ctx, "chat-db-fail", nil, "/repo")
	assert.Error(t, err)
	assert.Nil(t, rel)
	assert.ErrorContains(t, err, "failed to get session metadata for chat chat-db-fail")
}

func TestHandleGitLog_AutoUnlockGuardedAgainstWaitingHumanAndRunning(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		waitingHuman bool
		running      bool
		askUser      bool
		wantUnlocked bool
	}{
		{
			name:         "clean git and idle completes auto-unlock",
			wantUnlocked: true,
		},
		{
			name:         "waiting_human blocks auto-unlock",
			waitingHuman: true,
			wantUnlocked: false,
		},
		{
			name:         "running blocks auto-unlock",
			running:      true,
			wantUnlocked: false,
		},
		{
			name:         "unreplied ask_user blocks auto-unlock",
			askUser:      true,
			wantUnlocked: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server, repo, _ := setupDirLockTestServer(t)

			chatID := "chat-" + uuidV7Str(t)
			require.NoError(t, repo.UpdateAgentSession(chatID, "test-agent", "Git Test", "", nil))

			if tt.waitingHuman {
				require.NoError(t, repo.UpdateAgentStatus(chatID, "test-agent", dbmodels.AgentStatusWaitingHuman))
			} else if tt.running {
				require.NoError(t, repo.UpdateAgentStatus(chatID, "test-agent", dbmodels.AgentStatusRunning))
			}
			if tt.askUser {
				require.NoError(t, repo.AppendMessage(chatID, dbmodels.ChatMessage{
					ID:      "msg-1",
					Role:    "ask_user",
					Replied: false,
				}))
			}

			// Chat acquires lock and retains uncommitted
			ctx := context.Background()
			rel, err := server.acquireChatDirLock(ctx, chatID, nil, "/repo")
			require.NoError(t, err)
			rel(true)

			st := server.dirLocks.StatusForChat(chatID)
			require.NotNil(t, st)
			assert.True(t, st.IsLocked)
			assert.True(t, st.CanUnlock)

			// Mock chatFS to return 0 unstashed changes
			server.fsResolver = func(cid string) (runnerfs.FS, error) {
				return stubGitLogFS{
					gitLogFn: func(ctx context.Context, runDir string, limit int) (runnerfs.GitLogResult, error) {
						return runnerfs.GitLogResult{
							Commits:        nil,
							UnstashedCount: 0,
						}, nil
					},
				}, nil
			}

			req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/git/log?session_id=%s", chatID), nil)
			rr := httptest.NewRecorder()
			server.ServeHTTP(rr, req)
			assert.Equal(t, http.StatusOK, rr.Code)

			afterSt := server.dirLocks.StatusForChat(chatID)
			if tt.wantUnlocked {
				assert.Nil(t, afterSt)
			} else {
				require.NotNil(t, afterSt)
				assert.True(t, afterSt.IsLocked)
			}
		})
	}
}

func TestHasUncommittedChanges_ChecksAllLockedDirs(t *testing.T) {
	t.Parallel()
	server, _, _ := setupDirLockTestServer(t)

	chatID := "chat-multidir"
	checkedDirs := make(map[string]bool)
	var checkMu sync.Mutex

	server.fsResolver = func(cid string) (runnerfs.FS, error) {
		return stubGitLogFS{
			gitLogFn: func(ctx context.Context, runDir string, limit int) (runnerfs.GitLogResult, error) {
				checkMu.Lock()
				checkedDirs[runDir] = true
				checkMu.Unlock()
				if runDir == "/mount/data/repo-b" {
					return runnerfs.GitLogResult{UnstashedCount: 2}, nil
				}
				return runnerfs.GitLogResult{UnstashedCount: 0}, nil
			},
		}, nil
	}

	cfg := &agentspec.AgentConfig{
		MountDirs: agentspec.MountConfig{
			ReadWrite: []string{"/mount/data/repo-b"},
		},
	}

	ctx := context.Background()
	rel, err := server.acquireChatDirLock(ctx, chatID, &agentspec.Agent{Config: *cfg}, "/repo-a")
	require.NoError(t, err)
	defer rel(false)

	hasDirty := server.hasUncommittedChanges(chatID, "/repo-a")
	assert.True(t, hasDirty)

	checkMu.Lock()
	defer checkMu.Unlock()
	assert.True(t, checkedDirs["/repo-a"])
	assert.True(t, checkedDirs["/mount/data/repo-b"])
}

func uuidV7Str(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("%08x-0000-7000-8000-000000000000", time.Now().UnixNano()%0xffffffff)
}
