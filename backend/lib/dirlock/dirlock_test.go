package dirlock_test

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/backend/lib/dirlock"
	"github.com/AgentDrasil/asgard/pkg/agentspec"
	runnerpaths "github.com/AgentDrasil/asgard/pkg/paths/runner"
)

func TestIsExemptDir_Table(t *testing.T) {
	t.Parallel()

	chatID := "chat-123"
	sessTmp := runnerpaths.SessionTmpDir(chatID)
	sessDir := runnerpaths.SessionDir(chatID)

	tests := []struct {
		name string
		dir  string
		want bool
	}{
		{"empty dir", "", true},
		{"current dir", ".", true},
		{"root tmp", "/tmp", true},
		{"under root tmp", "/tmp/project", true},
		{"root session", "/session", true},
		{"under root session", "/session/artifacts", true},
		{"session-data tmp", "/session-data/tmp", true},
		{"under session-data tmp", "/session-data/tmp/chat-999", true},
		{"session-data sessions", "/session-data/sessions", true},
		{"under session-data sessions", "/session-data/sessions/chat-999", true},
		{"asgard data tmp", "/asgard/data/tmp", true},
		{"under asgard data tmp", "/asgard/data/tmp/foo", true},
		{"asgard data sessions", "/asgard/data/sessions", true},
		{"under asgard data sessions", "/asgard/data/sessions/bar", true},
		{"chat session tmp", sessTmp, true},
		{"under chat session tmp", filepath.Join(sessTmp, "nested"), true},
		{"chat session dir", sessDir, true},
		{"under chat session dir", filepath.Join(sessDir, "repo"), true},
		{"normal project dir", "/home/user/project", false},
		{"normal src dir", "/data/src/repo-a", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := dirlock.IsExemptDir(chatID, tt.dir)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestPathsOverlap_Table(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		a    string
		b    string
		want bool
	}{
		{"exact match", "/src/repo", "/src/repo", true},
		{"exact match trailing slash", "/src/repo/", "/src/repo", true},
		{"ancestor a of b", "/src", "/src/repo", true},
		{"ancestor b of a", "/src/repo", "/src", true},
		{"root overlap a", "/", "/src/repo", true},
		{"root overlap b", "/src/repo", "/", true},
		{"sibling subdirs no overlap", "/src/repo-a", "/src/repo-b", false},
		{"common prefix name no overlap", "/src/repo", "/src/repository", false},
		{"different trees", "/data/a", "/var/a", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, dirlock.PathsOverlap(tt.a, tt.b))
		})
	}
}

func TestComputeLockedDirs(t *testing.T) {
	t.Parallel()

	t.Run("read only agent mode", func(t *testing.T) {
		t.Parallel()
		cfg := &agentspec.AgentConfig{
			LockMode: agentspec.LockModeReadOnly,
			MountDirs: agentspec.MountConfig{
				ReadOnly: []string{"/data/src/repo"},
			},
		}
		dirs, mode := dirlock.ComputeLockedDirs("chat-1", "/data/src/repo", cfg)
		assert.Equal(t, dirlock.ModeReadOnly, mode)
		assert.Equal(t, []string{"/data/src/repo"}, dirs)
	})

	t.Run("mount with mask rules does not lock parent container", func(t *testing.T) {
		t.Parallel()
		rule := &agentspec.SubdirMaskRule{Base: "/data/src"}
		cfg := &agentspec.AgentConfig{
			MountDirs: agentspec.MountConfig{
				ReadWriteEntries: []agentspec.MountPathEntry{
					{Path: "/data/src", MaskRules: rule},
				},
			},
		}
		// Run dir inside parent container
		dirs, mode := dirlock.ComputeLockedDirs("chat-1", "/data/src/repo-a", cfg)
		assert.Equal(t, dirlock.ModeExclusive, mode)
		assert.Equal(t, []string{"/data/src/repo-a"}, dirs)
	})

	t.Run("mount without mask rules locks additional readwrite paths", func(t *testing.T) {
		t.Parallel()
		cfg := &agentspec.AgentConfig{
			MountDirs: agentspec.MountConfig{
				ReadWrite: []string{"/data/shared-extra", "/data/shared-extra"},
			},
		}
		dirs, mode := dirlock.ComputeLockedDirs("chat-1", "/data/src/repo-a", cfg)
		assert.Equal(t, dirlock.ModeExclusive, mode)
		assert.Equal(t, []string{"/data/shared-extra", "/data/src/repo-a"}, dirs)
	})

	t.Run("exempt runDir and mount dirs are excluded", func(t *testing.T) {
		t.Parallel()
		cfg := &agentspec.AgentConfig{
			MountDirs: agentspec.MountConfig{
				ReadWrite: []string{"/tmp/scratch"},
			},
		}
		dirs, mode := dirlock.ComputeLockedDirs("chat-1", "/tmp/session", cfg)
		assert.Equal(t, dirlock.ModeExclusive, mode)
		assert.Empty(t, dirs)
	})
}

