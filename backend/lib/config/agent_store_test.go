package config

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/elmhuangyu/configs/pkg/configlib"
	"github.com/getsops/sops/v3"
	"github.com/getsops/sops/v3/aes"
	sopsage "github.com/getsops/sops/v3/age"
	"github.com/getsops/sops/v3/cmd/sops/common"
	sopsjson "github.com/getsops/sops/v3/stores/json"
	sopsyaml "github.com/getsops/sops/v3/stores/yaml"
	sopsversion "github.com/getsops/sops/v3/version"

	"github.com/AgentDrasil/asgard/agentwrapper"
	"github.com/AgentDrasil/asgard/agentwrapper/types"
	"github.com/AgentDrasil/asgard/pkg/agentspec"
	"github.com/AgentDrasil/asgard/pkg/paths"
)

type stubCLIClient struct{ models []string }

func (c *stubCLIClient) Usage(context.Context, types.UsageOptions) ([]types.ModelUsage, error) {
	return nil, nil
}

func (c *stubCLIClient) Models(context.Context, types.UsageOptions) ([]string, error) {
	return c.models, nil
}

func (c *stubCLIClient) Prompt(context.Context, string, types.PromptOptions) (*types.PromptResult, error) {
	return &types.PromptResult{}, nil
}

// newTestAgentStore starts an AgentStore over an in-memory source. It sets
// process-global state (HOME, CLI clients), so callers must not be parallel.
func newTestAgentStore(t *testing.T, kvs configlib.FlattenKVs) (*AgentStore, *configlib.MemSource) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	agentwrapper.SetClients(map[string]types.CLIClient{"opencode": &stubCLIClient{models: []string{"deepseek-chat"}}})
	t.Cleanup(func() { agentwrapper.SetClients(nil) })

	src := configlib.NewMemSource(kvs)
	store := NewAgentStore(PrefixedSource{Source: src})
	require.NoError(t, store.Start(context.Background()))
	t.Cleanup(store.Close)
	return store, src
}

func testAgentYAML(id string) []byte {
	return []byte("id: " + id + "\nname: " + id + "\ndescription: d\nmain_agent: false\ncli:\n  - cli: opencode\n    model: deepseek-chat\n")
}

func agentIDs(agents []*agentspec.Agent) []string {
	ids := make([]string, 0, len(agents))
	for _, a := range agents {
		ids = append(ids, a.Config.ID)
	}
	return ids
}

func TestAgentStore_LoadAllMaterializes(t *testing.T) {
	store, _ := newTestAgentStore(t, configlib.FlattenKVs{
		"agents/a/config.yaml": testAgentYAML("a"),
		"agents/a/AGENTS.md":   []byte("rules"),
		"agents/b/config.yaml": testAgentYAML("b"),
	})

	agents, err := store.LoadAll()
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b"}, agentIDs(agents))

	got, err := os.ReadFile(filepath.Join(agents[0].Path, "AGENTS.md"))
	require.NoError(t, err)
	assert.Equal(t, "rules", string(got))
}

func TestAgentStore_WatchReload(t *testing.T) {
	store, src := newTestAgentStore(t, configlib.FlattenKVs{
		"agents/a/config.yaml": testAgentYAML("a"),
		"agents/b/config.yaml": testAgentYAML("b"),
	})
	before, err := store.LoadAll()
	require.NoError(t, err)

	changed := make(chan struct{}, 4)
	store.OnChange(func() { changed <- struct{}{} })
	waitChange := func() {
		t.Helper()
		select {
		case <-changed:
		case <-time.After(5 * time.Second):
			require.FailNow(t, "timed out waiting for agent store change")
		}
	}

	// Edit b, add c, keep a untouched.
	src.Set(configlib.FlattenKVs{
		"agents/a/config.yaml": testAgentYAML("a"),
		"agents/b/config.yaml": testAgentYAML("b"),
		"agents/b/AGENTS.md":   []byte("new"),
		"agents/c/config.yaml": testAgentYAML("c"),
	})
	waitChange()

	after, err := store.LoadAll()
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b", "c"}, agentIDs(after))
	assert.Equal(t, before[0].Path, after[0].Path, "unchanged agent keeps its materialized path")
	assert.NotEqual(t, before[1].Path, after[1].Path, "changed agent gets a new revision dir")

	// An invalid update surfaces an error, then recovers.
	src.Set(configlib.FlattenKVs{"agents/a/config.yaml": testAgentYAML("zzz")})
	waitChange()
	_, err = store.LoadAll()
	require.Error(t, err)
	assert.ErrorContains(t, err, "does not match directory name")

	src.Set(configlib.FlattenKVs{"agents/a/config.yaml": testAgentYAML("a")})
	waitChange()
	agents, err := store.LoadAll()
	require.NoError(t, err)
	assert.Equal(t, []string{"a"}, agentIDs(agents))
}

