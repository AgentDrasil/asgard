package main

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// ANSI color codes for terminal highlights
const (
	colorReset  = "\033[0m"
	colorRed    = "\033[31m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorBlue   = "\033[34m"
	colorCyan   = "\033[36m"
	colorBold   = "\033[1m"
)

var supportedProviders = map[string]bool{
	"agy":      true,
	"claude":   true,
	"opencode": true,
	"simplest": true,
}

// checkCfgctl verifies that cfgctl is present in PATH.
var checkCfgctl = func() error {
	if os.Getenv("ASGARD_INIT_SKIP_CFGCTL_CHECK") == "1" {
		return nil
	}
	_, err := exec.LookPath("cfgctl")
	if err != nil {
		return fmt.Errorf("cfgctl is not installed or not in your PATH.\nPlease install cfgctl (e.g. `go install github.com/elmhuangyu/configs/cmd/cfgctl@latest` or ensure cfgctl is in PATH) before running asgard-init")
	}
	return nil
}

var promptUser = prompt

func main() {
	if err := run(os.Args[1:]); err != nil {
		if errors.Is(err, ErrPromptCancelled) {
			os.Exit(130)
		}
		fmt.Fprintf(os.Stderr, "%sError:%s %v\n", colorRed+colorBold, colorReset, err)
		os.Exit(1)
	}
}

func run(args []string) (err error) {
	var (
		targetDir string
		state     *InitState
	)
	defer func() {
		if err != nil && errors.Is(err, ErrPromptCancelled) {
			if state != nil {
				_ = state.Save(targetDir)
				savedPath := filepath.Join(targetDir, stateFileName)
				if cwd, cwdErr := os.Getwd(); cwdErr == nil && cwd != "" {
					savedPath = filepath.Join(cwd, stateFileName)
				}
				fmt.Printf("\n%sOperation cancelled.%s\n", colorYellow, colorReset)
				fmt.Printf("Current setup saved to: %s%s%s\n", colorCyan, savedPath, colorReset)
				fmt.Printf("Resume anytime with: %sasgard-init --resume%s\n\n", colorCyan+colorBold, colorReset)
			} else {
				fmt.Printf("\n%sOperation cancelled.%s\n", colorYellow, colorReset)
			}
		}
	}()

	fs := flag.NewFlagSet("asgard-init", flag.ContinueOnError)

	var (
		dirFlag              = fs.String("dir", ".", "Target deployment directory")
		forceFlag            = fs.Bool("force", false, "Overwrite existing deployment files if directory is non-empty")
		nonInteractiveFlag   = fs.Bool("non-interactive", false, "Disable interactive prompts and use defaults/flags")
		hostFlag             = fs.String("host", "http://localhost:7000", "Public base URL of Asgard backend")
		portFlag             = fs.Int("port", 7000, "Backend HTTP port")
		etcdEndpointsFlag    = fs.String("etcd-endpoints", "http://etcd:2379", "Etcd cluster endpoints")
		etcdPrefixFlag       = fs.String("etcd-prefix", "/asgard/configs", "Etcd prefix for configurations")
		authIssuerFlag       = fs.String("auth-issuer", "https://auth.example.com/oauth2", "OIDC Provider Issuer URL")
		authClientIDFlag     = fs.String("auth-client-id", "asgard", "OIDC Client ID")
		authClientSecretFlag = fs.String("auth-client-secret", "change-me-secret", "OIDC Client Secret")
		runnerFlag           = fs.String("runner", "runner-1", "Initial Runner Node ID")
		mountFlag            = fs.String("mount", "", "Workspace mount in format <host_path>:<container_path> (e.g. /home/user/src:/data/src)")
		gcpKmsFlag           = fs.String("gcp-kms", "", "Google Cloud KMS key resource ID for SOPS")
		ageFlag              = fs.String("age", "", "Age recipient public key for SOPS")
		providersFlag        = fs.String("providers", "", "Comma-separated list of CLI providers to activate (supported: agy, claude, opencode, simplest; default: all)")
		addNewRunnerFlag     = fs.String("add-new-runner", "", "Add a new runner to an existing deployment")
		gcpSAKeyFlag         = fs.String("gcp-sa-key", "", "GCP Service Account JSON key file path for GCP KMS")
		resumeFlag           = fs.Bool("resume", false, "Resume deployment setup from saved state")
	)

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	explicitFlags := make(map[string]bool)
	fs.Visit(func(f *flag.Flag) {
		explicitFlags[f.Name] = true
	})

	// Mandatory check: cfgctl must be installed
	if err := checkCfgctl(); err != nil {
		return err
	}

	targetDir, err = filepath.Abs(*dirFlag)
	if err != nil {
		return fmt.Errorf("resolve target directory: %w", err)
	}

	// Mode: --add-new-runner
	if *addNewRunnerFlag != "" {
		runnerName := *addNewRunnerFlag
		var mounts []string
		if *mountFlag != "" {
			mounts = append(mounts, *mountFlag)
		}
		if err := AddRunner(targetDir, runnerName, mounts); err != nil {
			return err
		}
		printAddRunnerSuccess(targetDir, runnerName)
		return nil
	}

	// Normal initialization mode
	var cliProviders []string
	if *providersFlag != "" {
		seen := make(map[string]bool)
		for _, p := range strings.Split(*providersFlag, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				if !supportedProviders[p] {
					return fmt.Errorf("invalid provider %q: supported providers are agy, claude, opencode, simplest", p)
				}
				if !seen[p] {
					seen[p] = true
					cliProviders = append(cliProviders, p)
				}
			}
		}
		if len(cliProviders) == 0 {
			return fmt.Errorf("no valid providers specified in --providers flag")
		}
	}

	if *gcpSAKeyFlag != "" {
		if err := validateJSONFile(*gcpSAKeyFlag); err != nil {
			return fmt.Errorf("invalid --gcp-sa-key: %w", err)
		}
	}

	data := &ConfigData{
		Host:             *hostFlag,
		BackendPort:      *portFlag,
		RegistryPort:     9311,
		EtcdEndpoints:    *etcdEndpointsFlag,
		EtcdPrefix:       *etcdPrefixFlag,
		AuthIssuer:       *authIssuerFlag,
		AuthClientID:     *authClientIDFlag,
		AuthClientSecret: *authClientSecretFlag,
		AuthTokenSource:  "access_token",
		InitialRunner:    *runnerFlag,
		GCPKMS:           *gcpKmsFlag,
		GCPSAKey:         *gcpSAKeyFlag,
		AgeRecipient:     *ageFlag,
		Force:            *forceFlag,
	}

	if *mountFlag != "" {
		data.WorkspaceMounts = append(data.WorkspaceMounts, *mountFlag)
	}

	// State persistence & resume check
	statePath, loadedState, loadErr := loadState(targetDir)

	if *resumeFlag {
		if loadErr != nil || loadedState == nil {
			return fmt.Errorf("no saved state file found to resume (checked %s and cwd)", filepath.Join(targetDir, stateFileName))
		}
		state = loadedState
		fmt.Printf("%s✔ Resuming from saved state:%s %s\n", colorGreen+colorBold, colorReset, statePath)
	} else if !*nonInteractiveFlag && loadErr == nil && loadedState != nil {
		if defaultStdinReader == nil {
			defaultStdinReader = bufio.NewReader(os.Stdin)
		}
		resumeAns, err := promptUser(defaultStdinReader, "Saved deployment state detected. Resume previous session? [Y/n]", "Y")
		if err != nil {
			return err
		}
		resumeAns = strings.ToLower(strings.TrimSpace(resumeAns))
		if resumeAns == "" || resumeAns == "y" || resumeAns == "yes" {
			state = loadedState
			fmt.Printf("%s✔ Resuming from saved state:%s %s\n", colorGreen+colorBold, colorReset, statePath)
		} else {
			removeState(targetDir)
			state = newInitState()
		}
	} else {
		state = newInitState()
	}

	// Restore answered fields into data and targetDir, honoring explicit CLI flags
	if !explicitFlags["dir"] && state.TargetDir != "" {
		targetDir = state.TargetDir
	}
	if !explicitFlags["host"] && state.Host != "" {
		data.Host = state.Host
	}
	if !explicitFlags["port"] && state.BackendPort != 0 {
		data.BackendPort = state.BackendPort
	}
	if !explicitFlags["etcd-endpoints"] && state.EtcdEndpoints != "" {
		data.EtcdEndpoints = state.EtcdEndpoints
	}
	if !explicitFlags["etcd-prefix"] && state.EtcdPrefix != "" {
		data.EtcdPrefix = state.EtcdPrefix
	}
	if !explicitFlags["auth-issuer"] && state.AuthIssuer != "" {
		data.AuthIssuer = state.AuthIssuer
	}
	if !explicitFlags["auth-client-id"] && state.AuthClientID != "" {
		data.AuthClientID = state.AuthClientID
	}
	if !explicitFlags["auth-client-secret"] && state.AuthClientSecret != "" {
		data.AuthClientSecret = state.AuthClientSecret
	}
	if !explicitFlags["runner"] && state.InitialRunner != "" {
		data.InitialRunner = state.InitialRunner
	}
	if !explicitFlags["mount"] && len(state.WorkspaceMounts) > 0 {
		data.WorkspaceMounts = state.WorkspaceMounts
	}
	if !explicitFlags["gcp-kms"] && state.GCPKMS != "" {
		data.GCPKMS = state.GCPKMS
	}
	if !explicitFlags["gcp-sa-key"] && state.GCPSAKey != "" {
		data.GCPSAKey = state.GCPSAKey
	}
	if !explicitFlags["age"] && state.AgeRecipient != "" {
		data.AgeRecipient = state.AgeRecipient
	}
	if !explicitFlags["providers"] && len(state.Providers) > 0 {
		data.Providers = state.Providers
	}
	if *gcpSAKeyFlag != "" {
		data.GCPSAKey = *gcpSAKeyFlag
	}

	var reader *bufio.Reader
	// If interactive mode is enabled, run the interactive prompt wizard
	if !*nonInteractiveFlag {
		if defaultStdinReader == nil {
			defaultStdinReader = bufio.NewReader(os.Stdin)
		}
		reader = defaultStdinReader

		// Immediately save initial state (flags/defaults) upon wizard start
		state.TargetDir = targetDir
		state.Host = data.Host
		state.BackendPort = data.BackendPort
		state.EtcdEndpoints = data.EtcdEndpoints
		state.EtcdPrefix = data.EtcdPrefix
		state.AuthIssuer = data.AuthIssuer
		state.AuthClientID = data.AuthClientID
		state.AuthClientSecret = data.AuthClientSecret
		state.InitialRunner = data.InitialRunner
		state.WorkspaceMounts = data.WorkspaceMounts
		state.GCPKMS = data.GCPKMS
		state.GCPSAKey = data.GCPSAKey
		state.AgeRecipient = data.AgeRecipient
		state.Providers = data.Providers
		_ = state.Save(targetDir)

		if !state.IsCompleted("target_dir") {
			printWelcomeBanner()
		}

		if !state.IsCompleted("target_dir") {
			oldTargetDir := targetDir
			targetDirInput, err := promptUser(reader, "Deployment target directory", targetDir)
			if err != nil {
				return err
			}
			newTargetDir, err := filepath.Abs(targetDirInput)
			if err != nil {
				return fmt.Errorf("resolve target directory: %w", err)
			}
			if oldTargetDir != "" && oldTargetDir != newTargetDir {
				_ = os.Remove(filepath.Join(oldTargetDir, stateFileName))
			}
			targetDir = newTargetDir
			state.TargetDir = targetDir
			state.MarkCompleted("target_dir")
			_ = state.Save(targetDir)
		} else {
			targetDir = state.TargetDir
		}

		if !state.IsCompleted("host") {
			data.Host, err = promptUser(reader, "Asgard public base URL (host)", data.Host)
			if err != nil {
				return err
			}
			state.Host = data.Host
			state.MarkCompleted("host")
			_ = state.Save(targetDir)
		}

		if !state.IsCompleted("port") {
			portStr, err := promptUser(reader, "Backend HTTP port", strconv.Itoa(data.BackendPort))
			if err != nil {
				return err
			}
			if p, err := strconv.Atoi(portStr); err == nil && p > 0 {
				data.BackendPort = p
			}
			state.BackendPort = data.BackendPort
			state.MarkCompleted("port")
			_ = state.Save(targetDir)
		}

		if !state.IsCompleted("etcd_endpoints") {
			data.EtcdEndpoints, err = promptUser(reader, "Etcd endpoints", data.EtcdEndpoints)
			if err != nil {
				return err
			}
			state.EtcdEndpoints = data.EtcdEndpoints
			state.MarkCompleted("etcd_endpoints")
			_ = state.Save(targetDir)
		}

		if !state.IsCompleted("etcd_prefix") {
			data.EtcdPrefix, err = promptUser(reader, "Etcd prefix", data.EtcdPrefix)
			if err != nil {
				return err
			}
			state.EtcdPrefix = data.EtcdPrefix
			state.MarkCompleted("etcd_prefix")
			_ = state.Save(targetDir)
		}

		if !state.IsCompleted("auth_issuer") || !state.IsCompleted("auth_client_id") || !state.IsCompleted("auth_client_secret") {
			fmt.Printf("\n%s--- Authentication (OIDC) Settings (Required) ---%s\n", colorCyan+colorBold, colorReset)
		}

		if !state.IsCompleted("auth_issuer") {
			data.AuthIssuer, err = promptUser(reader, "OIDC Issuer URL", data.AuthIssuer)
			if err != nil {
				return err
			}
			state.AuthIssuer = data.AuthIssuer
			state.MarkCompleted("auth_issuer")
			_ = state.Save(targetDir)
		}

		if !state.IsCompleted("auth_client_id") {
			data.AuthClientID, err = promptUser(reader, "OIDC Client ID", data.AuthClientID)
			if err != nil {
				return err
			}
			state.AuthClientID = data.AuthClientID
			state.MarkCompleted("auth_client_id")
			_ = state.Save(targetDir)
		}

		if !state.IsCompleted("auth_client_secret") {
			data.AuthClientSecret, err = promptUser(reader, "OIDC Client Secret", data.AuthClientSecret, WithSecret(true))
			if err != nil {
				return err
			}
			state.AuthClientSecret = data.AuthClientSecret
			state.MarkCompleted("auth_client_secret")
			_ = state.Save(targetDir)
		}

		if !state.IsCompleted("initial_runner") || !state.IsCompleted("workspace_mount") {
			fmt.Printf("\n%s--- Runner Node Settings ---%s\n", colorCyan+colorBold, colorReset)
		}

		if !state.IsCompleted("initial_runner") {
			data.InitialRunner, err = promptUser(reader, "Initial runner node ID", data.InitialRunner)
			if err != nil {
				return err
			}
			state.InitialRunner = data.InitialRunner
			state.MarkCompleted("initial_runner")
			_ = state.Save(targetDir)
		}

		if !state.IsCompleted("workspace_mount") {
			defaultMount := ""
			if len(data.WorkspaceMounts) > 0 {
				defaultMount = strings.Join(data.WorkspaceMounts, ",")
			}
			workspaceMount, err := promptUser(reader, "Host workspace directory to mount into runner (optional, e.g. /home/user/src:/data/src)", defaultMount)
			if err != nil {
				return err
			}
			if workspaceMount != "" {
				var mounts []string
				for _, m := range strings.Split(workspaceMount, ",") {
					if trimmed := strings.TrimSpace(m); trimmed != "" {
						mounts = append(mounts, trimmed)
					}
				}
				data.WorkspaceMounts = mounts
			} else {
				data.WorkspaceMounts = nil
			}
			state.WorkspaceMounts = data.WorkspaceMounts
			state.MarkCompleted("workspace_mount")
			_ = state.Save(targetDir)
		}

		if !state.IsCompleted("sops") {
			fmt.Printf("\n%s--- SOPS Encryption Settings (Optional) ---%s\n", colorCyan+colorBold, colorReset)
			data.GCPKMS, err = promptUser(reader, "GCP KMS key resource ID (leave empty if not using GCP KMS)", data.GCPKMS)
			if err != nil {
				return err
			}
			state.GCPKMS = data.GCPKMS

			if data.GCPKMS == "" {
				data.AgeRecipient, err = promptUser(reader, "Age recipient public key (leave empty to configure manually later)", data.AgeRecipient)
				if err != nil {
					return err
				}
				state.AgeRecipient = data.AgeRecipient
			}
			state.MarkCompleted("sops")
			_ = state.Save(targetDir)
		}

		if data.GCPKMS != "" && !state.IsCompleted("gcp_sa_key") {
			credDir := filepath.Join(targetDir, "etcd-tree", "credentials")
			if err := os.MkdirAll(credDir, 0755); err != nil {
				return fmt.Errorf("create credentials directory %s: %w", credDir, err)
			}
			if *gcpSAKeyFlag != "" {
				if err := copyGCPSAKey(targetDir, *gcpSAKeyFlag); err != nil {
					return err
				}
			}
			if PromptGCPSACredentials(reader, os.Stdout, targetDir, true) {
				data.GCPSAKey = filepath.Join(targetDir, "etcd-tree", "credentials", "gcp-sa.json")
				state.GCPSAKey = data.GCPSAKey
			} else {
				data.GCPSAKey = ""
				state.GCPSAKey = ""
			}
			state.MarkCompleted("gcp_sa_key")
			_ = state.Save(targetDir)
		}
	}

	// Warn if auth client secret is still the placeholder
	if data.AuthClientSecret == "change-me-secret" {
		fmt.Printf("\n%sWarning:%s auth.client_secret is set to placeholder %q. Remember to update it with your real OIDC provider credentials.\n", colorYellow+colorBold, colorReset, data.AuthClientSecret)
	}

	// In interactive mode, ensure directories exist and prompt for providers and SSH
	if !*nonInteractiveFlag {
		dirs := []string{
			filepath.Join(targetDir, "etcd-tree", "credentials", "claude"),
			filepath.Join(targetDir, "etcd-tree", "credentials", "ssh"),
			filepath.Join(targetDir, "etcd-tree", "aw"),
			filepath.Join(targetDir, "etcd-tree", "simplest"),
		}
		for _, d := range dirs {
			_ = os.MkdirAll(d, 0755)
		}

		if !state.IsCompleted("providers") {
			if len(cliProviders) > 0 {
				data.Providers = cliProviders
			} else {
				data.Providers = PromptProviderSetup(reader, os.Stdout, targetDir, true)
			}
			state.Providers = data.Providers
			state.MarkCompleted("providers")
			_ = state.Save(targetDir)
		}
		if !state.IsCompleted("ssh") {
			PromptSSHCredentials(reader, os.Stdout, targetDir, true)
			state.MarkCompleted("ssh")
			_ = state.Save(targetDir)
		}
	} else {
		if len(cliProviders) > 0 {
			data.Providers = cliProviders
		} else if len(data.Providers) == 0 {
			data.Providers = []string{"agy", "claude", "opencode", "simplest"}
		}
	}

	if len(data.Providers) == 0 {
		data.Providers = []string{"agy"}
	}

	// Validate target directory and generate deployment
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return fmt.Errorf("create directory %s: %w", targetDir, err)
	}

	if *nonInteractiveFlag && *gcpSAKeyFlag != "" {
		if err := copyGCPSAKey(targetDir, *gcpSAKeyFlag); err != nil {
			return err
		}
	}

	if err := GenerateDeployment(targetDir, data); err != nil {
		return err
	}

	// Clean up temporary init state on successful generation
	removeState(targetDir)

	if *nonInteractiveFlag {
		PromptCLICredentials(reader, os.Stdout, targetDir, false)
		PromptClaudeCredentials(reader, os.Stdout, targetDir, false)
		PromptOpencodeConfig(reader, os.Stdout, targetDir, false)
		PromptSimplestConfig(reader, os.Stdout, targetDir, false)
		PromptSSHCredentials(reader, os.Stdout, targetDir, false)
		PromptGCPSACredentials(reader, os.Stdout, targetDir, false)
	}

	printInitSuccess(targetDir, data)
	return nil
}

