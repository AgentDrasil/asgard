package agentspec

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

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

	want := []string{"fixer", "impler", "implreviewer", "orchestrator", "planner", "planreviewer"}
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
	assert.Equal(t, []string{"orchestrator"}, mains, "only orchestrator is a main agent")

	arch := set.Global["orchestrator"].Config
	assert.NotEmpty(t, arch.RunDirs)
	assert.Equal(t, "resume", arch.SessionMode)
	assert.Equal(t, LockModeExclusive, arch.EffectiveLockMode())

	for _, id := range []string{"planner", "planreviewer", "implreviewer"} {
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
	for _, id := range []string{"planner", "planreviewer", "impler", "implreviewer", "fixer"} {
		assert.Empty(t, set.Global[id].Config.RunDirs, id)
	}

	// Every `ask --topic <t>` must be registered by `route add --topic <t>` in
	// the prompt of an ancestor on every spawn path, or be `user`. Spawn prompt
	// bodies (`subagent spawn --agent X ... -- "<prompt>"`) are asks of X, not
	// of the spawner, and are stripped from the spawner's own text.
	// One command: the spawn line plus backslash-continued lines, then `-- "<prompt>"`.
	spawnRe := regexp.MustCompile(`subagent spawn --agent (\S+)[^\n]*?(?:\\\n[^\n]*?)*-- \\?\n?\s*"((?:[^"\\]|\\.)*)"`)
	spawnCountRe := regexp.MustCompile(`subagent spawn --agent`)
	askRe := regexp.MustCompile(`(?:subagent )?ask [^\n]*?--topic ([a-z0-9][a-z0-9_-]*)`)
	routeRe := regexp.MustCompile(`route add --topic ([a-z0-9][a-z0-9_-]*)`)
	timeoutRe := regexp.MustCompile(`--timeout[ =](\d+)(ms|s|m|h)?`)

	parents := map[string][]string{}
	registers := map[string]map[string]bool{}
	asks := map[string][]string{} // agent -> topics asked in its own text or in spawn bodies aimed at it
	for id, def := range set.Global {
		doc := string(def.Files["AGENTS.md"])
		registers[id] = map[string]bool{}
		for _, m := range routeRe.FindAllStringSubmatch(doc, -1) {
			registers[id][m[1]] = true
		}
		own := doc
		spawns := spawnRe.FindAllStringSubmatchIndex(doc, -1)
		assert.Len(t, spawns, len(spawnCountRe.FindAllStringIndex(doc, -1)), "%s: unsupported spawn prompt form", id)
		for i := len(spawns) - 1; i >= 0; i-- {
			m := spawns[i]
			child := doc[m[2]:m[3]]
			body := doc[m[4]:m[5]]
			parents[child] = append(parents[child], id)
			for _, a := range askRe.FindAllStringSubmatch(body, -1) {
				asks[child] = append(asks[child], a[1])
			}
			own = own[:m[4]] + own[m[5]:]
		}
		doc = own
		for _, a := range askRe.FindAllStringSubmatch(doc, -1) {
			asks[id] = append(asks[id], a[1])
		}
		for _, m := range timeoutRe.FindAllStringSubmatch(string(def.Files["AGENTS.md"]), -1) {
			unit := m[2]
			if unit == "" {
				unit = "s" // like cmd/subagent parseTimeout
			}
			d, err := time.ParseDuration(m[1] + unit)
			require.NoError(t, err)
			assert.LessOrEqual(t, d, 90*time.Second, "%s uses --timeout %s%s", id, m[1], m[2])
		}
	}
	var covered func(id, topic string, depth int) bool
	covered = func(id, topic string, depth int) bool {
		if depth > 8 || len(parents[id]) == 0 {
			return false
		}
		for _, p := range parents[id] {
			if !registers[p][topic] && !covered(p, topic, depth+1) {
				return false
			}
		}
		return true
	}
	for id, topics := range asks {
		for _, topic := range topics {
			if topic == "user" {
				continue
			}
			assert.True(t, covered(id, topic, 0), "%s asks topic %q not registered by an ancestor on every path", id, topic)
		}
	}
}
