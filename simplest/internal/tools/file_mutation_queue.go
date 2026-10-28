package tools

import (
	"context"
	"path/filepath"
	"sync"
)

// Port of pi 74a46fc7e: serialize mutations to the same path while allowing
// different paths in parallel.
//
// All low-level mutators (write/edit) wrap their critical
// section in acquireFileMutation, so same-path mutations serialize and
// different paths run in parallel. edit does not declare
// ExecutionSequential either: it relies on this queue and may join parallel
// tool batches.
//
// The queue key is the symlink-resolved absolute path when the target exists,
// or the plain absolute path otherwise. This means concurrent creates of a
// not-yet-existing file serialize, and a symlink and its real target share a
// single queue.

var (
	fileMutationMu   sync.Mutex
	fileMutationKeys = map[string]*fileMutationEntry{}
)

// fileMutationEntry is a capacity-1 semaphore guarding one resolved path.
// refs counts active holders plus queued waiters so the map entry can be
// deleted once idle.
type fileMutationEntry struct {
	ch   chan struct{}
	refs int
}

func fileMutationKey(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved, nil
	}
	// Missing target or unresolvable symlink: fall back to the absolute path
	// so concurrent writes to a not-yet-created file still serialize.
	return abs, nil
}

// acquireFileMutation serializes access to path: the returned release func
// must be called exactly once when the critical section ends. A caller whose
// ctx is canceled while queued gets the context error back instead.
func acquireFileMutation(ctx context.Context, path string) (release func(), err error) {
	key, err := fileMutationKey(path)
	if err != nil {
		return nil, err
	}

	fileMutationMu.Lock()
	e, ok := fileMutationKeys[key]
	if !ok {
		e = &fileMutationEntry{ch: make(chan struct{}, 1)}
		fileMutationKeys[key] = e
	}
	e.refs++
	fileMutationMu.Unlock()

	release = func() { releaseFileMutation(e, key) }

	select {
	case e.ch <- struct{}{}:
		return release, nil
	case <-ctx.Done():
		// The ctx case won, so no permit was taken. Do not touch e.ch here:
		// the channel may hold the current holder's permit, and receiving it
		// would silently release the holder's lock. Just drop our reference.
		dropFileMutationRef(e, key)
		return nil, ctx.Err()
	}
}

// releaseFileMutation drops the holder's permit exactly once, then removes a
// reference and deletes the map entry when the last holder/waiter is gone, so
// idle paths never leak queue entries.
func releaseFileMutation(e *fileMutationEntry, key string) {
	<-e.ch
	dropFileMutationRef(e, key)
}

// dropFileMutationRef decrements refs and removes the map entry when idle.
// It never touches the permit channel, so it is safe for canceled waiters.
func dropFileMutationRef(e *fileMutationEntry, key string) {
	fileMutationMu.Lock()
	e.refs--
	if e.refs == 0 && fileMutationKeys[key] == e {
		delete(fileMutationKeys, key)
	}
	fileMutationMu.Unlock()
}
