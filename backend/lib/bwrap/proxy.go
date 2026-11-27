package bwrap

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/backend/lib/proxy"
	"github.com/AgentDrasil/asgard/fakebash"
	"github.com/AgentDrasil/asgard/pkg/paths"
	runnerpaths "github.com/AgentDrasil/asgard/pkg/paths/runner"
)

// ProxySandboxConfig defines proxy configuration options for Bubblewrap sandbox.
type ProxySandboxConfig struct {
	Enabled   bool
	ProxyAddr string            // e.g. "http://127.0.0.1:8082"
	CACert    string            // Host Asgard CA cert path (absolute)
	CAKey     string            // Host Asgard CA private key path (absolute)
	EnvVars   map[string]string // Custom environment variables (e.g. dummy secrets) to expose to sandbox
	// ExtraEnv are trusted per-run KEY=VALUE variables (run identity,
	// opts.Env) set inside the sandbox. It rides on this struct because every
	// sandbox constructor already takes it. Protected keys (HOME, PATH,
	// ASGARD_SANDBOXED, proxy/CA variables) are ignored.
	ExtraEnv []string
}

// appendProxySensitiveMaskArgs masks the Asgard config directory (which holds
// config.yaml and the CA private key) to prevent code inside the
// sandbox from reading sensitive credentials.
func appendProxySensitiveMaskArgs(args []string, caKey string) []string {
	configDir := paths.ConfigDir()
	if fi, err := os.Stat(configDir); err == nil && fi.IsDir() {
		args = append(args, "--tmpfs", configDir)
	}

	if caKey != "" {
		if fi, err := os.Stat(caKey); err == nil && !fi.IsDir() {
			args = append(args, "--ro-bind", "/dev/null", caKey)
		}
	} else {
		// Defense against leftover private keys in the default path when proxy is disabled
		defaultKey := paths.CAKeyFile()
		if fi, err := os.Stat(defaultKey); err == nil && !fi.IsDir() {
			args = append(args, "--ro-bind", "/dev/null", defaultKey)
		}
	}

	return args
}

// appendCrossSessionBindArgs mounts the cross-session data root (/session-data)
// into the sandbox when allowCrossSession is true.
func appendCrossSessionBindArgs(args []string) []string {
	dataDir := runnerpaths.DataDir()
	if _, err := os.Stat(dataDir); err == nil {
		args = append(args, "--bind", dataDir, dataDir)
	}
	return args
}

// appendCrossSessionMaskArgs hides the cross-session data root when it lies beneath
// home (e.g. in development/unprivileged fallback layout) to prevent leaking other sessions.
func appendCrossSessionMaskArgs(args []string, home string) []string {
	dataDir := runnerpaths.DataDir()
	if beneathAny([]string{home}, dataDir) {
		args = append(args, "--tmpfs", dataDir)
	}
	return args
}

// appendProxyArgs mounts the merged CA bundle and injects the protected proxy
// environment (and the custom proxy env vars) into a sandbox.
func appendProxyArgs(args []string, chatID string, proxyCfg ProxySandboxConfig) ([]string, error) {
	cID := chatID
	if cID == "" {
		cID = "default"
	}
	mergedBundlePath := filepath.Join(runnerpaths.CABundleDir(cID), "merged-ca-certificates.crt")
	if err := proxy.MergeCACert("/etc/ssl/certs/ca-certificates.crt", proxyCfg.CACert, mergedBundlePath); err != nil {
		return nil, fmt.Errorf("merging CA cert bundle: %w", err)
	}

	args = append(args, "--ro-bind", mergedBundlePath, "/etc/ssl/certs/ca-certificates.crt")
	if _, err := os.Stat("/etc/ssl/cert.pem"); err == nil {
		args = append(args, "--ro-bind", mergedBundlePath, "/etc/ssl/cert.pem")
	}

	// Inject protected proxy and CA bundle environment variables
	for _, k := range fakebash.ProtectedProxyEnvKeys {
		switch k {
		case "HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "ALL_PROXY", "all_proxy":
			if proxyCfg.ProxyAddr != "" {
				args = append(args, "--setenv", k, proxyCfg.ProxyAddr)
			}
		case "NO_PROXY", "no_proxy":
			args = append(args, "--setenv", k, "localhost,127.0.0.1")
		case "SSL_CERT_FILE", "REQUESTS_CA_BUNDLE", "NODE_EXTRA_CA_CERTS", "CURL_CA_BUNDLE":
			args = append(args, "--setenv", k, "/etc/ssl/certs/ca-certificates.crt")
		}
	}

	// Inject custom proxy env vars (dummy secrets exposed to command sandbox)
	for envKey, dummyVal := range proxyCfg.EnvVars {
		if strings.TrimSpace(envKey) != "" {
			log.Debug().Str("env_key", envKey).Str("masked_value", proxy.MaskSecret(dummyVal)).Msg("injecting proxy env into command sandbox")
			args = append(args, "--setenv", envKey, dummyVal)
		}
	}
	return args, nil
}
