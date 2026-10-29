package workflow

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/backend/lib/runnerfs"
	"github.com/AgentDrasil/asgard/pkg/workflowspec"
)

// recordingFS records every path the workflow touches through the FS.
type recordingFS struct {
	runnerfs.FS
	mu    sync.Mutex
	paths []string
}

func (r *recordingFS) note(p string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.paths = append(r.paths, p)
}

func (r *recordingFS) seen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.paths...)
}

func (r *recordingFS) Open(ctx context.Context, req runnerfs.ReadRequest) (*runnerfs.File, error) {
	r.note("open " + req.Path)
	return r.FS.Open(ctx, req)
}

func (r *recordingFS) Stat(ctx context.Context, ref runnerfs.PathRef) (runnerfs.StatResult, error) {
	r.note("stat " + ref.Path)
	return r.FS.Stat(ctx, ref)
}

func (r *recordingFS) WriteFile(ctx context.Context, req runnerfs.WriteRequest, rd io.Reader) (int64, error) {
	r.note("write " + req.Ref.Path)
	return r.FS.WriteFile(ctx, req, rd)
}

// offlineFS answers every call as an unreachable runner.
type offlineFS struct{ runnerfs.FS }

func (offlineFS) err() error { return runnerfs.Errf(runnerfs.CodeUnavailable, "runner offline") }

func (o offlineFS) Open(context.Context, runnerfs.ReadRequest) (*runnerfs.File, error) {
	return nil, o.err()
}

func (o offlineFS) Stat(context.Context, runnerfs.PathRef) (runnerfs.StatResult, error) {
	return runnerfs.StatResult{}, o.err()
}

func (o offlineFS) WriteFile(context.Context, runnerfs.WriteRequest, io.Reader) (int64, error) {
	return 0, o.err()
}

func (o offlineFS) Remove(context.Context, runnerfs.PathRef, bool, string) error { return o.err() }

func (o offlineFS) Tree(context.Context, runnerfs.TreeRequest) (runnerfs.TreeResult, error) {
	return runnerfs.TreeResult{}, o.err()
}

func (r *recordingFS) Mkdir(ctx context.Context, ref runnerfs.PathRef, id string) error {
	r.note("mkdir " + ref.Path)
	return r.FS.Mkdir(ctx, ref, id)
}

func TestEnsureDirStatsFirstAndLeavesNoProbe(t *testing.T) {
	const sid = "chat-mkdir"
	rfs := &recordingFS{FS: runnerfs.NewLocal(sid)}
	ctx := withFS(t.Context(), rfs)
	runDir := t.TempDir()
	dir := filepath.Join(runDir, "tmp", "a", "b")
	o := newFileOps(ctx, sid, dir, "", runDir)

	require.NoError(t, o.ensureDir(ctx, dir))
	assert.DirExists(t, dir)
	assert.Equal(t, []string{"stat tmp/a/b", "mkdir tmp/a/b"}, rfs.seen())

	// Existing directory: one Stat, no Mkdir.
	rfs.mu.Lock()
	rfs.paths = nil
	rfs.mu.Unlock()
	require.NoError(t, o.ensureDir(ctx, dir))
	assert.Equal(t, []string{"stat tmp/a/b"}, rfs.seen())

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries)

	// A file in the way is an error, not silently accepted.
	f := filepath.Join(runDir, "afile")
	require.NoError(t, os.WriteFile(f, nil, 0o644))
	assert.Error(t, o.ensureDir(ctx, f))

	// The chat's default namespace dirs are created on demand.
	def := newFileOps(ctx, sid, DefaultTmpDir(sid), "", runDir)
	require.NoError(t, def.ensureDir(ctx, DefaultTmpDir(sid)))
	assert.DirExists(t, DefaultTmpDir(sid))
	require.NoError(t, def.ensureDir(ctx, filepath.Join(DefaultTmpDir(sid), "x", "y")))
	assert.DirExists(t, filepath.Join(DefaultTmpDir(sid), "x", "y"))

	// A custom dir outside the run dir (dev mode) is created from "/".
	outside := filepath.Join(t.TempDir(), "o", "p")
	require.NoError(t, o.ensureDir(ctx, outside))
	assert.DirExists(t, outside)
}

// internalErrFS fails every Open with an internal error.
type internalErrFS struct{ runnerfs.FS }

func (internalErrFS) Open(context.Context, runnerfs.ReadRequest) (*runnerfs.File, error) {
	return nil, runnerfs.Errf(runnerfs.CodeInternal, "boom")
}