func isNonEmptyFile(path string) bool {
	fi, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !fi.IsDir() && fi.Size() > 0
}

// CheckCLICredentials checks if oauth_creds.json or antigravity-oauth-token exists in targetDir/etcd-tree/credentials and is a non-empty file.
func CheckCLICredentials(targetDir string) (string, bool) {
	oauthPath := filepath.Join(targetDir, "etcd-tree", "credentials", "oauth_creds.json")
	if isNonEmptyFile(oauthPath) {
		return oauthPath, true
	}
	tokenPath := filepath.Join(targetDir, "etcd-tree", "credentials", "antigravity-oauth-token")
	if isNonEmptyFile(tokenPath) {
		return tokenPath, true
	}
	return "", false
}

// CheckClaudeCredentials checks if claude/.credentials.json or .credentials.json exists in targetDir/etcd-tree/credentials and is a non-empty file.
func CheckClaudeCredentials(targetDir string) (string, bool) {
	claudePath := filepath.Join(targetDir, "etcd-tree", "credentials", "claude", ".credentials.json")
	if isNonEmptyFile(claudePath) {
		return claudePath, true
	}
	rootPath := filepath.Join(targetDir, "etcd-tree", "credentials", ".credentials.json")
	if isNonEmptyFile(rootPath) {
		return rootPath, true
	}
	return "", false
}

