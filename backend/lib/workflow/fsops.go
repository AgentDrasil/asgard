package workflow

import (
	"bytes"
	"context"
	"fmt"
	"path"
	"path/filepath"
	"strings"
	"uuid"

	"github.com/AgentDrasil/asgard/backend/lib/runnerfs"
)

// FSResolver returns the file access surface of a chat: its runner's
// RunnerService, or this host's own layout when no runner is configured.
type FSResolver func(chatID string) (runnerfs.FS, error)

type fsCtxKey struct{}

// withFS carries the run's chat FS to the node runners.
func withFS(ctx context.Context, fs runnerfs.FS) context.Context {
	return context.WithValue(ctx, fsCtxKey{}, fs)
}

// fileOps is the workflow's file access to one chat's directories. All reads,
// writes and stats go through the chat's runnerfs.FS, so with a remote runner
// the files are those of the runner, confined and authorized there. Paths the
// engine derives from the chat's default tmp/session dirs are sent in the
// namespaced "/tmp/..." and "/session/..." forms (never as backend-host
// paths); paths inside the run dir are sent relative to it, other absolute paths stay
// absolute (the runner confines them).
type fileOps struct {
	fs         runnerfs.FS
	sessionID  string
	tmpDir     string
	sessionDir string
	runDir     string
}

// newFileOps builds the file access of a run. The FS comes from ctx (set by
// Engine.Execute); a bare context falls back to the host's own layout.
func newFileOps(ctx context.Context, sessionID, tmpDir, sessionDir, runDir string) fileOps {
	fs, _ := ctx.Value(fsCtxKey{}).(runnerfs.FS)
	if fs == nil {
		fs = runnerfs.NewLocal(sessionID)
	}
	return fileOps{fs: fs, sessionID: sessionID, tmpDir: tmpDir, sessionDir: sessionDir, runDir: runDir}
}

func opsOf(ctx context.Context, nctx *NodeContext) fileOps {
	return newFileOps(ctx, nctx.SessionID, nctx.TmpDir, nctx.SessionDir, nctx.RunDir)
}

// ref maps a path (absolute, or relative to the run dir) onto a PathRef.
func (o fileOps) ref(p string) runnerfs.PathRef {
	p = filepath.Clean(p)
	if !filepath.IsAbs(p) {
		return runnerfs.PathRef{RunDir: o.runDir, Path: filepath.ToSlash(p), Scope: "workspace"}
	}
	if o.sessionID != "" {
		if o.tmpDir == DefaultTmpDir(o.sessionID) {
			if rel, ok := relUnder(o.tmpDir, p); ok {
				return runnerfs.PathRef{RunDir: o.runDir, Path: "/tmp/" + rel}
			}
		}
		if o.sessionDir == DefaultSessionDir(o.sessionID) {
			if rel, ok := relUnder(o.sessionDir, p); ok {
				return runnerfs.PathRef{RunDir: o.runDir, Path: "/session/" + rel}
			}
		}
	}
	// Other dirs are addressed relative to themselves: an absolute path
	// starting with /tmp or /session would name the chat's namespace dirs.
	for _, base := range []string{o.runDir, o.tmpDir, o.sessionDir} {
		if base == "" {
			continue
		}
		if rel, ok := relUnder(base, p); ok {
			return runnerfs.PathRef{RunDir: base, Path: rel, Scope: "workspace"}
		}
	}
	return runnerfs.PathRef{RunDir: o.runDir, Path: p, Scope: "workspace"}
}

