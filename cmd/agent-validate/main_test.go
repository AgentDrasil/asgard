package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/getsops/sops/v3"
	"github.com/getsops/sops/v3/aes"
	sopsage "github.com/getsops/sops/v3/age"
	"github.com/getsops/sops/v3/cmd/sops/common"
	sopsyaml "github.com/getsops/sops/v3/stores/yaml"
	sopsversion "github.com/getsops/sops/v3/version"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/agentwrapper"
	"github.com/AgentDrasil/asgard/agentwrapper/types"
)

func writeTreeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
}

func TestIsTreeRoot(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		files []string
		want  bool
	}{
		{"agents teams.yaml", []string{"agents/teams.yaml"}, true},
		{"runners dir", []string{"runners/n1/agents/a/config.yaml"}, true},
		{"legacy layout", []string{"teams.yaml", "agents/a/config.yaml"}, false},
		{"single agent dir", []string{"config.yaml"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			for _, f := range tt.files {
				writeTreeFile(t, root, f, "x")
			}
			assert.Equal(t, tt.want, isTreeRoot(root))
		})
	}
}

func TestReadTreeFiles(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeTreeFile(t, root, "agents/teams.yaml", "teams: []")
	writeTreeFile(t, root, "agents/a/skills/s/SKILL.md", "s")
	writeTreeFile(t, root, "runners/n1/agents/b/config.yaml", "b")
	writeTreeFile(t, root, "asgard/config.yaml", "ignored")

	files, err := readTreeFiles(root)
	require.NoError(t, err)
	assert.Len(t, files, 3)
	assert.Equal(t, []byte("s"), files["agents/a/skills/s/SKILL.md"])
	assert.Contains(t, files, "runners/n1/agents/b/config.yaml")
}

const validAgentYAML = "id: %s\nname: %s\ndescription: d\nmain_agent: false\ncli:\n  - cli: opencode\n    model: deepseek-chat\n"

func useMockCLIs(t *testing.T) {
	t.Helper()
	agentwrapper.SetClients(map[string]types.CLIClient{"opencode": stubClient{}})
	t.Cleanup(func() { agentwrapper.SetClients(nil) })
}

type stubClient struct{}

func (stubClient) Usage(context.Context, types.UsageOptions) ([]types.ModelUsage, error) {
	return nil, nil
}

func (stubClient) Models(context.Context, types.UsageOptions) ([]string, error) {
	return []string{"deepseek-chat"}, nil
}

func (stubClient) Prompt(context.Context, string, types.PromptOptions) (*types.PromptResult, error) {
	return &types.PromptResult{}, nil
}

func agentFixture(id string) string { return fmt.Sprintf(validAgentYAML, id, id) }

func TestValidateTree(t *testing.T) {
	useMockCLIs(t)

	tests := []struct {
		name    string
		files   map[string]string
		wantOK  bool
		wantOut string
		wantErr string
	}{
		{
			name: "reference shape without teams.yaml or runners",
			files: map[string]string{
				"agents/a/config.yaml": agentFixture("a"),
				"agents/a/AGENTS.md":   "rules",
				"asgard/config.yaml":   "debug: true",
				"agents/b/config.yaml": agentFixture("b"),
			},
			wantOK:  true,
			wantOut: "Agent tree is valid",
		},
		{
			name: "collision",
			files: map[string]string{
				"agents/a/config.yaml":            agentFixture("a"),
				"runners/n1/agents/a/config.yaml": agentFixture("a"),
			},
			wantErr: `agent id "a" is defined both`,
		},
		{
			name: "bad runner agent",
			files: map[string]string{
				"agents/a/config.yaml":                 agentFixture("a"),
				"runners/n1/agents/Bad_Id/config.yaml": agentFixture("Bad_Id"),
			},
			wantErr: "runner n1:",
		},
		{
			name: "bad runner node id",
			files: map[string]string{
				"agents/a/config.yaml":                  agentFixture("a"),
				"runners/Bad Node/agents/b/config.yaml": agentFixture("b"),
			},
			wantErr: "invalid runner node id",
		},
		{
			name:    "neither agents nor runners",
			files:   map[string]string{"asgard/config.yaml": "debug: true"},
			wantErr: "neither agents/ nor runners/",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			for k, v := range tt.files {
				writeTreeFile(t, root, k, v)
			}
			if tt.name == "reference shape without teams.yaml or runners" {
				assert.True(t, isTreeRoot(root))
			}

			var out, errOut bytes.Buffer
			ok := validateTree(root, &out, &errOut)
			assert.Equal(t, tt.wantOK, ok)
			assert.Contains(t, out.String(), tt.wantOut)
			assert.Contains(t, errOut.String(), tt.wantErr)
		})
	}
}

func TestReadTreeFiles_DecryptsInlineSOPSYAML(t *testing.T) {
	t.Setenv(sopsage.SopsAgeKeyEnv, "AGE-SECRET-KEY-1G0Q5K9TV4REQ3ZSQRMTMG8NSWQGYT0T7TZ33RAZEE0GZYVZN0APSU24RK7")

	key, err := sopsage.MasterKeyFromRecipient("age1lzd99uklcjnc0e7d860axevet2cz99ce9pq6tzuzd05l5nr28ams36nvun")
	require.NoError(t, err)
	store := &sopsyaml.Store{}
	branches, err := store.LoadPlainFile([]byte(agentFixture("enc")))
	require.NoError(t, err)
	tree := sops.Tree{
		Branches: branches,
		Metadata: sops.Metadata{KeyGroups: []sops.KeyGroup{{key}}, Version: sopsversion.Version},
	}
	dataKey, errs := tree.GenerateDataKey()
	require.Empty(t, errs)
	require.NoError(t, common.EncryptTree(common.EncryptTreeOpts{DataKey: dataKey, Tree: &tree, Cipher: aes.NewCipher()}))
	enc, err := store.EmitEncryptedFile(tree)
	require.NoError(t, err)
	require.Contains(t, string(enc), "sops:")

	root := t.TempDir()
	writeTreeFile(t, root, "agents/enc/config.yaml", string(enc))
	files, err := readTreeFiles(root)
	require.NoError(t, err)
	assert.Contains(t, string(files["agents/enc/config.yaml"]), "id: enc")
	assert.NotContains(t, string(files["agents/enc/config.yaml"]), "ENC[")

	t.Setenv(sopsage.SopsAgeKeyEnv, "")
	_, err = readTreeFiles(root)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "invalid character")
}