// CheckGCPSACredentials checks whether filepath.Join(targetDir, "etcd-tree", "credentials", "gcp-sa.json")
// exists, is non-empty, and passes validateJSONFile (valid JSON with project_id).
// If valid, tightens permissions to 0600 and returns (path, true).
func CheckGCPSACredentials(targetDir string) (string, bool) {
	saPath := filepath.Join(targetDir, "etcd-tree", "credentials", "gcp-sa.json")
	if !isNonEmptyFile(saPath) {
		return "", false
	}
	if err := validateJSONFile(saPath); err != nil {
		return "", false
	}
	_ = os.Chmod(saPath, 0600)
	return saPath, true
}

// CheckOpencodeConfig checks if opencode.aw.yaml exists in targetDir/etcd-tree/aw and contains non-comment content.
func CheckOpencodeConfig(targetDir string) (string, bool) {
	path := filepath.Join(targetDir, "etcd-tree", "aw", "opencode.aw.yaml")
	data, err := os.ReadFile(path)
	if err != nil || len(bytes.TrimSpace(data)) == 0 {
		return "", false
	}
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			_ = os.Chmod(path, 0600)
			return path, true
		}
	}
	return "", false
}

// CheckSimplestConfig checks if key.yaml exists in targetDir/etcd-tree/simplest and contains non-comment content.
func CheckSimplestConfig(targetDir string) (string, bool) {
	keyPath := filepath.Join(targetDir, "etcd-tree", "simplest", "key.yaml")
	data, err := os.ReadFile(keyPath)
	if err != nil || len(bytes.TrimSpace(data)) == 0 {
		return "", false
	}
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			_ = os.Chmod(keyPath, 0600)
			return keyPath, true
		}
	}
	return "", false
}

