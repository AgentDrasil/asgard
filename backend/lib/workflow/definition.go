package workflow

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/goccy/go-yaml"
)

// MountDirsConfig defines read-only and read-write mounts for the workflow.
type MountDirsConfig struct {
	ReadOnly  []string `yaml:"readonly"`
	ReadWrite []string `yaml:"readwrite"`
}

// WorkflowDefinition is the parsed YAML workflow definition.
type WorkflowDefinition struct {
	Name      string          `yaml:"name"`
	TmpDir    string          `yaml:"tmp_dir"`
	RunDirs   []string        `yaml:"run_dirs"`
	MountDirs MountDirsConfig `yaml:"mount_dirs"`
	// MaxNodeExecutions is the global per-node execution cap (default 100).
	MaxNodeExecutions int         `yaml:"max_node_executions"`
	Loops             []*LoopSpec `yaml:"loops"`
	Nodes             []*NodeSpec `yaml:"nodes"`

	// raw is the YAML source this definition was parsed from; it is persisted
	// as the DAG snapshot for pause/resume and crash recovery.
	raw string
}

// LoopSpec declares a named loop scope with an iteration quota. Counting and
// resetting are anchored on specific dependency edges via counts_loop /
// resets_loop rather than implicit backedge detection.
type LoopSpec struct {
	// ID is the unique loop identifier referenced by edge-level metadata.
	ID string `yaml:"id"`
	// Nodes lists the node IDs belonging to this loop's scope.
	Nodes []string `yaml:"nodes"`
	// Parent names an enclosing loop (for nested loop declarations).
	Parent string `yaml:"parent"`
	// MaxIterations is the circuit-breaker quota; 0 means unlimited.
	MaxIterations int `yaml:"max_iterations"`
	// OnExhausted names the node activated (instead of re-entering the loop)
	// when MaxIterations is reached. It must live outside the loop and has
	// no static in-edges (an "orphan" node).
	OnExhausted string `yaml:"on_exhausted"`
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
	// CountsLoop names the loop counted each time this edge fires and
	// enqueues its target; descendant loop counters are reset as well.
	CountsLoop string `yaml:"counts_loop"`
	// ResetsLoop names the loop whose counter (and its descendants') this
	// edge resets to zero before enqueueing its target, bypassing the
	// exhaustion check.
	ResetsLoop string `yaml:"resets_loop"`
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
	// Entry marks the agent node that receives the raw user input as its
	// prompt. Non-entry agent nodes are kicked off with a directive; their
	// inputs are files produced by earlier nodes.
	Entry bool `yaml:"entry"`

	// LLM node fields.
	SystemPrompt string `yaml:"system_prompt"`

	// Command node fields.
	Command    string `yaml:"command"`
	OutputFile string `yaml:"output_file"`
	Sandbox    *bool  `yaml:"sandbox"`
	// AllowedExitCodes lists exit codes (besides 0) that count as
	// StatusSucceeded for command nodes; useful for Unix commands where a
	// non-zero code encodes a normal boolean result (e.g. grep exit 1).
	AllowedExitCodes []int `yaml:"allowed_exit_codes"`

	// Human node fields. Options is the optional list of canned replies
	// offered to the user; when empty any free-form text is accepted.
	Options []string `yaml:"options"`

	// Output quality gate & retry fields.
	// RequiredOutputs lists file paths (supporting ${tmp_dir}, ${run_dir}, ${session_id})
	// that must exist and be non-empty upon node completion.
	RequiredOutputs []string `yaml:"required_outputs"`
	// MaxRetries specifies the maximum self-correction retry attempts if required outputs are missing.
	// Defaults to 2 if required_outputs is non-empty and max_retries is not explicitly specified.
	MaxRetries *int `yaml:"max_retries"`
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
	if d.MaxNodeExecutions < 0 {
		return fmt.Errorf("max_node_executions cannot be negative")
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

		if len(node.AllowedExitCodes) > 0 {
			if node.Type != NodeTypeCommand {
				return fmt.Errorf("node %s: allowed_exit_codes is only allowed on command nodes", node.ID)
			}
			seenExitCodes := make(map[int]bool, len(node.AllowedExitCodes))
			for _, code := range node.AllowedExitCodes {
				if code < 0 || code > 255 {
					return fmt.Errorf("node %s: allowed_exit_codes entry %d out of valid exit code range (0-255)", node.ID, code)
				}
				if seenExitCodes[code] {
					return fmt.Errorf("node %s: duplicate entry %d in allowed_exit_codes", node.ID, code)
				}
				seenExitCodes[code] = true
			}
		}

		switch node.Join {
		case "", "always":
		default:
			return fmt.Errorf("node %s: invalid join %q (must be always)", node.ID, node.Join)
		}
		if node.Entry && node.Type != NodeTypeAgent {
			return fmt.Errorf("node %s: entry is only allowed on agent nodes", node.ID)
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

		if node.MaxRetries != nil {
			if node.Type != NodeTypeAgent {
				return fmt.Errorf("node %s: max_retries is only allowed on agent nodes", node.ID)
			}
			if *node.MaxRetries < 0 {
				return fmt.Errorf("node %s: max_retries cannot be negative", node.ID)
			}
		}
		if len(node.RequiredOutputs) > 0 {
			if node.Type != NodeTypeAgent {
				return fmt.Errorf("node %s: required_outputs is only allowed on agent nodes", node.ID)
			}
			for _, ro := range node.RequiredOutputs {
				if strings.TrimSpace(ro) == "" {
					return fmt.Errorf("node %s: required_outputs entry cannot be empty", node.ID)
				}
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
	if err := validateLoops(d); err != nil {
		return err
	}
	if err := validateHumanNodes(d); err != nil {
		return err
	}

	// Workflows with agent nodes must declare where the raw user input lands;
	// every other agent node is kicked off with a directive instead.
	agentCount, entryCount := 0, 0
	for _, node := range d.Nodes {
		if node.Type == NodeTypeAgent {
			agentCount++
			if node.Entry {
				entryCount++
			}
		}
	}
	if agentCount > 0 && entryCount == 0 {
		return fmt.Errorf("workflow must mark at least one agent node with entry: true to receive the user input")
	}
	return nil
}

// validateHumanNodes enforces the Phase 3 single-active-human-node constraint:
// human nodes must be totally ordered by the dependency graph. Two human nodes
// placed on branches that may execute concurrently are rejected. Humans that
// are only activated via a loop's on_exhausted routing have no static in-edges
// and are exempt from this check.
func validateHumanNodes(d *WorkflowDefinition) error {
	exempt := make(map[string]bool)
	for _, loop := range d.Loops {
		if loop.OnExhausted != "" {
			exempt[loop.OnExhausted] = true
		}
	}

	var humans []string
	for _, node := range d.Nodes {
		if node.Type == NodeTypeHuman {
			if exempt[node.ID] {
				if len(node.Depends) > 0 {
					return fmt.Errorf("node %s: on_exhausted human node must have no incoming dependency edges (must be an orphan)", node.ID)
				}
				continue
			}
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

// validateLoops statically validates the declared loops metadata and the
// edge-level counts_loop / resets_loop references.
func validateLoops(d *WorkflowDefinition) error {
	nodeIDs := make(map[string]bool, len(d.Nodes))
	for _, node := range d.Nodes {
		nodeIDs[node.ID] = true
	}

	allLoopNodes := make(map[string]string) // nodeID -> owning loopID
	loops := make(map[string]*LoopSpec, len(d.Loops))
	for _, loop := range d.Loops {
		if loop.ID == "" {
			return fmt.Errorf("loop id cannot be empty")
		}
		if _, dup := loops[loop.ID]; dup {
			return fmt.Errorf("duplicate loop id: %s", loop.ID)
		}
		loops[loop.ID] = loop

		if len(loop.Nodes) == 0 {
			return fmt.Errorf("loop %s: nodes list cannot be empty", loop.ID)
		}
		seenInLoop := make(map[string]bool, len(loop.Nodes))
		for _, id := range loop.Nodes {
			if !nodeIDs[id] {
				return fmt.Errorf("loop %s: references unknown node %q", loop.ID, id)
			}
			if seenInLoop[id] {
				return fmt.Errorf("loop %s: duplicate node %q in nodes list", loop.ID, id)
			}
			seenInLoop[id] = true
			allLoopNodes[id] = loop.ID
		}
		if loop.MaxIterations < 0 {
			return fmt.Errorf("loop %s: max_iterations cannot be negative", loop.ID)
		}
		if loop.MaxIterations == 0 && loop.OnExhausted != "" {
			return fmt.Errorf("loop %s: max_iterations: 0 (unlimited) cannot declare on_exhausted fallback", loop.ID)
		}
		if loop.OnExhausted != "" && !nodeIDs[loop.OnExhausted] {
			return fmt.Errorf("loop %s: on_exhausted references unknown node %q", loop.ID, loop.OnExhausted)
		}
	}

	// Parent references must be valid and acyclic; a child loop's nodes must
	// be a subset of its parent's nodes (transitively implied by checking the
	// direct parent only).
	for _, loop := range d.Loops {
		if loop.Parent == "" {
			continue
		}
		parent, ok := loops[loop.Parent]
		if !ok {
			return fmt.Errorf("loop %s: unknown parent loop %q", loop.ID, loop.Parent)
		}
		seen := map[string]bool{loop.ID: true}
		cur := loop.Parent
		for cur != "" {
			if seen[cur] {
				return fmt.Errorf("loop %s: parent cycle detected at %s", loop.ID, cur)
			}
			seen[cur] = true
			if ancestor := loops[cur]; ancestor != nil {
				cur = ancestor.Parent
			} else {
				break
			}
		}
		parentNodes := make(map[string]bool, len(parent.Nodes))
		for _, id := range parent.Nodes {
			parentNodes[id] = true
		}
		for _, id := range loop.Nodes {
			if !parentNodes[id] {
				return fmt.Errorf("loop %s: node %q is not part of parent loop %s", loop.ID, id, parent.ID)
			}
		}
	}

	// Loops without an ancestor/descendant relation must not share nodes.
	for i := 0; i < len(d.Loops); i++ {
		for j := i + 1; j < len(d.Loops); j++ {
			a, b := d.Loops[i], d.Loops[j]
			if loopsRelated(a, b, loops) {
				continue
			}
			bNodes := make(map[string]bool, len(b.Nodes))
			for _, id := range b.Nodes {
				bNodes[id] = true
			}
			for _, id := range a.Nodes {
				if bNodes[id] {
					return fmt.Errorf("loops %s and %s share node %q but have no parent/child relation", a.ID, b.ID, id)
				}
			}
		}
	}

	// Edge-level loop references.
	countingEdges := make(map[string]int, len(loops))
	for _, node := range d.Nodes {
		for _, dep := range node.Depends {
			if dep.CountsLoop != "" && dep.ResetsLoop != "" {
				return fmt.Errorf("node %s: dependency on %s cannot declare both counts_loop and resets_loop", node.ID, dep.NodeID)
			}
			if dep.CountsLoop != "" {
				loop, ok := loops[dep.CountsLoop]
				if !ok {
					return fmt.Errorf("node %s: counts_loop references unknown loop %q", node.ID, dep.CountsLoop)
				}
				countingEdges[dep.CountsLoop]++
				if !loopContainsNode(loop, dep.NodeID) || !loopContainsNode(loop, node.ID) {
					return fmt.Errorf("node %s: counts_loop %q requires both edge source %s and target %s inside loop %s",
						node.ID, loop.ID, dep.NodeID, node.ID, loop.ID)
				}
			}
			if dep.ResetsLoop != "" {
				loop, ok := loops[dep.ResetsLoop]
				if !ok {
					return fmt.Errorf("node %s: resets_loop references unknown loop %q", node.ID, dep.ResetsLoop)
				}
				if !loopContainsNode(loop, node.ID) {
					return fmt.Errorf("node %s: resets_loop %q requires edge target %s inside loop %s",
						node.ID, loop.ID, node.ID, loop.ID)
				}
			}
		}
	}

	// Every declared loop needs at least one counting edge.
	for _, loop := range d.Loops {
		if countingEdges[loop.ID] == 0 {
			return fmt.Errorf("loop %s: no dependency edge declares counts_loop: %s", loop.ID, loop.ID)
		}
	}

	// on_exhausted targets must live outside ALL loops.
	for _, loop := range d.Loops {
		if loop.OnExhausted != "" {
			if owningLoop, inLoop := allLoopNodes[loop.OnExhausted]; inLoop {
				return fmt.Errorf("loop %s: on_exhausted node %s must not belong to any loop (found in %s)", loop.ID, loop.OnExhausted, owningLoop)
			}
		}
	}
	return nil
}

// loopsRelated reports whether a and b are in the same ancestor chain.
func loopsRelated(a, b *LoopSpec, loops map[string]*LoopSpec) bool {
	return loopInChain(a, b.ID, loops) || loopInChain(b, a.ID, loops)
}

// loopInChain walks loop's parent chain looking for targetID.
func loopInChain(loop *LoopSpec, targetID string, loops map[string]*LoopSpec) bool {
	cur := loop.Parent
	for cur != "" {
		if cur == targetID {
			return true
		}
		if ancestor := loops[cur]; ancestor != nil {
			cur = ancestor.Parent
		} else {
			break
		}
	}
	return false
}

func loopContainsNode(loop *LoopSpec, nodeID string) bool {
	for _, id := range loop.Nodes {
		if id == nodeID {
			return true
		}
	}
	return false
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
