package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/elmhuangyu/configs/pkg/cfgfs"
	"github.com/elmhuangyu/configs/pkg/configlib"
	"github.com/getsops/sops/v3/decrypt"
	"github.com/goccy/go-yaml"

	"github.com/AgentDrasil/asgard/pkg/agentspec"
	"github.com/AgentDrasil/asgard/pkg/paths"
	"github.com/AgentDrasil/asgard/pkg/workflowspec"
)

var agentsDirFlag = flag.String("agents-dir", "", "Path to the agents root directory containing agents/ subdirectories and teams.yaml")

var noDecryptFlag = flag.Bool("no-decrypt", false, "Do not decrypt SOPS envelopes when validating an etcd tree (encrypted values are validated as-is)")

func main() {
	flag.Parse()
	args := flag.Args()
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "Usage: agent-validate [--agents-dir=<path>] <path-to-config-file-or-dir-or-workflow-or-etcd-tree-root>")
		os.Exit(1)
	}

	targetPath := args[0]
	info, err := os.Stat(targetPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error accessing path %q: %v\n", targetPath, err)
		os.Exit(1)
	}

	if info.IsDir() {
		if isTreeRoot(targetPath) {
			if !validateTree(targetPath, os.Stdout, os.Stderr) {
				os.Exit(1)
			}
			return
		}
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
		// Check if this directory is the parent agents/ directory containing sub-agent directories
		entries, err := os.ReadDir(dirPath)
		if err == nil {
			var subAgents []string
			for _, e := range entries {
				if e.IsDir() {
					subPath := filepath.Join(dirPath, e.Name())
					if fileExists(filepath.Join(subPath, "config.yaml")) || fileExists(filepath.Join(subPath, "workflow.yaml")) {
						subAgents = append(subAgents, subPath)
					}
				}
			}
			if len(subAgents) > 0 {
				for _, subPath := range subAgents {
					validateDir(subPath)
				}
				return
			}
		}

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
		defn, err := workflowspec.ParseDefinition(data)
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
	var cfg agentspec.AgentConfig
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
	defn, err := workflowspec.ParseDefinition(data)
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

	var cfg agentspec.AgentConfig
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
	defn, err := workflowspec.LoadDefinition(workflowPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Workflow definition validation failed for %q: %v\n", workflowPath, err)
		os.Exit(1)
	}
	fmt.Printf("Workflow definition is valid (File: %q, Name: %q, Nodes: %d)\n", filepath.Base(workflowPath), defn.Name, len(defn.Nodes))
	checkAgentIDReferences(defn, filepath.Dir(workflowPath))
}

func checkAgentIDReferences(defn *workflowspec.WorkflowDefinition, baseDir string) {
	agentsDir := findAgentsDir(baseDir)
	if agentsDir == "" {
		if len(defn.ModelPairings) > 0 {
			fmt.Fprintln(os.Stderr, "  Warning: model pairing coverage was NOT verified (agents directory not found)")
		}
		return
	}

	loader := agentspec.NewLoader(agentsDir)
	loadedAgents, err := loader.LoadAll()
	if err != nil {
		fmt.Fprintf(os.Stderr, "  Notice: could not load agents from %q to verify agent_id references: %v\n", agentsDir, err)
		if len(defn.ModelPairings) > 0 {
			fmt.Fprintln(os.Stderr, "  Warning: model pairing coverage was NOT verified (agents pool failed to load)")
		}
		return
	}

	infos := agentspec.AgentsToWorkflowAgentInfos(loadedAgents)
	if err := defn.ValidateWithAgents(infos); err != nil {
		fmt.Fprintf(os.Stderr, "  Workflow validation against agents failed: %v\n", err)
		os.Exit(1)
	}

	for _, warning := range defn.ModelPairingWarnings() {
		fmt.Fprintf(os.Stderr, "  Warning: %s\n", warning)
	}
	if len(defn.ModelPairings) > 0 {
		fmt.Printf("  Checked %d model pairing group(s) against agents pool [OK]\n", len(defn.ModelPairings))
	}
	fmt.Printf("  Checked %d agent_id references against agents pool (%s) [OK]\n", countAgentNodes(defn), agentsDir)
}

func countAgentNodes(defn *workflowspec.WorkflowDefinition) int {
	cnt := 0
	for _, n := range defn.Nodes {
		if n.Type == workflowspec.NodeTypeAgent {
			cnt++
		}
	}
	return cnt
}

