package exec

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/AgentDrasil/asgard/pkg/agentspec"
	"github.com/AgentDrasil/asgard/pkg/runnerpb"
)

// AgentSource is the runner's live view of its effective agent definitions
// (config.Remote).
type AgentSource interface {
	Agents() ([]*agentspec.Agent, error)
}

// resolveAgent finds the agent a StartRun names. Definitions reach the runner
// through its own etcd watch, so the wanted hash may not have arrived yet:
// resolve polls until the agent with that id, source and hash shows up or wait
// elapses, and then fails with an error saying what the runner does have.
func resolveAgent(ctx context.Context, src AgentSource, sr *runnerpb.StartRun, wait, poll time.Duration) (*agentspec.Agent, error) {
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		agent, why := lookupAgent(src, sr)
		if agent != nil {
			return agent, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			return nil, fmt.Errorf("agent %q (source %s, hash %s) not available on this runner after %s: %s",
				sr.GetAgentId(), sr.GetAgentSource(), sr.GetAgentHash(), wait, why)
		case <-ticker.C:
		}
	}
}

func lookupAgent(src AgentSource, sr *runnerpb.StartRun) (*agentspec.Agent, string) {
	agents, err := src.Agents()
	if err != nil {
		return nil, "agent definitions not usable: " + err.Error()
	}
	wantRunner := sr.GetAgentSource() == runnerpb.AgentSource_AGENT_SOURCE_RUNNER
	why := "no such agent"
	for _, a := range agents {
		if a.Config.ID != sr.GetAgentId() {
			continue
		}
		if (a.NodeID != "") != wantRunner {
			why = "agent exists with a different source"
			continue
		}
		if a.Hash != sr.GetAgentHash() {
			why = "runner has hash " + a.Hash
			continue
		}
		return a, ""
	}
	return nil, why
}

// applyOverlay returns a copy of agent with the StartRun's workflow-effective
// run_dirs and mount dirs applied; empty lists keep the agent config's values.
func applyOverlay(agent *agentspec.Agent, sr *runnerpb.StartRun) *agentspec.Agent {
	out := *agent
	out.Config.MountDirs = agent.Config.MountDirs.Clone()
	if len(sr.GetRunDirs()) > 0 {
		out.Config.RunDirs = append([]string(nil), sr.GetRunDirs()...)
	}
	if len(sr.GetMountDirsReadonly()) > 0 {
		out.Config.MountDirs.ReadOnly = append([]string(nil), sr.GetMountDirsReadonly()...)
	}

	if out.Config.IsReadOnly() {
		// A readonly agent never gets RW mounts from overlay or agent config
		out.Config.MountDirs.ReadWrite = nil
		out.Config.MountDirs.ReadWriteEntries = nil
		out.Config.MountDirs.MaskRules = nil
	} else if len(sr.GetMountDirsReadwrite()) > 0 {
		out.Config.MountDirs.ReadWrite = append([]string(nil), sr.GetMountDirsReadwrite()...)
		rwSet := make(map[string]bool, len(sr.GetMountDirsReadwrite()))
		for _, rw := range sr.GetMountDirsReadwrite() {
			rwSet[filepath.Clean(rw)] = true
		}

		if len(out.Config.MountDirs.ReadWriteEntries) > 0 {
			var filteredEntries []agentspec.MountPathEntry
			for _, entry := range out.Config.MountDirs.ReadWriteEntries {
				if rwSet[filepath.Clean(entry.Path)] {
					filteredEntries = append(filteredEntries, entry)
				}
			}
			out.Config.MountDirs.ReadWriteEntries = filteredEntries
		}

		if len(out.Config.MountDirs.MaskRules) > 0 {
			filteredMaskRules := make(map[string]*agentspec.SubdirMaskRule)
			for base, rule := range out.Config.MountDirs.MaskRules {
				cleanBase := filepath.Clean(base)
				if rule != nil && rule.Base != "" {
					cleanBase = filepath.Clean(rule.Base)
				}
				if rwSet[cleanBase] {
					filteredMaskRules[base] = rule
				}
			}
			if len(filteredMaskRules) > 0 {
				out.Config.MountDirs.MaskRules = filteredMaskRules
			} else {
				out.Config.MountDirs.MaskRules = nil
			}
		}
	}
	return &out
}
