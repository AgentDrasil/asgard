package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"

	"github.com/spf13/cobra"

	opencode "github.com/AgentDrasil/asgard/lib/agentwrapper/opencode"
	"github.com/AgentDrasil/asgard/lib/agentwrapper/types"
)

var (
	opencodeDir     string
	opencodePrompt  string
	opencodeSession string
	opencodeUsage   bool
	opencodeModel   string
)

var opencodeCmd = &cobra.Command{
	Use:   "opencode",
	Short: "Run an opencode agent",
	Long:  `opencode starts an opencode agent session with the given prompt and optional session ID.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if _, err := exec.LookPath("opencode"); err != nil {
			return fmt.Errorf("opencode command not found in PATH: %w", err)
		}

		dir := opencodeDir
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
		if !opencodeUsage {
			var err error
			prompt, err = resolvePrompt(opencodePrompt)
			if err != nil {
				return err
			}
		}

		if opencodeUsage {
			entries, err := opencode.Usage(ctx, types.UsageOptions{Dir: dir})
			if err != nil {
				return fmt.Errorf("fetching usage: %w", err)
			}
			var filtered []types.ModelUsage
			for _, entry := range entries {
				if GlobalConfig.IsModelAllowed("opencode", entry.Model) {
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

		if opencodeModel != "" {
			if !GlobalConfig.IsModelAllowed("opencode", opencodeModel) {
				return fmt.Errorf("model %q is not allowed by config", opencodeModel)
			}
		}

		result, err := opencode.Prompt(ctx, prompt, types.PromptOptions{
			Dir:       dir,
			SessionID: opencodeSession,
			Model:     opencodeModel,
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

func init() {
	opencodeCmd.Flags().StringVar(&opencodeDir, "dir", "", "Working directory for the agent (defaults to current directory)")
	opencodeCmd.Flags().StringVarP(&opencodePrompt, "prompt", "p", "", "Prompt to send to the agent (or pipe via stdin)")
	opencodeCmd.Flags().StringVarP(&opencodeSession, "session", "s", "", "Session ID to resume")
	opencodeCmd.Flags().BoolVar(&opencodeUsage, "usage", false, "Print token usage information")
	opencodeCmd.Flags().StringVarP(&opencodeModel, "model", "m", "", "Model to select for the session")
}
