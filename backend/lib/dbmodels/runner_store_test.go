package dbmodels

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/moznion/go-optional"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/AgentDrasil/asgard/backend/lib/db"
	"github.com/AgentDrasil/asgard/backend/lib/runnerfs"
	runnerpaths "github.com/AgentDrasil/asgard/pkg/paths/runner"
)

// localResolver serves every chat from this host's layout through runnerfs,
// the same code the runner runs behind RunnerService. HOME is redirected to a
// temp dir, and the repository's own local session dir points at a sentinel
// that must stay untouched.
func localResolver(t *testing.T) (FSResolver, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	sentinel := filepath.Join(t.TempDir(), "backend-local")
	return func(chatID string) (runnerfs.FS, error) { return runnerfs.NewLocal(chatID), nil }, sentinel
}

func TestSessionRepository_TranscriptThroughRunnerFS(t *testing.T) {
	resolve, sentinel := localResolver(t)
	testDB := db.NewDBForTest(t)
	require.NoError(t, testDB.AutoMigrate(&Session{}))
	repo := NewSessionRepository(testDB)
	repo.SetSessionDirFunc(func(chatID string) string { return filepath.Join(sentinel, chatID) })
	repo.SetFSResolver(resolve)

	const chat = "chat-rfs-1"
	require.NoError(t, repo.UpsertSession(&Session{ChatID: chat}))

	require.NoError(t, repo.AppendMessage(chat, ChatMessage{Role: "user", Content: "hi"}))
	require.NoError(t, repo.AppendMessage(chat, ChatMessage{ID: "ask-1", Role: "ask_user", Content: "ok?"}))
	// Stable id: updated in place, count unchanged.
	require.NoError(t, repo.AppendMessage(chat, ChatMessage{ID: "ask-1", Role: "ask_user", Content: "ok??"}))

	sess, err := repo.GetSession(chat)
	require.NoError(t, err)
	require.Len(t, sess.Messages, 2)
	assert.Equal(t, "ok??", sess.Messages[1].Content)
	assert.Equal(t, 2, sess.MessageCount)
	assert.True(t, sess.HasAskUserUnreplied)

	upd, err := repo.MarkAskUserReplied(chat, "ask-1", "yes")
	require.NoError(t, err)
	require.NotNil(t, upd)
	assert.True(t, upd.Replied)
	sess, err = repo.GetSession(chat)
	require.NoError(t, err)
	assert.False(t, sess.HasAskUserUnreplied)
	assert.Equal(t, "yes", sess.Messages[1].ReplyText)

	// The runner's copy is the file the sandbox reads as /session/messages.jsonl.
	assert.FileExists(t, filepath.Join(runnerpaths.SessionDir(chat), "messages.jsonl"))
	// The backend never created its own per-chat directory.
	assert.NoDirExists(t, filepath.Join(sentinel, chat))

	meta, err := repo.GetSessionMeta(chat)
	require.NoError(t, err)
	assert.Empty(t, meta.Messages)
}

func TestSessionRepository_TranscriptRunnerOffline(t *testing.T) {
	testDB := db.NewDBForTest(t)
	require.NoError(t, testDB.AutoMigrate(&Session{}))
	repo := NewSessionRepository(testDB)
	repo.SetFSResolver(func(string) (runnerfs.FS, error) {
		return nil, runnerfs.Errf(runnerfs.CodeUnavailable, "runner offline")
	})
	require.NoError(t, repo.UpsertSession(&Session{ChatID: "chat-off"}))

	err := repo.AppendMessage("chat-off", ChatMessage{Role: "user", Content: "x"})
	require.Error(t, err)
	assert.True(t, IsUnavailable(err))
	_, err = repo.GetSession("chat-off")
	require.Error(t, err)
	assert.True(t, IsUnavailable(err))
	// Metadata stays readable.
	meta, err := repo.GetSessionMeta("chat-off")
	require.NoError(t, err)
	assert.NotNil(t, meta)
}