// PromptCLIActivation asks the user whether to activate a CLI provider even if credentials were skipped.
func PromptCLIActivation(r io.Reader, w io.Writer, cliName string) bool {
	var reader *bufio.Reader
	if br, ok := r.(*bufio.Reader); ok {
		reader = br
	} else if r != nil {
		reader = bufio.NewReader(r)
	} else {
		reader = bufio.NewReader(os.Stdin)
	}

	_, _ = fmt.Fprintf(w, "%sActivate %s anyway? [y/N]:%s ", colorBold, cliName, colorReset)
	line, err := reader.ReadString('\n')
	if err != nil {
		return false
	}
	ans := strings.ToLower(strings.TrimSpace(line))
	return ans == "y" || ans == "yes"
}

// PromptClaudeCredentials prompts the user to place their Claude credentials into etcd-tree/credentials/claude/.credentials.json.
func PromptClaudeCredentials(r io.Reader, w io.Writer, targetDir string, interactive bool) bool {
	credPath := filepath.Join(targetDir, "etcd-tree", "credentials", "claude", ".credentials.json")
	if !interactive {
		if foundPath, ok := CheckClaudeCredentials(targetDir); ok {
			_, _ = fmt.Fprintf(w, "\n%s✔ Claude credential verified:%s %s\n", colorGreen+colorBold, colorReset, foundPath)
			return true
		}
		return false
	}

	var reader *bufio.Reader
	if br, ok := r.(*bufio.Reader); ok {
		reader = br
	} else if r != nil {
		reader = bufio.NewReader(r)
	} else {
		reader = bufio.NewReader(os.Stdin)
	}
	_, _ = fmt.Fprintf(w, "\n%s--- Claude CLI Credential Setup ---%s\n", colorCyan+colorBold, colorReset)
	_, _ = fmt.Fprintf(w, "Please place your Claude credential file into %s%s%s (or etcd-tree/credentials/.credentials.json).\n", colorYellow, credPath, colorReset)

	for {
		_, _ = fmt.Fprintf(w, "%sPress 'y' when ready to verify (or 's' to skip for now):%s ", colorBold, colorReset)
		line, err := reader.ReadString('\n')
		if err != nil {
			return false
		}
		ans := strings.ToLower(strings.TrimSpace(line))
		switch ans {
		case "y", "yes":
			if foundPath, ok := CheckClaudeCredentials(targetDir); ok {
				_, _ = fmt.Fprintf(w, "%s✔ Claude credential verified successfully:%s %s\n", colorGreen+colorBold, colorReset, foundPath)
				return true
			}
			_, _ = fmt.Fprintf(w, "%sError:%s No valid, non-empty Claude credential file found at %s (or etcd-tree/credentials/.credentials.json).\n", colorRed+colorBold, colorReset, credPath)
		case "s", "skip":
			_, _ = fmt.Fprintf(w, "%sSkipped Claude credential setup.%s You can place it into etcd-tree/credentials/claude/ later before running just publish.\n", colorYellow, colorReset)
			return false
		default:
			_, _ = fmt.Fprintf(w, "Invalid input %q. Enter 'y' to verify or 's' to skip.\n", ans)
		}
	}
}

