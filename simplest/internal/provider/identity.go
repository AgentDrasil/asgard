package provider

import (
	"net/http"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
)

// userAgentOverride holds an embedder-supplied User-Agent, accessed
// atomically because requests stream on their own goroutines while the
// setter may be called at any time. The setter (SetClientUserAgent) is the
// only public surface for changing it.
var userAgentOverride atomic.Value // holds string

// computedDefaultUserAgent caches the identity string once; it depends only
// on build info, GOOS, and the Go version, none of which change at runtime.
var computedDefaultUserAgent = sync.OnceValue(func() string {
	return formatUserAgent(resolveModuleVersion())
})

// SetClientUserAgent overrides the User-Agent sent with provider requests.
// An empty string restores the computed default at request time. Safe for
// concurrent use.
func SetClientUserAgent(ua string) { userAgentOverride.Store(ua) }

// formatUserAgent renders the default User-Agent string:
//
//	simplest/<version> (<goos>; <go version>)
func formatUserAgent(version string) string {
	return "simplest/" + version + " (" + runtime.GOOS + "; " + runtime.Version() + ")"
}

// defaultUserAgent returns the cached computed identity string.
func defaultUserAgent() string { return computedDefaultUserAgent() }

// resolveUserAgent returns the User-Agent to send: the override if non-empty,
// otherwise the computed default.
func resolveUserAgent() string {
	if ua, ok := userAgentOverride.Load().(string); ok && ua != "" {
		return ua
	}
	return defaultUserAgent()
}

// resolveModuleVersion inspects build info for the main simplest module and
// returns its version, falling back to "dev" during development builds.
func resolveModuleVersion() string {
	const modulePath = "github.com/AgentDrasil/asgard/simplest"
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	if info.Main.Version != "" && info.Main.Version != "(devel)" &&
		(info.Main.Path == modulePath || strings.HasSuffix(info.Main.Path, "/simplest")) {
		return info.Main.Version
	}
	for _, dep := range info.Deps {
		if dep.Path == modulePath && dep.Version != "" && dep.Version != "(devel)" {
			return dep.Version
		}
	}
	return "dev"
}

// applyUserAgent installs the default User-Agent on headers unless an
// override already provides one (case-insensitive), then overlays model
// headers so configured headers always win. headers must be non-nil.
func applyUserAgent(headers map[string]string, overrides map[string]string) map[string]string {
	if !hasHeaderKey(headers, "User-Agent") && !hasHeaderKey(overrides, "User-Agent") {
		headers["User-Agent"] = resolveUserAgent()
	}
	for k, v := range overrides {
		headers[k] = v
	}
	return headers
}

// genaiHeaders builds the HTTP headers for the Gemini client: the default
// User-Agent unless model.Headers provides one, with configured headers
// overlaid last so they always win.
func genaiHeaders(overrides map[string]string) http.Header {
	headers := http.Header{}
	if !hasHeaderKey(overrides, "User-Agent") {
		headers.Set("User-Agent", resolveUserAgent())
	}
	for k, v := range overrides {
		headers.Set(k, v)
	}
	return headers
}

// hasHeaderKey reports whether a string map contains key, ignoring case.
func hasHeaderKey(m map[string]string, key string) bool {
	for k := range m {
		if strings.EqualFold(k, key) {
			return true
		}
	}
	return false
}