func TestAgentStore_AgentsForRunner(t *testing.T) {
	store, src := newTestAgentStore(t, configlib.FlattenKVs{
		"agents/g/config.yaml":            testAgentYAML("g"),
		"runners/n1/agents/r/config.yaml": testAgentYAML("r"),
		"runners/n1/agents/r/AGENTS.md":   []byte("runner rules"),
	})

	agents, err := store.AgentsFor("n1")
	require.NoError(t, err)
	assert.Equal(t, []string{"g", "r"}, agentIDs(agents))
	got, err := os.ReadFile(filepath.Join(agents[1].Path, "AGENTS.md"))
	require.NoError(t, err)
	assert.Equal(t, "runner rules", string(got))

	global, err := store.LoadAll()
	require.NoError(t, err)
	assert.Equal(t, []string{"g"}, agentIDs(global), "global list excludes runner agents")

	agents, err = store.AgentsFor("other")
	require.NoError(t, err)
	assert.Equal(t, []string{"g"}, agentIDs(agents))

	// A collision breaks only the colliding runner and is reported.
	changed := make(chan struct{}, 1)
	store.OnChange(func() { changed <- struct{}{} })
	src.Set(configlib.FlattenKVs{
		"agents/g/config.yaml":            testAgentYAML("g"),
		"runners/n1/agents/g/config.yaml": testAgentYAML("g"),
	})
	select {
	case <-changed:
	case <-time.After(5 * time.Second):
		require.FailNow(t, "timed out waiting for agent store change")
	}

	_, err = store.AgentsFor("n1")
	require.Error(t, err)
	assert.ErrorContains(t, err, `agent id "g" is defined both`)
	agents, err = store.AgentsFor("n2")
	require.NoError(t, err)
	assert.Equal(t, []string{"g"}, agentIDs(agents))
	cols := store.Collisions()
	require.Len(t, cols, 1)
	assert.ErrorContains(t, cols[0], "runners/n1/agents/g")
}

func TestAgentStore_BadRunnerAgentOnlyBreaksThatRunner(t *testing.T) {
	store, _ := newTestAgentStore(t, configlib.FlattenKVs{
		"agents/g/config.yaml":                 testAgentYAML("g"),
		"runners/n1/agents/Bad_Id/config.yaml": testAgentYAML("Bad_Id"),
		"runners/n2/agents/r/config.yaml":      testAgentYAML("r"),
	})

	global, err := store.LoadAll()
	require.NoError(t, err)
	assert.Equal(t, []string{"g"}, agentIDs(global))

	_, err = store.AgentsFor("n1")
	require.Error(t, err)
	assert.ErrorContains(t, err, "runner n1:")
	agents, err := store.AgentsFor("n2")
	require.NoError(t, err)
	assert.Equal(t, []string{"g", "r"}, agentIDs(agents))
	assert.Len(t, store.RunnerErrors(), 1)
}

const (
	testAgeRecipient = "age1lzd99uklcjnc0e7d860axevet2cz99ce9pq6tzuzd05l5nr28ams36nvun"
	testAgeIdentity  = "AGE-SECRET-KEY-1G0Q5K9TV4REQ3ZSQRMTMG8NSWQGYT0T7TZ33RAZEE0GZYVZN0APSU24RK7"
)