func TestWorkflowRunRepository_OffloadThroughRunnerFS(t *testing.T) {
	resolve, sentinel := localResolver(t)
	testDB := db.NewDBForTest(t)
	require.NoError(t, testDB.AutoMigrate(&WorkflowRun{}))
	repo := NewWorkflowRunRepository(testDB)
	repo.SetSessionDirFunc(func(id string) string { return filepath.Join(sentinel, id) })
	repo.SetFSResolver(resolve)

	states, err := EncodeNodeStates(map[string]NodeState{"n1": {Output: "node output"}})
	require.NoError(t, err)
	run := &WorkflowRun{RunID: "run-1", SessionID: "chat-wf-1", Status: WorkflowStatusRunning, DAGSpec: "name: x\n", Input: "in", NodeStates: states}
	require.NoError(t, repo.SaveRun(run))

	assert.Equal(t, "/session/workflows/run-1/dag_spec.yaml", run.DAGSpecPath)
	assert.Equal(t, "/session/workflows/run-1/input.txt", run.InputPath)
	data, err := os.ReadFile(filepath.Join(runnerpaths.SessionDir("chat-wf-1"), "workflows", "run-1", "nodes", "n1.log"))
	require.NoError(t, err)
	assert.Equal(t, "node output", string(data))
	assert.NoDirExists(t, filepath.Join(sentinel, "chat-wf-1"))

	got, err := repo.GetRun("run-1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "name: x\n", got.DAGSpec)
	assert.Equal(t, "in", got.Input)
	hydrated, err := DecodeNodeStates(got.NodeStates)
	require.NoError(t, err)
	assert.Equal(t, "node output", hydrated["n1"].Output)

	// A missing offloaded file keeps its typed error through the runner.
	require.NoError(t, os.Remove(filepath.Join(runnerpaths.SessionDir("chat-wf-1"), "workflows", "run-1", "input.txt")))
	_, err = repo.GetRun("run-1")
	assert.ErrorIs(t, err, ErrOffloadedFileMissing)
}

func TestResetOrphanedQuotaSuspensions_DeferredUntilRunnerOnline(t *testing.T) {
	resolve, _ := localResolver(t)
	testDB := db.NewDBForTest(t)
	require.NoError(t, testDB.AutoMigrate(&Session{}))
	repo := NewSessionRepository(testDB)

	const chat = "chat-quota-1"
	require.NoError(t, repo.UpsertSession(&Session{ChatID: chat, RunnerNodeID: "r1"}))
	require.NoError(t, repo.UpdateAgentSession(chat, "a", "k", "s", optional.None[string]()))
	var sess Session
	require.NoError(t, testDB.First(&sess, "chat_id = ?", chat).Error)
	sess.Agents[0].Status = AgentStatusWaitingHuman
	sess.RunnerNodeID = "r1"
	require.NoError(t, testDB.Save(&sess).Error)

	online := false
	repo.SetFSResolver(func(id string) (runnerfs.FS, error) {
		if !online {
			return nil, runnerfs.Errf(runnerfs.CodeUnavailable, "runner offline")
		}
		return resolve(id)
	})
	// The ask exists on the runner already.
	online = true
	require.NoError(t, repo.AppendMessage(chat, ChatMessage{ID: QuotaAskMessagePrefix + "1", Role: "ask_user", Content: "quota?", AgentName: "a"}))

	// Backend restart: runner offline, reset deferred.
	online = false
	require.NoError(t, repo.ResetOrphanedQuotaSuspensions("restarted"))
	repo.ResetPendingQuotaSuspensions("other-runner") // wrong runner: untouched
	meta, err := repo.GetSessionMeta(chat)
	require.NoError(t, err)
	assert.True(t, meta.HasAskUserUnreplied)

	online = true
	repo.ResetPendingQuotaSuspensions("r1")
	repo.ResetPendingQuotaSuspensions("r1") // idempotent
	got, err := repo.GetSession(chat)
	require.NoError(t, err)
	require.Len(t, got.Messages, 1)
	assert.True(t, got.Messages[0].Replied)
	assert.Equal(t, "restarted", got.Messages[0].ReplyText)
	assert.False(t, got.HasAskUserUnreplied)
	assert.Equal(t, AgentStatusCompleted, got.Agents[0].Status)
}

func TestResetOrphanedQuotaSuspensions_DeferredKeepsPostRestartAsks(t *testing.T) {
	resolve, _ := localResolver(t)
	testDB := db.NewDBForTest(t)
	require.NoError(t, testDB.AutoMigrate(&Session{}))
	repo := NewSessionRepository(testDB)
	online := true
	repo.SetFSResolver(func(id string) (runnerfs.FS, error) {
		if !online {
			return nil, runnerfs.Errf(runnerfs.CodeUnavailable, "runner offline")
		}
		return resolve(id)
	})

	const chat = "chat-quota-2"
	require.NoError(t, repo.UpsertSession(&Session{ChatID: chat}))
	require.NoError(t, testDB.Model(&Session{}).Where("chat_id = ?", chat).Update("runner_node_id", "r1").Error)
	old := ChatMessage{ID: QuotaAskMessagePrefix + "old", Role: "ask_user", Content: "q", AgentName: "a", Timestamp: time.Now().Add(-time.Hour).UnixMilli()}
	require.NoError(t, repo.AppendMessage(chat, old))

	online = false
	require.NoError(t, repo.ResetOrphanedQuotaSuspensions("restarted"))

	// After the restart a live waiter raises a new quota ask; a concurrent
	// status update must survive the deferred reset as well.
	online = true
	time.Sleep(5 * time.Millisecond)
	fresh := ChatMessage{ID: QuotaAskMessagePrefix + "new", Role: "ask_user", Content: "q2", AgentName: "a", Timestamp: time.Now().UnixMilli()}
	require.NoError(t, repo.AppendMessage(chat, fresh))
	require.NoError(t, repo.UpdateAgentStatus(chat, "a", AgentStatusWaitingHuman))

	repo.ResetPendingQuotaSuspensions("r1")
	got, err := repo.GetSession(chat)
	require.NoError(t, err)
	require.Len(t, got.Messages, 2)
	assert.True(t, got.Messages[0].Replied, "pre-restart orphan retired")
	assert.False(t, got.Messages[1].Replied, "post-restart ask untouched")
	assert.True(t, got.HasAskUserUnreplied)
	require.Len(t, got.Agents, 1)
	assert.Equal(t, AgentStatusWaitingHuman, got.Agents[0].Status, "live waiter keeps its status")
}

func TestResetOrphanedQuotaSuspensions_ConvergesAfterDBFailure(t *testing.T) {
	resolve, _ := localResolver(t)
	testDB := db.NewDBForTest(t)
	require.NoError(t, testDB.AutoMigrate(&Session{}))
	repo := NewSessionRepository(testDB)
	repo.SetFSResolver(resolve)

	for _, chat := range []string{"chat-conv-1", "chat-conv-2"} {
		require.NoError(t, repo.UpsertSession(&Session{ChatID: chat}))
		require.NoError(t, testDB.Model(&Session{}).Where("chat_id = ?", chat).Update("runner_node_id", "r1").Error)
		require.NoError(t, repo.AppendMessage(chat, ChatMessage{ID: QuotaAskMessagePrefix + "1", Role: "ask_user", Content: "q", AgentName: "a", Timestamp: time.Now().Add(-time.Hour).UnixMilli()}))
		require.NoError(t, repo.UpdateAgentStatus(chat, "a", AgentStatusWaitingHuman))
	}

	// Fail the reconcile (second sessions update: the first is the journal) of
	// the first chat only; the sweep must go on to the other chat.
	fail := true
	updates := 0
	require.NoError(t, testDB.Callback().Update().Before("gorm:update").Register("test:fail", func(tx *gorm.DB) {
		if fail && tx.Statement.Table == "sessions" {
			updates++
			if updates < 2 {
				return
			}
			fail = false
			_ = tx.AddError(errors.New("injected db failure"))
		}
	}))

	require.NoError(t, repo.ResetOrphanedQuotaSuspensions("restarted"))

	// Exactly one chat is stale in the DB although its transcript is marked.
	stale := 0
	for _, chat := range []string{"chat-conv-1", "chat-conv-2"} {
		got, err := repo.GetSession(chat)
		require.NoError(t, err)
		assert.True(t, got.Messages[0].Replied)
		if got.HasAskUserUnreplied {
			stale++
			assert.Equal(t, AgentStatusWaitingHuman, got.Agents[0].Status)
		} else {
			assert.Equal(t, AgentStatusCompleted, got.Agents[0].Status)
		}
	}
	require.Equal(t, 1, stale)

	repo.ResetPendingQuotaSuspensions("r1")
	for _, chat := range []string{"chat-conv-1", "chat-conv-2"} {
		got, err := repo.GetSession(chat)
		require.NoError(t, err)
		assert.False(t, got.HasAskUserUnreplied, chat)
		assert.Equal(t, AgentStatusCompleted, got.Agents[0].Status, chat)
	}
}

// quotaChat sets up a chat bound to r1 with a WaitingHuman agent "a" and an
// old unreplied quota ask of that agent.
func quotaChat(t *testing.T, repo *SessionRepository, testDB *gorm.DB, chat string) {
	t.Helper()
	require.NoError(t, repo.UpsertSession(&Session{ChatID: chat}))
	require.NoError(t, testDB.Model(&Session{}).Where("chat_id = ?", chat).Update("runner_node_id", "r1").Error)
	require.NoError(t, repo.AppendMessage(chat, ChatMessage{ID: QuotaAskMessagePrefix + "1", Role: "ask_user", Content: "q", AgentName: "a", Timestamp: time.Now().Add(-time.Hour).UnixMilli()}))
	require.NoError(t, repo.UpdateAgentStatus(chat, "a", AgentStatusWaitingHuman))
}

func TestResetOrphanedQuotaSuspensions_JournalResumesAfterInterruption(t *testing.T) {
	resolve, _ := localResolver(t)
	for _, stage := range []string{"journaled", "marked"} {
		t.Run(stage, func(t *testing.T) {
			testDB := db.NewDBForTest(t)
			require.NoError(t, testDB.AutoMigrate(&Session{}))
			repo := NewSessionRepository(testDB)
			repo.SetFSResolver(resolve)
			chat := "chat-journal-" + stage
			quotaChat(t, repo, testDB, chat)

			repo.quotaResetHook = func(s string) error {
				if s == stage {
					return errors.New("interrupted")
				}
				return nil
			}
			require.NoError(t, repo.ResetOrphanedQuotaSuspensions("restarted"))
			meta, err := repo.GetSessionMeta(chat)
			require.NoError(t, err)
			require.NotNil(t, meta.QuotaResetIDs, "journal survives the interruption")
			assert.True(t, meta.HasAskUserUnreplied)

			// Next start: the journal drives the resume.
			repo.quotaResetHook = nil
			repo.pendingQuotaReset.Delete(chat)
			require.NoError(t, repo.ResetOrphanedQuotaSuspensions("restarted"))
			got, err := repo.GetSession(chat)
			require.NoError(t, err)
			assert.True(t, got.Messages[0].Replied)
			assert.False(t, got.HasAskUserUnreplied)
			assert.Equal(t, AgentStatusCompleted, got.Agents[0].Status)
			assert.Nil(t, got.QuotaResetIDs)
		})
	}
}

func TestResetOrphanedQuotaSuspensions_UserReplyEqualToNoteIsNotAMarker(t *testing.T) {
	resolve, _ := localResolver(t)
	testDB := db.NewDBForTest(t)
	require.NoError(t, testDB.AutoMigrate(&Session{}))
	repo := NewSessionRepository(testDB)
	repo.SetFSResolver(resolve)
	const chat = "chat-marker"
	quotaChat(t, repo, testDB, chat)
	// The user genuinely answered the quota ask with the same text as the note.
	_, err := repo.MarkAskUserReplied(chat, QuotaAskMessagePrefix+"1", "restarted")
	require.NoError(t, err)
	// The agent is waiting on something else now.
	require.NoError(t, repo.UpdateAgentStatus(chat, "a", AgentStatusWaitingHuman))

	require.NoError(t, repo.ResetOrphanedQuotaSuspensions("restarted"))
	got, err := repo.GetSession(chat)
	require.NoError(t, err)
	assert.Equal(t, AgentStatusWaitingHuman, got.Agents[0].Status, "no journal, nothing to reconcile")
}

func TestResetOrphanedQuotaSuspensions_WorkflowHumanAgentNotFlipped(t *testing.T) {
	resolve, _ := localResolver(t)
	testDB := db.NewDBForTest(t)
	require.NoError(t, testDB.AutoMigrate(&Session{}, &WorkflowRun{}))
	repo := NewSessionRepository(testDB)
	repo.SetFSResolver(resolve)
	const chat = "chat-wfhuman"
	quotaChat(t, repo, testDB, chat)
	// Another agent waits on a workflow human node; a regular ask is open.
	require.NoError(t, repo.UpdateAgentStatus(chat, "wf-agent", AgentStatusWaitingHuman))
	require.NoError(t, repo.AppendMessage(chat, ChatMessage{ID: "ask-wf", Role: "ask_user", Content: "approve?", AgentName: "wf-agent"}))

	require.NoError(t, repo.ResetOrphanedQuotaSuspensions("restarted"))
	got, err := repo.GetSession(chat)
	require.NoError(t, err)
	status := map[string]AgentStatus{}
	for _, a := range got.Agents {
		status[a.Name] = a.Status
	}
	assert.Equal(t, AgentStatusCompleted, status["a"], "owner of the retired quota ask")
	assert.Equal(t, AgentStatusWaitingHuman, status["wf-agent"], "other agent keeps waiting")
	assert.True(t, got.HasAskUserUnreplied)

	// With a WAITING_HUMAN workflow run in the chat nothing is flipped.
	const chat2 = "chat-wfrun"
	quotaChat(t, repo, testDB, chat2)
	require.NoError(t, testDB.Create(&WorkflowRun{RunID: "run-w", SessionID: chat2, Status: WorkflowStatusWaitingHuman}).Error)
	require.NoError(t, repo.ResetOrphanedQuotaSuspensions("restarted"))
	got, err = repo.GetSession(chat2)
	require.NoError(t, err)
	assert.Equal(t, AgentStatusWaitingHuman, got.Agents[0].Status)
	assert.True(t, got.Messages[0].Replied)
}

func TestResetOrphanedQuotaSuspensions_ResumeKeepsReusedIdLive(t *testing.T) {
	resolve, _ := localResolver(t)
	testDB := db.NewDBForTest(t)
	require.NoError(t, testDB.AutoMigrate(&Session{}))
	repo := NewSessionRepository(testDB)
	repo.SetFSResolver(resolve)
	const chat = "chat-reuse"
	quotaChat(t, repo, testDB, chat)

	repo.quotaResetHook = func(s string) error {
		if s == "journaled" {
			return errors.New("interrupted")
		}
		return nil
	}
	require.NoError(t, repo.ResetOrphanedQuotaSuspensions("restarted"))
	repo.quotaResetHook = nil

	// A later suspension reuses the deterministic id (replaced in place).
	live := ChatMessage{ID: QuotaAskMessagePrefix + "1", Role: "ask_user", Content: "again", AgentName: "a", Timestamp: time.Now().Add(time.Minute).UnixMilli()}
	require.NoError(t, repo.AppendMessage(chat, live))

	repo.pendingQuotaReset.Delete(chat)
	require.NoError(t, repo.ResetOrphanedQuotaSuspensions("restarted"))
	got, err := repo.GetSession(chat)
	require.NoError(t, err)
	assert.False(t, got.Messages[0].Replied, "the live ask is not retired")
	assert.True(t, got.HasAskUserUnreplied)
	assert.Nil(t, got.QuotaResetIDs, "stale journal item dropped")
}

func TestResetOrphanedQuotaSuspensions_NeverMarksByPosition(t *testing.T) {
	resolve, _ := localResolver(t)
	testDB := db.NewDBForTest(t)
	require.NoError(t, testDB.AutoMigrate(&Session{}))
	repo := NewSessionRepository(testDB)
	repo.SetFSResolver(resolve)
	const chat = "chat-nopos"
	require.NoError(t, repo.UpsertSession(&Session{ChatID: chat}))
	require.NoError(t, repo.AppendMessage(chat, ChatMessage{ID: "ask-other", Role: "ask_user", Content: "approve?", Timestamp: 1}))
	// A journal naming an id that is not in the transcript.
	journal := `[{"id":"ask-quota-gone","agent":"a","ts":1}]`
	require.NoError(t, testDB.Model(&Session{}).Where("chat_id = ?", chat).Update("quota_reset_ids", journal).Error)

	require.NoError(t, repo.ResetOrphanedQuotaSuspensions("restarted"))
	got, err := repo.GetSession(chat)
	require.NoError(t, err)
	assert.False(t, got.Messages[0].Replied, "the unrelated ask is not marked by position")
	assert.Nil(t, got.QuotaResetIDs)
}

func TestRetryPendingQuotaReset_AfterTransientFailure(t *testing.T) {
	resolve, _ := localResolver(t)
	testDB := db.NewDBForTest(t)
	require.NoError(t, testDB.AutoMigrate(&Session{}))
	repo := NewSessionRepository(testDB)
	repo.SetFSResolver(resolve)
	const chat = "chat-retry"
	quotaChat(t, repo, testDB, chat)

	repo.quotaResetHook = func(s string) error {
		if s == "marked" {
			return errors.New("transient")
		}
		return nil
	}
	require.NoError(t, repo.ResetOrphanedQuotaSuspensions("restarted"))
	assert.Equal(t, []string{"r1"}, repo.PendingQuotaRunners())

	repo.quotaResetHook = nil
	repo.RetryPendingQuotaReset(chat)
	assert.Empty(t, repo.PendingQuotaRunners())
	got, err := repo.GetSession(chat)
	require.NoError(t, err)
	assert.False(t, got.HasAskUserUnreplied)
	assert.Equal(t, AgentStatusCompleted, got.Agents[0].Status)
}
