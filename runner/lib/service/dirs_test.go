package service

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/AgentDrasil/asgard/pkg/agentspec"
	runnerpaths "github.com/AgentDrasil/asgard/pkg/paths/runner"
	"github.com/AgentDrasil/asgard/pkg/runnerpb"
)

func absRef(p string) *runnerpb.DirRef {
	return &runnerpb.DirRef{Scope: runnerpb.FileScope_FILE_SCOPE_ABSOLUTE, RelOrPath: p}
}

func put(t *testing.T, p, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=T", "-c", "user.email=t@example.com", "-c", "init.defaultBranch=main", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	return strings.TrimSpace(string(out))
}

func TestTreePagingScopeAndTraversal(t *testing.T) {
	cl, ws := startService(t, Options{})
	for _, n := range []string{"a", "b", "c", "d", "e"} {
		put(t, filepath.Join(ws, n+".txt"), n)
	}
	require.NoError(t, os.MkdirAll(filepath.Join(ws, "sub"), 0o755))
	outside := t.TempDir()
	require.NoError(t, os.Symlink(outside, filepath.Join(ws, "escape")))

	var names []string
	token := ""
	pages := 0
	for {
		resp, err := cl.Tree(t.Context(), &runnerpb.TreeRequest{SessionId: chat, RunDir: absRef(ws), Limit: 2, PageToken: token})
		require.NoError(t, err)
		pages++
		for _, e := range resp.GetEntries() {
			names = append(names, e.GetName())
		}
		if token = resp.GetNextPageToken(); token == "" {
			break
		}
	}
	assert.Equal(t, []string{"sub", "a.txt", "b.txt", "c.txt", "d.txt", "e.txt", "escape"}, names)
	assert.Equal(t, 4, pages)

	for name, tc := range map[string]struct {
		req  *runnerpb.TreeRequest
		code codes.Code
	}{
		"dotdot":          {&runnerpb.TreeRequest{SessionId: chat, RunDir: absRef(ws), Path: "../x"}, codes.PermissionDenied},
		"symlink escape":  {&runnerpb.TreeRequest{SessionId: chat, RunDir: absRef(ws), Path: "escape"}, codes.PermissionDenied},
		"outside run dir": {&runnerpb.TreeRequest{SessionId: chat, RunDir: absRef(outside)}, codes.PermissionDenied},
		"is a file":       {&runnerpb.TreeRequest{SessionId: chat, RunDir: absRef(ws), Path: "a.txt"}, codes.InvalidArgument},
		"missing":         {&runnerpb.TreeRequest{SessionId: chat, RunDir: absRef(ws), Path: "nope"}, codes.NotFound},
		"bad chat":        {&runnerpb.TreeRequest{SessionId: "../x", RunDir: absRef(ws)}, codes.InvalidArgument},
		"bad token":       {&runnerpb.TreeRequest{SessionId: chat, RunDir: absRef(ws), PageToken: "?"}, codes.InvalidArgument},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := cl.Tree(t.Context(), tc.req)
			assert.Equal(t, tc.code, status.Code(err))
		})
	}

	// TMP-scoped dir refs resolve in the chat's own tmp dir.
	put(t, filepath.Join(runnerpaths.SessionTmpDir(chat), "t.txt"), "t")
	resp, err := cl.Tree(t.Context(), &runnerpb.TreeRequest{SessionId: chat, RunDir: &runnerpb.DirRef{Scope: runnerpb.FileScope_FILE_SCOPE_TMP}})
	require.NoError(t, err)
	require.Len(t, resp.GetEntries(), 1)
	assert.Equal(t, "t.txt", resp.GetEntries()[0].GetName())
	_, err = cl.Tree(t.Context(), &runnerpb.TreeRequest{SessionId: chat, RunDir: &runnerpb.DirRef{Scope: runnerpb.FileScope_FILE_SCOPE_TMP, RelOrPath: "../other"}})
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
}