// PromptGCPSACredentials prompts the user to place their GCP Service Account JSON key file into etcd-tree/credentials/gcp-sa.json.
func PromptGCPSACredentials(r io.Reader, w io.Writer, targetDir string, interactive bool) bool {
	credPath := filepath.Join(targetDir, "etcd-tree", "credentials", "gcp-sa.json")
	if !interactive {
		if foundPath, ok := CheckGCPSACredentials(targetDir); ok {
			_, _ = fmt.Fprintf(w, "\n%s✔ GCP Service Account key verified:%s %s\n", colorGreen+colorBold, colorReset, foundPath)
			return true
		}
		return false
	}

	var reader *bufio.Reader
	if br, ok := r.(*bufio.Reader); ok {
		reader = br
	} else if r != nil {
		reader = bufio.NewReader(r)
	} else {
		reader = bufio.NewReader(os.Stdin)
	}

	_, _ = fmt.Fprintf(w, "\n%s--- GCP Service Account Key Setup (for GCP KMS) ---%s\n", colorCyan+colorBold, colorReset)
	_, _ = fmt.Fprintf(w, "Please place your GCP Service Account JSON key file into %s%s%s.\n", colorYellow, credPath, colorReset)

	for {
		_, _ = fmt.Fprintf(w, "%sPress 'y' when ready to verify (or 's' to skip for now):%s ", colorBold, colorReset)
		line, err := reader.ReadString('\n')
		if err != nil {
			return false
		}
		ans := strings.ToLower(strings.TrimSpace(line))
		switch ans {
		case "y", "yes":
			if foundPath, ok := CheckGCPSACredentials(targetDir); ok {
				_, _ = fmt.Fprintf(w, "%s✔ GCP Service Account key verified successfully:%s %s\n", colorGreen+colorBold, colorReset, foundPath)
				return true
			}
			_, _ = fmt.Fprintf(w, "%sError:%s No valid GCP Service Account JSON key file found at %s (must be valid JSON with project_id).\n", colorRed+colorBold, colorReset, credPath)
		case "s", "skip":
			_, _ = fmt.Fprintf(w, "%sSkipped GCP Service Account key setup.%s You can place it into etcd-tree/credentials/gcp-sa.json later before running just publish.\n", colorYellow, colorReset)
			return false
		default:
			_, _ = fmt.Fprintf(w, "Invalid input %q. Enter 'y' to verify or 's' to skip.\n", ans)
		}
	}
}

