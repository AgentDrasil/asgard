package agents

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/pkg/agentspec"
	runnerpaths "github.com/AgentDrasil/asgard/pkg/paths/runner"
)

func viewerPaths(arts []StatusArtifact) []string {
	out := make([]string, 0, len(arts))
	for _, a := range arts {
		out = append(out, a.ViewerPath)
	}
	return out
}

func TestStatusArtifacts(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const chat = "chat-1"
	tmp, sess := runnerpaths.SessionTmpDir(chat), runnerpaths.SessionDir(chat)
	require.NoError(t, os.MkdirAll(filepath.Join(tmp, "plan"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "plan", "a.md"), []byte("x"), 0o644))
	require.NoError(t, os.MkdirAll(sess, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sess, "n.md"), []byte("x"), 0o644))

	ws := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(ws, "tmp"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(ws, "tmp", "local.txt"), []byte("x"), 0o644))
	require.NoError(t, exec.Command("git", "-C", ws, "init", "-q").Run())
	require.NoError(t, os.WriteFile(filepath.Join(ws, ".gitignore"), []byte("build/\n"), 0o644))
	cfg := &agentspec.AgentConfig{}

	tests := []struct {
		name      string
		workspace string
		files     []string
		want      []string
		wantHost  []string
	}{
		{
			name: "relative tmp in session-tmp run dir is canonicalised", workspace: tmp,
			files:    []string{"tmp/plan/a.md", "tmp/chat-1/plan/a.md", "tmp"},
			want:     []string{"/tmp/plan/a.md", "/tmp/plan/a.md", "/tmp"},
			wantHost: []string{filepath.Join(tmp, "plan/a.md"), filepath.Join(tmp, "plan/a.md"), tmp},
		},
		{
			name: "relative tmp existing in workspace stays a workspace file", workspace: ws,
			files: []string{"tmp/local.txt"},
			want:  nil, // tracked/unignored workspace file is not an artifact
		},
		{
			name: "relative tmp missing in workspace but present in session tmp", workspace: ws,
			files:    []string{"tmp/plan/a.md"},
			want:     []string{"/tmp/plan/a.md"},
			wantHost: []string{filepath.Join(tmp, "plan/a.md")},
		},
		{
			name: "relative session present in session dir", workspace: ws,
			files:    []string{"session/n.md"},
			want:     []string{"/session/n.md"},
			wantHost: []string{filepath.Join(sess, "n.md")},
		},
		{
			name: "relative session missing everywhere is a workspace path", workspace: ws,
			files: []string{"session/missing.md"},
			want:  nil,
		},
		{
			name: "explicit sandbox forms", workspace: ws,
			files:    []string{"/tmp/x.md", ".tmp/y.md", "/session/z.md", ".session/w.md"},
			want:     []string{"/tmp/x.md", "/tmp/y.md", "/session/z.md", "/session/w.md"},
			wantHost: []string{filepath.Join(tmp, "x.md"), filepath.Join(tmp, "y.md"), filepath.Join(sess, "z.md"), filepath.Join(sess, "w.md")},
		},
		{
			name: "gitignore decides inside the workspace", workspace: ws,
			files:    []string{"build/out.bin", "src/main.go"},
			want:     []string{"build/out.bin"},
			wantHost: []string{filepath.Join(ws, "build/out.bin")},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StatusArtifacts(chat, tt.files, cfg, tt.workspace)
			assert.ElementsMatch(t, tt.want, viewerPaths(got))
			if tt.wantHost != nil {
				hosts := make([]string, 0, len(got))
				for _, a := range got {
					hosts = append(hosts, a.HostPath)
				}
				assert.ElementsMatch(t, tt.wantHost, hosts)
			}
		})
	}
}

