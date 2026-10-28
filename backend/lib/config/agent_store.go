package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	clientv3 "go.etcd.io/etcd/client/v3"

	"github.com/elmhuangyu/configs/pkg/configlib"
	"github.com/getsops/sops/v3/decrypt"

	"github.com/AgentDrasil/asgard/agentwrapper"
	"github.com/AgentDrasil/asgard/pkg/agentspec"
	"github.com/AgentDrasil/asgard/pkg/paths"
)

// Agent definitions are not bound through AsgardTreeConfig: configlib maps a
// fixed set of key-tagged fields and only supports dynamic map keys whose
// elements are structs of such fields, so it cannot capture the arbitrary
// per-agent files (AGENTS.md, workflow.yaml, skills, ...). AgentStore instead
// consumes raw configlib.Source snapshots for the agents/ and runners/
// prefixes, so credentials are never fetched on behalf of agent loading.

// PrefixedSource is a configlib.Source whose relative keys are re-rooted under
// Prefix (for example "agents/") to rebuild the etcd-tree layout.
type PrefixedSource struct {
	Prefix string
	Source configlib.Source
}

// AgentStore keeps the agent definitions parsed from etcd up to date. It
// satisfies agentspec.Lister with the global agent list and can produce the
// effective list for a given runner.
type AgentStore struct {
	sources []PrefixedSource
	// clis is the supported CLI/model map, computed once (it execs vendor
	// CLIs) and never while holding mu.
	clis map[string][]string

	mu       sync.Mutex
	files    []configlib.FlattenKVs
	set      *agentspec.Set
	accepted map[string]bool // dirs of the list the consumer is actually serving
	handed   map[string]bool // dirs returned by LoadAll since the last Accept
	global   []*agentspec.Agent
	built    map[string]builtAgent
	loadErr  error
	onChange func()
	cancel   context.CancelFunc
	wg       sync.WaitGroup
}

type builtAgent struct {
	hash  string
	agent *agentspec.Agent
}

// NewAgentStore creates a store over arbitrary sources (used by tests).
func NewAgentStore(sources ...PrefixedSource) *AgentStore {
	return &AgentStore{
		sources: sources,
		files:   make([]configlib.FlattenKVs, len(sources)),
		built:   make(map[string]builtAgent),
	}
}

// NewEtcdAgentStore creates a store watching <prefix>/agents/ and
// <prefix>/runners/ in etcd. It does not take ownership of client.
func NewEtcdAgentStore(client *clientv3.Client, prefix string) *AgentStore {
	prefix = strings.TrimRight(prefix, "/")
	return NewAgentStore(
		PrefixedSource{Prefix: "agents/", Source: configlib.NewEtcdSource(client, prefix+"/agents")},
		PrefixedSource{Prefix: "runners/", Source: configlib.NewEtcdSource(client, prefix+"/runners")},
	)
}

