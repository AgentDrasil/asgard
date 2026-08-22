package workflow

import (
	"github.com/AgentDrasil/asgard/pkg/workflowspec"
)

type MountDirsConfig = workflowspec.MountDirsConfig
type WorkflowDefinition = workflowspec.WorkflowDefinition
type LoopSpec = workflowspec.LoopSpec
type NodeSpec = workflowspec.NodeSpec
type NodeDependency = workflowspec.NodeDependency
type FanoutSpec = workflowspec.FanoutSpec

type NodeType = workflowspec.NodeType

const (
	NodeTypeAgent    = workflowspec.NodeTypeAgent
	NodeTypeCommand  = workflowspec.NodeTypeCommand
	NodeTypeLLM      = workflowspec.NodeTypeLLM
	NodeTypeWorkflow = workflowspec.NodeTypeWorkflow
	NodeTypeHuman    = workflowspec.NodeTypeHuman
	NodeTypeFunction = workflowspec.NodeTypeFunction
)

type NodeStatus = workflowspec.NodeStatus

const (
	StatusPending   = workflowspec.StatusPending
	StatusRunning   = workflowspec.StatusRunning
	StatusSucceeded = workflowspec.StatusSucceeded
	StatusSkipped   = workflowspec.StatusSkipped
	StatusFailed    = workflowspec.StatusFailed
)

type SkipReason = workflowspec.SkipReason

const (
	SkipReasonConditionFalse  = workflowspec.SkipReasonConditionFalse
	SkipReasonCascadedFailure = workflowspec.SkipReasonCascadedFailure
	SkipReasonNeverActivated  = workflowspec.SkipReasonNeverActivated
)

type NodeResult = workflowspec.NodeResult

var (
	ParseDefinition    = workflowspec.ParseDefinition
	LoadDefinition     = workflowspec.LoadDefinition
	Interpolate        = workflowspec.Interpolate
	ParseExprTarget    = workflowspec.ParseExprTarget
	EvaluateSimpleExpr = workflowspec.EvaluateSimpleExpr
	ResolveNodeValue   = workflowspec.ResolveNodeValue
)
