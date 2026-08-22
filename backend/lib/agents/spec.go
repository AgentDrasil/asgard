package agents

import (
	"github.com/AgentDrasil/asgard/pkg/agentspec"
)

type AgentConfig = agentspec.AgentConfig
type CLITarget = agentspec.CLITarget
type MountConfig = agentspec.MountConfig
type Agent = agentspec.Agent
type Loader = agentspec.Loader

const AgentFatherID = agentspec.AgentFatherID

var NewLoader = agentspec.NewLoader
