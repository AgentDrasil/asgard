// Package version exposes the build version of the asgard binaries.
package version

// Version is the build version, formatted as "<UTC build time>:<short commit>"
// (e.g. "2026-01-02-15-23-30:abc123ed"). It is overridden at link time with
// -ldflags "-X github.com/AgentDrasil/asgard/pkg/version.Version=...".
var Version = "dev"

// Get returns the build version.
func Get() string {
	return Version
}
