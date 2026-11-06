package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"sort"
	"strings"

	awconfig "github.com/AgentDrasil/asgard/agentwrapper/config"
	"github.com/AgentDrasil/asgard/backend/lib/proxy"
)

// SimplestRawScope maps the simplest/ sub-namespace raw configuration bytes.
type SimplestRawScope struct {
	Models    []byte `key:"models.yaml"`
	Providers []byte `key:"providers.yaml"`
}

// CredentialsScope maps standard credentials stored under credentials/.
type CredentialsScope struct {
	Auth         []byte   `key:"auth.json"`
	Opencode     []byte   `key:"opencode/opencode.aw.yaml"`
	Simplest     []byte   `key:"simplest/key.yaml"`
	GeminiAPIKey string   `key:"gemini-api-key"`
	XAIAPIKey    string   `key:"xai-api-key"`
	GCPSAKey     []byte   `key:"gcp/gcp-sa.json"`
	SSH          SSHScope `key:"ssh/"`
}

// SSHManifestName is the Asgard-owned manifest file in ~/.ssh listing synced file names.
// It is never synced from etcd.
const SSHManifestName = ".asgard-managed"

// SSHFile is a single file under credentials/ssh/. The "." key binds the leaf
// key itself (path.Join(prefix, ".") == prefix), so any filename is supported.
type SSHFile struct {
	Data []byte `key:"."`
}

// SSHScope maps SSH credentials stored under credentials/ssh/. Any filename is allowed.
type SSHScope struct {
	Files map[string]SSHFile `key:"."`
}

// SafeSSHName reports whether name is a single, syncable path segment that also
// round-trips through the newline-separated ~/.ssh/.asgard-managed manifest.
func SafeSSHName(name string) bool {
	if name == "" || name == "." || name == ".." || name == SSHManifestName {
		return false
	}
	if strings.ContainsAny(name, "/\\\x00\n\r") || strings.TrimSpace(name) != name || strings.HasSuffix(name, ".sops.json") {
		return false
	}
	return true
}

// AllFiles returns a map of filename -> non-empty []byte for all syncable files.
func (s *SSHScope) AllFiles() map[string][]byte {
	m := make(map[string][]byte)
	if s == nil {
		return m
	}
	for name, f := range s.Files {
		if len(f.Data) > 0 && SafeSSHName(name) {
			m[name] = f.Data
		}
	}
	return m
}

// PrivateKeys returns filename -> []byte for private keys only: files that are not
// *.pub, config or known_hosts and whose content contains a PEM "-----BEGIN " header.
func (s *SSHScope) PrivateKeys() map[string][]byte {
	m := make(map[string][]byte)
	for name, data := range s.AllFiles() {
		if strings.HasSuffix(name, ".pub") || name == "config" || name == "known_hosts" {
			continue
		}
		if bytes.Contains(data, []byte("-----BEGIN ")) {
			m[name] = data
		}
	}
	return m
}

// HasKeys returns true if PrivateKeys() is non-empty.
func (s *SSHScope) HasKeys() bool {
	return len(s.PrivateKeys()) > 0
}

// Fingerprint returns a deterministic sha256 hash of sorted keys and contents for change detection.
// Uses length-prefix encoding (4-byte big-endian key length + key + 4-byte big-endian content length + content)
// to eliminate delimiter ambiguity.
func (s *SSHScope) Fingerprint() []byte {
	if s == nil {
		return nil
	}
	files := s.AllFiles()
	if len(files) == 0 {
		return nil
	}
	keys := make([]string, 0, len(files))
	for k := range files {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	var lenBuf [4]byte
	for _, k := range keys {
		binary.BigEndian.PutUint32(lenBuf[:], uint32(len(k)))
		h.Write(lenBuf[:])
		h.Write([]byte(k))
		data := files[k]
		binary.BigEndian.PutUint32(lenBuf[:], uint32(len(data)))
		h.Write(lenBuf[:])
		h.Write(data)
	}
	return h.Sum(nil)
}

// AsgardScope maps the asgard/ sub-namespace.
type AsgardScope struct {
	Config Config       `key:"config.yaml"`
	Proxy  proxy.Config `key:"proxy.yaml"`
}

// AsgardTreeConfig is the root configuration struct mapping the etcd prefix tree.
type AsgardTreeConfig struct {
	Asgard      AsgardScope      `key:"asgard/"`
	AW          awconfig.Config  `key:"aw/config.yaml"`
	Simplest    SimplestRawScope `key:"simplest/"`
	Credentials CredentialsScope `key:"credentials/"`
}