func TestFanoutItemsReadErrorKeepsOutputFile(t *testing.T) {
	const sid = "chat-fanerr"
	childDefn, err := workflowspec.ParseDefinition([]byte("name: child\nnodes:\n  - id: s\n    type: command\n    command: \"true\""))
	require.NoError(t, err)
	engine, _ := setupTestEngineWithWorkflowRunner(func(string) (*workflowspec.WorkflowDefinition, error) { return childDefn, nil })
	defn, err := workflowspec.ParseDefinition([]byte("name: parent\nnodes:\n  - id: fan\n    type: workflow\n    workflow: child\n    fanout:\n      items_file: items.txt\n      output_file: out.jsonl\n"))
	require.NoError(t, err)

	runDir := t.TempDir()
	out := filepath.Join(runDir, "out.jsonl")
	require.NoError(t, os.WriteFile(out, []byte("previous\n"), 0o644))
	sub := NewSubWorkflowRunner(func(string) (*workflowspec.WorkflowDefinition, error) { return childDefn, nil })
	sub.SetEngine(engine)
	ctx := withFS(t.Context(), internalErrFS{FS: runnerfs.NewLocal(sid)})
	node := defn.Nodes[0]
	res, err := sub.runFanout(ctx, &NodeContext{SessionID: sid, RunDir: runDir, TmpDir: runDir, Node: node, Defn: defn}, node, childDefn)
	require.NoError(t, err)
	assert.Equal(t, workflowspec.StatusFailed, res.Status)
	got, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Equal(t, "previous\n", string(got))
}

func TestRemoteFSNeverDefaultsRunDirToBackendCwd(t *testing.T) {
	const sid = "chat-nocwd"
	defn, err := workflowspec.ParseDefinition([]byte("name: wf\nnodes:\n  - id: n\n    type: command\n    command: \"true\""))
	require.NoError(t, err)
	var gotRunDir string
	registry := NewNodeRunnerRegistry()
	registry.Register(&runDirRecorder{out: &gotRunDir})
	engine := NewEngine(registry)

	// Not a runnerfs.Local: stands in for a remote runner's FS.
	engine.SetFSResolver(func(string) (runnerfs.FS, error) {
		return &recordingFS{FS: runnerfs.NewLocal(sid)}, nil
	})
	res, err := engine.Execute(t.Context(), defn, RunContext{SessionID: sid, TmpDir: t.TempDir(), SessionDir: t.TempDir()})
	require.NoError(t, err)
	require.Equal(t, RunStatusCompleted, res.Status)
	assert.Empty(t, gotRunDir)

	// The host-local FS keeps the working directory default.
	engine.SetFSResolver(nil)
	_, err = engine.Execute(t.Context(), defn, RunContext{SessionID: sid, TmpDir: t.TempDir(), SessionDir: t.TempDir()})
	require.NoError(t, err)
	wd, _ := os.Getwd()
	assert.Equal(t, wd, gotRunDir)

	// The effective run dir is validated.
	_, err = engine.Execute(t.Context(), defn, RunContext{SessionID: sid, RunDir: filepath.Join(t.TempDir(), "missing")})
	assert.ErrorContains(t, err, "does not exist")
}

type runDirRecorder struct{ out *string }

func (r *runDirRecorder) Supports(t workflowspec.NodeType) bool {
	return t == workflowspec.NodeTypeCommand
}

func (r *runDirRecorder) Run(_ context.Context, nctx *NodeContext) (*workflowspec.NodeResult, error) {
	*r.out = nctx.RunDir
	return &workflowspec.NodeResult{Status: workflowspec.StatusSucceeded}, nil
}

