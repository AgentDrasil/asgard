package workflow

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/goccy/go-yaml"
)

// WorkflowDefinition is the parsed YAML workflow definition.
type WorkflowDefinition struct {
	Name   string      `yaml:"name"`
	TmpDir string      `yaml:"tmp_dir"`
	Nodes  []*NodeSpec `yaml:"nodes"`

	// raw is the YAML source this definition was parsed from; it is persisted
	// as the DAG snapshot for pause/resume and crash recovery.
	raw string
}

// RawSpec returns the YAML source the definition was parsed from (may be empty
// for hand-constructed definitions).
func (d *WorkflowDefinition) RawSpec() string {
	return d.raw
}

// NodeDependency is one incoming dependency edge of a node.
type NodeDependency struct {
	NodeID string `yaml:"node"`
	// When is an optional dot-notation condition guarding only this edge,
	// e.g. `nodes.build_cmd.exit_code != 0`.
	When string `yaml:"when"`
}

// NodeSpec is the YAML specification of a single workflow node.
type NodeSpec struct {
	ID   string   `yaml:"id"`
	Type NodeType `yaml:"type"`

	// Common fields.
	Prompt     string           `yaml:"prompt"`
	Depends    []NodeDependency `yaml:"depends"`
	WorkingDir string           `yaml:"working_dir"`
	Timeout    string           `yaml:"timeout"`
	// Join controls behavior when upstream nodes are skipped or fail.
	// "always" is sugar for on_skip: run + on_fail: run.
	Join string `yaml:"join"`
	// OnSkip / OnFail take "run" or "skip" (default skip).
	OnSkip string `yaml:"on_skip"`
	OnFail string `yaml:"on_fail"`

	// Agent node fields.
	AgentID       string `yaml:"agent_id"`
	SessionPolicy string `yaml:"session_policy"` // inherit | fresh
	Model         string `yaml:"model"`

	// LLM node fields.
	SystemPrompt string `yaml:"system_prompt"`

	// Command node fields.
	Command    string `yaml:"command"`
	OutputFile string `yaml:"output_file"`
	Sandbox    *bool  `yaml:"sandbox"`

	// Human node fields. Options is the optional list of canned replies
	// offered to the user; when empty any free-form text is accepted.
	Options []string `yaml:"options"`
}

// TimeoutDuration parses the node timeout (e.g. "300s"). Zero means no timeout.
func (n *NodeSpec) TimeoutDuration() time.Duration {
	if n.Timeout == "" {
		return 0
	}
	d, err := time.ParseDuration(n.Timeout)
	if err != nil {
		return 0
	}
	return d
}

// AllowsFail reports whether the node should still run when an upstream node
// failed (on_fail: run or join: always).
func (n *NodeSpec) AllowsFail() bool {
	return n.OnFail == "run" || n.Join == "always"
}

// AllowsSkip reports whether the node should still run when an upstream node
// was skipped (on_skip: run or join: always).
func (n *NodeSpec) AllowsSkip() bool {
	return n.OnSkip == "run" || n.Join == "always"
}

// SessionPolicyInherit reports whether the agent node inherits the CLI session
// of a previous node using the same agent_id.
func (n *NodeSpec) SessionPolicyInherit() bool {
	return n.SessionPolicy != "fresh"
}

// LoadDefinition reads and validates a workflow definition from a YAML file.
func LoadDefinition(path string) (*WorkflowDefinition, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read workflow definition: %w", err)
	}
	defn, err := ParseDefinition(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return defn, nil
}

// ParseDefinition parses and validates a workflow definition from YAML bytes.
func ParseDefinition(data []byte) (*WorkflowDefinition, error) {
	var defn WorkflowDefinition
	if err := yaml.Unmarshal(data, &defn); err != nil {
		return nil, fmt.Errorf("failed to parse workflow definition: %w", err)
	}
	if err := defn.Validate(); err != nil {
		return nil, fmt.Errorf("validation error: %w", err)
	}
	defn.raw = string(data)
	return &defn, nil
}

