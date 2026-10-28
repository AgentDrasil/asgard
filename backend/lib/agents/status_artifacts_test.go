package agents

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/pkg/agentspec"
	"github.com/AgentDrasil/asgard/pkg/paths"
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
	tmp, sess := paths.SessionTmpDir(chat), paths.SessionDir(chat)
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
