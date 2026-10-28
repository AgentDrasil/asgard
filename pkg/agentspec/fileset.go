package agentspec

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/AgentDrasil/asgard/agentwrapper"
)

const (
	// TeamsKey is the file-set key of the global teams definition.
	TeamsKey = "agents/teams.yaml"

	globalPrefix  = "agents/"
	runnersPrefix = "runners/"
)

// ValidateNodeID checks a runner node id with the same strictness as agent
// ids; it is used as a path segment, so separators and ".." are rejected.
func ValidateNodeID(id string) error {
	if !idRegex.MatchString(id) {
		return fmt.Errorf("invalid runner node id %q: must match %s", id, idRegex.String())
	}
	return nil
}

// Definition is one agent parsed from a file set: its validated config plus
// every file of its directory (config.yaml, AGENTS.md, workflow.yaml, skills,
// ...), keyed by path relative to the agent directory.
type Definition struct {
	Config AgentConfig
	// NodeID is the owning runner, or empty for a global agent.
	NodeID string
	Files  map[string][]byte
}

// Hash is a content digest of the agent's files; equal hashes mean an
// identical materialized directory.
func (d *Definition) Hash() string {
	names := make([]string, 0, len(d.Files))
	for name := range d.Files {
		names = append(names, name)
	}
	sort.Strings(names)
	h := sha256.New()
	for _, name := range names {
		_, _ = fmt.Fprintf(h, "%d:%s:%d:", len(name), name, len(d.Files[name]))
		h.Write(d.Files[name])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Materialize writes the agent's files to dir and returns the Agent pointing
// at it. The directory is expected to be content-addressed (see Hash), so an
// existing dir is treated as already complete; new content is staged next to dir and renamed
// into place so readers never observe a partial agent.
func (d *Definition) Materialize(dir string) (*Agent, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolving agent dir %q: %w", dir, err)
	}
	if _, err := os.Stat(absDir); err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("stat agent dir %q: %w", absDir, err)
		}
		if err := d.writeFiles(absDir); err != nil {
			return nil, err
		}
	}

	agent := &Agent{Config: d.Config, Path: absDir}
	if d.Config.Type == "workflow" {
		agent.WorkflowPath = filepath.Join(absDir, "workflow.yaml")
	}
	return agent, nil
}

func (d *Definition) writeFiles(dir string) error {
	parent := filepath.Dir(dir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("creating %q: %w", parent, err)
	}
	stage, err := os.MkdirTemp(parent, "."+filepath.Base(dir)+".tmp-")
	if err != nil {
		return fmt.Errorf("staging agent %s: %w", d.Config.ID, err)
	}
	defer func() { _ = os.RemoveAll(stage) }()

	for name, data := range d.Files {
		if !filepath.IsLocal(name) {
			return fmt.Errorf("agent %s: unsafe file path %q", d.Config.ID, name)
		}
		target := filepath.Join(stage, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("agent %s: %w", d.Config.ID, err)
		}
		mode := os.FileMode(0o644)
		if strings.HasPrefix(string(data), "#!") {
			// etcd keeps no file modes; keep shebang scripts runnable.
			mode = 0o755
		}
		if err := os.WriteFile(target, data, mode); err != nil {
			return fmt.Errorf("agent %s: writing %q: %w", d.Config.ID, name, err)
		}
	}

	if err := os.Rename(stage, dir); err != nil {
		// A concurrent materializer may have won the race.
		if _, statErr := os.Stat(dir); statErr == nil {
			return nil
		}
		return fmt.Errorf("agent %s: publishing %q: %w", d.Config.ID, dir, err)
	}
	return nil
}

// Set is the parsed agent tree: global agents plus per-runner agents. Use
// Effective to get the list one runner sees.
type Set struct {
	Teams   []string
	Global  map[string]*Definition
	Runners map[string]map[string]*Definition
	// RunnerErrs holds parse errors of runner agents per node. They only
	// invalidate that runner's effective list, not the global agents.
	RunnerErrs map[string][]error
}

// ParseFiles parses an etcd-tree-shaped file map. Keys are slash paths:
//
//	agents/teams.yaml
//	agents/<id>/<file>
//	runners/<node_id>/agents/<id>/<file>
//
// Other keys are ignored. Each agent gets the same validation as Loader.LoadAll
// (ID matches directory name, supported CLIs, team listed in teams.yaml,
// workflow.yaml present for workflow agents). The first failure in a global
// agent or teams.yaml aborts; a failure under runners/<node_id>/ is recorded in
// RunnerErrs and only invalidates that runner (see RunnerErrors). An id
// present both globally and under a runner is not a parse error here; see
// Collisions and Effective.
func ParseFiles(files map[string][]byte) (*Set, error) {
	return ParseFilesWithCLIs(files, agentwrapper.GetSupportedCLIsAndModels())
}