// encryptSOPS returns plaintext as a SOPS envelope for the given store.
func encryptSOPS(t *testing.T, store sops.Store, plaintext []byte) []byte {
	t.Helper()
	key, err := sopsage.MasterKeyFromRecipient(testAgeRecipient)
	require.NoError(t, err)
	branches, err := store.LoadPlainFile(plaintext)
	require.NoError(t, err)
	tree := sops.Tree{
		Branches: branches,
		Metadata: sops.Metadata{KeyGroups: []sops.KeyGroup{{key}}, Version: sopsversion.Version},
	}
	dataKey, errs := tree.GenerateDataKey()
	require.Empty(t, errs)
	require.NoError(t, common.EncryptTree(common.EncryptTreeOpts{DataKey: dataKey, Tree: &tree, Cipher: aes.NewCipher()}))
	out, err := store.EmitEncryptedFile(tree)
	require.NoError(t, err)
	return out
}

func TestAgentStore_DecryptsSOPSFiles(t *testing.T) {
	t.Setenv(sopsage.SopsAgeKeyEnv, testAgeIdentity)
	enc := encryptSOPS(t, &sopsyaml.Store{}, testAgentYAML("secret"))
	encText := encryptSOPS(t, &sopsjson.Store{}, []byte(`{"data":"top secret notes"}`))

	store, _ := newTestAgentStore(t, configlib.FlattenKVs{
		"agents/secret/config.yaml": enc,
		"agents/secret/notes.txt":   encText,
	})

	agents, err := store.LoadAll()
	require.NoError(t, err)
	require.Len(t, agents, 1)
	assert.Equal(t, "secret", agents[0].Config.ID)
	got, err := os.ReadFile(filepath.Join(agents[0].Path, "notes.txt"))
	require.NoError(t, err)
	assert.NotContains(t, string(got), "ENC[")
}

func TestAgentStore_AgentsForStableAcrossRebuilds(t *testing.T) {
	kvs := configlib.FlattenKVs{
		"agents/g/config.yaml":            testAgentYAML("g"),
		"runners/n1/agents/r/config.yaml": testAgentYAML("r"),
	}
	store, src := newTestAgentStore(t, kvs)
	first, err := store.AgentsFor("n1")
	require.NoError(t, err)

	changed := make(chan struct{}, 1)
	store.OnChange(func() { changed <- struct{}{} })
	next := configlib.FlattenKVs{"agents/other/config.yaml": testAgentYAML("other")}
	for k, v := range kvs {
		next[k] = v
	}
	src.Set(next)
	<-changed

	second, err := store.AgentsFor("n1")
	require.NoError(t, err)
	assert.Equal(t, []string{"g", "other", "r"}, agentIDs(second))
	assert.Equal(t, first[0].Path, second[0].Path)
	assert.Equal(t, first[1].Path, second[2].Path)
}

func TestAgentStore_ConcurrentSources(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	agentwrapper.SetClients(map[string]types.CLIClient{"opencode": &stubCLIClient{models: []string{"deepseek-chat"}}})
	t.Cleanup(func() { agentwrapper.SetClients(nil) })

	globalSrc := configlib.NewMemSource(configlib.FlattenKVs{"g/config.yaml": testAgentYAML("g")})
	runnerSrc := configlib.NewMemSource(configlib.FlattenKVs{})
	store := NewAgentStore(
		PrefixedSource{Prefix: "agents/", Source: globalSrc},
		PrefixedSource{Prefix: "runners/", Source: runnerSrc},
	)
	var calls atomic.Int32
	store.OnChange(func() { calls.Add(1) })
	require.NoError(t, store.Start(context.Background()))
	t.Cleanup(store.Close)

	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			globalSrc.Set(configlib.FlattenKVs{"g/config.yaml": testAgentYAML("g"), fmt.Sprintf("x%d/config.yaml", i): testAgentYAML(fmt.Sprintf("x%d", i))})
		}()
		go func() {
			defer wg.Done()
			runnerSrc.Set(configlib.FlattenKVs{"n1/agents/r/config.yaml": testAgentYAML("r")})
			_, _ = store.AgentsFor("n1")
		}()
	}
	wg.Wait()

	require.Eventually(t, func() bool { return calls.Load() >= 2 }, 5*time.Second, 10*time.Millisecond)
	agents, err := store.LoadAll()
	require.NoError(t, err)
	assert.NotEmpty(t, agents)
	runnerAgents, err := store.AgentsFor("n1")
	require.NoError(t, err)
	assert.Contains(t, agentIDs(runnerAgents), "r")
}

