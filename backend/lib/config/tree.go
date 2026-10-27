package config

import (
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
	AntigravityOAuthToken []byte   `key:"antigravity-oauth-token"`
	GeminiAPIKey          string   `key:"gemini-api-key"`
	XAIAPIKey             string   `key:"xai-api-key"`
	SSH                   SSHScope `key:"ssh/"`
}

// SSHScope maps SSH credentials stored under credentials/ssh/.
type SSHScope struct {
	Key    []byte `key:"key"`
	KeyPub []byte `key:"key.pub"`
	Config []byte `key:"config"`
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
