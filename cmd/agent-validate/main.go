package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/goccy/go-yaml"

	"github.com/AgentDrasil/asgard/lib/agents"
	"github.com/AgentDrasil/asgard/lib/workflow"
)

var agentsDirFlag = flag.String("agents-dir", "", "Path to the agents root directory containing agents/ subdirectories and teams.yaml")

func main() {
	flag.Parse()
	args := flag.Args()
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "Usage: agent-validate [--agents-dir=<path>] <path-to-config-file-or-dir-or-workflow>")
		os.Exit(1)
	}

	targetPath := args[0]
	info, err := os.Stat(targetPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error accessing path %q: %v\n", targetPath, err)
		os.Exit(1)
	}

	if info.IsDir() {
		validateDir(targetPath)
		return
	}

	validateFile(targetPath)
}

func validateDir(dirPath string) {
	configPath := filepath.Join(dirPath, "config.yaml")
	workflowPath := filepath.Join(dirPath, "workflow.yaml")

	hasConfig := fileExists(configPath)
	hasWorkflow := fileExists(workflowPath)

	if !hasConfig && !hasWorkflow {
		fmt.Fprintf(os.Stderr, "Error: directory %q contains neither config.yaml nor workflow.yaml\n", dirPath)
		os.Exit(1)
	}

	if hasConfig {
		validateAgentConfigFile(configPath)
	}

	if hasWorkflow {
		validateWorkflowFile(workflowPath)
	}
}

func validateFile(filePath string) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading file %q: %v\n", filePath, err)
		os.Exit(1)
	}

	// Try parsing as WorkflowDefinition first if filename suggests it or contains 'nodes:'
	isWorkflow := filepath.Base(filePath) == "workflow.yaml" || containsNodesKey(data)

	if isWorkflow {
		defn, err := workflow.ParseDefinition(data)
		if err == nil {
			fmt.Printf("Workflow definition is valid (Name: %q, Nodes: %d)\n", defn.Name, len(defn.Nodes))
			checkAgentIDReferences(defn, filepath.Dir(filePath))
			return
		}
		// If it was explicitly a workflow file, fail with workflow error
		if filepath.Base(filePath) == "workflow.yaml" {
			fmt.Fprintf(os.Stderr, "Workflow validation failed for %q: %v\n", filePath, err)
			os.Exit(1)
		}
	}

	// Try validating as AgentConfig
	var cfg agents.AgentConfig
	if err := yaml.Unmarshal(data, &cfg); err == nil && cfg.ID != "" {
		if err := cfg.Validate(); err != nil {
			fmt.Fprintf(os.Stderr, "Agent config validation failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Agent config is valid (ID: %q, Name: %q, Type: %q)\n", cfg.ID, cfg.Name, cfg.Type)

		if cfg.Type == "workflow" {
			wfPath := filepath.Join(filepath.Dir(filePath), "workflow.yaml")
			if fileExists(wfPath) {
				validateWorkflowFile(wfPath)
			} else {
				fmt.Fprintf(os.Stderr, "Warning: agent is of type workflow but accompanying workflow.yaml was not found in %q\n", filepath.Dir(filePath))
			}
		}
		return
	}

	// If neither passed, try workflow parse explicitly to output error
	defn, err := workflow.ParseDefinition(data)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Validation failed: file %q is neither a valid agent config nor a valid workflow definition: %v\n", filePath, err)
		os.Exit(1)
	}

	fmt.Printf("Workflow definition is valid (Name: %q, Nodes: %d)\n", defn.Name, len(defn.Nodes))
	checkAgentIDReferences(defn, filepath.Dir(filePath))
}

func validateAgentConfigFile(configPath string) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading agent config %q: %v\n", configPath, err)
		os.Exit(1)
	}

	var cfg agents.AgentConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing agent config YAML in %q: %v\n", configPath, err)
		os.Exit(1)
	}

	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "Agent config validation failed for %q: %v\n", configPath, err)
		os.Exit(1)
	}
	fmt.Printf("Agent config is valid (ID: %q, Name: %q, Type: %q)\n", cfg.ID, cfg.Name, cfg.Type)
}

func validateWorkflowFile(workflowPath string) {
	defn, err := workflow.LoadDefinition(workflowPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Workflow definition validation failed for %q: %v\n", workflowPath, err)
		os.Exit(1)
	}
	fmt.Printf("Workflow definition is valid (File: %q, Name: %q, Nodes: %d)\n", filepath.Base(workflowPath), defn.Name, len(defn.Nodes))
	checkAgentIDReferences(defn, filepath.Dir(workflowPath))
}

func checkAgentIDReferences(defn *workflow.WorkflowDefinition, baseDir string) {
	agentsDir := findAgentsDir(baseDir)
	if agentsDir == "" {
		return
	}

	loader := agents.NewLoader(agentsDir)
	loadedAgents, err := loader.LoadAll()
	if err != nil {
		fmt.Fprintf(os.Stderr, "  Notice: could not load agents from %q to verify agent_id references: %v\n", agentsDir, err)
		return
	}

	knownAgents := make(map[string]bool)
	for _, ag := range loadedAgents {
		knownAgents[ag.Config.ID] = true
	}

	missing := false
	for _, node := range defn.Nodes {
		if node.Type == workflow.NodeTypeAgent && node.AgentID != "" {
			if !knownAgents[node.AgentID] {
				fmt.Fprintf(os.Stderr, "  Warning: node %q references agent_id %q which is not registered in agents pool (%s)\n", node.ID, node.AgentID, agentsDir)
				missing = true
			}
		}
	}

	if !missing && len(knownAgents) > 0 {
		fmt.Printf("  Checked %d agent_id references against agents pool (%s) [OK]\n", countAgentNodes(defn), agentsDir)
	}
}

func countAgentNodes(defn *workflow.WorkflowDefinition) int {
	cnt := 0
	for _, n := range defn.Nodes {
		if n.Type == workflow.NodeTypeAgent {
			cnt++
		}
	}
	return cnt
}

func findAgentsDir(baseDir string) string {
	if *agentsDirFlag != "" {
		if fileExists(filepath.Join(*agentsDirFlag, "agents")) {
			return *agentsDirFlag
		}
		return *agentsDirFlag
	}

	// Search upwards from baseDir for a directory containing agents/
	curr := baseDir
	for i := 0; i < 5; i++ {
		if curr == "" || curr == "/" {
			break
		}
		if fileExists(filepath.Join(curr, "agents")) && fileExists(filepath.Join(curr, "teams.yaml")) {
			return curr
		}
		curr = filepath.Dir(curr)
	}

	// Check common environment locations
	home, err := os.UserHomeDir()
	if err == nil {
		commonPaths := []string{
			filepath.Join(home, "asgard", "home", "asgard"),
			filepath.Join(home, ".asgard"),
		}
		for _, p := range commonPaths {
			if fileExists(filepath.Join(p, "agents")) && fileExists(filepath.Join(p, "teams.yaml")) {
				return p
			}
		}
	}

	return ""
}

func containsNodesKey(data []byte) bool {
	var raw map[string]interface{}
	if err := yaml.Unmarshal(data, &raw); err == nil {
		_, ok := raw["nodes"]
		return ok
	}
	return false
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