func TestManager_ExclusiveMutualExclusionAndFIFO(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	notifications := make(map[string]int)
	mgr := dirlock.NewManager(func(chatID string) {
		mu.Lock()
		defer mu.Unlock()
		notifications[chatID]++
	})

	ctx := context.Background()

	// Chat 1 acquires exclusive lock on /repo
	rel1, err := mgr.Acquire(ctx, dirlock.AcquireRequest{
		ChatID:   "chat-1",
		Title:    "Chat 1",
		RunnerID: "runner-1",
		Dirs:     []string{"/repo"},
		Mode:     dirlock.ModeExclusive,
	})
	require.NoError(t, err)

	st1 := mgr.StatusForChat("chat-1")
	require.NotNil(t, st1)
	assert.True(t, st1.IsLocked)
	assert.Equal(t, dirlock.ModeExclusive, st1.LockMode)
	assert.Equal(t, dirlock.ReasonRunning, st1.LockReason)
	assert.False(t, st1.CanUnlock)

	// Chat 2 attempts to acquire /repo in a background goroutine and should block
	chat2Acquired := make(chan func(bool), 1)
	go func() {
		rel2, err2 := mgr.Acquire(ctx, dirlock.AcquireRequest{
			ChatID:   "chat-2",
			Title:    "Chat 2",
			RunnerID: "runner-1",
			Dirs:     []string{"/repo"},
			Mode:     dirlock.ModeExclusive,
		})
		if err2 == nil {
			chat2Acquired <- rel2
		}
	}()

	// Wait briefly for chat-2 to register as waiting
	assert.Eventually(t, func() bool {
		st2 := mgr.StatusForChat("chat-2")
		return st2 != nil && st2.IsWaitingForLock && st2.LockedByChatID == "chat-1" && st2.BlockingReason == dirlock.ReasonRunning
	}, 2*time.Second, 10*time.Millisecond)

	// Chat 3 also attempts to acquire /repo and should queue behind Chat 2
	chat3Acquired := make(chan func(bool), 1)
	go func() {
		rel3, err3 := mgr.Acquire(ctx, dirlock.AcquireRequest{
			ChatID:   "chat-3",
			Title:    "Chat 3",
			RunnerID: "runner-1",
			Dirs:     []string{"/repo"},
			Mode:     dirlock.ModeExclusive,
		})
		if err3 == nil {
			chat3Acquired <- rel3
		}
	}()

	assert.Eventually(t, func() bool {
		st3 := mgr.StatusForChat("chat-3")
		return st3 != nil && st3.IsWaitingForLock && st3.LockedByChatID == "chat-1"
	}, 2*time.Second, 10*time.Millisecond)

	// Release Chat 1 completely
	rel1(false)

	// Chat 2 must be granted first (FIFO)
	var rel2 func(bool)
	select {
	case rel2 = <-chat2Acquired:
		st2 := mgr.StatusForChat("chat-2")
		require.NotNil(t, st2)
		assert.True(t, st2.IsLocked)
		assert.False(t, st2.IsWaitingForLock)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for chat-2 to acquire lock")
	}

	// Chat 3 should still be waiting, now blocked by Chat 2
	st3 := mgr.StatusForChat("chat-3")
	require.NotNil(t, st3)
	assert.True(t, st3.IsWaitingForLock)
	assert.Equal(t, "chat-2", st3.LockedByChatID)

	// Release Chat 2
	rel2(false)

	// Chat 3 must now be granted
	select {
	case rel3 := <-chat3Acquired:
		st3After := mgr.StatusForChat("chat-3")
		require.NotNil(t, st3After)
		assert.True(t, st3After.IsLocked)
		rel3(false)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for chat-3 to acquire lock")
	}

	assert.Nil(t, mgr.StatusForChat("chat-1"))
	assert.Nil(t, mgr.StatusForChat("chat-2"))
	assert.Nil(t, mgr.StatusForChat("chat-3"))
}

