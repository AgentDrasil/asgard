package dbmodels

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"time"

	"github.com/AgentDrasil/asgard/backend/lib/runnerfs"
)

// FSResolver returns the file access surface of a chat's runner.
type FSResolver func(chatID string) (runnerfs.FS, error)

// storeTimeout bounds one transcript or workflow-store operation against a
// runner.
const storeTimeout = 30 * time.Second

// sessionStore is the persistent per-chat file store behind the transcript
// and the offloaded workflow files. The data lives in the chat's session
// directory, which is on the runner the chat is bound to (the sandbox reads
// it as /session): fsStore reaches it through RunnerService, dirStore through
// a plain local directory (tests and runner-less development). All paths are
// absolute strings under Root; the same strings are persisted in the DB.
type sessionStore interface {
	// Root is the session directory as the store names it.
	Root() string
	// Read returns the file content; a missing file wraps os.ErrNotExist.
	Read(path string) ([]byte, error)
	// Append adds data to the end of the file (created with parents), synced.
	Append(path string, data []byte) error
	// WriteAtomic replaces the file via temp file + fsync + rename + parent
	// dir fsync, creating parents. With skipIfUnchanged an identical file is
	// left alone.
	WriteAtomic(path string, data []byte, skipIfUnchanged bool) error
	// CleanOrphanTmp removes temp files WriteAtomic left behind under dir.
	CleanOrphanTmp(dir string) error
}

// dirStore is a sessionStore over a local directory.
type dirStore struct{ dir string }

func (s dirStore) Root() string { return s.dir }

func (s dirStore) Read(path string) ([]byte, error) { return os.ReadFile(path) }

func (s dirStore) Append(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open for append: %w", err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("append: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync: %w", err)
	}
	return nil
}

func (s dirStore) WriteAtomic(path string, data []byte, skipIfUnchanged bool) error {
	return atomicWrite(path, data, skipIfUnchanged)
}

func (s dirStore) CleanOrphanTmp(dir string) error { return CleanOrphanTmpFiles(dir) }

// fsStore is a sessionStore over a chat's runnerfs.FS. Paths are the
// canonical /session/... form; the runner resolves them beneath the chat's
// session directory root.
type fsStore struct{ fs runnerfs.FS }

func (s fsStore) Root() string { return "/session" }

func newReqID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// fsErr keeps the runnerfs classification (so callers can answer 503 for an
// offline runner) and maps not-found onto os.ErrNotExist.
func fsErr(op string, err error) error {
	if err == nil {
		return nil
	}
	if runnerfs.CodeOf(err) == runnerfs.CodeNotFound {
		return fmt.Errorf("%s: %w", op, errors.Join(os.ErrNotExist, err))
	}
	return fmt.Errorf("%s: %w", op, err)
}

func (s fsStore) Read(path string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), storeTimeout)
	defer cancel()
	f, err := s.fs.Open(ctx, runnerfs.ReadRequest{Path: path, Raw: true})
	if err != nil {
		return nil, fsErr("read "+path, err)
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, fsErr("read "+path, err)
	}
	return data, nil
}

func (s fsStore) Append(path string, data []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), storeTimeout)
	defer cancel()
	return fsErr("append "+path, s.fs.AppendFile(ctx, runnerfs.AppendRequest{
		Ref: runnerfs.PathRef{Path: path}, Mkdir: true, Data: data, Sync: true, RequestID: newReqID(),
	}))
}

func (s fsStore) WriteAtomic(path string, data []byte, skipIfUnchanged bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), storeTimeout)
	defer cancel()
	_, err := s.fs.WriteFile(ctx, runnerfs.WriteRequest{
		Ref: runnerfs.PathRef{Path: path}, Mkdir: true, Atomic: true, Sync: true,
		SkipIfUnchanged: skipIfUnchanged, RequestID: newReqID(),
	}, bytes.NewReader(data))
	return fsErr("write "+path, err)
}

// CleanOrphanTmp is a no-op remotely: the FS surface has no sweep operation and
// a per-write RPC walk is not worth it. The leftover temp files of a crashed
// atomic write are removed by the daily cleanup instead, which sends the
// runner a Cleanup orphan_tmp_sweeps entry for every chat's workflows dir
// (backend/lib/cleanup).
func (s fsStore) CleanOrphanTmp(string) error { return nil }

// IsUnavailable reports whether err means the chat's runner could not be
// reached (the transcript and workflow files live there).
func IsUnavailable(err error) bool {
	var fe *runnerfs.Error
	return errors.As(err, &fe) && fe.Code == runnerfs.CodeUnavailable
}

func storePath(s sessionStore, rel ...string) string {
	return path.Join(append([]string{s.Root()}, rel...)...)
}
