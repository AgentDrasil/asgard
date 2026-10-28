package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/AgentDrasil/asgard/agentwrapper"
	claude "github.com/AgentDrasil/asgard/agentwrapper/claude"
	"github.com/AgentDrasil/asgard/agentwrapper/types"
)

var (
	claudeDir     string
	claudePrompt  string
	claudeSession string
	claudeUsage   bool
	claudeModel   string
	claudeAgent   string
)

var claudeCmd = &cobra.Command{
	Use:   "claude",
	Short: "Run a claude agent",
	Long:  `claude starts a claude agent session with the given prompt and optional session ID.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if _, err := exec.LookPath("claude"); err != nil {
			return fmt.Errorf("claude command not found in PATH: %w", err)
		}

		dir := claudeDir
		if dir == "" {
			var err error
			dir, err = os.Getwd()
			if err != nil {
				return fmt.Errorf("could not determine current directory: %w", err)
			}
		}

		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
		defer stop()

		var prompt string
		if !claudeUsage {
			var err error
			prompt, err = resolvePrompt(claudePrompt)
			if err != nil {
				return err
			}
		}

		if claudeUsage {
			entries, err := claude.Usage(ctx, types.UsageOptions{Dir: dir})
			if err != nil {
				return fmt.Errorf("fetching usage: %w", err)
			}
			var filtered []types.ModelUsage
			for _, entry := range entries {
				if GlobalConfig.IsModelAllowed("claude", entry.Model) {
					filtered = append(filtered, entry)
				}
			}
			out, err := json.MarshalIndent(filtered, "", "  ")
			if err != nil {
				return fmt.Errorf("encoding usage: %w", err)
			}
			fmt.Println(string(out))
			return nil
		}

		if claudeModel != "" {
			allowed := false
			for _, name := range agentwrapper.ModelCandidates("claude", claudeModel) {
				if GlobalConfig.IsModelAllowed("claude", name) {
					allowed = true
					break
				}
			}
			if !allowed {
				return fmt.Errorf("model %q is not allowed by config", claudeModel)
			}
		}

		result, err := claude.Prompt(ctx, prompt, types.PromptOptions{
			Dir:            dir,
			SessionID:      claudeSession,
			Model:          claudeModel,
			AgentID:        claudeAgent,
			ReportCallback: buildHTTPReporter(),
		})
		if err != nil {
			return fmt.Errorf("running prompt: %w", err)
		}
		out, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return fmt.Errorf("encoding result: %w", err)
		}
		fmt.Println(string(out))
		return nil
	},
}

var claudeModelsCmd = newAgentModelsCmd("claude", "claude", &claudeDir, claude.Models)

func init() {
	claudeCmd.PersistentFlags().StringVar(&claudeDir, "dir", "", "Working directory for the agent (defaults to current directory)")
	claudeCmd.Flags().StringVarP(&claudePrompt, "prompt", "p", "", "Prompt to send to the agent (or pipe via stdin)")
	claudeCmd.Flags().StringVarP(&claudeSession, "session", "s", "", "Session ID to resume")
	claudeCmd.Flags().BoolVar(&claudeUsage, "usage", false, "Print token usage information")
	claudeCmd.Flags().StringVarP(&claudeModel, "model", "m", "", "Model to select for the session")
	claudeCmd.Flags().StringVar(&claudeAgent, "agent", "", "Asgard agent ID (drives CLI-native agent adaptation from the AW_AGENTS.md contract)")

	claudeCmd.AddCommand(claudeModelsCmd)
}
