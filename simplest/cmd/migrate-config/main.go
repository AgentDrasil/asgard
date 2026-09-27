package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/AgentDrasil/asgard/simplest"
)

func main() {
	var (
		configPath    string
		keyPath       string
		providersPath string
		modelsPath    string
		dryRun        bool
	)

	defaultPath := simplest.DefaultConfigPath()
	flag.StringVar(&configPath, "config", defaultPath, "Path to configuration file to migrate (e.g. config.yaml or key.yaml)")
	flag.StringVar(&keyPath, "key", "", "Target path for key.yaml (default: key.yaml in same directory, or overwrites config if named key.yaml)")
	flag.StringVar(&providersPath, "providers", "", "Target path for providers.yaml (default: providers.yaml in same directory as config)")
	flag.StringVar(&modelsPath, "models", "", "Target path for models.yaml (default: models.yaml in same directory as config)")
	flag.BoolVar(&dryRun, "dry-run", false, "Preview changes without modifying files")
	flag.Parse()

	if configPath == "" {
		fmt.Fprintln(os.Stderr, "Error: no config file found or specified. Please provide --config <path>")
		os.Exit(1)
	}

	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Error: config file %q does not exist\n", configPath)
		os.Exit(1)
	}

	dir := filepath.Dir(configPath)
	if keyPath == "" {
		keyPath = filepath.Join(dir, "key.yaml")
	}
	if providersPath == "" {
		providersPath = filepath.Join(dir, "providers.yaml")
	}
	if modelsPath == "" {
		modelsPath = filepath.Join(dir, "models.yaml")
	}

	fmt.Printf("Migrating and splitting config file: %s -> key: %s, providers: %s, models: %s (dry-run=%v)\n", configPath, keyPath, providersPath, modelsPath, dryRun)

	changed, err := MigrateAndSplitConfigFile(configPath, keyPath, providersPath, modelsPath, dryRun)
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
		fmt.Printf("Successfully migrated and split into %s, %s, and %s\n", keyPath, providersPath, modelsPath)
	}
}
