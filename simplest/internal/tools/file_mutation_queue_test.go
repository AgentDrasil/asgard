package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/simplest/internal/types"
)

// holdMutation acquires the queue for path and returns a release func,
// failing the test on error.
func holdMutation(t *testing.T, path string) (release func()) {
	t.Helper()
	release, err := acquireFileMutation(context.Background(), path)
	require.NoError(t, err)
	return release
}

func TestFileMutationQueueSerializesSamePath(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "file.txt")

	release := holdMutation(t, path)

	entered := make(chan struct{})
	go func() {
		r := holdMutation(t, path)
		close(entered)
		r()
	}()

	select {
	case <-entered:
		t.Fatal("second operation entered while the first still holds the queue")
	case <-time.After(50 * time.Millisecond):
	}

	release()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("second operation never entered after release")
	}
}

func TestFileMutationQueueDifferentPathsConcurrent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	a := filepath.Join(dir, "a.txt")
	b := filepath.Join(dir, "b.txt")

	releaseA := holdMutation(t, a)
	defer releaseA()

	done := make(chan struct{})
	go func() {
		r := holdMutation(t, b)
		r()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("blocked op on a must not block a different key")
	}
}

func TestFileMutationQueueSymlinkSharesKey(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	real := filepath.Join(dir, "real.txt")
	link := filepath.Join(dir, "link.txt")
	require.NoError(t, os.WriteFile(real, []byte("x"), 0o644))
	require.NoError(t, os.Symlink(real, link))

	release := holdMutation(t, real)

	entered := make(chan struct{})
	go func() {
		r := holdMutation(t, link)
		close(entered)
		r()
	}()

	select {
	case <-entered:
		t.Fatal("symlink path did not share the queue with the real path")
	case <-time.After(50 * time.Millisecond):
	}

	release()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("symlink waiter never entered after release")
	}
}

func TestFileMutationQueueCanceledWaiter(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "file.txt")

	release := holdMutation(t, path)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, err := acquireFileMutation(ctx, path)
		errCh <- err
	}()
	// Wait until the goroutine is queued, then cancel.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("canceled waiter deadlocked")
	}
	release()

	// Queue remains usable afterwards.
	r := holdMutation(t, path)
	r()
}

// TestFileMutationQueueCanceledWaiterDoesNotStealPermit proves that a waiter
// canceled while queued cannot consume the current holder's permit: a third
// acquire must stay blocked until the original holder releases.
func TestFileMutationQueueCanceledWaiterDoesNotStealPermit(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "file.txt")

	releaseH := holdMutation(t, path)

	ctx, cancel := context.WithCancel(context.Background())
	wErr := make(chan error, 1)
	go func() {
		_, err := acquireFileMutation(ctx, path)
		wErr <- err
	}()
	// Let W queue up, then cancel it while H still holds the permit.
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-wErr:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("canceled waiter deadlocked")
	}

	// T must NOT enter while H still holds: W must not have stolen the permit.
	tDone := make(chan struct{})
	go func() {
		r := holdMutation(t, path)
		close(tDone)
		r()
	}()
	select {
	case <-tDone:
		t.Fatal("third acquire entered while H still holds: canceled waiter stole the permit")
	case <-time.After(100 * time.Millisecond):
	}

	releaseH()
	select {
	case <-tDone:
	case <-time.After(time.Second):
		t.Fatal("third acquire never entered after H released")
	}

	key, err := fileMutationKey(path)
	require.NoError(t, err)
	fileMutationMu.Lock()
	_, held := fileMutationKeys[key]
	fileMutationMu.Unlock()
	assert.False(t, held, "entry should be deleted once everyone released")
}

func TestFileMutationQueueCleansUpIdleEntries(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "file.txt")

	release := holdMutation(t, path)
	key, err := fileMutationKey(path)
	require.NoError(t, err)

	fileMutationMu.Lock()
	_, held := fileMutationKeys[key]
	fileMutationMu.Unlock()
	require.True(t, held, "entry should exist while held")

	release()

	fileMutationMu.Lock()
	_, held = fileMutationKeys[key]
	fileMutationMu.Unlock()
	assert.False(t, held, "entry should be deleted once idle")
}