// Accept records the agent list the consumer is serving and releases the
// protection LoadAll placed on earlier lists. Its directories are
// never pruned, even when later rebuilds the consumer rejects move on.
func (s *AgentStore) Accept(agents []*agentspec.Agent) {
	dirs := make(map[string]bool, len(agents))
	for _, a := range agents {
		dirs[a.Path] = true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.accepted = dirs
	s.handed = nil
}

// SetSupportedCLIs overrides the supported CLI/model map instead of probing the
// installed CLIs at Start. Call before Start.
func (s *AgentStore) SetSupportedCLIs(clis map[string][]string) { s.clis = clis }

// OnChange registers fn, called after every applied update (including ones
// that left the store with a load error). It may be set at any time.
func (s *AgentStore) OnChange(fn func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onChange = fn
}

// Start fetches every source, builds the initial agent set and begins
// watching. A fetch failure is an error; invalid agent definitions are not:
// they surface through LoadAll so the server can start degraded.
func (s *AgentStore) Start(ctx context.Context) error {
	if s.clis == nil {
		s.clis = agentwrapper.GetSupportedCLIsAndModels()
	}
	watchCtx, cancel := context.WithCancel(ctx)

	chans := make([]<-chan *configlib.Snapshot, len(s.sources))
	s.mu.Lock()
	for i, src := range s.sources {
		snap, err := src.Source.Fetch(watchCtx)
		if err != nil {
			s.mu.Unlock()
			cancel()
			return fmt.Errorf("initial fetch of %q agent definitions failed: %w", src.Prefix, err)
		}
		s.files[i] = snap.KVs
	}
	cleanStaging(paths.AgentsCacheDir())
	s.rebuildLocked()
	s.mu.Unlock()

	for i, src := range s.sources {
		ch, err := src.Source.Watch(watchCtx)
		if err != nil {
			cancel()
			return fmt.Errorf("watching %q agent definitions failed: %w", src.Prefix, err)
		}
		chans[i] = ch
	}

	s.cancel = cancel
	for i, ch := range chans {
		s.wg.Add(1)
		go s.watchLoop(i, ch)
	}
	return nil
}

// Close stops the watchers. The underlying sources stay owned by the caller.
func (s *AgentStore) Close() {
	if s.cancel != nil {
		s.cancel()
	}
	s.wg.Wait()
}

func (s *AgentStore) watchLoop(i int, ch <-chan *configlib.Snapshot) {
	defer s.wg.Done()
	for snap := range ch {
		s.mu.Lock()
		s.files[i] = snap.KVs
		s.rebuildLocked()
		fn := s.onChange
		s.mu.Unlock()
		if fn != nil {
			fn()
		}
	}
}

// rebuildLocked re-parses the merged tree. On failure the previously good
// agent list is kept and the error is returned from LoadAll.
func (s *AgentStore) rebuildLocked() {
	oldDirs := agentDirs(s.built)
	merged := make(map[string][]byte)
	for i, src := range s.sources {
		for k, v := range s.files[i] {
			merged[src.Prefix+k] = v
		}
	}

	set, err := s.parseLocked(merged)
	if err != nil {
		s.loadErr = err
		return
	}

	global := make([]*agentspec.Agent, 0, len(set.Global))
	keep := make(map[string]builtAgent, len(set.Global))
	for _, id := range sortedDefIDs(set.Global) {
		agent, err := s.materializeLocked(set.Global[id], keep)
		if err != nil {
			s.loadErr = err
			return
		}
		global = append(global, agent)
	}

	// Runner agents already materialized stay put while their content is
	// unchanged, so AgentsFor paths are stable across unrelated rebuilds.
	for node, defs := range set.Runners {
		for id, def := range defs {
			key := node + "/" + id
			if prev, ok := s.built[key]; ok && prev.hash == def.Hash() {
				keep[key] = prev
			}
		}
	}

	s.set, s.global, s.loadErr = set, global, nil
	s.built = keep
	// Keep the current and previous generation (so sandboxes started against
	// the previous one can still read their files) plus whatever the consumer
	// accepted, which may be older if it rejected newer lists.
	newDirs := agentDirs(keep)
	for d := range oldDirs {
		newDirs[d] = true
	}
	for d := range s.accepted {
		newDirs[d] = true
	}
	for d := range s.handed {
		newDirs[d] = true
	}
	pruneCache(paths.AgentsCacheDir(), newDirs)
}

func (s *AgentStore) parseLocked(merged map[string][]byte) (*agentspec.Set, error) {
	for k, v := range merged {
		dec, err := decryptFile(k, v)
		if err != nil {
			return nil, fmt.Errorf("decrypt %s: %w", k, err)
		}
		merged[k] = dec
	}
	return agentspec.ParseFilesWithCLIs(merged, s.clis)
}

// materializeLocked writes def to the cache, reusing the previous directory
// when its content is unchanged so unrelated edits do not move agent paths.
func (s *AgentStore) materializeLocked(def *agentspec.Definition, keep map[string]builtAgent) (*agentspec.Agent, error) {
	key := def.NodeID + "/" + def.Config.ID
	hash := def.Hash()
	if prev, ok := s.built[key]; ok && prev.hash == hash {
		keep[key] = prev
		return prev.agent, nil
	}

	if def.NodeID != "" {
		if err := agentspec.ValidateNodeID(def.NodeID); err != nil {
			return nil, err
		}
	}
	// Content-addressed: the etcd revision of independently watched sources
	// can repeat with different content, the hash cannot.
	version := hash[:16]
	dir := paths.AgentCacheDir(def.Config.ID, version)
	if def.NodeID != "" {
		dir = paths.RunnerAgentCacheDir(def.NodeID, def.Config.ID, version)
	}
	agent, err := def.Materialize(dir)
	if err != nil {
		return nil, err
	}
	keep[key] = builtAgent{hash: hash, agent: agent}
	return agent, nil
}

// LoadAll returns the global agent list. If the latest etcd state is invalid
// it returns that error; callers keep their previous list in that case.
func (s *AgentStore) LoadAll() ([]*agentspec.Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return nil, s.loadErr
	}
	// Protect the handed-out list until the consumer accepts a list, so a
	// rebuild between LoadAll and Accept cannot prune it.
	if s.handed == nil {
		s.handed = make(map[string]bool)
	}
	for _, a := range s.global {
		s.handed[a.Path] = true
	}
	return append([]*agentspec.Agent(nil), s.global...), nil
}