func findAgentsDir(baseDir string) string {
	if *agentsDirFlag != "" {
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

	// Check the fixed Asgard agents root
	p := paths.AgentsRoot()
	if fileExists(filepath.Join(p, "agents")) && fileExists(filepath.Join(p, "teams.yaml")) {
		return p
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

// isTreeRoot reports whether dir is shaped like the etcd tree: agents/teams.yaml,
// a runners/ directory, or an agents/ directory of agent subdirectories with no
// legacy teams.yaml beside it.
func isTreeRoot(dir string) bool {
	if fileExists(filepath.Join(dir, "agents", "teams.yaml")) {
		return true
	}
	if info, err := os.Stat(filepath.Join(dir, "runners")); err == nil && info.IsDir() {
		return true
	}
	if fileExists(filepath.Join(dir, "teams.yaml")) {
		return false
	}
	entries, err := os.ReadDir(filepath.Join(dir, "agents"))
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() && fileExists(filepath.Join(dir, "agents", e.Name(), "config.yaml")) {
			return true
		}
	}
	return false
}

// readTreeFiles loads agents/ and runners/ of an etcd-tree-shaped directory
// into a slash-keyed file map, using the same on-disk to key convention as
// publishing (cfgfs: ignore rules, ".sops.json" suffix stripped) and decrypting
// SOPS envelopes. A file that cannot be decrypted is an error.
func readTreeFiles(root string) (map[string][]byte, error) {
	files := make(map[string][]byte)
	err := cfgfs.Walk(root, func(rel, abs string) error {
		key := cfgfs.LogicalKey(filepath.ToSlash(rel))
		if !strings.HasPrefix(key, "agents/") && !strings.HasPrefix(key, "runners/") {
			return nil
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			return err
		}
		if !*noDecryptFlag {
			data, err = decryptFile(rel, data)
			if err != nil {
				return fmt.Errorf("%s: %w (use --no-decrypt to validate without decrypting)", rel, err)
			}
		}
		files[key] = data
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

// validateTree validates an etcd-tree-shaped directory: every global and
// runner agent, name collisions between scopes, and each agent's workflow
// against the agents visible in its scope.
func validateTree(root string, out, errOut io.Writer) bool {
	files, err := readTreeFiles(root)
	if err != nil {
		say(errOut, "Error reading agent tree %q: %v\n", root, err)
		return false
	}

	if len(files) == 0 {
		say(errOut, "Error: %q contains neither agents/ nor runners/ files\n", root)
		return false
	}

	set, err := agentspec.ParseFiles(files)
	if err != nil {
		say(errOut, "Agent tree validation failed: %v\n", err)
		return false
	}

	failed := false
	for _, cerr := range set.Collisions() {
		say(errOut, "Error: %v\n", cerr)
		failed = true
	}

	for _, rerr := range set.RunnerErrors() {
		say(errOut, "Error: %v\n", rerr)
		failed = true
	}

	say(out, "Global agents: %d\n", len(set.Global))
	if validateScopeWorkflows(out, errOut, set, "", set.Global) {
		failed = true
	}
	for node, own := range set.Runners {
		say(out, "Runner %q agents: %d\n", node, len(own))
		if validateScopeWorkflows(out, errOut, set, node, own) {
			failed = true
		}
	}

	if failed {
		return false
	}
	sayln(out, "Agent tree is valid")
	return true
}

// validateScopeWorkflows validates the workflows of the given agents against
// the effective agent list of their scope (global, or global plus the runner).
// A scope with a name collision has no effective list and is skipped (the
// collision is already reported). It returns true when any check failed.
func validateScopeWorkflows(out, errOut io.Writer, set *agentspec.Set, node string, defs map[string]*agentspec.Definition) bool {
	effective, err := set.Effective(node)
	if err != nil {
		return false
	}
	var agents []*agentspec.Agent
	for _, d := range effective {
		agents = append(agents, &agentspec.Agent{Config: d.Config})
	}
	infos := agentspec.AgentsToWorkflowAgentInfos(agents)

	failed := false
	for _, id := range sortedIDs(defs) {
		d := defs[id]
		say(out, "  Agent config is valid (ID: %q, Name: %q, Type: %q)\n", d.Config.ID, d.Config.Name, d.Config.Type)
		if d.Config.Type != "workflow" {
			continue
		}
		defn, err := workflowspec.ParseDefinition(d.Files["workflow.yaml"])
		if err == nil {
			err = defn.ValidateWithAgents(infos)
		}
		if err == nil {
			err = defn.ValidateScheduleRunner(node)
		}
		if err != nil {
			say(errOut, "  Workflow validation failed for agent %q: %v\n", id, err)
			failed = true
			continue
		}
		for _, warning := range defn.ModelPairingWarnings() {
			say(errOut, "  Warning: %s\n", warning)
		}
		say(out, "  Workflow definition is valid (Agent: %q, Nodes: %d)\n", id, len(defn.Nodes))
	}
	return failed
}

func sortedIDs(m map[string]*agentspec.Definition) []string {
	ids := make([]string, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func say(w io.Writer, format string, args ...any) { _, _ = fmt.Fprintf(w, format, args...) }

func sayln(w io.Writer, args ...any) { _, _ = fmt.Fprintln(w, args...) }

// decryptFile decrypts a SOPS envelope and passes plaintext through. For YAML
// files a failure reports the YAML store's error: configlib tries yaml then
// json and returns only the last (json) error, which masks real causes such as
// a missing KMS key.
func decryptFile(name string, raw []byte) ([]byte, error) {
	out, err := configlib.DecryptIfNeeded(raw)
	if err != nil && (strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml")) {
		if _, yerr := decrypt.Data(raw, "yaml"); yerr != nil {
			return nil, fmt.Errorf("sops decryption failed: %w", yerr)
		}
	}
	return out, err
}