func TestManager_DifferentRunnersDoNotBlock(t *testing.T) {
	t.Parallel()

	mgr := dirlock.NewManager(nil)
	ctx := context.Background()

	rel1, err1 := mgr.Acquire(ctx, dirlock.AcquireRequest{
		ChatID:   "chat-1",
		RunnerID: "runner-a",
		Dirs:     []string{"/data/repo"},
		Mode:     dirlock.ModeExclusive,
	})
	require.NoError(t, err1)
	t.Cleanup(func() { rel1(false) })

	// Same path on runner-b must not block
	rel2, err2 := mgr.Acquire(ctx, dirlock.AcquireRequest{
		ChatID:   "chat-2",
		RunnerID: "runner-b",
		Dirs:     []string{"/data/repo"},
		Mode:     dirlock.ModeExclusive,
	})
	require.NoError(t, err2)
	t.Cleanup(func() { rel2(false) })

	st1 := mgr.StatusForChat("chat-1")
	st2 := mgr.StatusForChat("chat-2")
	require.NotNil(t, st1)
	require.NotNil(t, st2)
	assert.True(t, st1.IsLocked)
	assert.True(t, st2.IsLocked)
}

func TestManager_SubdirMaskRulesIndependentRuns(t *testing.T) {
	t.Parallel()

	mgr := dirlock.NewManager(nil)
	ctx := context.Background()

	// Two chats in sibling directories under shared /src
	maskRule := &agentspec.SubdirMaskRule{Base: "/src"}
	cfg := &agentspec.AgentConfig{
		MountDirs: agentspec.MountConfig{
			ReadWriteEntries: []agentspec.MountPathEntry{
				{Path: "/src", MaskRules: maskRule},
			},
		},
	}

	dirs1, mode1 := dirlock.ComputeLockedDirs("chat-1", "/src/proj-a", cfg)
	dirs2, mode2 := dirlock.ComputeLockedDirs("chat-2", "/src/proj-b", cfg)

	rel1, err1 := mgr.Acquire(ctx, dirlock.AcquireRequest{
		ChatID:   "chat-1",
		RunnerID: "runner-1",
		Dirs:     dirs1,
		Mode:     mode1,
	})
	require.NoError(t, err1)
	t.Cleanup(func() { rel1(false) })

	rel2, err2 := mgr.Acquire(ctx, dirlock.AcquireRequest{
		ChatID:   "chat-2",
		RunnerID: "runner-1",
		Dirs:     dirs2,
		Mode:     mode2,
	})
	require.NoError(t, err2)
	t.Cleanup(func() { rel2(false) })

	assert.True(t, mgr.StatusForChat("chat-1").IsLocked)
	assert.True(t, mgr.StatusForChat("chat-2").IsLocked)
}

func TestManager_ParentChildOverlap(t *testing.T) {
	t.Parallel()

	mgr := dirlock.NewManager(nil)
	ctx := context.Background()

	relParent, err := mgr.Acquire(ctx, dirlock.AcquireRequest{
		ChatID:   "chat-parent",
		RunnerID: "runner-1",
		Dirs:     []string{"/data/src"},
		Mode:     dirlock.ModeExclusive,
	})
	require.NoError(t, err)
	defer relParent(false)

	// Child /data/src/repo should be blocked
	acquireCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()

	_, errChild := mgr.Acquire(acquireCtx, dirlock.AcquireRequest{
		ChatID:   "chat-child",
		RunnerID: "runner-1",
		Dirs:     []string{"/data/src/repo"},
		Mode:     dirlock.ModeExclusive,
	})
	assert.ErrorIs(t, errChild, context.DeadlineExceeded)
}

func TestManager_ReentrancySameChat(t *testing.T) {
	t.Parallel()

	mgr := dirlock.NewManager(nil)
	ctx := context.Background()

	// Initial acquisition
	rel1, err1 := mgr.Acquire(ctx, dirlock.AcquireRequest{
		ChatID:   "chat-1",
		RunnerID: "runner-1",
		Dirs:     []string{"/repo"},
		Mode:     dirlock.ModeExclusive,
	})
	require.NoError(t, err1)

	// Nested call-peer in the same chat
	rel2, err2 := mgr.Acquire(ctx, dirlock.AcquireRequest{
		ChatID:   "chat-1",
		RunnerID: "runner-1",
		Dirs:     []string{"/repo", "/repo/sub"},
		Mode:     dirlock.ModeReadOnly,
	})
	require.NoError(t, err2)

	st := mgr.StatusForChat("chat-1")
	require.NotNil(t, st)
	assert.True(t, st.IsLocked)
	assert.Equal(t, dirlock.ModeExclusive, st.LockMode)
	assert.False(t, st.CanUnlock)

	// Release nested call
	rel2(false)
	stAfterNested := mgr.StatusForChat("chat-1")
	require.NotNil(t, stAfterNested)
	assert.True(t, stAfterNested.IsLocked)
	assert.False(t, stAfterNested.CanUnlock) // Root run still active

	// Release root run
	rel1(false)
	assert.Nil(t, mgr.StatusForChat("chat-1"))
}