// AgentsFor returns the effective agents of a runner: the global agents plus
// runners/<nodeID>/agents. An id defined in both scopes is an error.
func (s *AgentStore) AgentsFor(nodeID string) ([]*agentspec.Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return nil, s.loadErr
	}
	if s.set == nil {
		return nil, errors.New("agent definitions not loaded")
	}

	defs, err := s.set.Effective(nodeID)
	if err != nil {
		return nil, fmt.Errorf("runner %s: %w", nodeID, err)
	}
	out := make([]*agentspec.Agent, 0, len(defs))
	for _, def := range defs {
		if def.NodeID == "" {
			out = append(out, s.built["/"+def.Config.ID].agent)
			continue
		}
		agent, err := s.materializeLocked(def, s.built)
		if err != nil {
			return nil, err
		}
		out = append(out, agent)
	}
	return out, nil
}

// Collisions lists ids defined both globally and under a runner. They do not
// block the global list; each only invalidates the colliding runner's list.
func (s *AgentStore) Collisions() []error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.set == nil {
		return nil
	}
	return s.set.Collisions()
}

// RunnerErrors lists runner agents that failed to parse. They do not block the
// global list; each only invalidates its own runner's list.
func (s *AgentStore) RunnerErrors() []error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.set == nil {
		return nil
	}
	return s.set.RunnerErrors()
}

func sortedDefIDs(m map[string]*agentspec.Definition) []string {
	ids := make([]string, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func agentDirs(built map[string]builtAgent) map[string]bool {
	dirs := make(map[string]bool, len(built))
	for _, b := range built {
		dirs[b.agent.Path] = true
	}
	return dirs
}

// cleanStaging removes staging dirs orphaned by a crash mid-materialization.
func cleanStaging(root string) {
	for _, dir := range cacheScopes(root) {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if e.IsDir() && strings.HasPrefix(e.Name(), ".") && strings.Contains(e.Name(), ".tmp-") {
				_ = os.RemoveAll(filepath.Join(dir, e.Name()))
			}
		}
	}
}

// pruneCache removes materialized <id>@<hash> dirs not in keep.
func pruneCache(root string, keep map[string]bool) {
	for _, dir := range cacheScopes(root) {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			full := filepath.Join(dir, e.Name())
			if e.IsDir() && !strings.HasPrefix(e.Name(), ".") && strings.Contains(e.Name(), "@") && !keep[full] {
				_ = os.RemoveAll(full)
			}
		}
	}
}

// cacheScopes lists the global cache dir and each runner's cache dir.
func cacheScopes(root string) []string {
	scopes := []string{root}
	nodes, _ := os.ReadDir(filepath.Join(root, "runners"))
	for _, n := range nodes {
		if n.IsDir() {
			scopes = append(scopes, filepath.Join(root, "runners", n.Name()))
		}
	}
	return scopes
}

// decryptFile decrypts a SOPS envelope and passes plaintext through. For YAML
// files a failure reports the YAML store's error: configlib tries yaml then
// json and returns only the last (json) error, which masks real causes such as
// a missing KMS key.
func decryptFile(name string, raw []byte) ([]byte, error) {
	out, err := configlib.DecryptIfNeeded(raw)
	if err != nil && (strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml")) {
		if _, yerr := decrypt.Data(raw, "yaml"); yerr != nil {
			return nil, fmt.Errorf("sops decryption failed: %w", yerr)
		}
	}
	return out, err
}