func TestFileOpsRef(t *testing.T) {
	const sid = "chat-ref"
	runDir := t.TempDir()
	o := fileOps{sessionID: sid, tmpDir: DefaultTmpDir(sid), sessionDir: DefaultSessionDir(sid), runDir: runDir}

	cases := []struct {
		name, in string
		want     runnerfs.PathRef
	}{
		{"default tmp", filepath.Join(DefaultTmpDir(sid), "a", "b.txt"), runnerfs.PathRef{RunDir: runDir, Path: "/tmp/a/b.txt"}},
		{"default session", filepath.Join(DefaultSessionDir(sid), "p.md"), runnerfs.PathRef{RunDir: runDir, Path: "/session/p.md"}},
		{"inside run dir", filepath.Join(runDir, "src", "x.go"), runnerfs.PathRef{RunDir: runDir, Path: "src/x.go", Scope: "workspace"}},
		{"namespace-like dir inside run dir", filepath.Join(runDir, "tmp", "chat", "x.txt"), runnerfs.PathRef{RunDir: runDir, Path: "tmp/chat/x.txt", Scope: "workspace"}},
		{"relative", "src/x.go", runnerfs.PathRef{RunDir: runDir, Path: "src/x.go", Scope: "workspace"}},
		{"elsewhere", "/etc/passwd", runnerfs.PathRef{RunDir: runDir, Path: "/etc/passwd", Scope: "workspace"}},
		{"dotdot is cleaned", filepath.Join(DefaultTmpDir(sid), "..", "other", "f"), runnerfs.PathRef{RunDir: runDir, Path: filepath.Join(filepath.Dir(DefaultTmpDir(sid)), "other", "f"), Scope: "workspace"}},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, o.ref(tc.in), tc.name)
	}

	// A custom tmp dir is not the chat's /tmp: it is never sent as "/tmp/..."
	// (even when it lies under the host's /tmp) but relative to itself.
	elsewhere := filepath.Join(t.TempDir(), "mytmp")
	custom := fileOps{sessionID: sid, tmpDir: elsewhere, runDir: runDir}
	assert.Equal(t, runnerfs.PathRef{RunDir: elsewhere, Path: "f/g", Scope: "workspace"}, custom.ref(filepath.Join(elsewhere, "f", "g")))
}

func TestFanoutAndOutputsGoThroughChatFS(t *testing.T) {
	const sid = "chat-fanout"
	childDefn, err := workflowspec.ParseDefinition([]byte("name: child\nnodes:\n  - id: s\n    type: command\n    command: \"echo got ${input}\""))
	require.NoError(t, err)
	engine, _ := setupTestEngineWithWorkflowRunner(func(string) (*workflowspec.WorkflowDefinition, error) { return childDefn, nil })
	rfs := &recordingFS{FS: runnerfs.NewLocal(sid)}
	engine.SetFSResolver(func(id string) (runnerfs.FS, error) {
		assert.Equal(t, sid, id)
		return rfs, nil
	})

	runDir := t.TempDir()
	// Items live in the chat's default tmp dir, written through the FS.
	itemsPath := filepath.Join(DefaultTmpDir(sid), "items.txt")
	require.NoError(t, os.MkdirAll(filepath.Dir(itemsPath), 0o755))
	require.NoError(t, os.WriteFile(itemsPath, []byte("a\n\nb\n"), 0o644))

	defn, err := workflowspec.ParseDefinition([]byte("name: parent\nnodes:\n  - id: fan\n    type: workflow\n    workflow: child\n    fanout:\n      items_file: items.txt\n      output_file: out/results.jsonl\n"))
	require.NoError(t, err)
	res, err := engine.Execute(t.Context(), defn, RunContext{SessionID: sid, RunDir: runDir})
	require.NoError(t, err)
	require.Equal(t, RunStatusCompleted, res.Status)

	out, err := os.ReadFile(filepath.Join(DefaultTmpDir(sid), "out", "results.jsonl"))
	require.NoError(t, err)
	assert.Contains(t, string(out), `"item":"a"`)
	assert.Contains(t, string(out), `"item":"b"`)

	seen := rfs.seen()
	assert.Contains(t, seen, "open /tmp/items.txt")
	assert.Contains(t, seen, "write /tmp/out/results.jsonl")
	for _, p := range seen {
		assert.NotContains(t, p, DefaultTmpDir(sid), "backend host paths never reach the runner")
	}
}

func TestOfflineRunnerFailsRatherThanLooksEmpty(t *testing.T) {
	const sid = "chat-offline"
	childDefn, err := workflowspec.ParseDefinition([]byte("name: child\nnodes:\n  - id: s\n    type: command\n    command: \"true\""))
	require.NoError(t, err)
	engine, _ := setupTestEngineWithWorkflowRunner(func(string) (*workflowspec.WorkflowDefinition, error) { return childDefn, nil })

	// A resolver that cannot reach the runner fails the run up front.
	engine.SetFSResolver(func(string) (runnerfs.FS, error) {
		return nil, runnerfs.Errf(runnerfs.CodeUnavailable, "runner offline")
	})
	defn, err := workflowspec.ParseDefinition([]byte("name: parent\nnodes:\n  - id: fan\n    type: workflow\n    workflow: child\n    fanout:\n      items_file: items.txt\n"))
	require.NoError(t, err)
	_, err = engine.Execute(t.Context(), defn, RunContext{SessionID: sid, RunDir: t.TempDir(), TmpDir: t.TempDir()})
	require.Error(t, err)
	assert.Equal(t, runnerfs.CodeUnavailable, runnerfs.CodeOf(unwrapFS(err)))

	// A runner that drops after the run started: an unreadable items file is
	// a failed fanout node, not an empty item list.
	engine.SetFSResolver(func(string) (runnerfs.FS, error) {
		return offlineFS{FS: runnerfs.NewLocal(sid)}, nil
	})
	_, err = engine.Execute(t.Context(), defn, RunContext{SessionID: sid, RunDir: t.TempDir(), TmpDir: t.TempDir()})
	require.Error(t, err) // creating the (custom) tmp dir already needs the runner
	ctx := withFS(t.Context(), offlineFS{FS: runnerfs.NewLocal(sid)})
	node := defn.Nodes[0]
	sub := NewSubWorkflowRunner(func(string) (*workflowspec.WorkflowDefinition, error) { return childDefn, nil })
	sub.SetEngine(engine)
	nres, err := sub.runFanout(ctx, &NodeContext{SessionID: sid, RunDir: t.TempDir(), TmpDir: t.TempDir(), Node: node, Defn: defn}, node, childDefn)
	require.NoError(t, err)
	assert.Equal(t, workflowspec.StatusFailed, nres.Status)
}