// PromptOpencodeConfig prompts the user to configure their OpenCode configuration in etcd-tree/aw/opencode.aw.yaml.
func PromptOpencodeConfig(r io.Reader, w io.Writer, targetDir string, interactive bool) bool {
	cfgPath := filepath.Join(targetDir, "etcd-tree", "aw", "opencode.aw.yaml")
	if !interactive {
		if foundPath, ok := CheckOpencodeConfig(targetDir); ok {
			_, _ = fmt.Fprintf(w, "\n%s✔ OpenCode configuration verified:%s %s\n", colorGreen+colorBold, colorReset, foundPath)
			return true
		}
		return false
	}

	var reader *bufio.Reader
	if br, ok := r.(*bufio.Reader); ok {
		reader = br
	} else if r != nil {
		reader = bufio.NewReader(r)
	} else {
		reader = bufio.NewReader(os.Stdin)
	}
	_, _ = fmt.Fprintf(w, "\n%s--- OpenCode Configuration Setup ---%s\n", colorCyan+colorBold, colorReset)
	_, _ = fmt.Fprintf(w, "Please configure your OpenCode environment keys in %s%s%s.\n", colorYellow, cfgPath, colorReset)

	for {
		_, _ = fmt.Fprintf(w, "%sPress 'y' when ready to verify (or 's' to skip for now):%s ", colorBold, colorReset)
		line, err := reader.ReadString('\n')
		if err != nil {
			return false
		}
		ans := strings.ToLower(strings.TrimSpace(line))
		switch ans {
		case "y", "yes":
			if foundPath, ok := CheckOpencodeConfig(targetDir); ok {
				_, _ = fmt.Fprintf(w, "%s✔ OpenCode configuration verified successfully:%s %s\n", colorGreen+colorBold, colorReset, foundPath)
				return true
			}
			_, _ = fmt.Fprintf(w, "%sError:%s No valid, uncommented configuration found at %s.\n", colorRed+colorBold, colorReset, cfgPath)
		case "s", "skip":
			_, _ = fmt.Fprintf(w, "%sSkipped OpenCode configuration setup.%s You can configure it in etcd-tree/aw/opencode.aw.yaml later before running just publish.\n", colorYellow, colorReset)
			return false
		default:
			_, _ = fmt.Fprintf(w, "Invalid input %q. Enter 'y' to verify or 's' to skip.\n", ans)
		}
	}
}

// PromptSimplestConfig prompts the user to configure API keys in etcd-tree/simplest/key.yaml.
func PromptSimplestConfig(r io.Reader, w io.Writer, targetDir string, interactive bool) bool {
	cfgPath := filepath.Join(targetDir, "etcd-tree", "simplest", "key.yaml")
	if !interactive {
		if foundPath, ok := CheckSimplestConfig(targetDir); ok {
			_, _ = fmt.Fprintf(w, "\n%s✔ Simplest configuration verified:%s %s\n", colorGreen+colorBold, colorReset, foundPath)
			return true
		}
		return false
	}

	var reader *bufio.Reader
	if br, ok := r.(*bufio.Reader); ok {
		reader = br
	} else if r != nil {
		reader = bufio.NewReader(r)
	} else {
		reader = bufio.NewReader(os.Stdin)
	}
	_, _ = fmt.Fprintf(w, "\n%s--- Simplest Provider Configuration Setup ---%s\n", colorCyan+colorBold, colorReset)
	_, _ = fmt.Fprintf(w, "Please configure provider API keys in %s%s%s.\n", colorYellow, cfgPath, colorReset)

	for {
		_, _ = fmt.Fprintf(w, "%sPress 'y' when ready to verify (or 's' to skip for now):%s ", colorBold, colorReset)
		line, err := reader.ReadString('\n')
		if err != nil {
			return false
		}
		ans := strings.ToLower(strings.TrimSpace(line))
		switch ans {
		case "y", "yes":
			if foundPath, ok := CheckSimplestConfig(targetDir); ok {
				_, _ = fmt.Fprintf(w, "%s✔ Simplest configuration verified successfully:%s %s\n", colorGreen+colorBold, colorReset, foundPath)
				return true
			}
			_, _ = fmt.Fprintf(w, "%sError:%s No valid, uncommented API keys found at %s.\n", colorRed+colorBold, colorReset, cfgPath)
		case "s", "skip":
			_, _ = fmt.Fprintf(w, "%sSkipped Simplest configuration setup.%s You can configure it in etcd-tree/simplest/key.yaml later before running just publish.\n", colorYellow, colorReset)
			return false
		default:
			_, _ = fmt.Fprintf(w, "Invalid input %q. Enter 'y' to verify or 's' to skip.\n", ans)
		}
	}
}

func promptProviderWithActivation(
	r io.Reader,
	w io.Writer,
	targetDir string,
	interactive bool,
	cliName string,
	promptCredFn func(io.Reader, io.Writer, string, bool) bool,
) bool {
	if promptCredFn(r, w, targetDir, interactive) {
		return true
	}
	if !interactive {
		return false
	}
	return PromptCLIActivation(r, w, cliName)
}

// PromptProviderSetup prompts for CLI credential setup and provider activation for each supported provider.
func PromptProviderSetup(r io.Reader, w io.Writer, targetDir string, interactive bool) []string {
	var reader *bufio.Reader
	if br, ok := r.(*bufio.Reader); ok {
		reader = br
	} else if r != nil {
		reader = bufio.NewReader(r)
	} else {
		reader = bufio.NewReader(os.Stdin)
	}

	var providers []string
	if promptProviderWithActivation(reader, w, targetDir, interactive, "agy", PromptCLICredentials) {
		providers = append(providers, "agy")
	}
	if promptProviderWithActivation(reader, w, targetDir, interactive, "claude", PromptClaudeCredentials) {
		providers = append(providers, "claude")
	}
	if promptProviderWithActivation(reader, w, targetDir, interactive, "opencode", PromptOpencodeConfig) {
		providers = append(providers, "opencode")
	}
	if promptProviderWithActivation(reader, w, targetDir, interactive, "simplest", PromptSimplestConfig) {
		providers = append(providers, "simplest")
	}

	if len(providers) == 0 {
		_, _ = fmt.Fprintf(w, "\n%sWarning:%s At least one provider must be enabled for Asgard to function.\n", colorYellow+colorBold, colorReset)
		if _, ok := CheckSimplestConfig(targetDir); ok {
			providers = append(providers, "simplest")
		} else {
			providers = append(providers, "agy")
		}
	}
	return providers
}

