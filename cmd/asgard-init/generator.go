package main

import (
	"bytes"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"
)

var runnerNameRegex = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,62}$`)

func validateRunnerName(name string) error {
	if !runnerNameRegex.MatchString(name) {
		return fmt.Errorf("invalid runner name %q: must match regex ^[a-zA-Z0-9][a-zA-Z0-9_-]{0,62}$", name)
	}
	return nil
}

type ConfigData struct {
	Host                         string
	BackendPort                  int
	RegistryPort                 int
	EtcdEndpoints                string
	EtcdPrefix                   string
	AuthIssuer                   string
	AuthClientID                 string
	AuthClientSecret             string
	AuthTokenSource              string
	AuthTokenAudience            string
	InitialRunner                string
	InitialRunnerService         string
	RunnerName                   string
	RunnerService                string
	WorkspaceMounts              []string
	GCPKMS                       string
	AgeRecipient                 string
	GoogleApplicationCredentials string
	Force                        bool
}

func runnerServiceName(name string) string {
	if strings.HasPrefix(name, "runner-") {
		return name
	}
	return "runner-" + name
}

// GenerateDeployment generates the complete Asgard deployment structure.
func GenerateDeployment(targetDir string, data *ConfigData) error {
	if data.RegistryPort == 0 {
		data.RegistryPort = 9311
	}
	if data.BackendPort == 0 {
		data.BackendPort = 7000
	}
	if data.AuthTokenSource == "" {
		data.AuthTokenSource = "access_token"
	}
	if data.AuthTokenAudience == "" {
		data.AuthTokenAudience = data.AuthClientID
	}
	if data.InitialRunner == "" {
		data.InitialRunner = "runner-1"
	}
	if err := validateRunnerName(data.InitialRunner); err != nil {
		return fmt.Errorf("invalid initial runner: %w", err)
	}
	data.InitialRunnerService = runnerServiceName(data.InitialRunner)

	if data.EtcdEndpoints == "" {
		data.EtcdEndpoints = "http://etcd:2379"
	}
	if data.EtcdPrefix == "" {
		data.EtcdPrefix = "/asgard/configs"
	}

	// 0. Guard against accidental destructive overwrite of existing deployment
	if !data.Force {
		composeCheck := filepath.Join(targetDir, "compose.yaml")
		caKeyCheck := filepath.Join(targetDir, "tls", "ca.key")
		if fileExists(composeCheck) || fileExists(caKeyCheck) {
			return fmt.Errorf("existing Asgard deployment detected in %s (found compose.yaml or tls/ca.key); use --force to overwrite or use --add-new-runner to add a runner", targetDir)
		}
	}

	// 1. Create directories
	dirs := []string{
		filepath.Join(targetDir, "tls"),
		filepath.Join(targetDir, "backend", "asgard", "logs"),
		filepath.Join(targetDir, "backend", "asgard", "data"),
		filepath.Join(targetDir, "backend", "certs"),
		filepath.Join(targetDir, "runners", data.InitialRunner, "asgard"),
		filepath.Join(targetDir, "runners", data.InitialRunner, "session-data"),
		filepath.Join(targetDir, "runners", data.InitialRunner, "certs"),
		filepath.Join(targetDir, "etcd-tree", "asgard"),
		filepath.Join(targetDir, "etcd-tree", "aw"),
		filepath.Join(targetDir, "etcd-tree", "simplest"),
		filepath.Join(targetDir, "etcd-tree", "credentials"),
		filepath.Join(targetDir, "etcd-tree", "agents", "agent-father"),
		filepath.Join(targetDir, "etcd-tree", "agents", "default-agent"),
	}

	for _, d := range dirs {
		if err := os.MkdirAll(d, 0755); err != nil {
			return fmt.Errorf("failed to create directory %q: %w", d, err)
		}
	}

	// 2. Generate TLS certificates
	caCert, caKey, caCertPEM, caKeyPEM, err := GenerateCA()
	if err != nil {
		return err
	}

	caCertPath := filepath.Join(targetDir, "tls", "ca.crt")
	caKeyPath := filepath.Join(targetDir, "tls", "ca.key")
	if err := os.WriteFile(caCertPath, caCertPEM, 0644); err != nil {
		return fmt.Errorf("write ca.crt: %w", err)
	}
	if err := os.WriteFile(caKeyPath, caKeyPEM, 0600); err != nil {
		return fmt.Errorf("write ca.key: %w", err)
	}

	// Server cert (Backend)
	dnsNames := []string{"backend", "asgard-backend", "localhost"}
	serverIPs := []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}

	hostStr := data.Host
	if !strings.Contains(hostStr, "://") {
		hostStr = "http://" + hostStr
	}
	if u, err := url.Parse(hostStr); err == nil {
		hostname := u.Hostname()
		if hostname != "" && hostname != "localhost" && hostname != "backend" && hostname != "asgard-backend" {
			if ip := net.ParseIP(hostname); ip != nil {
				serverIPs = append(serverIPs, ip)
			} else {
				dnsNames = append(dnsNames, hostname)
			}
		}
	}

	srvCertPEM, srvKeyPEM, err := GenerateServerCert(caCert, caKey, dnsNames, serverIPs)
	if err != nil {
		return err
	}

	backendCertPath := filepath.Join(targetDir, "tls", "backend.crt")
	backendKeyPath := filepath.Join(targetDir, "tls", "backend.key")
	if err := os.WriteFile(backendCertPath, srvCertPEM, 0644); err != nil {
		return fmt.Errorf("write backend.crt: %w", err)
	}
	if err := os.WriteFile(backendKeyPath, srvKeyPEM, 0600); err != nil {
		return fmt.Errorf("write backend.key: %w", err)
	}

	// Initial Runner Client Cert
	runnerCertPEM, runnerKeyPEM, err := GenerateClientCert(caCert, caKey, data.InitialRunner)
	if err != nil {
		return err
	}
	rCertPath := filepath.Join(targetDir, "tls", fmt.Sprintf("%s.crt", data.InitialRunner))
	rKeyPath := filepath.Join(targetDir, "tls", fmt.Sprintf("%s.key", data.InitialRunner))
	if err := os.WriteFile(rCertPath, runnerCertPEM, 0644); err != nil {
		return fmt.Errorf("write %s.crt: %w", data.InitialRunner, err)
	}
	if err := os.WriteFile(rKeyPath, runnerKeyPEM, 0600); err != nil {
		return fmt.Errorf("write %s.key: %w", data.InitialRunner, err)
	}

	// 3. Copy certificates to container mount destinations
	// Backend:
	if err := copyFile(backendCertPath, filepath.Join(targetDir, "backend", "certs", "backend.crt"), 0644); err != nil {
		return err
	}
	if err := copyFile(backendKeyPath, filepath.Join(targetDir, "backend", "certs", "backend.key"), 0600); err != nil {
		return err
	}
	if err := copyFile(caCertPath, filepath.Join(targetDir, "backend", "certs", "client-ca.crt"), 0644); err != nil {
		return err
	}

	// Runner:
	runnerCertsDir := filepath.Join(targetDir, "runners", data.InitialRunner, "certs")
	if err := copyFile(rCertPath, filepath.Join(runnerCertsDir, "tls.crt"), 0644); err != nil {
		return err
	}
	if err := copyFile(rKeyPath, filepath.Join(runnerCertsDir, "tls.key"), 0600); err != nil {
		return err
	}
	if err := copyFile(caCertPath, filepath.Join(runnerCertsDir, "ca.crt"), 0644); err != nil {
		return err
	}

	// 4. Render template files
	templateMap := []struct {
		tmpl string
		dest string
		perm os.FileMode
	}{
		{"templates/compose.yaml.tmpl", filepath.Join(targetDir, "compose.yaml"), 0644},
		{"templates/Justfile.tmpl", filepath.Join(targetDir, "Justfile"), 0644},
		{"templates/gitignore.tmpl", filepath.Join(targetDir, ".gitignore"), 0644},
		{"templates/etcd-tree/sops.yaml.tmpl", filepath.Join(targetDir, "etcd-tree", ".sops.yaml"), 0644},
		{"templates/etcd-tree/asgard/config.yaml.tmpl", filepath.Join(targetDir, "etcd-tree", "asgard", "config.yaml"), 0600},
		{"templates/etcd-tree/asgard/proxy.yaml.tmpl", filepath.Join(targetDir, "etcd-tree", "asgard", "proxy.yaml"), 0644},
		{"templates/etcd-tree/aw/config.yaml.tmpl", filepath.Join(targetDir, "etcd-tree", "aw", "config.yaml"), 0644},
		{"templates/etcd-tree/simplest/key.yaml.tmpl", filepath.Join(targetDir, "etcd-tree", "simplest", "key.yaml"), 0600},
		{"templates/etcd-tree/simplest/models.yaml.tmpl", filepath.Join(targetDir, "etcd-tree", "simplest", "models.yaml"), 0644},
		{"templates/etcd-tree/simplest/providers.yaml.tmpl", filepath.Join(targetDir, "etcd-tree", "simplest", "providers.yaml"), 0644},
		{"templates/etcd-tree/credentials/auth.json.tmpl", filepath.Join(targetDir, "etcd-tree", "credentials", "auth.json"), 0600},
		{"templates/etcd-tree/agents/agent-father/config.yaml.tmpl", filepath.Join(targetDir, "etcd-tree", "agents", "agent-father", "config.yaml"), 0644},
		{"templates/etcd-tree/agents/agent-father/AGENTS.md.tmpl", filepath.Join(targetDir, "etcd-tree", "agents", "agent-father", "AGENTS.md"), 0644},
		{"templates/etcd-tree/agents/default-agent/config.yaml.tmpl", filepath.Join(targetDir, "etcd-tree", "agents", "default-agent", "config.yaml"), 0644},
		{"templates/etcd-tree/agents/default-agent/AGENTS.md.tmpl", filepath.Join(targetDir, "etcd-tree", "agents", "default-agent", "AGENTS.md"), 0644},
	}

	for _, item := range templateMap {
		if err := renderTemplate(item.tmpl, item.dest, data, item.perm); err != nil {
			return fmt.Errorf("failed to render %s -> %s: %w", item.tmpl, item.dest, err)
		}
	}

	return nil
}

// AddRunner adds a new runner to an existing Asgard deployment.
func AddRunner(targetDir string, runnerName string, workspaceMounts []string) error {
	runnerName = strings.TrimSpace(runnerName)
	if err := validateRunnerName(runnerName); err != nil {
		return fmt.Errorf("invalid runner name: %w", err)
	}

	tlsDir := filepath.Join(targetDir, "tls")
	caCertPath := filepath.Join(tlsDir, "ca.crt")
	caKeyPath := filepath.Join(tlsDir, "ca.key")

	caCertPEM, err := os.ReadFile(caCertPath)
	if err != nil {
		return fmt.Errorf("cannot read CA certificate at %s: %w", caCertPath, err)
	}
	caKeyPEM, err := os.ReadFile(caKeyPath)
	if err != nil {
		return fmt.Errorf("cannot read CA key at %s: %w", caKeyPath, err)
	}

	caCert, caKey, err := LoadCA(caCertPEM, caKeyPEM)
	if err != nil {
		return fmt.Errorf("load CA: %w", err)
	}

	runnerTLSCert := filepath.Join(tlsDir, fmt.Sprintf("%s.crt", runnerName))
	runnerTLSKey := filepath.Join(tlsDir, fmt.Sprintf("%s.key", runnerName))

	// 1. Generate client cert for new runner only if not already present
	if !fileExists(runnerTLSCert) || !fileExists(runnerTLSKey) {
		certPEM, keyPEM, err := GenerateClientCert(caCert, caKey, runnerName)
		if err != nil {
			return fmt.Errorf("generate cert for runner %s: %w", runnerName, err)
		}

		if err := os.WriteFile(runnerTLSCert, certPEM, 0644); err != nil {
			return fmt.Errorf("write %s.crt: %w", runnerName, err)
		}
		if err := os.WriteFile(runnerTLSKey, keyPEM, 0600); err != nil {
			return fmt.Errorf("write %s.key: %w", runnerName, err)
		}
	}

	// 2. Create runner directories and copy certs
	runnerDir := filepath.Join(targetDir, "runners", runnerName)
	runnerCertsDir := filepath.Join(runnerDir, "certs")
	if err := os.MkdirAll(runnerCertsDir, 0755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(runnerDir, "asgard"), 0755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(runnerDir, "session-data"), 0755); err != nil {
		return err
	}

	if err := copyFile(runnerTLSCert, filepath.Join(runnerCertsDir, "tls.crt"), 0644); err != nil {
		return err
	}
	if err := copyFile(runnerTLSKey, filepath.Join(runnerCertsDir, "tls.key"), 0600); err != nil {
		return err
	}
	if err := copyFile(caCertPath, filepath.Join(runnerCertsDir, "ca.crt"), 0644); err != nil {
		return err
	}

	// 3. Update etcd-tree/asgard/config.yaml allowed_nodes
	configFile := filepath.Join(targetDir, "etcd-tree", "asgard", "config.yaml")
	if err := appendAllowedNode(configFile, runnerName); err != nil {
		return fmt.Errorf("update allowed_nodes in %s: %w", configFile, err)
	}

	// 4. Update compose.yaml with new runner service
	composeFile := filepath.Join(targetDir, "compose.yaml")
	if err := appendRunnerService(composeFile, runnerName, workspaceMounts); err != nil {
		return fmt.Errorf("update %s: %w", composeFile, err)
	}

	return nil
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func renderTemplate(tmplPath, destPath string, data any, perm os.FileMode) error {
	content, err := templateFS.ReadFile(tmplPath)
	if err != nil {
		return err
	}

	t, err := template.New(filepath.Base(tmplPath)).Parse(string(content))
	if err != nil {
		return err
	}

	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return err
	}

	return os.WriteFile(destPath, buf.Bytes(), perm)
}

func appendAllowedNode(configFile, nodeID string) error {
	data, err := os.ReadFile(configFile)
	if err != nil {
		return err
	}

	content := string(data)

	// Whole-entry check avoiding substring false positives (e.g. runner-2 vs runner-21)
	entryPattern := regexp.MustCompile(`(?m)^\s*-\s*["']?` + regexp.QuoteMeta(nodeID) + `["']?\s*$`)
	if entryPattern.MatchString(content) {
		return nil // already present
	}

	lines := strings.Split(content, "\n")
	allowedNodesIdx := -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "allowed_nodes:") {
			allowedNodesIdx = i
			break
		}
	}

	if allowedNodesIdx == -1 {
		return fmt.Errorf("allowed_nodes section not found in %s", configFile)
	}

	// Find the last item belonging to allowed_nodes:
	lastItemIdx := allowedNodesIdx
	for i := allowedNodesIdx + 1; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			// Empty line or comment might separate sections
			if i > allowedNodesIdx+1 && !strings.HasPrefix(lines[i-1], " ") && !strings.HasPrefix(lines[i-1], "\t") {
				break
			}
			continue
		}
		// If line is indented with '-' it's a list item under allowed_nodes
		if strings.HasPrefix(trimmed, "- ") {
			lastItemIdx = i
		} else if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			// Unindented line indicates next top-level section
			break
		}
	}

	// Append at the end of allowed_nodes list so the primary (first) runner is preserved
	insertion := fmt.Sprintf("    - \"%s\"", nodeID)
	newLines := make([]string, 0, len(lines)+1)
	newLines = append(newLines, lines[:lastItemIdx+1]...)
	newLines = append(newLines, insertion)
	newLines = append(newLines, lines[lastItemIdx+1:]...)

	return os.WriteFile(configFile, []byte(strings.Join(newLines, "\n")), 0600)
}

