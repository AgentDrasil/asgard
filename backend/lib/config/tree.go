package config

import (
	"crypto/sha256"
	"encoding/binary"
	"sort"

	awconfig "github.com/AgentDrasil/asgard/agentwrapper/config"
	"github.com/AgentDrasil/asgard/backend/lib/proxy"
)

// SimplestRawScope maps the simplest/ sub-namespace raw configuration bytes.
type SimplestRawScope struct {
	Key       []byte `key:"key.yaml"`
	Models    []byte `key:"models.yaml"`
	Providers []byte `key:"providers.yaml"`
}

// CredentialsScope maps standard credentials stored under credentials/.
type CredentialsScope struct {
	Auth                  []byte   `key:"auth.json"`
	OAuthCreds            []byte   `key:"agy/oauth_creds.json"`
	AntigravityOAuthToken []byte   `key:"agy/antigravity-oauth-token"`
	Claude                []byte   `key:"claude/.credentials.json"`
	GeminiAPIKey          string   `key:"gemini-api-key"`
	XAIAPIKey             string   `key:"xai-api-key"`
	GCPSAKey              []byte   `key:"gcp/gcp-sa.json"`
	SSH                   SSHScope `key:"ssh/"`
}

// SSHScope maps SSH credentials stored under credentials/ssh/.
type SSHScope struct {
	Key          []byte `key:"key"`
	KeyPub       []byte `key:"key.pub"`
	IdEd25519    []byte `key:"id_ed25519"`
	IdEd25519Pub []byte `key:"id_ed25519.pub"`
	IdRSA        []byte `key:"id_rsa"`
	IdRSAPub     []byte `key:"id_rsa.pub"`
	IdECDSA      []byte `key:"id_ecdsa"`
	IdECDSAPub   []byte `key:"id_ecdsa.pub"`
	IdDSA        []byte `key:"id_dsa"`
	IdDSAPub     []byte `key:"id_dsa.pub"`
	Config       []byte `key:"config"`
	KnownHosts   []byte `key:"known_hosts"`
}

// AllFiles returns a map of filename -> non-empty []byte for all fields.
func (s *SSHScope) AllFiles() map[string][]byte {
	m := make(map[string][]byte)
	if s == nil {
		return m
	}
	if len(s.Key) > 0 {
		m["key"] = s.Key
	}
	if len(s.KeyPub) > 0 {
		m["key.pub"] = s.KeyPub
	}
	if len(s.IdEd25519) > 0 {
		m["id_ed25519"] = s.IdEd25519
	}
	if len(s.IdEd25519Pub) > 0 {
		m["id_ed25519.pub"] = s.IdEd25519Pub
	}
	if len(s.IdRSA) > 0 {
		m["id_rsa"] = s.IdRSA
	}
	if len(s.IdRSAPub) > 0 {
		m["id_rsa.pub"] = s.IdRSAPub
	}
	if len(s.IdECDSA) > 0 {
		m["id_ecdsa"] = s.IdECDSA
	}
	if len(s.IdECDSAPub) > 0 {
		m["id_ecdsa.pub"] = s.IdECDSAPub
	}
	if len(s.IdDSA) > 0 {
		m["id_dsa"] = s.IdDSA
	}
	if len(s.IdDSAPub) > 0 {
		m["id_dsa.pub"] = s.IdDSAPub
	}
	if len(s.Config) > 0 {
		m["config"] = s.Config
	}
	if len(s.KnownHosts) > 0 {
		m["known_hosts"] = s.KnownHosts
	}
	return m
}

// PrivateKeys returns a map of filename -> []byte for private keys only (excluding .pub, config, known_hosts).
func (s *SSHScope) PrivateKeys() map[string][]byte {
	m := make(map[string][]byte)
	if s == nil {
		return m
	}
	if len(s.Key) > 0 {
		m["key"] = s.Key
	}
	if len(s.IdEd25519) > 0 {
		m["id_ed25519"] = s.IdEd25519
	}
	if len(s.IdRSA) > 0 {
		m["id_rsa"] = s.IdRSA
	}
	if len(s.IdECDSA) > 0 {
		m["id_ecdsa"] = s.IdECDSA
	}
	if len(s.IdDSA) > 0 {
		m["id_dsa"] = s.IdDSA
	}
	return m
}

// HasKeys returns true if PrivateKeys() is non-empty.
func (s *SSHScope) HasKeys() bool {
	if s == nil {
		return false
	}
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
	OpencodeAW  []byte           `key:"aw/opencode.aw.yaml"`
}