// PromptCLICredentials prompts the user to place their CLI credential into etcd-tree/credentials.
// When interactive is false, it does not block on r and checks if the file exists or skips gracefully.
func PromptCLICredentials(r io.Reader, w io.Writer, targetDir string, interactive bool) bool {
	credPath := filepath.Join(targetDir, "etcd-tree", "credentials", "oauth_creds.json")
	if !interactive {
		if foundPath, ok := CheckCLICredentials(targetDir); ok {
			_, _ = fmt.Fprintf(w, "\n%s✔ CLI credential verified:%s %s\n", colorGreen+colorBold, colorReset, foundPath)
			return true
		}
		return false
	}

	var reader *bufio.Reader
	if br, ok := r.(*bufio.Reader); ok {
		reader = br
	} else if r != nil {
		reader = bufio.NewReader(r)
	} else {
		reader = bufio.NewReader(os.Stdin)
	}
	_, _ = fmt.Fprintf(w, "\n%s--- CLI Credential Setup (e.g. Gemini / Antigravity OAuth) ---%s\n", colorCyan+colorBold, colorReset)
	_, _ = fmt.Fprintf(w, "Please place your CLI credential file into %s%s%s.\n", colorYellow, credPath, colorReset)

	for {
		_, _ = fmt.Fprintf(w, "%sPress 'y' when ready to verify (or 's' to skip for now):%s ", colorBold, colorReset)
		line, err := reader.ReadString('\n')
		if err != nil {
			return false
		}
		ans := strings.ToLower(strings.TrimSpace(line))
		switch ans {
		case "y", "yes":
			if foundPath, ok := CheckCLICredentials(targetDir); ok {
				_, _ = fmt.Fprintf(w, "%s✔ CLI credential verified successfully:%s %s\n", colorGreen+colorBold, colorReset, foundPath)
				return true
			}
			_, _ = fmt.Fprintf(w, "%sError:%s No valid, non-empty credential file found at %s (or antigravity-oauth-token).\n", colorRed+colorBold, colorReset, credPath)
		case "s", "skip":
			_, _ = fmt.Fprintf(w, "%sSkipped CLI credential setup.%s You can place it into etcd-tree/credentials/ later before running just publish.\n", colorYellow, colorReset)
			return false
		default:
			_, _ = fmt.Fprintf(w, "Invalid input %q. Enter 'y' to verify or 's' to skip.\n", ans)
		}
	}
}

// CheckSSHCredentials checks whether valid SSH private keys exist in targetDir/etcd-tree/credentials/ssh/.
// A valid private key MUST be in supportedPrivateKeys, is a regular file, and its content should start with (or contain) "-----BEGIN ".
// If an unsupported file is found (not ending in .pub, not .sops.json, not config, not known_hosts, not .gitkeep),
// a warning is printed to the user that the filename is not in supported list {key, id_ed25519, id_rsa, id_ecdsa, id_dsa} and won't be synced to runner.
// It tightens every valid private key's permissions to 0600, and returns the sorted list of key names.
func CheckSSHCredentials(targetDir string) ([]string, bool) {
	sshDir := filepath.Join(targetDir, "etcd-tree", "credentials", "ssh")
	entries, err := os.ReadDir(sshDir)
	if err != nil {
		return nil, false
	}

	supportedPrivateKeys := map[string]bool{
		"key":        true,
		"id_ed25519": true,
		"id_rsa":     true,
		"id_ecdsa":   true,
		"id_dsa":     true,
	}

	var foundKeys []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasSuffix(name, ".pub") || strings.HasSuffix(name, ".sops.json") {
			continue
		}
		switch name {
		case "config", "known_hosts", ".gitkeep":
			continue
		}

		if !supportedPrivateKeys[name] {
			fmt.Printf("\n%sWarning:%s %s is not in supported list {key, id_ed25519, id_rsa, id_ecdsa, id_dsa} and won't be synced to runner.\n", colorYellow+colorBold, colorReset, name)
			continue
		}

		keyPath := filepath.Join(sshDir, name)
		fi, err := os.Stat(keyPath)
		if err != nil || !fi.Mode().IsRegular() || fi.Size() == 0 {
			continue
		}

		data, err := os.ReadFile(keyPath)
		if err != nil || !strings.Contains(string(data), "-----BEGIN ") {
			continue
		}

		_ = os.Chmod(keyPath, 0600)
		foundKeys = append(foundKeys, name)
	}

	if len(foundKeys) == 0 {
		return nil, false
	}

	sort.Strings(foundKeys)
	return foundKeys, true
}

// PromptSSHCredentials prompts the user to place their SSH private key (and optional *.pub / config) into etcd-tree/credentials/ssh/.
// When interactive is false, it does not block on r and checks if valid keys exist or skips gracefully.
func PromptSSHCredentials(r *bufio.Reader, w io.Writer, targetDir string, interactive bool) bool {
	sshDir := filepath.Join(targetDir, "etcd-tree", "credentials", "ssh")
	if !interactive {
		if keys, ok := CheckSSHCredentials(targetDir); ok {
			_, _ = fmt.Fprintf(w, "\n%s✔ SSH credentials verified:%s %s\n", colorGreen+colorBold, colorReset, strings.Join(keys, ", "))
			return true
		}
		return false
	}

	if r == nil {
		r = bufio.NewReader(os.Stdin)
	}

	_, _ = fmt.Fprintf(w, "\n%s--- SSH Credentials Setup ---%s\n", colorCyan+colorBold, colorReset)
	_, _ = fmt.Fprintf(w, "Please place your SSH private key (and optional *.pub / config) into %s%s/%s (e.g. id_ed25519, id_rsa, key).\n", colorYellow, sshDir, colorReset)

	for {
		_, _ = fmt.Fprintf(w, "%sPress 'y' when ready to verify (or 's' to skip for now):%s ", colorBold, colorReset)
		line, err := r.ReadString('\n')
		if err != nil {
			return false
		}
		ans := strings.ToLower(strings.TrimSpace(line))
		switch ans {
		case "y", "yes":
			if keys, ok := CheckSSHCredentials(targetDir); ok {
				_, _ = fmt.Fprintf(w, "%s✔ SSH credentials verified successfully:%s %s\n", colorGreen+colorBold, colorReset, strings.Join(keys, ", "))
				return true
			}
			_, _ = fmt.Fprintf(w, "%sError:%s No valid, non-empty SSH private key found in %s (e.g. id_ed25519, id_rsa, key).\n", colorRed+colorBold, colorReset, sshDir)
		case "s", "skip":
			_, _ = fmt.Fprintf(w, "%sSkipped SSH credentials setup.%s You can place it into %s/ later before running just publish.\n", colorYellow, colorReset, sshDir)
			return false
		default:
			_, _ = fmt.Fprintf(w, "Invalid input %q. Enter 'y' to verify or 's' to skip.\n", ans)
		}
	}
}

