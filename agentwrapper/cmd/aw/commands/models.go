package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"sort"

	"github.com/spf13/cobra"

	"github.com/AgentDrasil/asgard/agentwrapper"
	"github.com/AgentDrasil/asgard/agentwrapper/types"
)

// newAgentModelsCmd builds a "models" subcommand for a single agent.
// binary, when non-empty, is checked for presence in PATH before fetching.
func newAgentModelsCmd(agentName, binary string, dirFlag *string, fetch func(ctx context.Context, opts types.UsageOptions) ([]string, error)) *cobra.Command {
	return &cobra.Command{
		Use:   "models",
		Short: fmt.Sprintf("List available models for %s", agentName),
		RunE: func(cmd *cobra.Command, args []string) error {
			if binary != "" {
				if _, err := exec.LookPath(binary); err != nil {
					return fmt.Errorf("%s command not found in PATH: %w", binary, err)
				}
			}

			dir := *dirFlag
			if dir == "" {
				var err error
				dir, err = os.Getwd()
				if err != nil {
					return fmt.Errorf("could not determine current directory: %w", err)
				}
			}

			baseCtx := cmd.Context()
			if baseCtx == nil {
				baseCtx = context.Background()
			}
			ctx, stop := signal.NotifyContext(baseCtx, os.Interrupt)
			defer stop()

			models, err := fetch(ctx, types.UsageOptions{Dir: dir})
			if err != nil {
				return fmt.Errorf("fetching models: %w", err)
			}

			filtered := make([]string, 0, len(models))
			for _, m := range models {
				if GlobalConfig.IsModelAllowed(agentName, m) {
					filtered = append(filtered, m)
				}
			}
			sort.Strings(filtered)

			out, err := json.MarshalIndent(filtered, "", "  ")
			if err != nil {
				return fmt.Errorf("encoding models: %w", err)
			}
			if _, err := fmt.Fprintln(cmd.OutOrStdout(), string(out)); err != nil {
				return fmt.Errorf("writing models: %w", err)
			}
			return nil
		},
	}
}

var modelsCmd = &cobra.Command{
	Use:   "models",
	Short: "List available models for all supported agents",
	Long:  `models queries all registered agents and outputs their available models.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		allModels := agentwrapper.GetSupportedCLIsAndModels()
		filtered := make(map[string][]string, len(allModels))
		for agent, models := range allModels {
			list := make([]string, 0, len(models))
			for _, m := range models {
				if GlobalConfig.IsModelAllowed(agent, m) {
					list = append(list, m)
				}
			}
			sort.Strings(list)
			filtered[agent] = list
		}

		out, err := json.MarshalIndent(filtered, "", "  ")
		if err != nil {
			return fmt.Errorf("encoding models: %w", err)
		}
		if _, err := fmt.Fprintln(cmd.OutOrStdout(), string(out)); err != nil {
			return fmt.Errorf("writing models: %w", err)
		}
		return nil
	},
}
