package run

import (
	"context"
	"fmt"

	"github.com/moznion/go-optional"

	"github.com/AgentDrasil/asgard/lib/agents"
	"github.com/AgentDrasil/asgard/lib/agentwrapper"
	"github.com/AgentDrasil/asgard/lib/bwrap"
)

// Run checks the remaining quota for each CLI target configured on the agent.
// It runs the bubblewrap command for the first target that has more than 20% quota remaining.
// If no targets have more than 20% quota remaining, it returns an error.
func Run(ctx context.Context, agent *agents.Agent, prompt string, session optional.Option[string]) ([]byte, error) {
	if len(agent.Config.CLI) == 0 {
		return nil, fmt.Errorf("no CLI targets configured for agent %s", agent.Config.ID)
	}

	var selectedTarget *agents.CLITarget
	for _, target := range agent.Config.CLI {
		quota := agentwrapper.CheckQuota(target.CLI, target.Model)
		if quota > 0.20 {
			selectedTarget = &target
			break
		}
	}

	if selectedTarget == nil {
		return nil, fmt.Errorf("no CLI target with more than 20%% quota remaining is available for agent %s", agent.Config.ID)
	}

	cmd, err := bwrap.CommandForAgent(&agent.Config, *selectedTarget, prompt, session)
	if err != nil {
		return nil, fmt.Errorf("creating command for agent: %w", err)
	}

	if ctx.Done() != nil {
		done := make(chan struct{})
		defer close(done)
		go func() {
			select {
			case <-ctx.Done():
				if cmd.Process != nil {
					_ = cmd.Process.Kill()
				}
			case <-done:
			}
		}()
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return out, fmt.Errorf("running agent sandbox command: %w (output: %q)", err, string(out))
	}

	return out, nil
}
