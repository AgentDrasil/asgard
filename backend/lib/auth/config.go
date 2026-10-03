package auth

import (
	"fmt"
	"net/url"
	"strings"
)

// Token source values accepted by Config.TokenSource.
const (
	TokenSourceAccessToken = "access_token"
	TokenSourceIDToken     = "id_token"
)

// DefaultRoleClaim is the claim path consulted when RoleClaim is unset.
const DefaultRoleClaim = "roles"

// Config is the optional auth section of the app configuration. A nil Config
// (or the absence of the auth section) disables authentication entirely.
type Config struct {
	// Issuer is the OIDC issuer URL. It must be an absolute URL and must match
	// the issuer advertised by the provider's discovery document.
	Issuer string `yaml:"issuer" json:"issuer"`
	// ClientID / ClientSecret are the OAuth client credentials registered with
	// the provider.
	ClientID     string `yaml:"client_id" json:"client_id"`
	ClientSecret string `yaml:"client_secret" json:"client_secret"`
	// TokenSource selects which credential the API middleware verifies:
	// TokenSourceAccessToken (default) or TokenSourceIDToken.
	//
	// access_token is the default because a refresh re-issues the access token
	// but usually does not re-issue the id token; verifying the access token
	// therefore keeps the session alive across refreshes. Providers whose access
	// token is not a signed JWT (e.g. Google) require TokenSourceIDToken.
	TokenSource string `yaml:"token_source" json:"token_source"`
	// TokenAudience is the expected "aud" when TokenSource is access_token. It
	// defaults to ClientID. Providers that issue access tokens with a different
	// audience (e.g. Keycloak's "account") must set it, otherwise every request
	// fails verification.
	TokenAudience string `yaml:"token_audience" json:"token_audience"`
	// RoleClaim is a dot path into the verified token's claims, e.g. "roles"
	// (default) or "realm_access.roles". The value may be a space/comma
	// separated string or an array of strings.
	RoleClaim string `yaml:"role_claim" json:"role_claim"`
	// RequiredRole, when non-empty, requires the RoleClaim value to contain
	// this role. Surrounding whitespace is ignored. When empty, role
	// validation is skipped entirely.
	RequiredRole string `yaml:"required_role" json:"required_role"`
	// AuthCodeOptions are extra query parameters appended to the authorization
	// request. This covers provider-specific requirements such as Google's
	// {"access_type": "offline", "prompt": "consent"} needed to obtain a refresh
	// token. Standard OIDC providers only need the offline_access scope.
	AuthCodeOptions map[string]string `yaml:"auth_code_options" json:"auth_code_options,omitempty"`
}

// Validate checks the auth configuration. The externally reachable base URL
// (config.Config.Host) is validated separately by the caller.
func (c *Config) Validate() error {
	if c == nil {
		return nil
	}
	if err := requireAbsoluteURL("auth issuer", c.Issuer); err != nil {
		return err
	}
	if c.ClientID == "" {
		return fmt.Errorf("auth client_id is required")
	}
	if c.ClientSecret == "" {
		return fmt.Errorf("auth client_secret is required")
	}
	switch c.TokenSource {
	case "", TokenSourceAccessToken, TokenSourceIDToken:
	default:
		return fmt.Errorf("auth token_source must be %q or %q, got %q",
			TokenSourceAccessToken, TokenSourceIDToken, c.TokenSource)
	}
	return nil
}

// TokenSourceOrDefault returns the effective token source.
func (c *Config) TokenSourceOrDefault() string {
	if c == nil || c.TokenSource == "" {
		return TokenSourceAccessToken
	}
	return c.TokenSource
}

// Audience returns the expected audience for access-token verification. It
// falls back to ClientID when unset.
func (c *Config) Audience() string {
	if c == nil {
		return ""
	}
	if c.TokenAudience == "" {
		return c.ClientID
	}
	return c.TokenAudience
}

// Claim returns the effective role claim path.
func (c *Config) Claim() string {
	if c == nil || c.RoleClaim == "" {
		return DefaultRoleClaim
	}
	return c.RoleClaim
}

// RequiredRoleTrimmed returns RequiredRole without surrounding whitespace.
func (c *Config) RequiredRoleTrimmed() string {
	if c == nil {
		return ""
	}
	return strings.TrimSpace(c.RequiredRole)
}

// requireAbsoluteURL rejects values that would silently produce a broken
// endpoint, such as a bare host name or a relative path.
func requireAbsoluteURL(name, value string) error {
	if value == "" {
		return fmt.Errorf("%s is required", name)
	}
	u, err := url.Parse(value)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("%s must be an absolute URL like https://host, got %q", name, value)
	}
	return nil
}