func TestFileMutationQueueMissingTargetKeyStable(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "missing", "file.txt")
	key, err := fileMutationKey(path)
	require.NoError(t, err)
	assert.Equal(t, path, key, "missing target should fall back to the absolute path")
}

// execAsync runs tool.Execute in the background and reports the error.
func execAsync(tool types.AgentTool, args string) chan error {
	done := make(chan error, 1)
	go func() {
		_, err := tool.Execute(context.Background(), "test-call-id", json.RawMessage(args), nil)
		done <- err
	}()
	return done
}

func TestWriteToolBlocksOnQueue(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	tool := NewWriteTool(dir)

	release := holdMutation(t, path)

	done := execAsync(tool, fmt.Sprintf(`{"path":%q,"content":"new"}`, path))

	select {
	case err := <-done:
		t.Fatalf("write did not block on the queue: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	release()

	select {
	case err := <-done:
		require.NoError(t, err)
		assert.Equal(t, "new", readFile(t, path))
	case <-time.After(time.Second):
		t.Fatal("write never completed after release")
	}
}

func TestEditToolBlocksOnQueue(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	require.NoError(t, os.WriteFile(path, []byte("a\n"), 0o644))
	tool := NewEditTool(dir)

	release := holdMutation(t, path)

	done := execAsync(tool, fmt.Sprintf(`{"path":%q,"edits":[{"oldText":"a","newText":"b"}]}`, path))

	select {
	case err := <-done:
		t.Fatalf("edit did not block on the queue: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	release()

	select {
	case err := <-done:
		require.NoError(t, err)
		assert.Equal(t, "b\n", readFile(t, path))
	case <-time.After(time.Second):
		t.Fatal("edit never completed after release")
	}
}

func TestDocToolsInheritQueueWithoutDeadlock(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "doc.md")
	tool := NewWriteDocTool(dir, DocToolOptions{})
	require.NoError(t, os.WriteFile(path, []byte("old\n"), 0o644))

	release := holdMutation(t, path)

	done := execAsync(tool, fmt.Sprintf(`{"path":%q,"content":"new"}`, path))

	select {
	case err := <-done:
		t.Fatalf("write_doc did not inherit the queue: %v", err)
	case <-time.After(500 * time.Millisecond):
	}
	release()

	select {
	case err := <-done:
		require.NoError(t, err, "write_doc must not double-acquire and deadlock")
		assert.Equal(t, "new", readFile(t, path))
	case <-time.After(2 * time.Second):
		t.Fatal("write_doc deadlocked (double-acquire)")
	}
}

// drainExec consumes an execAsync result; errors are tolerated because the
// regression test only asserts on the resulting file contents.
func drainExec(t *testing.T, ch chan error) {
	t.Helper()
	if err := <-ch; err != nil {
		t.Logf("execute error (tolerated): %v", err)
	}
}

func TestConcurrentWriteAndEditNoLostUpdate(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	writeTool := NewWriteTool(dir)
	editTool := NewEditTool(dir)

	for i := 0; i < 50; i++ {
		require.NoError(t, os.WriteFile(path, []byte("a\nb\n"), 0o644))
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			drainExec(t, execAsync(writeTool, fmt.Sprintf(`{"path":%q,"content":"w\n"}`, path)))
		}()
		go func() {
			defer wg.Done()
			drainExec(t, execAsync(editTool, fmt.Sprintf(`{"path":%q,"edits":[{"oldText":"a","newText":"A"}]}`, path)))
		}()
		wg.Wait()

		content, err := os.ReadFile(path)
		require.NoError(t, err)
		// The stale-edit outcome "A\nb\n" must never survive a concurrent
		// write: with serialization, either the write lands last ("w\n") or
		// the edit landed first and the write then overwrote it.
		assert.NotEqual(t, "A\nb\n", string(content), "stale edit outcome survived a concurrent write")
	}
}