func TestAgentStore_PrunesCacheKeepingPreviousGeneration(t *testing.T) {
	store, src := newTestAgentStore(t, configlib.FlattenKVs{"agents/a/config.yaml": testAgentYAML("a")})
	// An orphaned staging dir from a crashed run is not touched after start,
	// so plant stale state and restart a store to verify startup cleanup.
	cacheRoot := paths.AgentsCacheDir()
	gen1, err := store.LoadAll()
	require.NoError(t, err)

	changed := make(chan struct{}, 4)
	store.OnChange(func() { changed <- struct{}{} })
	update := func(note string) {
		src.Set(configlib.FlattenKVs{"agents/a/config.yaml": testAgentYAML("a"), "agents/a/AGENTS.md": []byte(note)})
		<-changed
	}
	update("v2")
	gen2, _ := store.LoadAll()
	store.Accept(gen2)
	update("v3")
	gen3, _ := store.LoadAll()

	assert.NoDirExists(t, gen1[0].Path, "two generations back is pruned")
	assert.DirExists(t, gen2[0].Path, "previous generation is kept")
	assert.DirExists(t, gen3[0].Path)

	require.NoError(t, os.MkdirAll(filepath.Join(cacheRoot, ".a@9.tmp-123"), 0o755))
	restarted := NewAgentStore(PrefixedSource{Source: src})
	require.NoError(t, restarted.Start(context.Background()))
	t.Cleanup(restarted.Close)
	assert.NoDirExists(t, filepath.Join(cacheRoot, ".a@9.tmp-123"))
}

func TestAgentStore_RejectsBadRunnerNodeID(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	src := configlib.NewMemSource(configlib.FlattenKVs{
		"runners/../agents/x/config.yaml": testAgentYAML("x"),
	})
	bad := NewAgentStore(PrefixedSource{Source: src})
	bad.SetSupportedCLIs(map[string][]string{})
	require.NoError(t, bad.Start(context.Background()))
	t.Cleanup(bad.Close)

	_, err := bad.LoadAll()
	require.Error(t, err)
	assert.ErrorContains(t, err, "invalid runner node id")
}

func TestAgentStore_AcceptedGenerationSurvivesPrune(t *testing.T) {
	store, src := newTestAgentStore(t, configlib.FlattenKVs{"agents/a/config.yaml": testAgentYAML("a")})
	served, err := store.LoadAll()
	require.NoError(t, err)
	store.Accept(served)

	changed := make(chan struct{}, 4)
	store.OnChange(func() { changed <- struct{}{} })
	// The consumer rejects every later list, so it keeps serving gen 1 while
	// the store moves on through several generations.
	for _, note := range []string{"v2", "v3", "v4"} {
		src.Set(configlib.FlattenKVs{"agents/a/config.yaml": testAgentYAML("a"), "agents/a/AGENTS.md": []byte(note)})
		<-changed
	}

	assert.DirExists(t, served[0].Path, "dir of the accepted generation is never pruned")
	assert.FileExists(t, filepath.Join(served[0].Path, "config.yaml"))
}

