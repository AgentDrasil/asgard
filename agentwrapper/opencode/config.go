package opencode

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/goccy/go-yaml"

	awconfig "github.com/AgentDrasil/asgard/agentwrapper/config"
)

// EnvOpencodeAWConfig is the environment variable that can explicitly override
// the path to opencode.aw.yaml.
const EnvOpencodeAWConfig = "OPENCODE_AW_CONFIG"

// ConfigPath discovers the path to opencode.aw.yaml according to the resolution order:
// 1. OPENCODE_AW_CONFIG environment variable if set.
// 2. Directory of awconfig.Path() (e.g. /session/opencode.aw.yaml or ~/.config/aw/opencode.aw.yaml).
// 3. Fall back to standard ~/.config/aw/opencode.aw.yaml (or $XDG_CONFIG_HOME/aw/opencode.aw.yaml).
// If the discovered file does not exist, ConfigPath returns "" (unless explicitly set via OPENCODE_AW_CONFIG).
func ConfigPath() string {
	if p := os.Getenv(EnvOpencodeAWConfig); p != "" {
		return p
	}

	if awConfigPath, err := awconfig.Path(); err == nil && awConfigPath != "" {
		p := filepath.Join(filepath.Dir(awConfigPath), "opencode.aw.yaml")
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}

	var xdgDir string
	if configHome := os.Getenv("XDG_CONFIG_HOME"); configHome != "" {
		xdgDir = filepath.Join(configHome, "aw")
	} else if home, err := os.UserHomeDir(); err == nil && home != "" {
		xdgDir = filepath.Join(home, ".config", "aw")
	}
	if xdgDir != "" {
		p := filepath.Join(xdgDir, "opencode.aw.yaml")
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}

	return ""
}

// LoadEnv loads environment variables defined in opencode.aw.yaml.
// If the config file does not exist, it returns empty/nil without error (fallback mode).
func LoadEnv() (map[string]string, error) {
	path := ConfigPath()
	if path == "" {
		return nil, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	return ParseEnvConfig(data)
}

func sanitizeYAMLError(err error) error {
	if err == nil {
		return nil
	}
	var yamlErr yaml.Error
	if errors.As(err, &yamlErr) {
		if tok := yamlErr.GetToken(); tok != nil && tok.Position != nil {
			return fmt.Errorf("parsing opencode.aw.yaml: syntax error at line %d, column %d", tok.Position.Line, tok.Position.Column)
		}
	}
	return errors.New("parsing opencode.aw.yaml: invalid YAML syntax")
}

func isValidEnvKey(k string) bool {
	if k == "" {
		return false
	}
	for i, r := range k {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_' {
			continue
		}
		if i > 0 && (r >= '0' && r <= '9') {
			continue
		}
		return false
	}
	return true
}

// ParseEnvConfig parses YAML configuration bytes supporting both:
//  1. Nested env mapping:
//     env:
//     KEY: "val"
//  2. Flat key-value pairs:
//     KEY: "val"
//
// If both formats are present, keys inside the "env" section take precedence.
// Scalar values (string, integer, float, boolean) are converted to string values.
func ParseEnvConfig(data []byte) (map[string]string, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil
	}

	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, sanitizeYAMLError(err)
	}
	if raw == nil {
		return nil, nil
	}

	result := make(map[string]string)

	// First pass: flat key-value pairs (excluding "env")
	for k, v := range raw {
		if k == "env" {
			continue
		}
		if s, ok := scalarToString(v); ok {
			if !isValidEnvKey(k) {
				return nil, fmt.Errorf("invalid environment variable name %q in opencode.aw.yaml", k)
			}
			result[k] = s
		}
	}

	// Second pass: nested "env" map (takes precedence)
	if envVal, ok := raw["env"]; ok {
		if envMap, ok := envVal.(map[string]any); ok {
			for k, v := range envMap {
				if !isValidEnvKey(k) {
					return nil, fmt.Errorf("invalid environment variable name %q in opencode.aw.yaml", k)
				}
				if s, ok := scalarToString(v); ok {
					result[k] = s
				}
			}
		}
	}

	return result, nil
}

func scalarToString(v any) (string, bool) {
	if v == nil {
		return "", false
	}
	switch val := v.(type) {
	case string:
		return val, true
	case int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, float64, bool:
		return fmt.Sprint(val), true
	default:
		return "", false
	}
}

// mergeEnv merges extra key-value environment variables into base.
// Any existing keys in base that match keys in extra are overwritten.
// Keys in extra are sorted to provide deterministic output ordering.
func mergeEnv(base []string, extra map[string]string) []string {
	if len(extra) == 0 {
		return base
	}

	res := make([]string, 0, len(base)+len(extra))
	for _, entry := range base {
		k, _, ok := strings.Cut(entry, "=")
		if ok {
			if _, exists := extra[k]; exists {
				continue
			}
		}
		res = append(res, entry)
	}

	keys := make([]string, 0, len(extra))
	for k := range extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		res = append(res, fmt.Sprintf("%s=%s", k, extra[k]))
	}

	return res
}

// injectOpencodeEnv injects environment variables configured in opencode.aw.yaml into cmd.Env.
// If opencode.aw.yaml does not exist or has no variables configured, cmd.Env is left untouched.
func injectOpencodeEnv(cmd *exec.Cmd) error {
	extra, err := LoadEnv()
	if err != nil {
		return err
	}
	if len(extra) == 0 {
		return nil
	}
	base := cmd.Env
	if base == nil {
		base = os.Environ()
	}
	cmd.Env = mergeEnv(base, extra)
	return nil
}
