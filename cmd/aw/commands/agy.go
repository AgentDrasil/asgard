package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"

	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"

	"github.com/AgentDrasil/asgard/lib/agentwrapper"
	agy "github.com/AgentDrasil/asgard/lib/agentwrapper/agy"
)

var (
	agyDir              string
	agyPrompt           string
	agySession          string
	agyUsage            bool
	agyModel            string
	supportedAgyVersion = "1.0.8"
)

var agyCmd = &cobra.Command{
	Use:   "agy",
	Short: "Run an agent",
	Long:  `agy starts an agent session with the given prompt and optional session ID.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if _, err := exec.LookPath("agy"); err != nil {
			return fmt.Errorf("agy command not found in PATH: %w", err)
		}

		if v, err := agy.Version(cmd.Context()); err != nil {
			log.Warn().Err(err).Msg("failed to check agy version")
		} else if v != supportedAgyVersion {
			log.Warn().Msgf("unsupported agy version: %s (supported is %s)", v, supportedAgyVersion)
		}

		dir := agyDir
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
		if !agyUsage {
			var err error
			prompt, err = resolvePrompt(agyPrompt)
			if err != nil {
				return err
			}
		}

		if agyUsage {
			entries, err := agy.Usage(ctx, agentwrapper.UsageOptions{Dir: dir})
			if err != nil {
				return fmt.Errorf("fetching usage: %w", err)
			}
			var filtered []agentwrapper.ModelUsage
			for _, entry := range entries {
				if GlobalConfig.IsModelAllowed("agy", entry.Model) {
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

		if agyModel != "" {
			if !GlobalConfig.IsModelAllowed("agy", agyModel) {
				return fmt.Errorf("model %q is not allowed by config", agyModel)
			}
		}

		result, err := agy.Prompt(ctx, prompt, agentwrapper.PromptOptions{
			Dir:       dir,
			SessionID: agySession,
			Model:     agyModel,
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

// resolvePrompt returns the effective prompt. It prefers the -p flag value;
// if that is empty it reads from stdin (only when stdin is not a terminal).
// Returns an error when neither source provides a value.
func resolvePrompt(flagValue string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}

	stat, err := os.Stdin.Stat()
	if err != nil {
		return "", fmt.Errorf("could not stat stdin: %w", err)
	}

	if (stat.Mode() & os.ModeCharDevice) != 0 {
		// stdin is a terminal — nothing piped in
		return "", fmt.Errorf("required flag \"prompt\" not set: provide -p or pipe a prompt via stdin")
	}

	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", fmt.Errorf("reading prompt from stdin: %w", err)
	}

	prompt := strings.TrimSpace(string(raw))
	if prompt == "" {
		return "", fmt.Errorf("prompt is empty: provide -p or pipe a non-empty prompt via stdin")
	}

	return prompt, nil
}

func init() {
	agyCmd.Flags().StringVar(&agyDir, "dir", "", "Working directory for the agent (defaults to current directory)")
	agyCmd.Flags().StringVarP(&agyPrompt, "prompt", "p", "", "Prompt to send to the agent (or pipe via stdin)")
	agyCmd.Flags().StringVarP(&agySession, "session", "s", "", "Session ID to resume")
	agyCmd.Flags().BoolVar(&agyUsage, "usage", false, "Print token usage information")
	agyCmd.Flags().StringVarP(&agyModel, "model", "m", "", "Model to select for the session")
}
