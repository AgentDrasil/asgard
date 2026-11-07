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
		deployEtcdFlag       = fs.Bool("deploy-etcd", true, "Deploy etcd container in docker-compose.yaml (set to false if using external etcd)")
		etcdEndpointsFlag    = fs.String("etcd-endpoints", "http://etcd:2379", "Etcd cluster endpoints")
		etcdPrefixFlag       = fs.String("etcd-prefix", "/asgard/configs", "Etcd prefix for configurations")
		authIssuerFlag       = fs.String("auth-issuer", "https://auth.example.com/oauth2", "OIDC Provider Issuer URL")
		authClientIDFlag     = fs.String("auth-client-id", "asgard", "OIDC Client ID")
		authClientSecretFlag = fs.String("auth-client-secret", "change-me-secret", "OIDC Client Secret")
		runnerFlag           = fs.String("runner", "runner-1", "Initial Runner Node ID")
		mountFlag            = fs.String("mount", "", "Workspace mount in format <host_path>:<container_path> (e.g. /home/user/src:/workspace)")
		gcpKmsFlag           = fs.String("gcp-kms", "", "Google Cloud KMS key resource ID for SOPS")
		ageFlag              = fs.String("age", "", "Age recipient public key for SOPS")
		providersFlag        = fs.String("providers", "", "Comma-separated list of CLI providers to activate (supported: agy, claude, opencode, simplest; default: all)")
		quotaProfilesFlag    = fs.String("quota-profiles", "", "Comma-separated quota policy profiles to generate into the aw config (supported: pro20, max200, ppr; default: none)")
		addNewRunnerFlag     = fs.Bool("add-new-runner", false, "Add a new runner to an existing deployment (interactive; optionally pass the runner name: --add-new-runner <name>)")
		gcpSAKeyFlag         = fs.String("gcp-sa-key", "", "GCP Service Account JSON key file path for GCP KMS")
		resumeFlag           = fs.Bool("resume", false, "Resume deployment setup from saved state")
	)

	args, addRunnerName := extractAddRunnerName(args)
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
	if *addNewRunnerFlag {
		runnerName := addRunnerName
		var mounts []string
		if *mountFlag != "" {
			mounts = append(mounts, *mountFlag)
		}
		if runnerName == "" || (*mountFlag == "" && !*nonInteractiveFlag) {
			if *nonInteractiveFlag {
				return fmt.Errorf("--add-new-runner requires a runner name in non-interactive mode")
			}
			if defaultStdinReader == nil {
				defaultStdinReader = bufio.NewReader(os.Stdin)
			}
			reader := defaultStdinReader
			if runnerName == "" {
				runnerName, err = promptUser(reader, "New runner name", "")
				if err != nil {
					return err
				}
				runnerName = strings.TrimSpace(runnerName)
			}
			if *mountFlag == "" {
				m, err := promptUser(reader, "Host workspace directory to mount into runner (optional, e.g. /home/user/src:/workspace)", "")
				if err != nil {
					return err
				}
				for _, part := range strings.Split(m, ",") {
					if t := strings.TrimSpace(part); t != "" {
						mounts = append(mounts, t)
					}
				}
			}
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

	cliQuotaProfiles, err := parseQuotaProfiles(*quotaProfilesFlag)
	if err != nil {
		return fmt.Errorf("invalid --quota-profiles: %w", err)
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
		Providers:        cliProviders,
		QuotaProfiles:    cliQuotaProfiles,
		GCPKMS:           *gcpKmsFlag,
		GCPSAKey:         *gcpSAKeyFlag,
		AgeRecipient:     *ageFlag,
		Force:            *forceFlag,
	}

	if explicitFlags["deploy-etcd"] {
		val := *deployEtcdFlag
		data.DeployEtcd = &val
	} else if explicitFlags["etcd-endpoints"] && *etcdEndpointsFlag != "http://etcd:2379" {
		val := false
		data.DeployEtcd = &val
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
	if !explicitFlags["deploy-etcd"] && state.DeployEtcd != nil {
		data.DeployEtcd = state.DeployEtcd
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
	if !explicitFlags["quota-profiles"] && len(state.QuotaProfiles) > 0 {
		data.QuotaProfiles = state.QuotaProfiles
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
		state.DeployEtcd = data.DeployEtcd
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
		state.QuotaProfiles = data.QuotaProfiles
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

		if !state.IsCompleted("deploy_etcd") {
			defaultChoice := "Y"
			if (data.DeployEtcd != nil && !*data.DeployEtcd) || (data.EtcdEndpoints != "" && data.EtcdEndpoints != "http://etcd:2379") {
				defaultChoice = "n"
			}
			ans, err := promptUser(reader, "Deploy etcd service in docker compose? [Y/n]", defaultChoice)
			if err != nil {
				return err
			}
			deployEtcd := strings.ToLower(strings.TrimSpace(ans)) != "n" && strings.ToLower(strings.TrimSpace(ans)) != "no"
			data.DeployEtcd = &deployEtcd
			state.DeployEtcd = &deployEtcd
			state.MarkCompleted("deploy_etcd")
			_ = state.Save(targetDir)
		}

		if !state.IsCompleted("etcd_endpoints") {
			promptMsg := "Etcd endpoints"
			if data.DeployEtcd != nil && !*data.DeployEtcd {
				promptMsg = "External etcd endpoints"
				if data.EtcdEndpoints == "http://etcd:2379" {
					data.EtcdEndpoints = "http://localhost:2379"
				}
			}
			data.EtcdEndpoints, err = promptUser(reader, promptMsg, data.EtcdEndpoints)
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
			workspaceMount, err := promptUser(reader, "Host workspace directory to mount into runner (optional, e.g. /home/user/src:/workspace)", defaultMount)
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
			if *gcpSAKeyFlag != "" {
				if err := copyGCPSAKey(targetDir, *gcpSAKeyFlag); err != nil {
					return err
				}
			}
			if PromptGCPSACredentials(reader, os.Stdout, targetDir, true) {
				data.GCPSAKey = filepath.Join(targetDir, GCPSAKeyFile)
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

	// In interactive mode, prompt for providers, ensure directories exist, and prompt for SSH
	if !*nonInteractiveFlag {
		if !state.IsCompleted("providers") {
			if len(cliProviders) > 0 {
				data.Providers = cliProviders
			} else {
				data.Providers = PromptProviderSetup(reader, os.Stdout, targetDir, true)
			}
			state.Providers = data.Providers
			state.MarkCompleted("providers")
			_ = state.Save(targetDir)
		} else if len(data.Providers) == 0 && len(state.Providers) > 0 {
			data.Providers = state.Providers
		}

		// Quota profiles only matter for the account-based CLIs.
		if !state.IsCompleted("quota_profiles") {
			if explicitFlags["quota-profiles"] {
				data.QuotaProfiles = cliQuotaProfiles
			} else if data.IsProviderEnabled("agy") || data.IsProviderEnabled("claude") {
				data.QuotaProfiles = PromptQuotaProfiles(reader, os.Stdout)
			}
			state.QuotaProfiles = data.QuotaProfiles
			state.MarkCompleted("quota_profiles")
			_ = state.Save(targetDir)
		}

		credDirs := []string{
			filepath.Join(targetDir, "etcd-tree", "credentials"),
			filepath.Join(targetDir, "etcd-tree", "credentials", "ssh"),
		}
		if data.IsProviderEnabled("opencode") || dirExists(filepath.Join(targetDir, "etcd-tree", "credentials", "opencode")) {
			credDirs = append(credDirs, filepath.Join(targetDir, "etcd-tree", "credentials", "opencode"))
		}
		if data.IsProviderEnabled("simplest") || dirExists(filepath.Join(targetDir, "etcd-tree", "credentials", "simplest")) {
			credDirs = append(credDirs, filepath.Join(targetDir, "etcd-tree", "credentials", "simplest"))
		}
		for _, cd := range credDirs {
			_ = os.MkdirAll(cd, 0700)
			_ = os.Chmod(cd, 0700)
		}

		otherDirs := []string{
			filepath.Join(targetDir, "etcd-tree", "aw"),
		}
		if data.IsProviderEnabled("simplest") || dirExists(filepath.Join(targetDir, "etcd-tree", "simplest")) {
			otherDirs = append(otherDirs, filepath.Join(targetDir, "etcd-tree", "simplest"))
		}
		for _, d := range otherDirs {
			_ = os.MkdirAll(d, 0755)
		}

		if !state.IsCompleted("ssh") {
			PromptSSHCredentials(reader, os.Stdout, targetDir, true)
			state.MarkCompleted("ssh")
			_ = state.Save(targetDir)
		}
	} else {
		if data.DeployEtcd == nil {
			val := *deployEtcdFlag
			data.DeployEtcd = &val
		}
		if data.DeployEtcd != nil && !*data.DeployEtcd && data.EtcdEndpoints == "http://etcd:2379" {
			return fmt.Errorf("external etcd requires explicit --etcd-endpoints when --deploy-etcd=false")
		}

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
		if data.IsProviderEnabled("opencode") {
			PromptOpencodeConfig(reader, os.Stdout, targetDir, false)
		}
		if data.IsProviderEnabled("simplest") {
			PromptSimplestConfig(reader, os.Stdout, targetDir, false)
		}
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

func isDirEmpty(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	_, err = f.Readdirnames(1)
	return err == io.EOF
}

// CheckGCPSACredentials checks whether filepath.Join(targetDir, GCPSAKeyFile)
// exists, is non-empty, and passes validateJSONFile (valid JSON with project_id).
// If valid, tightens permissions to 0600 and returns (path, true).
func CheckGCPSACredentials(targetDir string) (string, bool) {
	saPath := filepath.Join(targetDir, GCPSAKeyFile)
	if !isNonEmptyFile(saPath) {
		return "", false
	}
	if err := validateJSONFile(saPath); err != nil {
		return "", false
	}
	_ = os.Chmod(saPath, 0600)
	return saPath, true
}

// CheckOpencodeConfig checks if opencode.aw.yaml exists in targetDir/etcd-tree/credentials/opencode and contains non-comment content.
func CheckOpencodeConfig(targetDir string) (string, bool) {
	path := filepath.Join(targetDir, "etcd-tree", "credentials", "opencode", "opencode.aw.yaml")
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

// CheckSimplestConfig checks if key.yaml exists in targetDir/etcd-tree/credentials/simplest and contains non-comment content.
func CheckSimplestConfig(targetDir string) (string, bool) {
	keyPath := filepath.Join(targetDir, "etcd-tree", "credentials", "simplest", "key.yaml")
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

// PromptGCPSACredentials prompts the user to place their GCP Service Account JSON key file into <dir>/gcp-sa.json (local file mounted into containers, never pushed to etcd).
func PromptGCPSACredentials(r io.Reader, w io.Writer, targetDir string, interactive bool) bool {
	credPath := filepath.Join(targetDir, GCPSAKeyFile)
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
			_, _ = fmt.Fprintf(w, "%sSkipped GCP Service Account key setup.%s Place it at ./gcp-sa.json and re-run asgard-init (to add the container mount) before running just publish.\n", colorYellow, colorReset)
			return false
		default:
			_, _ = fmt.Fprintf(w, "Invalid input %q. Enter 'y' to verify or 's' to skip.\n", ans)
		}
	}
}

// PromptOpencodeConfig prompts the user to configure their OpenCode configuration in etcd-tree/credentials/opencode/opencode.aw.yaml.
func PromptOpencodeConfig(r io.Reader, w io.Writer, targetDir string, interactive bool) bool {
	cfgPath := filepath.Join(targetDir, "etcd-tree", "credentials", "opencode", "opencode.aw.yaml")
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
			_, _ = fmt.Fprintf(w, "%sSkipped OpenCode configuration setup.%s You can configure it in etcd-tree/credentials/opencode/opencode.aw.yaml later before running just publish.\n", colorYellow, colorReset)
			return false
		default:
			_, _ = fmt.Fprintf(w, "Invalid input %q. Enter 'y' to verify or 's' to skip.\n", ans)
		}
	}
}

// PromptSimplestConfig prompts the user to configure API keys in etcd-tree/credentials/simplest/key.yaml.
func PromptSimplestConfig(r io.Reader, w io.Writer, targetDir string, interactive bool) bool {
	cfgPath := filepath.Join(targetDir, "etcd-tree", "credentials", "simplest", "key.yaml")
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
			_, _ = fmt.Fprintf(w, "%sSkipped Simplest configuration setup.%s You can configure it in etcd-tree/credentials/simplest/key.yaml later before running just publish.\n", colorYellow, colorReset)
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
	var credDir string
	if interactive {
		switch cliName {
		case "opencode", "simplest":
			credDir = filepath.Join(targetDir, "etcd-tree", "credentials", cliName)
			_ = os.MkdirAll(credDir, 0700)
			_ = os.Chmod(credDir, 0700)
		}
	}

	ok := promptCredFn(r, w, targetDir, interactive)
	if !ok && interactive {
		ok = PromptCLIActivation(r, w, cliName)
	}
	if !ok && credDir != "" && isDirEmpty(credDir) {
		_ = os.Remove(credDir)
	}
	return ok
}

// PromptAccountProviderActivation asks whether to activate an account-based CLI
// provider (agy, claude). Credentials are not collected by asgard-init: the
// operator logs in on the runner after deployment.
func PromptAccountProviderActivation(r io.Reader, w io.Writer, cliName string) bool {
	_, _ = fmt.Fprintf(w, "\n%s--- %s provider ---%s\n", colorCyan+colorBold, cliName, colorReset)
	_, _ = fmt.Fprintf(w, "Credentials are not stored in etcd; after %sdocker compose up%s run %saw %s login <alias>%s on the runner.\n", colorYellow, colorReset, colorYellow, cliName, colorReset)
	var reader *bufio.Reader
	if br, ok := r.(*bufio.Reader); ok {
		reader = br
	} else if r != nil {
		reader = bufio.NewReader(r)
	} else {
		reader = bufio.NewReader(os.Stdin)
	}
	_, _ = fmt.Fprintf(w, "%sActivate %s? [y/N]:%s ", colorBold, cliName, colorReset)
	line, err := reader.ReadString('\n')
	if err != nil {
		return false
	}
	ans := strings.ToLower(strings.TrimSpace(line))
	return ans == "y" || ans == "yes"
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
	// agy and claude credentials live on the runner (aw <cli> login <alias>),
	// so there is nothing to collect here; only ask whether to activate them.
	for _, cli := range []string{"agy", "claude"} {
		if interactive && PromptAccountProviderActivation(reader, w, cli) {
			providers = append(providers, cli)
		}
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

// safeSSHName mirrors config.SafeSSHName (backend/lib/config/tree.go), duplicated to keep
// this tool free of the backend config dependency tree. Keep the two in sync.
func safeSSHName(name string) bool {
	if name == "" || name == "." || name == ".." || name == ".asgard-managed" {
		return false
	}
	return !strings.ContainsAny(name, "/\\\x00\n\r") && strings.TrimSpace(name) == name && !strings.HasSuffix(name, ".sops.json")
}

// CheckSSHCredentials checks whether valid SSH private keys exist in targetDir/etcd-tree/credentials/ssh/.
// Any filename is supported: a valid private key is a regular, non-empty file (not *.pub, *.sops.json, config,
// known_hosts or .gitkeep) whose content contains "-----BEGIN ".
// It tightens every valid private key's permissions to 0600, and returns the sorted list of key names.
func CheckSSHCredentials(targetDir string) ([]string, bool) {
	sshDir := filepath.Join(targetDir, "etcd-tree", "credentials", "ssh")
	entries, err := os.ReadDir(sshDir)
	if err != nil {
		return nil, false
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
		if !safeSSHName(name) {
			fmt.Printf("\n%sWarning:%s %s has an unsupported file name (path separators, surrounding whitespace/newlines or reserved name) and won't be synced to runner.\n", colorYellow+colorBold, colorReset, name)
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
	_, _ = fmt.Fprintf(w, "Please place your SSH private key (and optional *.pub / config) into %s%s/%s (any filename, e.g. id_ed25519 or myserver).\n", colorYellow, sshDir, colorReset)

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
			_, _ = fmt.Fprintf(w, "%sError:%s No valid, non-empty SSH private key found in %s (a file containing \"-----BEGIN \").\n", colorRed+colorBold, colorReset, sshDir)
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
	fmt.Printf("  • %setcd-tree/agents/%s        - 2 Default agents: %sagent_father%s & %sdefault-agent%s\n", colorYellow, colorReset, colorBold, colorReset, colorBold, colorReset)
	fmt.Printf("  • %setcd-tree/asgard/config.yaml%s - Minimal required config (optional fields commented)\n", colorYellow, colorReset)
	if data.IncludeEtcdService {
		fmt.Printf("  • %scompose.yaml%s             - Multi-container orchestration (etcd, backend, runner)\n", colorYellow, colorReset)
	} else {
		fmt.Printf("  • %scompose.yaml%s             - Multi-container orchestration (backend, runner; external etcd)\n", colorYellow, colorReset)
	}
	fmt.Printf("  • %sJustfile%s                 - Docker Compose & cfgctl push automation commands\n\n", colorYellow, colorReset)

	fmt.Printf("%s[Next Steps]%s\n", colorGreen+colorBold, colorReset)
	fmt.Printf("  1. %sReview configuration:%s\n", colorBold, colorReset)
	fmt.Printf("     cd %s\n", dir)
	fmt.Printf("     Check %setcd-tree/asgard/config.yaml%s and %setcd-tree/.sops.yaml%s\n\n", colorCyan, colorReset, colorCyan, colorReset)

	fmt.Printf("  2. %sEncrypt and publish configuration to etcd:%s\n", colorBold, colorReset)
	fmt.Printf("     %sjust publish%s   (runs cfgctl encrypt followed by cfgctl push)\n\n", colorCyan+colorBold, colorReset)

	fmt.Printf("  3. %sStart services:%s\n", colorBold, colorReset)
	fmt.Printf("     %sjust start%s     (runs docker compose up -d)\n\n", colorCyan+colorBold, colorReset)

	fmt.Printf("  4. %sLog in to agy / claude on each runner%s (accounts live only on the runner, not in etcd):\n", colorBold, colorReset)
	fmt.Printf("     %sdocker exec -it asgard-%s aw agy login <alias>%s\n", colorCyan, data.InitialRunner, colorReset)
	fmt.Printf("     %sdocker exec -it asgard-%s aw claude login <alias>%s\n\n", colorCyan, data.InitialRunner, colorReset)
	printQuotaBindingHint(data)

	fmt.Printf("  5. %sCheck logs and status:%s\n", colorBold, colorReset)
	fmt.Printf("     %sjust status%s\n", colorCyan, colorReset)
	fmt.Printf("     %sjust logs-backend%s\n", colorCyan, colorReset)
	fmt.Printf("     %sjust logs-runner%s\n\n", colorCyan, colorReset)

	fmt.Printf("  6. %sAdd more runners anytime:%s\n", colorBold, colorReset)
	fmt.Printf("     %sasgard-init --add-new-runner runner-2%s\n\n", colorCyan, colorReset)
}

func printAddRunnerSuccess(dir, runnerName string) {
	svcName := runnerServiceName(runnerName)
	fmt.Printf("\n%s%s✔ Runner %q successfully added!%s\n", colorGreen, colorBold, runnerName, colorReset)
	fmt.Printf("%sLocation:%s %s\n\n", colorBold, colorReset, dir)

	fmt.Printf("%s[Updated Components]%s\n", colorCyan+colorBold, colorReset)
	fmt.Printf("  • Generated client certificate: %stls/%s.crt%s\n", colorYellow, runnerName, colorReset)
	fmt.Printf("  • Created runner directory:     %srunners/%s/{certs,asgard,session-data,home,accounts}%s\n", colorYellow, runnerName, colorReset)
	fmt.Printf("  • Updated allowed_nodes:        %setcd-tree/asgard/config.yaml%s\n", colorYellow, colorReset)
	fmt.Printf("  • Appended service:             %scompose.yaml%s (%s)\n\n", colorYellow, colorReset, svcName)

	fmt.Printf("%s[Next Steps]%s\n", colorGreen+colorBold, colorReset)
	fmt.Printf("  1. Publish updated allowed_nodes to etcd:\n")
	fmt.Printf("     %sjust publish%s\n\n", colorCyan+colorBold, colorReset)

	fmt.Printf("  2. Start the new runner container:\n")
	fmt.Printf("     %sdocker compose up -d %s%s\n\n", colorCyan+colorBold, svcName, colorReset)

	fmt.Printf("  3. Log in to agy / claude on the new runner:\n")
	fmt.Printf("     %sdocker exec -it asgard-%s aw agy login <alias>%s\n", colorCyan, runnerName, colorReset)
	fmt.Printf("     %sdocker exec -it asgard-%s aw claude login <alias>%s\n\n", colorCyan, runnerName, colorReset)
}

// extractAddRunnerName supports both `--add-new-runner` (interactive) and the
// legacy `--add-new-runner <name>` / `--add-new-runner=<name>` forms. It
// returns the args rewritten with a plain boolean flag plus the runner name.
func extractAddRunnerName(args []string) ([]string, string) {
	var out []string
	name := ""
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--add-new-runner" || a == "-add-new-runner":
			out = append(out, "--add-new-runner")
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				name = args[i+1]
				i++
			}
		case strings.HasPrefix(a, "--add-new-runner=") || strings.HasPrefix(a, "-add-new-runner="):
			out = append(out, "--add-new-runner")
			name = a[strings.Index(a, "=")+1:]
		default:
			out = append(out, a)
		}
	}
	return out, name
}
