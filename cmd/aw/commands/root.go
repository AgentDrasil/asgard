package commands

import (
	"fmt"
	"os"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"

	"github.com/AgentDrasil/asgard/cmd/aw/config"
)

var debug bool

// GlobalConfig is the parsed configuration loaded from the YAML file.
var GlobalConfig *config.Config

var rootCmd = &cobra.Command{
	Use:   "aw",
	Short: "Agent Wrapper CLI",
	Long:  `aw is the command-line interface for Agent Wrapper.`,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		zerolog.TimeFieldFormat = time.RFC3339
		consoleWriter := zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339}
		log.Logger = zerolog.New(consoleWriter).With().Timestamp().Logger()
		if debug {
			zerolog.SetGlobalLevel(zerolog.DebugLevel)
		} else {
			zerolog.SetGlobalLevel(zerolog.InfoLevel)
		}

		var err error
		GlobalConfig, err = config.LoadConfig()
		if err != nil {
			return fmt.Errorf("loading config: %w", err)
		}
		return nil
	},
}

// Execute runs the root command.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().BoolVar(&debug, "debug", false, "enable debug logging")
	rootCmd.AddCommand(agyCmd)
	rootCmd.AddCommand(opencodeCmd)
}