// Validate checks structural correctness of the definition, including
// dependency references and cycle detection (Kahn topological sort).
func (d *WorkflowDefinition) Validate() error {
	if d.Name == "" {
		return fmt.Errorf("workflow name cannot be empty")
	}
	if len(d.Nodes) == 0 {
		return fmt.Errorf("workflow must contain at least one node")
	}

	ids := make(map[string]*NodeSpec, len(d.Nodes))
	for _, node := range d.Nodes {
		if node.ID == "" {
			return fmt.Errorf("node id cannot be empty")
		}
		if _, dup := ids[node.ID]; dup {
			return fmt.Errorf("duplicate node id: %s", node.ID)
		}
		ids[node.ID] = node

		switch node.Type {
		case NodeTypeAgent:
			if node.AgentID == "" {
				return fmt.Errorf("node %s: agent_id is required for agent nodes", node.ID)
			}
			if node.Prompt != "" {
				return fmt.Errorf("node %s: prompt is not allowed for agent nodes (agent instructions belong in AGENTS.md)", node.ID)
			}
			if node.SessionPolicy != "" && node.SessionPolicy != "inherit" && node.SessionPolicy != "fresh" {
				return fmt.Errorf("node %s: invalid session_policy %q (must be inherit or fresh)", node.ID, node.SessionPolicy)
			}
		case NodeTypeLLM:
			if node.Prompt == "" {
				return fmt.Errorf("node %s: prompt is required for llm nodes", node.ID)
			}
		case NodeTypeCommand:
			if node.Command == "" {
				return fmt.Errorf("node %s: command is required for command nodes", node.ID)
			}
		case NodeTypeHuman:
			if node.Prompt == "" {
				return fmt.Errorf("node %s: prompt is required for human nodes", node.ID)
			}
			for _, opt := range node.Options {
				if opt == "" {
					return fmt.Errorf("node %s: human node options cannot be empty", node.ID)
				}
			}
		default:
			return fmt.Errorf("node %s: invalid type %q (must be agent, llm, command or human)", node.ID, node.Type)
		}

		switch node.Join {
		case "", "always":
		default:
			return fmt.Errorf("node %s: invalid join %q (must be always)", node.ID, node.Join)
		}
		switch node.OnSkip {
		case "", "run", "skip":
		default:
			return fmt.Errorf("node %s: invalid on_skip %q", node.ID, node.OnSkip)
		}
		switch node.OnFail {
		case "", "run", "skip":
		default:
			return fmt.Errorf("node %s: invalid on_fail %q", node.ID, node.OnFail)
		}
		if node.Timeout != "" {
			if _, err := time.ParseDuration(node.Timeout); err != nil {
				return fmt.Errorf("node %s: invalid timeout %q: %w", node.ID, node.Timeout, err)
			}
		}
	}

	// Second pass: dependency references (all IDs are known now).
	for _, node := range d.Nodes {
		for _, dep := range node.Depends {
			if dep.NodeID == node.ID && dep.When == "" {
				return fmt.Errorf("circular dependency detected: %s -> %s", node.ID, node.ID)
			}
			if _, ok := ids[dep.NodeID]; !ok {
				return fmt.Errorf("node %s: depends on unknown node %q", node.ID, dep.NodeID)
			}
			if dep.When != "" {
				if _, err := EvaluateSimpleExpr(dep.When, nil, d); err != nil {
					return fmt.Errorf("node %s: invalid when expression %q: %w", node.ID, dep.When, err)
				}
			}
		}
	}

	if err := detectCycle(d); err != nil {
		return err
	}
	if err := validateHumanNodes(d); err != nil {
		return err
	}
	return nil
}