func TestManager_ReadOnlySharedLock(t *testing.T) {
	t.Parallel()

	mgr := dirlock.NewManager(nil)
	ctx := context.Background()

	// Two concurrent read-only chats on /repo
	rel1, err1 := mgr.Acquire(ctx, dirlock.AcquireRequest{
		ChatID:   "chat-ro-1",
		RunnerID: "runner-1",
		Dirs:     []string{"/repo"},
		Mode:     dirlock.ModeReadOnly,
	})
	require.NoError(t, err1)
	t.Cleanup(func() { rel1(false) })

	rel2, err2 := mgr.Acquire(ctx, dirlock.AcquireRequest{
		ChatID:   "chat-ro-2",
		RunnerID: "runner-1",
		Dirs:     []string{"/repo"},
		Mode:     dirlock.ModeReadOnly,
	})
	require.NoError(t, err2)
	t.Cleanup(func() { rel2(false) })

	assert.True(t, mgr.StatusForChat("chat-ro-1").IsLocked)
	assert.True(t, mgr.StatusForChat("chat-ro-2").IsLocked)

	// An exclusive writer should be blocked
	acquireCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()

	_, errEx := mgr.Acquire(acquireCtx, dirlock.AcquireRequest{
		ChatID:   "chat-writer",
		RunnerID: "runner-1",
		Dirs:     []string{"/repo"},
		Mode:     dirlock.ModeExclusive,
	})
	assert.ErrorIs(t, errEx, context.DeadlineExceeded)
}

func TestManager_KeepUncommittedAndUnlockIdle(t *testing.T) {
	t.Parallel()

	mgr := dirlock.NewManager(nil)
	ctx := context.Background()

	rel1, err := mgr.Acquire(ctx, dirlock.AcquireRequest{
		ChatID:   "chat-1",
		Title:    "Dirty Chat",
		RunnerID: "runner-1",
		Dirs:     []string{"/repo"},
		Mode:     dirlock.ModeExclusive,
	})
	require.NoError(t, err)

	// Release with keepUncommitted = true
	rel1(true)

	st1 := mgr.StatusForChat("chat-1")
	require.NotNil(t, st1)
	assert.True(t, st1.IsLocked)
	assert.Equal(t, dirlock.ReasonUncommitted, st1.LockReason)
	assert.True(t, st1.CanUnlock)

	// Another chat queueing should see uncommitted blocking reason
	chat2Acquired := make(chan struct{})
	go func() {
		rel2, err2 := mgr.Acquire(ctx, dirlock.AcquireRequest{
			ChatID:   "chat-2",
			RunnerID: "runner-1",
			Dirs:     []string{"/repo"},
			Mode:     dirlock.ModeExclusive,
		})
		if err2 == nil {
			close(chat2Acquired)
			rel2(false)
		}
	}()

	assert.Eventually(t, func() bool {
		st2 := mgr.StatusForChat("chat-2")
		return st2 != nil && st2.IsWaitingForLock && st2.BlockingReason == dirlock.ReasonUncommitted
	}, 2*time.Second, 10*time.Millisecond)

	// Unlock idle chat-1
	unlocked, err := mgr.UnlockIdle("chat-1")
	require.NoError(t, err)
	assert.True(t, unlocked)

	// Chat 2 should now acquire the lock
	select {
	case <-chat2Acquired:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for chat-2 after unlocking idle chat-1")
	}
}

func TestManager_UnlockIdleWhileRunningFails(t *testing.T) {
	t.Parallel()

	mgr := dirlock.NewManager(nil)
	ctx := context.Background()

	rel, err := mgr.Acquire(ctx, dirlock.AcquireRequest{
		ChatID:   "chat-1",
		RunnerID: "runner-1",
		Dirs:     []string{"/repo"},
		Mode:     dirlock.ModeExclusive,
	})
	require.NoError(t, err)
	defer rel(false)

	unlocked, err := mgr.UnlockIdle("chat-1")
	assert.False(t, unlocked)
	assert.ErrorIs(t, err, dirlock.ErrLockActiveRun)
}