func TestSharedArtifacts(t *testing.T) {
	t.Setenv(runnerpaths.EnvDataDir, t.TempDir())
	cfg := &agentspec.AgentConfig{SharedChatID: "root-1"}

	// /shared writes are artifacts wherever the workspace is; the viewer path
	// keeps the /shared/<rel> form and the host path is the tree's directory.
	arts := StatusArtifacts("child-1", []string{"/shared/out/r.md", "/shared", "/shared/../etc/passwd", "shared/rel.txt"}, cfg, t.TempDir())
	assert.Equal(t, []string{"/shared/out/r.md", "/shared"}, viewerPaths(arts))
	assert.Equal(t, filepath.Join(runnerpaths.SharedChatDir("root-1"), "out", "r.md"), arts[0].HostPath)
	assert.Equal(t, runnerpaths.SharedChatDir("root-1"), arts[1].HostPath)

	// Without a spawn tree there is no host directory behind /shared.
	assert.Empty(t, StatusArtifacts("child-1", []string{"/shared/x"}, &agentspec.AgentConfig{}, ""))

	assert.True(t, IsArtifact("/shared/a", cfg, ""))
	assert.True(t, IsArtifact("/shared", cfg, ""))
	assert.False(t, IsArtifact("/sharedx/a", cfg, ""))
	// Only a run with a spawn tree directory has a /shared.
	assert.False(t, IsArtifact("/shared/a", nil, ""))
	assert.False(t, IsArtifact("/shared/a", &agentspec.AgentConfig{}, ""))

	// An unsandboxed run names the directory by its host path.
	host := filepath.Join(runnerpaths.SharedChatDir("root-1"), "o", "r.md")
	arts = StatusArtifacts("child-1", []string{host, runnerpaths.SharedChatDir("root-1")}, cfg, "")
	assert.Equal(t, []string{"/shared/o/r.md", "/shared"}, viewerPaths(arts))
	assert.Equal(t, host, arts[0].HostPath)
	assert.Equal(t, "/shared/o", ViewerArtifactPathShared(filepath.Join(runnerpaths.SharedChatDir("r"), "o"), "", "", runnerpaths.SharedChatDir("r")))
	assert.Equal(t, "/shared/a/b", ViewerArtifactPath("/shared/a/b", "", ""))
	assert.Equal(t, "/shared/a", ViewerArtifactPath("/shared//a/", "", ""))
}

func TestResolveSharedPath(t *testing.T) {
	tests := []struct {
		req, scope string
		ok         bool
		sub        string
	}{
		{"/shared", "", true, ""},
		{"/shared/", "", true, ""},
		{"/shared/a/b.txt", "", true, "a/b.txt"},
		{"/shared/a/../b", "", true, "b"},
		{"/shared/../x", "", false, ""},
		{"/sharedx", "", false, ""},
		{"shared/a", "", false, ""},
		{"a/b", "", false, ""},
		{"", "shared", true, ""},
		{".", "shared", true, ""},
		{"a/b", "shared", true, "a/b"},
		{"/etc/passwd", "shared", true, "etc/passwd"},
		{"../x", "shared", true, "../x"},
		{"a/../../x", "shared", true, "../x"},
		{"a", "tmp", false, ""},
		{"/shared/a", "tmp", false, ""},
		{"/shared/a", "workspace", false, ""},
		{"/shared/a", "session", false, ""},
		{"/shared/a", "shared", true, "a"},
	}
	for _, tc := range tests {
		ok, sub := ResolveSharedPath(tc.req, tc.scope)
		assert.Equal(t, tc.ok, ok, "%q %q", tc.req, tc.scope)
		assert.Equal(t, tc.sub, sub, "%q %q", tc.req, tc.scope)
	}
	assert.True(t, SharedSubEscapes(".."))
	assert.True(t, SharedSubEscapes("../x"))
	assert.False(t, SharedSubEscapes("..x"))

	assert.Equal(t, []AuthCandidate{{"shared", "/shared/a"}}, AuthCandidates("/shared/a", "", "c1"))
	assert.Equal(t, []AuthCandidate{{"shared", "/shared"}}, AuthCandidates("", "shared", "c1"))
	// A workspace file called shared/x stays a workspace path.
	assert.Equal(t, []AuthCandidate{{"", "shared/x"}}, AuthCandidates("shared/x", "", "c1"))
}
