package workflow

import (
	"github.com/AgentDrasil/asgard/pkg/workflowspec"
)

// Deprecated: use workflowspec.MountDirsConfig from github.com/AgentDrasil/asgard/pkg/workflowspec instead.
type MountDirsConfig = workflowspec.MountDirsConfig

// Deprecated: use workflowspec.WorkflowDefinition from github.com/AgentDrasil/asgard/pkg/workflowspec instead.
type WorkflowDefinition = workflowspec.WorkflowDefinition

// Deprecated: use workflowspec.LoopSpec from github.com/AgentDrasil/asgard/pkg/workflowspec instead.
type LoopSpec = workflowspec.LoopSpec

// Deprecated: use workflowspec.NodeSpec from github.com/AgentDrasil/asgard/pkg/workflowspec instead.
type NodeSpec = workflowspec.NodeSpec

// Deprecated: use workflowspec.NodeDependency from github.com/AgentDrasil/asgard/pkg/workflowspec instead.
type NodeDependency = workflowspec.NodeDependency

// Deprecated: use workflowspec.FanoutSpec from github.com/AgentDrasil/asgard/pkg/workflowspec instead.
type FanoutSpec = workflowspec.FanoutSpec

// Deprecated: use workflowspec.NodeType from github.com/AgentDrasil/asgard/pkg/workflowspec instead.
type NodeType = workflowspec.NodeType

const (
	// Deprecated: use workflowspec.NodeTypeAgent instead.
	NodeTypeAgent = workflowspec.NodeTypeAgent
	// Deprecated: use workflowspec.NodeTypeCommand instead.
	NodeTypeCommand = workflowspec.NodeTypeCommand
	// Deprecated: use workflowspec.NodeTypeLLM instead.
	NodeTypeLLM = workflowspec.NodeTypeLLM
	// Deprecated: use workflowspec.NodeTypeWorkflow instead.
	NodeTypeWorkflow = workflowspec.NodeTypeWorkflow
	// Deprecated: use workflowspec.NodeTypeHuman instead.
	NodeTypeHuman = workflowspec.NodeTypeHuman
	// Deprecated: use workflowspec.NodeTypeFunction instead.
	NodeTypeFunction = workflowspec.NodeTypeFunction
)

// Deprecated: use workflowspec.NodeStatus from github.com/AgentDrasil/asgard/pkg/workflowspec instead.
type NodeStatus = workflowspec.NodeStatus

const (
	// Deprecated: use workflowspec.StatusPending instead.
	StatusPending = workflowspec.StatusPending
	// Deprecated: use workflowspec.StatusRunning instead.
	StatusRunning = workflowspec.StatusRunning
	// Deprecated: use workflowspec.StatusSucceeded instead.
	StatusSucceeded = workflowspec.StatusSucceeded
	// Deprecated: use workflowspec.StatusSkipped instead.
	StatusSkipped = workflowspec.StatusSkipped
	// Deprecated: use workflowspec.StatusFailed instead.
	StatusFailed = workflowspec.StatusFailed
)

// Deprecated: use workflowspec.SkipReason from github.com/AgentDrasil/asgard/pkg/workflowspec instead.
type SkipReason = workflowspec.SkipReason

const (
	// Deprecated: use workflowspec.SkipReasonConditionFalse instead.
	SkipReasonConditionFalse = workflowspec.SkipReasonConditionFalse
	// Deprecated: use workflowspec.SkipReasonCascadedFailure instead.
	SkipReasonCascadedFailure = workflowspec.SkipReasonCascadedFailure
	// Deprecated: use workflowspec.SkipReasonNeverActivated instead.
	SkipReasonNeverActivated = workflowspec.SkipReasonNeverActivated
)

// Deprecated: use workflowspec.NodeResult from github.com/AgentDrasil/asgard/pkg/workflowspec instead.
type NodeResult = workflowspec.NodeResult

var (
	// Deprecated: use workflowspec.ParseDefinition instead.
	ParseDefinition = workflowspec.ParseDefinition
	// Deprecated: use workflowspec.LoadDefinition instead.
	LoadDefinition = workflowspec.LoadDefinition
	// Deprecated: use workflowspec.Interpolate instead.
	Interpolate = workflowspec.Interpolate
	// Deprecated: use workflowspec.ParseExprTarget instead.
	ParseExprTarget = workflowspec.ParseExprTarget
	// Deprecated: use workflowspec.EvaluateSimpleExpr instead.
	EvaluateSimpleExpr = workflowspec.EvaluateSimpleExpr
	// Deprecated: use workflowspec.ResolveNodeValue instead.
	ResolveNodeValue = workflowspec.ResolveNodeValue
)