func TestSearchSubdirsOverRPC(t *testing.T) {
	cl, ws := startService(t, Options{})
	put(t, filepath.Join(ws, "src", "main.go"), "m")
	put(t, filepath.Join(ws, "src", "other.go"), "o")
	require.NoError(t, os.MkdirAll(filepath.Join(ws, "zdir"), 0o755))
	require.NoError(t, os.Symlink(t.TempDir(), filepath.Join(ws, "escape")))

	resp, err := cl.Search(t.Context(), &runnerpb.SearchRequest{SessionId: chat, RunDir: absRef(ws), Query: "main"})
	require.NoError(t, err)
	require.Len(t, resp.GetFiles(), 1)
	assert.Equal(t, "src/main.go", resp.GetFiles()[0].GetPath())
	assert.Equal(t, runnerpb.FileScope_FILE_SCOPE_WORKSPACE, resp.GetFiles()[0].GetScope())

	_, err = cl.Search(t.Context(), &runnerpb.SearchRequest{SessionId: chat, RunDir: absRef("/etc")})
	assert.Equal(t, codes.PermissionDenied, status.Code(err))

	sub, err := cl.Subdirs(t.Context(), &runnerpb.SubdirsRequest{Dir: ws, Limit: 1})
	require.NoError(t, err)
	assert.Equal(t, []string{"src"}, sub.GetSubdirs())
	assert.NotEmpty(t, sub.GetNextPageToken())
	sub, err = cl.Subdirs(t.Context(), &runnerpb.SubdirsRequest{Dir: ws, Limit: 1, PageToken: sub.GetNextPageToken()})
	require.NoError(t, err)
	assert.Equal(t, []string{"zdir"}, sub.GetSubdirs())
	assert.Empty(t, sub.GetNextPageToken())

	for dir, code := range map[string]codes.Code{
		"/":                         codes.PermissionDenied,
		"/etc":                      codes.PermissionDenied,
		filepath.Join(ws, "escape"): codes.PermissionDenied,
		filepath.Join(ws, "nope"):   codes.InvalidArgument,
		filepath.Join(ws, "../ws2"): codes.PermissionDenied,
		"relative":                  codes.InvalidArgument,
	} {
		_, err = cl.Subdirs(t.Context(), &runnerpb.SubdirsRequest{Dir: dir})
		assert.Equal(t, code, status.Code(err), dir)
	}
}

func TestGitRPCs(t *testing.T) {
	cl, ws := startService(t, Options{})
	git(t, ws, "init")
	put(t, filepath.Join(ws, "keep.txt"), "one\n")
	git(t, ws, "add", ".")
	git(t, ws, "commit", "-m", "first")
	put(t, filepath.Join(ws, "keep.txt"), "one\ntwo\n")
	put(t, filepath.Join(ws, "new.txt"), "n\n")
	remote := filepath.Join(t.TempDir(), "remote.git")
	git(t, filepath.Dir(remote), "init", "--bare", remote)
	fakeSSH(t)
	git(t, ws, "remote", "add", "origin", sshURL(remote))

	st, err := cl.GitDiff(t.Context(), &runnerpb.GitDiffRequest{SessionId: chat, RunDir: absRef(ws)})
	require.NoError(t, err)
	got := map[string]*runnerpb.GitDiffFile{}
	for {
		f, err := st.Recv()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		got[f.GetNewPath()] = f
	}
	require.Len(t, got, 2)
	assert.Equal(t, "M", got["keep.txt"].GetStatus())
	assert.Equal(t, "one\ntwo\n", got["keep.txt"].GetNewContent())
	assert.Equal(t, "A", got["new.txt"].GetStatus())

	// Dangerous commit values and escaping run dirs.
	st, err = cl.GitDiff(t.Context(), &runnerpb.GitDiffRequest{SessionId: chat, RunDir: absRef(ws), Commit: "--output=/tmp/x"})
	require.NoError(t, err)
	_, err = st.Recv()
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
	st, err = cl.GitDiff(t.Context(), &runnerpb.GitDiffRequest{SessionId: chat, RunDir: absRef("/etc")})
	require.NoError(t, err)
	_, err = st.Recv()
	assert.Equal(t, codes.PermissionDenied, status.Code(err))

	lg, err := cl.GitLog(t.Context(), &runnerpb.GitLogRequest{SessionId: chat, RunDir: absRef(ws), Limit: 5})
	require.NoError(t, err)
	require.Len(t, lg.GetCommits(), 1)
	assert.Equal(t, "first", lg.GetCommits()[0].GetMessage())
	assert.EqualValues(t, 2, lg.GetUnstashedCount())

	// Push is idempotent per request_id: the retry gets the cached result and
	// does not push the commit made in between.
	push := func(id string) *runnerpb.GitOpResponse {
		resp, err := cl.GitPush(t.Context(), &runnerpb.GitSessionRequest{SessionId: chat, RunDir: absRef(ws), RequestId: id})
		require.NoError(t, err)
		return resp
	}
	first := push("p1")
	assert.False(t, first.GetSuccess(), "no upstream configured: git reports the failure in the response")
	git(t, ws, "push", "-u", "origin", "main")
	put(t, filepath.Join(ws, "more.txt"), "m\n")
	git(t, ws, "add", ".")
	git(t, ws, "commit", "-m", "second")
	assert.Equal(t, first.GetError(), push("p1").GetError(), "retry returns the cached result")
	assert.NotEqual(t, git(t, ws, "rev-parse", "HEAD"), git(t, remote, "rev-parse", "main"), "retry did not push")
	assert.True(t, push("p2").GetSuccess())
	assert.Equal(t, git(t, ws, "rev-parse", "HEAD"), git(t, remote, "rev-parse", "main"))

	// Pull the clone's new commit; the retry with the same id is a cache hit.
	clone := filepath.Join(t.TempDir(), "clone")
	git(t, filepath.Dir(clone), "clone", remote, clone)
	put(t, filepath.Join(clone, "up.txt"), "u\n")
	git(t, clone, "add", ".")
	git(t, clone, "commit", "-m", "up")
	git(t, clone, "push")
	pull := func(id string) *runnerpb.GitOpResponse {
		resp, err := cl.GitPull(t.Context(), &runnerpb.GitSessionRequest{SessionId: chat, RunDir: absRef(ws), RequestId: id})
		require.NoError(t, err)
		return resp
	}
	assert.True(t, pull("u1").GetSuccess())
	assert.FileExists(t, filepath.Join(ws, "up.txt"))
	require.NoError(t, os.Remove(filepath.Join(ws, "up.txt")))
	assert.True(t, pull("u1").GetSuccess())
	assert.NoFileExists(t, filepath.Join(ws, "up.txt"), "cached pull is not re-run")

	_, err = cl.GitPull(t.Context(), &runnerpb.GitSessionRequest{SessionId: chat, RunDir: absRef("/etc")})
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
}

