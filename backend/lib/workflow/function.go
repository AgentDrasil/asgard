package workflow

import (
	"github.com/AgentDrasil/asgard/pkg/pluginsdk"
)

// WorkflowFunction is re-exported from pluginsdk.WorkflowFunction.
type WorkflowFunction = pluginsdk.WorkflowFunction

// FunctionRegistry is re-exported from pluginsdk.FunctionRegistry.
type FunctionRegistry = pluginsdk.FunctionRegistry

var (
	// NewFunctionRegistry is re-exported from pluginsdk.NewFunctionRegistry.
	NewFunctionRegistry = pluginsdk.NewFunctionRegistry
	// NewFunctionRegistryWithParent is re-exported from pluginsdk.NewFunctionRegistryWithParent.
	NewFunctionRegistryWithParent = pluginsdk.NewFunctionRegistryWithParent
	// RegisterFunction is re-exported from pluginsdk.RegisterFunction.
	RegisterFunction = pluginsdk.RegisterFunction
	// GetFunction is re-exported from pluginsdk.GetFunction.
	GetFunction = pluginsdk.GetFunction
	// DefaultFunctionRegistry is re-exported from pluginsdk.DefaultFunctionRegistry.
	DefaultFunctionRegistry = pluginsdk.DefaultFunctionRegistry
)
