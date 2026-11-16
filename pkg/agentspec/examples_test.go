package agentspec

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// loadExampleTree reads an examples/<name> tree into an etcd-shaped file map.
func loadExampleTree(t *testing.T, root string) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	require.NoError(t, filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = data
		return nil
	}))
	return files
}

func TestSubagentTeamExample(t *testing.T) {
	root := filepath.Join("..", "..", "examples", "subagent-team")
	set, err := ParseFilesWithCLIs(loadExampleTree(t, root), NoCLIValidation())
	require.NoError(t, err)

	want := []string{"architect", "impler", "lead", "researcher", "review-lead", "reviewer"}
	var got []string
	var mains []string
	for id, def := range set.Global {
		got = append(got, id)
		assert.Equal(t, "dev", def.Config.Team, id)
		assert.Contains(t, def.Files, "AGENTS.md", id)
		if def.Config.MainAgent != nil && *def.Config.MainAgent {
			mains = append(mains, id)
		}
	}
	assert.ElementsMatch(t, want, got)
	assert.Equal(t, []string{"architect"}, mains, "only architect is a main agent")

	arch := set.Global["architect"].Config
	assert.NotEmpty(t, arch.RunDirs)
	assert.Equal(t, "resume", arch.SessionMode)
	assert.Equal(t, LockModeExclusive, arch.EffectiveLockMode())

	for _, id := range []string{"review-lead", "reviewer", "researcher"} {
		assert.Equal(t, LockModeReadOnly, set.Global[id].Config.EffectiveLockMode(), id)
	}
	for id, def := range set.Global {
		assert.Equal(t, "resume", def.Config.SessionMode, id)
	}

	agentRe := regexp.MustCompile(`--agent (\S+)`)
	labelRe := regexp.MustCompile(`--label "([^"]*)"`)
	for id, def := range set.Global {
		doc := string(def.Files["AGENTS.md"])
		for _, m := range agentRe.FindAllStringSubmatch(doc, -1) {
			target, ok := set.Global[m[1]]
			if assert.True(t, ok, "%s spawns unknown agent %q", id, m[1]) {
				assert.Equal(t, "dev", target.Config.Team, "%s -> %s", id, m[1])
			}
		}
		for _, m := range labelRe.FindAllStringSubmatch(doc, -1) {
			assert.False(t, strings.Contains(m[1], "]"), "%s label %q contains ]", id, m[1])
		}
	}
	for _, id := range []string{"lead", "impler", "review-lead", "reviewer", "researcher"} {
		assert.Empty(t, set.Global[id].Config.RunDirs, id)
	}
}
