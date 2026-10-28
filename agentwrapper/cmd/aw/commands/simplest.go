package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"os/signal"

	"github.com/spf13/cobra"

	"github.com/AgentDrasil/asgard/agentwrapper"
	simplest "github.com/AgentDrasil/asgard/agentwrapper/simplest"
	"github.com/AgentDrasil/asgard/agentwrapper/types"
)

var (
	simplestDir         string
	simplestPrompt      string
	simplestSession     string
	simplestUsage       bool
	simplestModel       string
	simplestAddTmpToDir bool
)

var simplestCmd = &cobra.Command{
	Use:   "simplest",
	Short: "Run a simplest agent",
	Long:  `simplest starts an in-process simplest agent session with the given prompt and optional session ID.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		dir := simplestDir
		if dir == "" {
			var err error
			dir, err = os.Getwd()
			if err != nil {
				return fmt.Errorf("could not determine current directory: %w", err)
			}
		}

		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
		defer stop()

		var prompt string
		if !simplestUsage {
			var err error
			prompt, err = resolvePrompt(simplestPrompt)
			if err != nil {
				return err
			}
		}

		if simplestUsage {
			entries, err := simplest.Usage(ctx, types.UsageOptions{Dir: dir})
			if err != nil {
				return fmt.Errorf("fetching usage: %w", err)
			}
			filtered := make([]types.ModelUsage, 0, len(entries))
			for _, entry := range entries {
				if GlobalConfig.IsModelAllowed("simplest", entry.Model) {
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

		if simplestModel != "" {
			allowed := false
			for _, name := range agentwrapper.ModelCandidates("simplest", simplestModel) {
				if GlobalConfig.IsModelAllowed("simplest", name) {
					allowed = true
					break
				}
			}
			if !allowed {
				return fmt.Errorf("model %q is not allowed by config", simplestModel)
			}
		}

		result, err := simplest.Prompt(ctx, prompt, types.PromptOptions{
			Dir:            dir,
			SessionID:      simplestSession,
			Model:          simplestModel,
			AddTmpToDir:    simplestAddTmpToDir,
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

var simplestModelsCmd = newAgentModelsCmd("simplest", "", &simplestDir, simplest.Models)

func init() {
	simplestCmd.PersistentFlags().StringVar(&simplestDir, "dir", "", "Working directory for the agent (defaults to current directory)")
	simplestCmd.Flags().StringVarP(&simplestPrompt, "prompt", "p", "", "Prompt to send to the agent (or pipe via stdin)")
	simplestCmd.Flags().StringVarP(&simplestSession, "session", "s", "", "Session ID to resume")
	simplestCmd.Flags().BoolVar(&simplestUsage, "usage", false, "Print token usage information")
	simplestCmd.Flags().StringVarP(&simplestModel, "model", "m", "", "Model to select for the session")
	simplestCmd.Flags().BoolVar(&simplestAddTmpToDir, "add-tmp-to-dir", false, "Add /tmp to allowed directories for the agent (no-op for simplest)")

	simplestCmd.AddCommand(simplestModelsCmd)
}