func unwrapFS(err error) error {
	for err != nil {
		if _, ok := err.(*runnerfs.Error); ok {
			return err
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return err
		}
		err = u.Unwrap()
	}
	return err
}

func TestHumanReplyAndRequiredOutputsUseFS(t *testing.T) {
	const sid = "chat-human"
	rfs := &recordingFS{FS: runnerfs.NewLocal(sid)}
	ctx := withFS(t.Context(), rfs)
	runDir := t.TempDir()
	nctx := &NodeContext{
		SessionID: sid, RunDir: runDir, TmpDir: DefaultTmpDir(sid), SessionDir: DefaultSessionDir(sid),
		Node: &workflowspec.NodeSpec{ID: "ask", OutputFile: "answers/reply.md"},
	}
	res := humanReplyResult(ctx, nctx, "yes")
	require.Contains(t, res.Artifacts, "answers/reply.md")
	got, err := os.ReadFile(filepath.Join(DefaultSessionDir(sid), "answers", "reply.md"))
	require.NoError(t, err)
	assert.Equal(t, "yes", string(got))

	missing := checkRequiredOutputs(ctx, []string{"${session_dir}/answers/reply.md", "${session_dir}/nope.md"}, nctx)
	assert.Equal(t, []string{"/session/nope.md"}, missing)
	assert.Contains(t, rfs.seen(), "write /session/answers/reply.md")
	assert.Contains(t, rfs.seen(), "stat /session/nope.md")

	// An unreachable runner never counts as "output present".
	missing = checkRequiredOutputs(withFS(t.Context(), offlineFS{FS: rfs.FS}), []string{"${session_dir}/answers/reply.md"}, nctx)
	assert.Equal(t, []string{"/session/answers/reply.md"}, missing)
}

func TestValidateRunDirThroughFS(t *testing.T) {
	fs := runnerfs.NewLocal("chat-val")
	dir := t.TempDir()
	require.NoError(t, validateRunDir(t.Context(), fs, dir))

	file := filepath.Join(dir, "f")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0o644))
	assert.ErrorContains(t, validateRunDir(t.Context(), fs, file), "not a directory")
	assert.ErrorContains(t, validateRunDir(t.Context(), fs, filepath.Join(dir, "missing")), "does not exist")
	assert.Error(t, validateRunDir(t.Context(), offlineFS{FS: fs}, dir))
}

func TestEnsureDirAbsoluteUnderConfinedMountRoot(t *testing.T) {
	const sid = "chat-mount"
	t.Setenv("HOME", t.TempDir())
	// An absolute path under /tmp names the chat's tmp namespace, so the
	// mount root lives under the package dir instead.
	base, err := os.MkdirTemp(".", "mount-test-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	base, err = filepath.Abs(base)
	require.NoError(t, err)
	run, mount := t.TempDir(), filepath.Join(base, "mnt")
	require.NoError(t, os.MkdirAll(mount, 0o755))
	fs := runnerfs.NewConfined(sid, func(bool) []string { return []string{run, mount} }, nil)
	ctx := withFS(t.Context(), fs)
	dir := filepath.Join(mount, "wf", "tmp")
	o := newFileOps(ctx, sid, dir, "", run)

	require.NoError(t, o.ensureDir(ctx, dir))
	assert.DirExists(t, dir)
	require.NoError(t, o.ensureDir(ctx, dir))

	// Beyond every allowed root stays refused.
	assert.Error(t, o.ensureDir(ctx, filepath.Join(base, "other", "nope")))
}