func appendRunnerService(composeFile, runnerName string, workspaceMounts []string) error {
	data, err := os.ReadFile(composeFile)
	if err != nil {
		return err
	}

	content := string(data)
	svcName := runnerServiceName(runnerName)
	svcPattern := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(svcName) + `\s*:`)
	if svcPattern.MatchString(content) {
		return nil // already present
	}

	etcdEndpoints := "http://etcd:2379"
	etcdPrefix := "/asgard/configs"
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "- ETCD_ENDPOINTS=") {
			val := strings.TrimPrefix(trimmed, "- ETCD_ENDPOINTS=")
			// Strip any trailing inline comments
			if hashIdx := strings.Index(val, "#"); hashIdx != -1 {
				val = strings.TrimSpace(val[:hashIdx])
			}
			etcdEndpoints = strings.Trim(val, `"' `)
		}
		if strings.HasPrefix(trimmed, "- ETCD_PREFIX=") {
			val := strings.TrimPrefix(trimmed, "- ETCD_PREFIX=")
			if hashIdx := strings.Index(val, "#"); hashIdx != -1 {
				val = strings.TrimSpace(val[:hashIdx])
			}
			etcdPrefix = strings.Trim(val, `"' `)
		}
	}

	svcData := &ConfigData{
		RunnerName:      runnerName,
		RunnerService:   svcName,
		RegistryPort:    9311,
		EtcdEndpoints:   etcdEndpoints,
		EtcdPrefix:      etcdPrefix,
		WorkspaceMounts: workspaceMounts,
	}

	contentTmpl, err := templateFS.ReadFile("templates/runner-service.yaml.tmpl")
	if err != nil {
		return err
	}

	t, err := template.New("runner-service").Parse(string(contentTmpl))
	if err != nil {
		return err
	}

	var buf bytes.Buffer
	if err := t.Execute(&buf, svcData); err != nil {
		return err
	}

	newContent := strings.TrimRight(content, "\n") + "\n" + buf.String()
	return os.WriteFile(composeFile, []byte(newContent), 0644)
}