func printWelcomeBanner() {
	fmt.Printf("\n%s%s=================================================================%s\n", colorBlue, colorBold, colorReset)
	fmt.Printf("%s%s               Asgard Deployment Configuration Wizard             %s\n", colorBlue, colorBold, colorReset)
	fmt.Printf("%s%s=================================================================%s\n", colorBlue, colorBold, colorReset)
	fmt.Println("This wizard generates your docker compose setup, mTLS certificates,")
	fmt.Println("Justfile automation, etcd tree structure, and default agents.")
	fmt.Println()
}

func printInitSuccess(dir string, data *ConfigData) {
	fmt.Printf("\n%s%s✔ Asgard deployment configuration generated successfully!%s\n", colorGreen, colorBold, colorReset)
	fmt.Printf("%sLocation:%s %s\n\n", colorBold, colorReset, dir)

	fmt.Printf("%s[Generated Components]%s\n", colorCyan+colorBold, colorReset)
	fmt.Printf("  • %stls/%s                     - Root CA and mTLS keypairs generated and distributed\n", colorYellow, colorReset)
	fmt.Printf("  • %sbackend/certs/%s           - Backend server cert, key & client-ca bundle\n", colorYellow, colorReset)
	fmt.Printf("  • %srunners/%s/certs/%s   - Runner client cert, key & ca bundle (hardcoded /certs/tls.*)\n", colorYellow, data.InitialRunner, colorReset)
	fmt.Printf("  • %setcd-tree/agents/%s        - 2 Default agents: %sagent-father%s & %sdefault-agent%s\n", colorYellow, colorReset, colorBold, colorReset, colorBold, colorReset)
	fmt.Printf("  • %setcd-tree/asgard/config.yaml%s - Minimal required config (optional fields commented)\n", colorYellow, colorReset)
	fmt.Printf("  • %scompose.yaml%s             - Multi-container orchestration (etcd, backend, runner)\n", colorYellow, colorReset)
	fmt.Printf("  • %sJustfile%s                 - Docker Compose & cfgctl push automation commands\n\n", colorYellow, colorReset)

	fmt.Printf("%s[Next Steps]%s\n", colorGreen+colorBold, colorReset)
	fmt.Printf("  1. %sReview configuration:%s\n", colorBold, colorReset)
	fmt.Printf("     cd %s\n", dir)
	fmt.Printf("     Check %setcd-tree/asgard/config.yaml%s and %setcd-tree/.sops.yaml%s\n\n", colorCyan, colorReset, colorCyan, colorReset)

	fmt.Printf("  2. %sEncrypt and publish configuration to etcd:%s\n", colorBold, colorReset)
	fmt.Printf("     %sjust publish%s   (runs cfgctl encrypt followed by cfgctl push)\n\n", colorCyan+colorBold, colorReset)

	fmt.Printf("  3. %sStart services:%s\n", colorBold, colorReset)
	fmt.Printf("     %sjust start%s     (runs docker compose up -d)\n\n", colorCyan+colorBold, colorReset)

	fmt.Printf("  4. %sCheck logs and status:%s\n", colorBold, colorReset)
	fmt.Printf("     %sjust status%s\n", colorCyan, colorReset)
	fmt.Printf("     %sjust logs-backend%s\n", colorCyan, colorReset)
	fmt.Printf("     %sjust logs-runner%s\n\n", colorCyan, colorReset)

	fmt.Printf("  5. %sAdd more runners anytime:%s\n", colorBold, colorReset)
	fmt.Printf("     %sasgard-init --add-new-runner runner-2%s\n\n", colorCyan, colorReset)
}

func printAddRunnerSuccess(dir, runnerName string) {
	svcName := runnerServiceName(runnerName)
	fmt.Printf("\n%s%s✔ Runner %q successfully added!%s\n", colorGreen, colorBold, runnerName, colorReset)
	fmt.Printf("%sLocation:%s %s\n\n", colorBold, colorReset, dir)

	fmt.Printf("%s[Updated Components]%s\n", colorCyan+colorBold, colorReset)
	fmt.Printf("  • Generated client certificate: %stls/%s.crt%s\n", colorYellow, runnerName, colorReset)
	fmt.Printf("  • Created runner directory:     %srunners/%s/{certs,asgard,session-data}%s\n", colorYellow, runnerName, colorReset)
	fmt.Printf("  • Updated allowed_nodes:        %setcd-tree/asgard/config.yaml%s\n", colorYellow, colorReset)
	fmt.Printf("  • Appended service:             %scompose.yaml%s (%s)\n\n", colorYellow, colorReset, svcName)

	fmt.Printf("%s[Next Steps]%s\n", colorGreen+colorBold, colorReset)
	fmt.Printf("  1. Publish updated allowed_nodes to etcd:\n")
	fmt.Printf("     %sjust publish%s\n\n", colorCyan+colorBold, colorReset)

	fmt.Printf("  2. Start the new runner container:\n")
	fmt.Printf("     %sdocker compose up -d %s%s\n\n", colorCyan+colorBold, svcName, colorReset)
}