func TestAgentStore_DecryptsInlineSOPSYAML(t *testing.T) {
	t.Setenv(sopsage.SopsAgeKeyEnv, testAgeIdentity)
	// Inline-encrypted YAML config, as in the reference tree.
	plain := append(testAgentYAML("inline"), []byte("api_key: s3cret\n")...)
	enc := encryptSOPS(t, &sopsyaml.Store{}, plain)
	require.Contains(t, string(enc), "sops:")

	store, _ := newTestAgentStore(t, configlib.FlattenKVs{"agents/inline/config.yaml": enc})
	agents, err := store.LoadAll()
	require.NoError(t, err)
	require.Len(t, agents, 1)
	assert.Equal(t, "inline", agents[0].Config.ID)

	t.Run("failure reports the yaml error", func(t *testing.T) {
		t.Setenv(sopsage.SopsAgeKeyEnv, "")
		_, err := decryptFile("agents/inline/config.yaml", enc)
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "invalid character")
	})
}

func TestAgentStore_LoadAllSurvivesRebuildsBeforeAccept(t *testing.T) {
	store, src := newTestAgentStore(t, configlib.FlattenKVs{"agents/a/config.yaml": testAgentYAML("a")})

	changed := make(chan struct{}, 4)
	store.OnChange(func() { changed <- struct{}{} })
	handed, err := store.LoadAll()
	require.NoError(t, err)

	for _, note := range []string{"v2", "v3", "v4"} {
		src.Set(configlib.FlattenKVs{"agents/a/config.yaml": testAgentYAML("a"), "agents/a/AGENTS.md": []byte(note)})
		<-changed
	}
	assert.DirExists(t, handed[0].Path, "handed-out dir survives rebuilds before Accept")

	store.Accept(handed)
	assert.DirExists(t, handed[0].Path)

	// Once superseded by a newer accepted list, it becomes prunable.
	latest, err := store.LoadAll()
	require.NoError(t, err)
	store.Accept(latest)
	src.Set(configlib.FlattenKVs{"agents/a/config.yaml": testAgentYAML("a"), "agents/a/AGENTS.md": []byte("v5")})
	<-changed
	assert.NoDirExists(t, handed[0].Path)
}

// fixedRevSource is a Source whose snapshots always carry the same revision.
type fixedRevSource struct {
	kvs configlib.FlattenKVs
	ch  chan *configlib.Snapshot
}

func (s *fixedRevSource) Fetch(context.Context) (*configlib.Snapshot, error) {
	return &configlib.Snapshot{KVs: s.kvs, Revision: 5}, nil
}

func (s *fixedRevSource) Watch(context.Context) (<-chan *configlib.Snapshot, error) {
	return s.ch, nil
}

func (s *fixedRevSource) Close() error { return nil }

func TestAgentStore_SameRevisionDifferentContent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	src := &fixedRevSource{
		kvs: configlib.FlattenKVs{"a/config.yaml": testAgentYAML("a"), "a/AGENTS.md": []byte("old")},
		ch:  make(chan *configlib.Snapshot, 1),
	}
	store := NewAgentStore(PrefixedSource{Prefix: "agents/", Source: src})
	store.SetSupportedCLIs(map[string][]string{"opencode": {"deepseek-chat"}})
	changed := make(chan struct{}, 1)
	store.OnChange(func() { changed <- struct{}{} })
	require.NoError(t, store.Start(context.Background()))
	t.Cleanup(store.Close)
	t.Cleanup(func() { close(src.ch) }) // real sources close their channel on cancel

	before, err := store.LoadAll()
	require.NoError(t, err)

	src.ch <- &configlib.Snapshot{
		KVs:      configlib.FlattenKVs{"a/config.yaml": testAgentYAML("a"), "a/AGENTS.md": []byte("new")},
		Revision: 5,
	}
	<-changed

	after, err := store.LoadAll()
	require.NoError(t, err)
	assert.NotEqual(t, before[0].Path, after[0].Path)
	got, err := os.ReadFile(filepath.Join(after[0].Path, "AGENTS.md"))
	require.NoError(t, err)
	assert.Equal(t, "new", string(got))
}
