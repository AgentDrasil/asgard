package commands

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/AgentDrasil/asgard/agentwrapper"
	"github.com/AgentDrasil/asgard/agentwrapper/types"
)

var usageCLI string

var usageCmd = &cobra.Command{
	Use:   "usage",
	Short: "Print quota usage for all supported agents",
	Long: `usage queries all registered agents concurrently and outputs their per-model quota as JSON.
With --cli, only that agent is queried and only top-level quota is returned.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		var (
			res map[string][]types.ModelUsage
			err error
		)
		if usageCLI != "" {
			res, err = agentwrapper.GetCLIQuota(cmd.Context(), usageCLI)
		} else {
			res, err = agentwrapper.GetQuota(cmd.Context())
		}
		if err != nil {
			return fmt.Errorf("fetching usage: %w", err)
		}

		out, err := json.MarshalIndent(res, "", "  ")
		if err != nil {
			return fmt.Errorf("encoding usage: %w", err)
		}
		if _, err := fmt.Fprintln(cmd.OutOrStdout(), string(out)); err != nil {
			return fmt.Errorf("writing usage: %w", err)
		}
		return nil
	},
}

func init() {
	usageCmd.Flags().StringVar(&usageCLI, "cli", "", "Only query this agent (top-level quota only)")
}