// validateHumanNodes enforces the Phase 3 single-active-human-node constraint:
// human nodes must be totally ordered by the dependency graph. Two human nodes
// placed on branches that may execute concurrently are rejected.
func validateHumanNodes(d *WorkflowDefinition) error {
	var humans []string
	for _, node := range d.Nodes {
		if node.Type == NodeTypeHuman {
			humans = append(humans, node.ID)
		}
	}
	if len(humans) < 2 {
		return nil
	}

	deps := make(map[string][]string, len(d.Nodes))
	for _, node := range d.Nodes {
		for _, dep := range node.Depends {
			deps[node.ID] = append(deps[node.ID], dep.NodeID)
		}
	}

	reaches := func(from, to string) bool {
		seen := map[string]bool{from: true}
		stack := append([]string{}, deps[from]...)
		for len(stack) > 0 {
			id := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if id == to {
				return true
			}
			if seen[id] {
				continue
			}
			seen[id] = true
			stack = append(stack, deps[id]...)
		}
		return false
	}

	for i := 0; i < len(humans); i++ {
		for j := i + 1; j < len(humans); j++ {
			a, b := humans[i], humans[j]
			if !reaches(a, b) && !reaches(b, a) {
				return fmt.Errorf("parallel human nodes are not supported in Phase 3: nodes %s and %s may run concurrently", a, b)
			}
		}
	}
	return nil
}

// detectCycle runs a Kahn topological sort; if nodes remain unprocessed there
// is a cycle, which is then reported with an explicit path via DFS.
func detectCycle(d *WorkflowDefinition) error {
	indegree := make(map[string]int, len(d.Nodes))
	dependents := make(map[string][]string, len(d.Nodes))
	for _, node := range d.Nodes {
		if _, ok := indegree[node.ID]; !ok {
			indegree[node.ID] = 0
		}
		seen := make(map[string]bool, len(node.Depends))
		for _, dep := range node.Depends {
			if dep.When != "" {
				// Conditional loop/branch edges are evaluated dynamically at runtime
				continue
			}
			if seen[dep.NodeID] {
				continue
			}
			seen[dep.NodeID] = true
			indegree[node.ID]++
			dependents[dep.NodeID] = append(dependents[dep.NodeID], node.ID)
		}
	}

	queue := make([]string, 0, len(d.Nodes))
	for id, deg := range indegree {
		if deg == 0 {
			queue = append(queue, id)
		}
	}

	processed := 0
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		processed++
		for _, next := range dependents[id] {
			indegree[next]--
			if indegree[next] == 0 {
				queue = append(queue, next)
			}
		}
	}

	if processed == len(d.Nodes) {
		return nil
	}

	// Find an explicit cycle path for a helpful error message.
	remaining := make(map[string]bool, len(d.Nodes)-processed)
	for _, node := range d.Nodes {
		if indegree[node.ID] > 0 {
			remaining[node.ID] = true
		}
	}
	if cycle := findCyclePath(remaining, d); len(cycle) > 0 {
		return fmt.Errorf("circular dependency detected: %s", strings.Join(cycle, " -> "))
	}
	return fmt.Errorf("circular dependency detected among nodes: %v", mapKeys(remaining))
}

// findCyclePath returns one cycle as a path ending where it started
// (e.g. [A, B, A]) restricted to the given node set.
func findCyclePath(remaining map[string]bool, d *WorkflowDefinition) []string {
	deps := make(map[string][]string, len(d.Nodes))
	for _, node := range d.Nodes {
		if !remaining[node.ID] {
			continue
		}
		for _, dep := range node.Depends {
			if remaining[dep.NodeID] {
				deps[node.ID] = append(deps[node.ID], dep.NodeID)
			}
		}
	}

	const (
		white = 0
		grey  = 1
		black = 2
	)
	color := make(map[string]int, len(remaining))
	var stack []string
	var cycle []string

	var dfs func(id string) bool
	dfs = func(id string) bool {
		color[id] = grey
		stack = append(stack, id)
		for _, dep := range deps[id] {
			switch color[dep] {
			case grey:
				// Found a back edge; reconstruct the cycle path.
				idx := 0
				for i, n := range stack {
					if n == dep {
						idx = i
						break
					}
				}
				cycle = append(append([]string{}, stack[idx:]...), dep)
				return true
			case white:
				if dfs(dep) {
					return true
				}
			}
		}
		stack = stack[:len(stack)-1]
		color[id] = black
		return false
	}

	for id := range remaining {
		if color[id] == white && dfs(id) {
			return cycle
		}
	}
	return nil
}

func mapKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
