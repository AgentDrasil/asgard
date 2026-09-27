package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/AgentDrasil/asgard/simplest"
)

func main() {
	var (
		configPath string
		dryRun     bool
	)

	defaultPath := simplest.DefaultConfigPath()
	flag.StringVar(&configPath, "config", defaultPath, "Path to config.yaml file to migrate")
	flag.BoolVar(&dryRun, "dry-run", false, "Preview changes without modifying the config file")
	flag.Parse()

	if configPath == "" {
		fmt.Fprintln(os.Stderr, "Error: no config file found or specified. Please provide --config <path>")
		os.Exit(1)
	}

	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Error: config file %q does not exist\n", configPath)
		os.Exit(1)
	}

	fmt.Printf("Migrating config file: %s (dry-run=%v)\n", configPath, dryRun)
	changed, err := MigrateConfigFile(configPath, dryRun)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Migration failed: %v\n", err)
		os.Exit(1)
	}

	if !changed {
		fmt.Println("Config is already up to date. No changes needed.")
		return
	}

	if dryRun {
		fmt.Println("Dry-run preview completed successfully.")
	} else {
		fmt.Printf("Successfully migrated and updated %s\n", configPath)
	}
}
