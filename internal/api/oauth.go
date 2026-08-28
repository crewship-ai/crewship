package api

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/crewship-ai/crewship/internal/ws"
)

// generatePKCE creates a PKCE code_verifier and S256 code_challenge per RFC 7636.
func generatePKCE() (verifier, challenge string, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	verifier = base64.RawURLEncoding.EncodeToString(buf)
	h := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(h[:])
	return verifier, challenge, nil
}

// OAuthProvider holds pre-configured OAuth provider endpoints.

type OAuthProvider struct {
	AuthURL       string `json:"auth_url"`
	TokenURL      string `json:"token_url"`
	DefaultScopes string `json:"default_scopes"`
}

// Well-known OAuth providers for easy setup.

var OAuthProviders = map[string]OAuthProvider{
	"google": {
		AuthURL:       "https://accounts.google.com/o/oauth2/v2/auth",
		TokenURL:      "https://oauth2.googleapis.com/token",
		DefaultScopes: "https://mail.google.com/ https://www.googleapis.com/auth/calendar https://www.googleapis.com/auth/drive",
	},
	"slack": {
		AuthURL:       "https://slack.com/oauth/v2/authorize",
		TokenURL:      "https://slack.com/api/oauth.v2.access",
		DefaultScopes: "channels:read chat:write",
	},
	"github": {
		AuthURL:       "https://github.com/login/oauth/authorize",
		TokenURL:      "https://github.com/login/oauth/access_token",
		DefaultScopes: "repo user",
	},
	"microsoft": {
		AuthURL:       "https://login.microsoftonline.com/common/oauth2/v2.0/authorize",
		TokenURL:      "https://login.microsoftonline.com/common/oauth2/v2.0/token",
		DefaultScopes: "Mail.Read Mail.Send Calendars.ReadWrite",
	},
	"linear": {
		AuthURL:       "https://linear.app/oauth/authorize",
		TokenURL:      "https://api.linear.app/oauth/token",
		DefaultScopes: "read write",
	},
	"gitlab": {
		AuthURL:       "https://gitlab.com/oauth/authorize",
		TokenURL:      "https://gitlab.com/oauth/token",
		DefaultScopes: "api read_user",
	},
	"cloudflare": {
		AuthURL:       "https://dash.cloudflare.com/oauth2/authorize",
		TokenURL:      "https://dash.cloudflare.com/oauth2/token",
		DefaultScopes: "",
	},
	"stripe": {
		AuthURL:       "https://connect.stripe.com/oauth/authorize",
		TokenURL:      "https://connect.stripe.com/oauth/token",
		DefaultScopes: "read_write",
	},
	"notion": {
		AuthURL:       "https://api.notion.com/v1/oauth/authorize",
		TokenURL:      "https://api.notion.com/v1/oauth/token",
		DefaultScopes: "",
	},
}

// OAuthHandler manages OAuth credential flows including authorization, callback, and token exchange.

type OAuthHandler struct {
	db     *sql.DB
	hub    *ws.Hub
	logger *slog.Logger
}

// NewOAuthHandler creates an OAuthHandler with the given database and logger.

func NewOAuthHandler(db *sql.DB, logger *slog.Logger) *OAuthHandler {
	return &OAuthHandler{db: db, logger: logger}
}

// SetHub attaches a WebSocket hub for broadcasting OAuth completion events to the UI.

func (h *OAuthHandler) SetHub(hub *ws.Hub) { h.hub = hub }

// oauthCredConfig holds the decrypted OAuth configuration for a credential.

func generateOAuthState() (string, error) {
	// 32 bytes = 256 bits. NIST recommends >=128 for CSRF nonces; we
	// pad to 256 so the state token has a 2x margin against future
	// crypto-analytic improvements. Audit M22.
	stateBytes := make([]byte, 32)
	if _, err := rand.Read(stateBytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(stateBytes), nil
}

// buildOAuthURL constructs the full authorization URL with PKCE and standard params.
// If authURL already contains query parameters, they are preserved and merged.
//
// resource is the RFC 8707 resource indicator — the canonical identifier of
// the MCP server this grant must be bound to, sourced from the credential's
// discovered RFC 9728 protected-resource metadata (never invented here). An
// empty resource omits the parameter, which keeps non-MCP OAuth credentials
// (plain provider connections that never went through MCP discovery)
// byte-for-byte unchanged.

func buildOAuthURL(authURL, clientID, redirectURI, state, codeChallenge, scopes, resource string) string {
	parsed, err := url.Parse(authURL)
	if err != nil {
		parsed = &url.URL{Path: authURL}
	}

	params := parsed.Query() // preserve existing query params
	params.Set("client_id", clientID)
	params.Set("redirect_uri", redirectURI)
	params.Set("response_type", "code")
	params.Set("state", state)
	params.Set("access_type", "offline")
	params.Set("prompt", "consent")
	params.Set("code_challenge", codeChallenge)
	params.Set("code_challenge_method", "S256")
	if scopes != "" {
		params.Set("scope", scopes)
	}
	if resource != "" {
		params.Set("resource", resource)
	}
	parsed.RawQuery = params.Encode()
	return parsed.String()
}

// validateIssuer implements the RFC 9207 mix-up defence: when the credential
// was connected through discovery and carries a known authorization-server
// issuer, the authorization response's `iss` parameter MUST be present and
// MUST match it — otherwise a different (possibly attacker-controlled)
// authorization server could complete the flow in place of the one the user
// actually consented at.
//
// expectedIssuer == "" means no issuer was recorded for this credential
// (manually configured OAuth, or a hardcoded single-AS provider from the
// OAuthProviders table) — there is nothing to bind the response to, so
// there is nothing to check. This keeps every non-MCP-discovery OAuth path
// working exactly as before.
func validateIssuer(expectedIssuer, gotIssuer string) error {
	if expectedIssuer == "" {
		return nil
	}
	if gotIssuer == "" {
		return fmt.Errorf("authorization response missing iss parameter, expected %q", expectedIssuer)
	}
	if gotIssuer != expectedIssuer {
		return fmt.Errorf("issuer mismatch: expected %q, got %q", expectedIssuer, gotIssuer)
	}
	return nil
}

// loadOAuthCredential loads and decrypts the full OAuth configuration for a credential.

func (h *OAuthHandler) ListProviders(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, OAuthProviders)
}

// Initiate starts the OAuth flow — generates auth URL for browser redirect.