func TestManager_ForceReleaseChat(t *testing.T) {
	t.Parallel()

	mgr := dirlock.NewManager(nil)
	ctx := context.Background()

	rel1, err := mgr.Acquire(ctx, dirlock.AcquireRequest{
		ChatID:   "chat-1",
		RunnerID: "runner-1",
		Dirs:     []string{"/repo"},
		Mode:     dirlock.ModeExclusive,
	})
	require.NoError(t, err)
	defer rel1(false)

	chat2Acquired := make(chan struct{})
	go func() {
		rel2, err2 := mgr.Acquire(ctx, dirlock.AcquireRequest{
			ChatID:   "chat-2",
			RunnerID: "runner-1",
			Dirs:     []string{"/repo"},
			Mode:     dirlock.ModeExclusive,
		})
		if err2 == nil {
			close(chat2Acquired)
			rel2(false)
		}
	}()

	assert.Eventually(t, func() bool {
		st2 := mgr.StatusForChat("chat-2")
		return st2 != nil && st2.IsWaitingForLock
	}, 2*time.Second, 10*time.Millisecond)

	// Force release Chat 1 (e.g. archived or deleted session)
	mgr.ForceReleaseChat("chat-1")

	select {
	case <-chat2Acquired:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for chat-2 after force releasing chat-1")
	}
}

func TestManager_UpdateChatTitle(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	notifiedChats := make(map[string]bool)
	mgr := dirlock.NewManager(func(chatID string) {
		mu.Lock()
		defer mu.Unlock()
		notifiedChats[chatID] = true
	})

	ctx := context.Background()
	rel, err := mgr.Acquire(ctx, dirlock.AcquireRequest{
		ChatID:   "chat-1",
		Title:    "Original Title",
		RunnerID: "runner-1",
		Dirs:     []string{"/repo"},
		Mode:     dirlock.ModeExclusive,
	})
	require.NoError(t, err)
	defer rel(false)

	go func() {
		rel2, _ := mgr.Acquire(ctx, dirlock.AcquireRequest{
			ChatID:   "chat-2",
			RunnerID: "runner-1",
			Dirs:     []string{"/repo"},
			Mode:     dirlock.ModeExclusive,
		})
		if rel2 != nil {
			rel2(false)
		}
	}()

	assert.Eventually(t, func() bool {
		st := mgr.StatusForChat("chat-2")
		return st != nil && st.LockedByTitle == "Original Title"
	}, 2*time.Second, 10*time.Millisecond)

	mgr.UpdateChatTitle("chat-1", "Updated Title")

	assert.Eventually(t, func() bool {
		st := mgr.StatusForChat("chat-2")
		return st != nil && st.LockedByTitle == "Updated Title"
	}, 2*time.Second, 10*time.Millisecond)
}

func TestManager_ReentrantExpansionConflict(t *testing.T) {
	t.Parallel()

	mgr := dirlock.NewManager(nil)
	ctx := context.Background()

	// Chat A holds /repo-a
	relA, errA := mgr.Acquire(ctx, dirlock.AcquireRequest{
		ChatID:   "chat-a",
		RunnerID: "runner-1",
		Dirs:     []string{"/repo-a"},
		Mode:     dirlock.ModeExclusive,
	})
	require.NoError(t, errA)
	t.Cleanup(func() { relA(false) })

	// Chat B holds /repo-b
	relB, errB := mgr.Acquire(ctx, dirlock.AcquireRequest{
		ChatID:   "chat-b",
		RunnerID: "runner-1",
		Dirs:     []string{"/repo-b"},
		Mode:     dirlock.ModeExclusive,
	})
	require.NoError(t, errB)
	t.Cleanup(func() { relB(false) })

	// Chat A re-enters requesting expansion to [/repo-a, /repo-b]
	// It must NOT bypass Chat B's lock on /repo-b and must return an error.
	relExpanded, errExpanded := mgr.Acquire(ctx, dirlock.AcquireRequest{
		ChatID:   "chat-a",
		RunnerID: "runner-1",
		Dirs:     []string{"/repo-a", "/repo-b"},
		Mode:     dirlock.ModeExclusive,
	})
	require.Error(t, errExpanded)
	assert.Nil(t, relExpanded)
	assert.ErrorContains(t, errExpanded, "cannot expand or upgrade held directory lock for chat chat-a")
	assert.ErrorContains(t, errExpanded, "chat-b")
}