// relUnder returns p relative to dir when p is a strict descendant of it.
func relUnder(dir, p string) (string, bool) {
	rel, err := filepath.Rel(filepath.Clean(dir), p)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

func (o fileOps) stat(ctx context.Context, p string) (runnerfs.StatResult, error) {
	return o.fs.Stat(ctx, o.ref(p))
}

// open opens p for reading; the caller closes the file.
func (o fileOps) open(ctx context.Context, p string) (*runnerfs.File, error) {
	r := o.ref(p)
	return o.fs.Open(ctx, runnerfs.ReadRequest{RunDir: r.RunDir, Path: r.Path, Scope: r.Scope, Raw: true})
}

// write stores data at p, creating missing parent directories.
func (o fileOps) write(ctx context.Context, p string, data []byte) error {
	_, err := o.fs.WriteFile(ctx, runnerfs.WriteRequest{
		Ref: o.ref(p), Mkdir: true, Mode: 0o644, RequestID: uuid.NewV7().String(),
	}, bytes.NewReader(data))
	return err
}

// isFile reports whether p is a regular file of the run. Errors (including an
// unreachable runner) count as "not a file".
func (o fileOps) isFile(ctx context.Context) func(string) bool {
	return func(p string) bool {
		st, err := o.stat(ctx, p)
		return err == nil && st.Exists && !st.IsDir
	}
}

// dirRef lists the ways to address the directory dir itself (which may not
// exist yet), in order of preference, or reports ok=false when it is the run dir, which is validated, not created.
func (o fileOps) dirRef(dir string) (refs []runnerfs.PathRef, ok bool) {
	dir = filepath.Clean(dir)
	if o.sessionID != "" {
		if dir == o.tmpDir && o.tmpDir == DefaultTmpDir(o.sessionID) {
			return []runnerfs.PathRef{{RunDir: o.runDir, Path: "/tmp"}}, true
		}
		if dir == o.sessionDir && o.sessionDir == DefaultSessionDir(o.sessionID) {
			return []runnerfs.PathRef{{RunDir: o.runDir, Path: "/session"}}, true
		}
	}
	if !filepath.IsAbs(dir) {
		return []runnerfs.PathRef{{RunDir: o.runDir, Path: filepath.ToSlash(dir), Scope: "workspace"}}, true
	}
	if r := o.ref(filepath.Join(dir, "x")); r.Scope == "" {
		return []runnerfs.PathRef{{RunDir: o.runDir, Path: path.Dir(r.Path)}}, true
	}
	if o.runDir != "" {
		if dir == filepath.Clean(o.runDir) {
			return nil, false
		}
		if rel, in := relUnder(o.runDir, dir); in {
			return []runnerfs.PathRef{{RunDir: o.runDir, Path: rel, Scope: "workspace"}}, true
		}
	}
	// Outside the run dir: first from the filesystem root, which the
	// unconfined host-local FS accepts (an absolute path under /tmp would name
	// the chat's namespace there) and a confined runner denies; then the
	// absolute form, which a confined runner resolves beneath whichever
	// allowed root holds the dir.
	return []runnerfs.PathRef{
		{RunDir: "/", Path: strings.TrimPrefix(filepath.ToSlash(dir), "/"), Scope: "workspace"},
		{RunDir: o.runDir, Path: dir, Scope: "workspace"},
	}, true
}

// ensureDir creates dir (and its parents) through the FS. An existing
// directory costs a single Stat.
func (o fileOps) ensureDir(ctx context.Context, dir string) error {
	refs, ok := o.dirRef(dir)
	if !ok {
		return nil
	}
	var err error
	for _, ref := range refs {
		if err = o.ensureRef(ctx, dir, ref); err == nil || runnerfs.CodeOf(err) != runnerfs.CodeDenied {
			return err
		}
	}
	return err
}

func (o fileOps) ensureRef(ctx context.Context, dir string, ref runnerfs.PathRef) error {
	st, err := o.fs.Stat(ctx, ref)
	if err == nil && st.Exists {
		if !st.IsDir {
			return fmt.Errorf("%s exists and is not a directory", dir)
		}
		return nil
	}
	// Stat refuses a namespace root as "below the root": Mkdir decides then.
	if err != nil && runnerfs.CodeOf(err) != runnerfs.CodeBadRequest {
		return err
	}
	return o.fs.Mkdir(ctx, ref, uuid.NewV7().String())
}