func TestGitRPCRefusesHostileConfig(t *testing.T) {
	cl, ws := startService(t, Options{})
	git(t, ws, "init")
	marker := filepath.Join(t.TempDir(), "pwned")
	git(t, ws, "config", "--local", "core.fsmonitor", "touch "+marker)
	put(t, filepath.Join(ws, "f.txt"), "x")

	st, err := cl.GitDiff(t.Context(), &runnerpb.GitDiffRequest{SessionId: chat, RunDir: absRef(ws)})
	require.NoError(t, err)
	_, err = st.Recv()
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
	_, err = cl.GitLog(t.Context(), &runnerpb.GitLogRequest{SessionId: chat, RunDir: absRef(ws)})
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
	assert.NoFileExists(t, marker)
}

func TestGitReadonlyMountBlocksPull(t *testing.T) {
	ro := t.TempDir()
	cl, _ := startService(t, Options{Agents: func() ([]*agentspec.Agent, error) {
		return []*agentspec.Agent{{Config: agentspec.AgentConfig{RunDirs: []string{ro}, MountDirs: agentspec.MountConfig{ReadOnly: []string{ro}}}}}, nil
	}})
	git(t, ro, "init")
	resp, err := cl.GitLog(t.Context(), &runnerpb.GitLogRequest{SessionId: chat, RunDir: absRef(ro)})
	require.NoError(t, err)
	assert.Empty(t, resp.GetCommits())
	_, err = cl.GitPull(t.Context(), &runnerpb.GitSessionRequest{SessionId: chat, RunDir: absRef(ro)})
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
}

// fakeSSH points git at a stand-in ssh that runs the remote command locally,
// so tests can use ssh:// remotes (the only transports the runner allows).
func fakeSSH(t *testing.T) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "fakessh")
	script := "#!/bin/sh\nwhile [ \"${1#-}\" != \"$1\" ]; do\n  if [ \"$1\" = -o ] || [ \"$1\" = -p ]; then shift; fi\n  shift\ndone\nshift\nexec sh -c \"$1\"\n"
	require.NoError(t, os.WriteFile(p, []byte(script), 0o755))
	t.Setenv("GIT_SSH_COMMAND", p)
}

func sshURL(p string) string { return "ssh://localhost" + p }