// ParseFilesWithCLIs is ParseFiles with a caller-supplied supported CLI/model
// map. Computing that map execs vendor CLIs, so long-lived callers compute it
// once and reuse it.
func ParseFilesWithCLIs(files map[string][]byte, supportedCLIs map[string][]string) (*Set, error) {
	set := &Set{
		Global:  make(map[string]*Definition),
		Runners: make(map[string]map[string]*Definition),

		RunnerErrs: make(map[string][]error),
	}

	if data, ok := files[TeamsKey]; ok {
		teams, err := parseTeams(data)
		if err != nil {
			return nil, err
		}
		set.Teams = teams
	}
	validTeams := teamSet(set.Teams)

	type groupKey struct{ node, id string }
	groups := make(map[groupKey]map[string][]byte)
	for key, data := range files {
		node, id, rel, ok := splitAgentKey(key)
		if !ok {
			continue
		}
		if node != "" {
			if err := ValidateNodeID(node); err != nil {
				return nil, fmt.Errorf("%s: %w", key, err)
			}
		}
		gk := groupKey{node, id}
		if groups[gk] == nil {
			groups[gk] = make(map[string][]byte)
		}
		groups[gk][rel] = data
	}

	keys := make([]groupKey, 0, len(groups))
	for gk := range groups {
		keys = append(keys, gk)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].node != keys[j].node {
			return keys[i].node < keys[j].node
		}
		return keys[i].id < keys[j].id
	})

	for _, gk := range keys {
		agentFiles := groups[gk]
		cfgData, ok := agentFiles["config.yaml"]
		if !ok {
			continue
		}
		cfg, err := parseAgentConfig(gk.id, cfgData, validTeams, supportedCLIs)
		if err == nil && cfg.Type == "workflow" {
			if _, ok := agentFiles["workflow.yaml"]; !ok {
				err = fmt.Errorf("agent %s is of type workflow but workflow.yaml is missing", gk.id)
			}
		}
		if err != nil {
			if gk.node == "" {
				return nil, err
			}
			set.RunnerErrs[gk.node] = append(set.RunnerErrs[gk.node], scopeErr(gk.node, err))
			continue
		}

		def := &Definition{Config: cfg, NodeID: gk.node, Files: agentFiles}
		if gk.node == "" {
			set.Global[gk.id] = def
			continue
		}
		if set.Runners[gk.node] == nil {
			set.Runners[gk.node] = make(map[string]*Definition)
		}
		set.Runners[gk.node][gk.id] = def
	}
	return set, nil
}

func scopeErr(node string, err error) error {
	if node == "" {
		return err
	}
	return fmt.Errorf("runner %s: %w", node, err)
}

// splitAgentKey splits a file-set key into (node, agent id, relative file path).
func splitAgentKey(key string) (node, id, rel string, ok bool) {
	var rest string
	switch {
	case strings.HasPrefix(key, globalPrefix):
		rest = strings.TrimPrefix(key, globalPrefix)
	case strings.HasPrefix(key, runnersPrefix):
		parts := strings.SplitN(strings.TrimPrefix(key, runnersPrefix), "/", 3)
		if len(parts) < 3 || parts[0] == "" || parts[1] != "agents" {
			return "", "", "", false
		}
		node, rest = parts[0], parts[2]
	default:
		return "", "", "", false
	}
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", "", false
	}
	return node, parts[0], parts[1], true
}

// Collisions reports every agent id defined both globally and under a runner,
// in deterministic order. A colliding runner has no valid effective list.
func (s *Set) Collisions() []error {
	var errs []error
	for _, node := range s.runnerIDs() {
		for _, id := range sortedKeys(s.Runners[node]) {
			if _, dup := s.Global[id]; dup {
				errs = append(errs, collisionError(node, id))
			}
		}
	}
	return errs
}

func collisionError(node, id string) error {
	return fmt.Errorf("agent id %q is defined both in agents/%s and in runners/%s/agents/%s; agent names must be unique across global and runner scopes", id, id, node, id)
}

// RunnerErrors reports every runner agent that failed to parse, in
// deterministic order. A runner with such an error has no valid effective list.
func (s *Set) RunnerErrors() []error {
	var errs []error
	for _, node := range sortedKeys(s.RunnerErrs) {
		errs = append(errs, s.RunnerErrs[node]...)
	}
	return errs
}

// Effective returns the agents visible to a runner: every global agent plus
// the runner's own, sorted by id. An id present in both scopes, or a runner
// agent that failed to parse, is an error.
func (s *Set) Effective(nodeID string) ([]*Definition, error) {
	own := s.Runners[nodeID]
	errs := append([]error(nil), s.RunnerErrs[nodeID]...)
	for _, id := range sortedKeys(own) {
		if _, dup := s.Global[id]; dup {
			errs = append(errs, collisionError(nodeID, id))
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}

	out := make([]*Definition, 0, len(s.Global)+len(own))
	for _, id := range sortedKeys(s.Global) {
		out = append(out, s.Global[id])
	}
	for _, id := range sortedKeys(own) {
		out = append(out, own[id])
	}
	return out, nil
}

func (s *Set) runnerIDs() []string { return sortedKeys(s.Runners) }

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Lister is anything that can list the current global agents; satisfied by
// Loader (directory scan) and by etcd-backed stores.
type Lister interface {
	LoadAll() ([]*Agent, error)
}
