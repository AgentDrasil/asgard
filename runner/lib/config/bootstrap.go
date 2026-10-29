// Package config holds the runner's configuration: the local bootstrap
// settings needed to reach the backend, and the etcd-backed view of the shared
// configuration (agent definitions, aw, simplest, credentials, proxy).
package config

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
)

const (
	EnvBackend    = "ASGARD_RUNNER_BACKEND"
	EnvTLSCert    = "ASGARD_RUNNER_TLS_CERT"
	EnvTLSKey     = "ASGARD_RUNNER_TLS_KEY"
	EnvCA         = "ASGARD_RUNNER_CA"
	EnvServerName = "ASGARD_RUNNER_SERVER_NAME"
	// EnvAllowUnsandboxedCommands opts the runner in to workflow command
	// nodes that run without the bubblewrap sandbox ("1" or "true").
	EnvAllowUnsandboxedCommands = "ASGARD_RUNNER_ALLOW_UNSANDBOXED_COMMANDS"
)

// Bootstrap is the configuration a runner needs before it can reach etcd or
// the backend.
type Bootstrap struct {
	// Backend is the host:port of the backend's runner registry listener.
	Backend string
	// TLSCert and TLSKey are the runner's client certificate; its CN is the
	// runner's node id.
	TLSCert string
	TLSKey  string
	// CA is the PEM bundle used to verify the backend's certificate.
	CA string
	// ServerName overrides the name verified in the backend certificate
	// (default: the host part of Backend).
	ServerName string
	// AllowUnsandboxedCommands lets workflow command nodes that ask for
	// sandbox: false run directly with bash on this runner. Off by default:
	// commands then always run in the bubblewrap sandbox. Only the operator can
	// turn it on; a request never can.
	AllowUnsandboxedCommands bool
}

// ParseBootstrap reads the bootstrap settings from args (flags) with env as
// the fallback for flags that are not given.
func ParseBootstrap(args []string, getenv func(string) string) (*Bootstrap, error) {
	b := &Bootstrap{}
	fs := flag.NewFlagSet("asgard-runner", flag.ContinueOnError)
	fs.StringVar(&b.Backend, "backend", getenv(EnvBackend), "backend runner registry address (host:port) [$"+EnvBackend+"]")
	fs.StringVar(&b.TLSCert, "tls-cert", getenv(EnvTLSCert), "client certificate PEM; its CN is the node id [$"+EnvTLSCert+"]")
	fs.StringVar(&b.TLSKey, "tls-key", getenv(EnvTLSKey), "client private key PEM [$"+EnvTLSKey+"]")
	fs.StringVar(&b.CA, "ca", getenv(EnvCA), "CA bundle PEM verifying the backend [$"+EnvCA+"]")
	fs.StringVar(&b.ServerName, "server-name", getenv(EnvServerName), "expected backend certificate name (default: host of -backend) [$"+EnvServerName+"]")
	allowUnsandboxed, _ := strconv.ParseBool(getenv(EnvAllowUnsandboxedCommands))
	fs.BoolVar(&b.AllowUnsandboxedCommands, "allow-unsandboxed-commands", allowUnsandboxed, "let workflow command nodes with sandbox: false run without the sandbox (insecure; development only) [$"+EnvAllowUnsandboxedCommands+"]")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if err := b.Validate(); err != nil {
		return nil, err
	}
	return b, nil
}

// Validate checks that every required setting is present.
func (b *Bootstrap) Validate() error {
	var missing []string
	for _, f := range []struct{ name, val string }{
		{"backend", b.Backend}, {"tls-cert", b.TLSCert}, {"tls-key", b.TLSKey}, {"ca", b.CA},
	} {
		if strings.TrimSpace(f.val) == "" {
			missing = append(missing, "-"+f.name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required settings: %s", strings.Join(missing, ", "))
	}
	if _, _, err := net.SplitHostPort(b.Backend); err != nil {
		return fmt.Errorf("invalid backend address %q: %w", b.Backend, err)
	}
	return nil
}

// NodeID returns the runner's id: the CN of its client certificate.
func (b *Bootstrap) NodeID() (string, error) {
	cert, err := tls.LoadX509KeyPair(b.TLSCert, b.TLSKey)
	if err != nil {
		return "", fmt.Errorf("load client keypair: %w", err)
	}
	if len(cert.Certificate) == 0 {
		return "", errors.New("client certificate is empty")
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return "", fmt.Errorf("parse client certificate: %w", err)
	}
	if leaf.Subject.CommonName == "" {
		return "", errors.New("client certificate has no CN")
	}
	return leaf.Subject.CommonName, nil
}

// TLSConfig builds the mTLS client configuration. The files are re-read on
// every call so rotated certificates are picked up on reconnect.
func (b *Bootstrap) TLSConfig() (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(b.TLSCert, b.TLSKey)
	if err != nil {
		return nil, fmt.Errorf("load client keypair: %w", err)
	}
	caPEM, err := os.ReadFile(b.CA)
	if err != nil {
		return nil, fmt.Errorf("read ca: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("ca %s has no certificates", b.CA)
	}
	name := b.ServerName
	if name == "" {
		name, _, _ = net.SplitHostPort(b.Backend)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      pool,
		ServerName:   name,
		MinVersion:   tls.VersionTLS13,
	}, nil
}
