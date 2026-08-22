package workflowspec

// NodeType discriminates the kind of work a node executes.
type NodeType string

const (
	NodeTypeAgent    NodeType = "agent"
	NodeTypeCommand  NodeType = "command"
	NodeTypeLLM      NodeType = "llm"
	NodeTypeWorkflow NodeType = "workflow"
	NodeTypeHuman    NodeType = "human"
	NodeTypeFunction NodeType = "function"
)

// FanoutSpec configures fan-out execution for workflow nodes: the node runs
// one sub-workflow instance per line of ItemsFile, up to MaxParallel
// concurrently. MaxParallel is a pointer so definitions can distinguish
// "not provided" (nil -> runtime default) from an explicit value.
type FanoutSpec struct {
	// ItemsFile lists one item per line; each line spawns a sub-workflow run.
	ItemsFile string `yaml:"items_file"`
	// MaxParallel caps concurrent sub-workflow executions (nil -> default 3).
	MaxParallel *int `yaml:"max_parallel,omitempty"`
	// OutputFile aggregates per-item results when non-empty.
	OutputFile string `yaml:"output_file,omitempty"`
}

// NodeStatus is the lifecycle status of a single workflow node.
type NodeStatus string

const (
	StatusPending   NodeStatus = "PENDING"
	StatusRunning   NodeStatus = "RUNNING"
	StatusSucceeded NodeStatus = "SUCCEEDED"
	StatusSkipped   NodeStatus = "SKIPPED"
	StatusFailed    NodeStatus = "FAILED"
)

// SkipReason explains why a node was skipped.
type SkipReason string

const (
	// SkipReasonConditionFalse means the node (or an upstream edge) had a
	// `when` condition that evaluated to false. This is an intentional,
	// successful branch skip.
	SkipReasonConditionFalse SkipReason = "CONDITION_FALSE"
	// SkipReasonCascadedFailure means the node was forcibly skipped because
	// an upstream node failed and the failure was not absorbed by a `when`
	// edge. This propagates global workflow failure.
	SkipReasonCascadedFailure SkipReason = "CASCADED_FAILURE"
	// SkipReasonNeverActivated means the node is an on_exhausted orphan (or
	// its pure conditional downstream) that legitimately never fired during
	// the run. Benign: it does not fail the global workflow settlement.
	SkipReasonNeverActivated SkipReason = "NEVER_ACTIVATED"
)

// NodeResult is the outcome of executing (or skipping) a single node.
type NodeResult struct {
	Status     NodeStatus
	SkipReason SkipReason
	ExitCode   int
	Output     string
	Artifacts  map[string]string
	Error      error
	// AgentName names the concrete sub-agent that executed an agent node
	// (empty for other node types); used for chat message attribution.
	AgentName string
	// LoopIterations snapshots the iteration counters of the loops this node
	// belongs to at settle time; addressable in `when` expressions as
	// nodes.<id>.loop_iteration.<loop_id>.
	LoopIterations map[string]int
}
