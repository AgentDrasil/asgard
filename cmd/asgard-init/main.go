package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
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

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "%sError:%s %v\n", colorRed+colorBold, colorReset, err)
		os.Exit(1)
	}
}

func run(args []string) error {
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
		addNewRunnerFlag     = fs.String("add-new-runner", "", "Add a new runner to an existing deployment")
	)

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	// Mandatory check: cfgctl must be installed
	if err := checkCfgctl(); err != nil {
		return err
	}

	targetDir, err := filepath.Abs(*dirFlag)
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
		AgeRecipient:     *ageFlag,
		Force:            *forceFlag,
	}

	if *mountFlag != "" {
		data.WorkspaceMounts = append(data.WorkspaceMounts, *mountFlag)
	}

	var reader *bufio.Reader
	// If interactive mode is enabled, run the interactive prompt wizard
	if !*nonInteractiveFlag {
		reader = bufio.NewReader(os.Stdin)
		printWelcomeBanner()

		targetDirInput := prompt(reader, "Deployment target directory", targetDir)
		targetDir, err = filepath.Abs(targetDirInput)
		if err != nil {
			return fmt.Errorf("resolve target directory: %w", err)
		}

		data.Host = prompt(reader, "Asgard public base URL (host)", data.Host)
		portStr := prompt(reader, "Backend HTTP port", strconv.Itoa(data.BackendPort))
		if p, err := strconv.Atoi(portStr); err == nil && p > 0 {
			data.BackendPort = p
		}

		data.EtcdEndpoints = prompt(reader, "Etcd endpoints", data.EtcdEndpoints)
		data.EtcdPrefix = prompt(reader, "Etcd prefix", data.EtcdPrefix)

		fmt.Printf("\n%s--- Authentication (OIDC) Settings (Required) ---%s\n", colorCyan+colorBold, colorReset)
		data.AuthIssuer = prompt(reader, "OIDC Issuer URL", data.AuthIssuer)
		data.AuthClientID = prompt(reader, "OIDC Client ID", data.AuthClientID)
		data.AuthClientSecret = prompt(reader, "OIDC Client Secret", data.AuthClientSecret)

		fmt.Printf("\n%s--- Runner Node Settings ---%s\n", colorCyan+colorBold, colorReset)
		data.InitialRunner = prompt(reader, "Initial runner node ID", data.InitialRunner)

		workspaceMount := prompt(reader, "Host workspace directory to mount into runner (optional, e.g. /home/user/src:/data/src)", "")
		if workspaceMount != "" {
			data.WorkspaceMounts = []string{workspaceMount}
		}

		fmt.Printf("\n%s--- SOPS Encryption Settings (Optional) ---%s\n", colorCyan+colorBold, colorReset)
		data.GCPKMS = prompt(reader, "GCP KMS key resource ID (leave empty if not using GCP KMS)", data.GCPKMS)
		if data.GCPKMS == "" {
			data.AgeRecipient = prompt(reader, "Age recipient public key (leave empty to configure manually later)", data.AgeRecipient)
		}
	}

	// Warn if auth client secret is still the placeholder
	if data.AuthClientSecret == "change-me-secret" {
		fmt.Printf("\n%sWarning:%s auth.client_secret is set to placeholder %q. Remember to update it with your real OIDC provider credentials.\n", colorYellow+colorBold, colorReset, data.AuthClientSecret)
	}

	// Validate target directory and generate deployment
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return fmt.Errorf("create directory %s: %w", targetDir, err)
	}

	if err := GenerateDeployment(targetDir, data); err != nil {
		return err
	}

	PromptCLICredentials(reader, os.Stdout, targetDir, !*nonInteractiveFlag)
	PromptSSHCredentials(reader, os.Stdout, targetDir, !*nonInteractiveFlag)

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

// CheckSSHCredentials checks whether etcd-tree/credentials/ssh/key exists and is a non-empty file.
// It also ensures the file's permissions are tightened to 0600.
func CheckSSHCredentials(targetDir string) bool {
	keyPath := filepath.Join(targetDir, "etcd-tree", "credentials", "ssh", "key")
	if isNonEmptyFile(keyPath) {
		_ = os.Chmod(keyPath, 0600)
		return true
	}
	return false
}

// PromptSSHCredentials prompts the user to place their SSH private key (and optional key.pub / config) into etcd-tree/credentials/ssh/.
// When interactive is false, it does not block on r and checks if the file exists or skips gracefully.
func PromptSSHCredentials(r *bufio.Reader, w io.Writer, targetDir string, interactive bool) bool {
	sshDir := filepath.Join(targetDir, "etcd-tree", "credentials", "ssh")
	sshKeyPath := filepath.Join(sshDir, "key")
	if !interactive {
		if CheckSSHCredentials(targetDir) {
			_ = os.Chmod(sshKeyPath, 0600)
			_, _ = fmt.Fprintf(w, "\n%s✔ SSH credentials verified:%s %s\n", colorGreen+colorBold, colorReset, sshKeyPath)
			return true
		}
		return false
	}

	if r == nil {
		r = bufio.NewReader(os.Stdin)
	}

	_, _ = fmt.Fprintf(w, "\n%s--- SSH Credentials Setup ---%s\n", colorCyan+colorBold, colorReset)
	_, _ = fmt.Fprintf(w, "Please place your SSH private key (and optional key.pub / config) into %s%s/%s (e.g. key, key.pub, config).\n", colorYellow, sshDir, colorReset)

	for {
		_, _ = fmt.Fprintf(w, "%sPress 'y' when ready to verify (or 's' to skip for now):%s ", colorBold, colorReset)
		line, err := r.ReadString('\n')
		if err != nil {
			return false
		}
		ans := strings.ToLower(strings.TrimSpace(line))
		switch ans {
		case "y", "yes":
			if CheckSSHCredentials(targetDir) {
				_ = os.Chmod(sshKeyPath, 0600)
				_, _ = fmt.Fprintf(w, "%s✔ SSH credentials verified successfully:%s %s\n", colorGreen+colorBold, colorReset, sshKeyPath)
				return true
			}
			_, _ = fmt.Fprintf(w, "%sError:%s No valid, non-empty SSH private key found at %s.\n", colorRed+colorBold, colorReset, sshKeyPath)
		case "s", "skip":
			_, _ = fmt.Fprintf(w, "%sSkipped SSH credentials setup.%s You can place it into %s/ later before running just publish.\n", colorYellow, colorReset, sshDir)
			return false
		default:
			_, _ = fmt.Fprintf(w, "Invalid input %q. Enter 'y' to verify or 's' to skip.\n", ans)
		}
	}
}

func prompt(r *bufio.Reader, question, def string) string {
	if def != "" {
		fmt.Printf("%s%s%s [%s%s%s]: ", colorBold, question, colorReset, colorYellow, def, colorReset)
	} else {
		fmt.Printf("%s%s%s: ", colorBold, question, colorReset)
	}
	line, err := r.ReadString('\n')
	if err != nil {
		return def
	}
	val := strings.TrimSpace(line)
	if val == "" {
		return def
	}
	return val
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
