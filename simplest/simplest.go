// Package simplest is the public entry point of the simplest agent library.
//
// All implementation packages live under internal/ and cannot be imported
// by other modules. This file re-exports the supported surface only — use
// the examples/ directory for canonical usage.
package simplest

import (
	"github.com/AgentDrasil/asgard/simplest/internal/agent"
	"github.com/AgentDrasil/asgard/simplest/internal/config"
	"github.com/AgentDrasil/asgard/simplest/internal/prompt"
	"github.com/AgentDrasil/asgard/simplest/internal/provider"
	"github.com/AgentDrasil/asgard/simplest/internal/quota"
	"github.com/AgentDrasil/asgard/simplest/internal/session"
	"github.com/AgentDrasil/asgard/simplest/internal/tools"
	"github.com/AgentDrasil/asgard/simplest/internal/types"
)

// ---- config ----

type (
	Config         = config.Config
	ProviderConfig = config.ProviderConfig
	ModelConfig    = config.ModelConfig
)

var (
	LoadConfig              = config.Load
	LoadConfigFrom          = config.LoadFrom
	GetAvailableModels      = config.GetAvailableModels
	ResolveModelAndProvider = config.ResolveModelAndProvider
	DefaultConfigPath       = config.DefaultConfigPath
	SetGlobalConfig         = config.SetGlobalConfig
	ResetGlobalConfig       = config.ResetGlobalConfig
	GetGlobalConfig         = config.GetGlobalConfig
)

// ---- quota & usage ----

type (
	ModelUsage   = types.ModelUsage
	QuotaLimit   = types.QuotaLimit
	UsageOptions = types.UsageOptions
)

var (
	GetModelUsages          = quota.GetModelUsagesFromGlobal
	GetModelUsagesForConfig = quota.GetModelUsages
)

// ---- prompt ----

type (
	PromptBuildOptions = prompt.Options
	ContextFile        = prompt.ContextFile
)

var (
	BuildSystemPrompt       = prompt.BuildSystemPrompt
	LoadProjectContextFiles = prompt.LoadProjectContextFiles
)

// ---- agent ----

type (
	// Request describes one agent run.
	Request = agent.Request
)

// Run starts the agent loop and streams events until the run completes.
var Run = agent.Run

// ---- providers ----

type (
	Gemini       = provider.Gemini
	OpenAICompat = provider.OpenAICompat
)

var (
	NewGemini       = provider.NewGemini
	NewOpenAICompat = provider.NewOpenAICompat
	CalculateCost   = provider.CalculateCost
)

// ---- tools ----

type (
	// Registry holds the tools available to an agent run.
	Registry = tools.Registry
	// Func adapts a plain Go function into an agent tool.
	Func = tools.Func
)

var (
	DefaultRegistry = tools.DefaultRegistry
	NewRegistry     = tools.NewRegistry
	AllToolNames    = tools.AllToolNames
)

// ---- sessions ----

type (
	Manager        = session.Manager
	SessionFile    = session.SessionFile
	CreateOptions  = session.CreateOptions
	SessionSummary = session.SessionSummary
	SessionContext = session.Context
)

var (
	New             = session.New
	DefaultBaseDir  = session.DefaultBaseDir
	ListSessions    = session.List
	FindMostRecent  = session.FindMostRecent
	LoadSessionFile = session.LoadFile
)

// ---- types: messages & content ----

type (
	Message          = types.Message
	UserMessage      = types.UserMessage
	AssistantMessage = types.AssistantMessage
	AssistantContent = types.AssistantContent
	TextContent      = types.TextContent
	ThinkingContent  = types.ThinkingContent
	ImageContent     = types.ImageContent
	ToolCall         = types.ToolCall
	ToolResult       = types.ToolResult
	UpdateFunc       = types.UpdateFunc
	Usage            = types.Usage
	Cost             = types.Cost
	Role             = types.Role
	StopReason       = types.StopReason
	ThinkingLevel    = types.ThinkingLevel
)

var (
	TextOnly         = types.TextOnly
	StringContentOf  = types.StringContentOf
	MarshalMessage   = types.MarshalMessage
	UnmarshalMessage = types.UnmarshalMessage
)

const (
	RoleUser       = types.RoleUser
	RoleAssistant  = types.RoleAssistant
	RoleToolResult = types.RoleToolResult

	StopStop    = types.StopStop
	StopToolUse = types.StopToolUse
	StopLength  = types.StopLength
	StopError   = types.StopError
	StopAborted = types.StopAborted

	ThinkingOff     = types.ThinkingOff
	ThinkingMinimal = types.ThinkingMinimal
	ThinkingLow     = types.ThinkingLow
	ThinkingMedium  = types.ThinkingMedium
	ThinkingHigh    = types.ThinkingHigh
	ThinkingXHigh   = types.ThinkingXHigh
	ThinkingMax     = types.ThinkingMax
)

// ---- types: models & providers ----

type (
	Model          = types.Model
	ModelCostRates = types.ModelCostRates
	Provider       = types.Provider
	Context        = types.Context
)

const (
	APIGemini       = types.APIGemini
	APIOpenAICompat = types.APIOpenAICompat
)

// ---- types: tool authoring ----

type (
	// AgentTool is the interface a custom tool implements; embed *Func or
	// implement it directly.
	AgentTool = types.AgentTool
	ToolDef   = types.ToolDef
)

const (
	ExecutionSequential = types.ExecutionSequential
	ExecutionParallel   = types.ExecutionParallel
)

// ---- types: events ----

type (
	AgentEvent            = types.AgentEvent
	AssistantMessageEvent = types.AssistantMessageEvent
	Partial               = types.Partial
	DoneEvent             = types.DoneEvent
	StreamErrorEvent      = types.StreamErrorEvent
	StreamOptions         = types.StreamOptions
)

const (
	EvStart         = types.EvStart
	EvTextStart     = types.EvTextStart
	EvTextDelta     = types.EvTextDelta
	EvTextEnd       = types.EvTextEnd
	EvThinkingStart = types.EvThinkingStart
	EvThinkingDelta = types.EvThinkingDelta
	EvThinkingEnd   = types.EvThinkingEnd
	EvToolcallStart = types.EvToolcallStart
	EvToolcallDelta = types.EvToolcallDelta
	EvToolcallEnd   = types.EvToolcallEnd
	EvDone          = types.EvDone
	EvStreamError   = types.EvStreamError

	AgentStart          = types.AgentStart
	MessageStart        = types.MessageStart
	MessageUpdate       = types.MessageUpdate
	MessageEnd          = types.MessageEnd
	ToolExecutionStart  = types.ToolExecutionStart
	ToolExecutionUpdate = types.ToolExecutionUpdate
	ToolExecutionEnd    = types.ToolExecutionEnd
	TurnStart           = types.TurnStart
	TurnEnd             = types.TurnEnd
	AgentEnd            = types.AgentEnd
)
