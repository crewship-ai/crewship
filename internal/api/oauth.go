package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"time"

	"html"

	"github.com/crewship-ai/crewship/internal/encryption"
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

type OAuthHandler struct {
	db     *sql.DB
	hub    *ws.Hub
	logger *slog.Logger
}

func NewOAuthHandler(db *sql.DB, logger *slog.Logger) *OAuthHandler {
	return &OAuthHandler{db: db, logger: logger}
}

func (h *OAuthHandler) SetHub(hub *ws.Hub) { h.hub = hub }

// oauthCredConfig holds the decrypted OAuth configuration for a credential.
type oauthCredConfig struct {
	ClientID     string
	ClientSecret string // decrypted
	AuthURL      string
	TokenURL     string
	Scopes       string
}

// generateOAuthState produces a hex-encoded 16-byte random state token.
func generateOAuthState() (string, error) {
	stateBytes := make([]byte, 16)
	if _, err := rand.Read(stateBytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(stateBytes), nil
}

// buildOAuthURL constructs the full authorization URL with PKCE and standard params.
func buildOAuthURL(authURL, clientID, redirectURI, state, codeChallenge, scopes string) string {
	params := url.Values{
		"client_id":             {clientID},
		"redirect_uri":          {redirectURI},
		"response_type":         {"code"},
		"state":                 {state},
		"access_type":           {"offline"},
		"prompt":                {"consent"},
		"code_challenge":        {codeChallenge},
		"code_challenge_method": {"S256"},
	}
	if scopes != "" {
		params.Set("scope", scopes)
	}
	return authURL + "?" + params.Encode()
}

// loadOAuthCredential loads and decrypts the full OAuth configuration for a credential.
func (h *OAuthHandler) loadOAuthCredential(ctx context.Context, credID, wsID string) (*oauthCredConfig, error) {
	var clientID, clientSecretEnc, authURL, tokenURL, scopes string
	err := h.db.QueryRowContext(ctx, `
		SELECT oauth_client_id, COALESCE(oauth_client_secret_enc, ''),
			oauth_auth_url, oauth_token_url, COALESCE(oauth_scopes, '')
		FROM credentials WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`,
		credID, wsID).Scan(&clientID, &clientSecretEnc, &authURL, &tokenURL, &scopes)
	if err != nil {
		return nil, err
	}

	clientSecret := ""
	if clientSecretEnc != "" {
		decrypted, decErr := encryption.Decrypt(clientSecretEnc)
		if decErr != nil {
			return nil, fmt.Errorf("decrypt client secret: %w", decErr)
		}
		clientSecret = decrypted
	}

	return &oauthCredConfig{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		AuthURL:      authURL,
		TokenURL:     tokenURL,
		Scopes:       scopes,
	}, nil
}

// storeOAuthTokens encrypts and persists token response fields into the credentials table.
func (h *OAuthHandler) storeOAuthTokens(ctx context.Context, credID string, resp *tokenResponse) error {
	encAccessToken, err := encryption.Encrypt(resp.AccessToken)
	if err != nil {
		return fmt.Errorf("encrypt access token: %w", err)
	}

	var encRefreshToken string
	if resp.RefreshToken != "" {
		encRefreshToken, err = encryption.Encrypt(resp.RefreshToken)
		if err != nil {
			return fmt.Errorf("encrypt refresh token: %w", err)
		}
	}

	expiresAt := ""
	if resp.ExpiresIn > 0 {
		expiresAt = time.Now().Add(time.Duration(resp.ExpiresIn) * time.Second).UTC().Format(time.RFC3339)
	}

	_, err = h.db.ExecContext(ctx, `
		UPDATE credentials SET
			encrypted_value = ?,
			oauth_refresh_token_enc = CASE WHEN ? != '' THEN ? ELSE oauth_refresh_token_enc END,
			oauth_token_expires_at = ?,
			status = 'ACTIVE',
			updated_at = datetime('now')
		WHERE id = ?`,
		encAccessToken, encRefreshToken, encRefreshToken, expiresAt, credID)
	if err != nil {
		return fmt.Errorf("update credentials: %w", err)
	}
	return nil
}

// storeStateWithPKCE persists the OAuth state, credential context, and PKCE verifier.
func (h *OAuthHandler) storeStateWithPKCE(ctx context.Context, state, credID, wsID, redirectURI, codeVerifier string) error {
	_, err := h.db.ExecContext(ctx,
		"INSERT INTO oauth_states (state, credential_id, workspace_id, redirect_uri, code_verifier) VALUES (?, ?, ?, ?, ?)",
		state, credID, wsID, redirectURI, codeVerifier)
	return err
}

// ListProviders returns pre-configured OAuth providers.
func (h *OAuthHandler) ListProviders(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, OAuthProviders)
}

// Initiate starts the OAuth flow — generates auth URL for browser redirect.
func (h *OAuthHandler) Initiate(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())
	if !canRole(role, "manage") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
		return
	}

	var req struct {
		CredentialID string `json:"credential_id"`
		RedirectURI  string `json:"redirect_uri"`
	}
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON"})
		return
	}
	if req.CredentialID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "credential_id is required"})
		return
	}

	// Load credential OAuth config
	cred, err := h.loadOAuthCredential(r.Context(), req.CredentialID, workspaceID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "OAuth credential not found"})
		return
	}
	if cred.ClientID == "" || cred.AuthURL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Credential missing oauth_client_id or oauth_auth_url"})
		return
	}

	// Generate state token
	state, err := generateOAuthState()
	if err != nil {
		h.logger.Error("generate OAuth state", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to generate state token"})
		return
	}

	// Default redirect URI = Crewship backend callback
	redirectURI := req.RedirectURI
	if redirectURI == "" {
		scheme := "http"
		if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
			scheme = proto
		} else if r.TLS != nil {
			scheme = "https"
		}
		host := r.Host
		if fwdHost := r.Header.Get("X-Forwarded-Host"); fwdHost != "" {
			host = fwdHost
		}
		redirectURI = fmt.Sprintf("%s://%s/api/v1/oauth/callback", scheme, host)
	}

	// Generate PKCE
	codeVerifier, codeChallenge, err := generatePKCE()
	if err != nil {
		h.logger.Error("generate PKCE", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to generate PKCE"})
		return
	}

	// Store state + PKCE verifier for CSRF validation and token exchange
	if err := h.storeStateWithPKCE(r.Context(), state, req.CredentialID, workspaceID, redirectURI, codeVerifier); err != nil {
		h.logger.Error("store OAuth state", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to store OAuth state"})
		return
	}

	// Build auth URL with PKCE
	fullURL := buildOAuthURL(cred.AuthURL, cred.ClientID, redirectURI, state, codeChallenge, cred.Scopes)

	writeJSON(w, http.StatusOK, map[string]string{
		"auth_url": fullURL,
		"state":    state,
	})
}

// Callback handles the OAuth provider callback after user consent.
func (h *OAuthHandler) Callback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	errParam := r.URL.Query().Get("error")

	if errParam != "" {
		http.Error(w, fmt.Sprintf("OAuth error: %s", html.EscapeString(errParam)), http.StatusBadRequest)
		return
	}
	if code == "" || state == "" {
		http.Error(w, "Missing code or state parameter", http.StatusBadRequest)
		return
	}

	// Validate state token and retrieve PKCE verifier
	var credentialID, workspaceID, redirectURI, codeVerifier string
	err := h.db.QueryRowContext(r.Context(),
		"SELECT credential_id, workspace_id, redirect_uri, code_verifier FROM oauth_states WHERE state = ?", state).
		Scan(&credentialID, &workspaceID, &redirectURI, &codeVerifier)
	if err != nil {
		http.Error(w, "Invalid or expired state token", http.StatusBadRequest)
		return
	}

	// Delete used state to prevent replay
	if _, err := h.db.ExecContext(r.Context(), "DELETE FROM oauth_states WHERE state = ?", state); err != nil {
		h.logger.Error("delete used OAuth state", "error", err)
	}

	// Load credential OAuth config for token exchange
	cred, loadErr := h.loadOAuthCredential(r.Context(), credentialID, workspaceID)
	if loadErr != nil {
		http.Error(w, "Credential not found", http.StatusNotFound)
		return
	}

	// Exchange code for tokens (with PKCE verifier if present)
	tokenResp, err := exchangeOAuthCode(r.Context(), cred.TokenURL, cred.ClientID, cred.ClientSecret, code, redirectURI, codeVerifier)
	if err != nil {
		h.logger.Error("OAuth token exchange failed", "error", err, "credential_id", credentialID)
		http.Error(w, "Token exchange failed", http.StatusBadGateway)
		return
	}

	// Encrypt and store tokens
	if err := h.storeOAuthTokens(r.Context(), credentialID, tokenResp); err != nil {
		h.logger.Error("store OAuth tokens", "error", err)
		http.Error(w, "Failed to store tokens", http.StatusInternalServerError)
		return
	}

	h.logger.Info("OAuth tokens stored", "credential_id", credentialID)

	// Auto-bind credential to MCP server agent bindings.
	// Find MCP servers whose name matches the credential name prefix (e.g., "linear-oauth-xxx" → "linear")
	// and update any bindings that have no credential yet.
	h.autoBindCredentialToMCPServers(r.Context(), credentialID, workspaceID)

	// Redirect to frontend credentials page
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprintf(w, `<html><body><script>window.close(); window.opener && window.opener.location.reload();</script><p>Authorization successful! You can close this window.</p></body></html>`)
}

// Exchange handles manual code exchange — for when the automatic callback
// doesn't work (private IP, firewall, etc.). The frontend collects the
// authorization code and POSTs it here.
func (h *OAuthHandler) Exchange(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())
	if !canRole(role, "manage") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
		return
	}

	var req struct {
		CredentialID string `json:"credential_id"`
		Code         string `json:"code"`
		RedirectURI  string `json:"redirect_uri"`
		CodeVerifier string `json:"code_verifier"`
		State        string `json:"state"`
	}
	if err := readJSON(r, &req); err != nil || req.CredentialID == "" || req.Code == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "credential_id and code are required"})
		return
	}

	// If no code_verifier provided but state is available, recover the
	// server-side PKCE verifier that was stored during Initiate().
	codeVerifier := req.CodeVerifier
	if codeVerifier == "" && req.State != "" {
		var storedVerifier string
		err := h.db.QueryRowContext(r.Context(),
			"SELECT code_verifier FROM oauth_states WHERE state = ?", req.State).Scan(&storedVerifier)
		if err == nil {
			codeVerifier = storedVerifier
			// Delete used state to prevent replay
			if _, delErr := h.db.ExecContext(r.Context(), "DELETE FROM oauth_states WHERE state = ?", req.State); delErr != nil {
				h.logger.Error("delete used OAuth state in exchange", "error", delErr)
			}
		}
		// If state lookup fails, proceed without verifier (backward compat)
	}

	// Load credential OAuth config
	cred, err := h.loadOAuthCredential(r.Context(), req.CredentialID, workspaceID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Credential not found"})
		return
	}

	// Exchange code for tokens (with PKCE verifier if available)
	tokenResp, err := exchangeOAuthCode(r.Context(), cred.TokenURL, cred.ClientID, cred.ClientSecret, req.Code, req.RedirectURI, codeVerifier)
	if err != nil {
		h.logger.Error("OAuth manual code exchange failed", "error", err, "credential_id", req.CredentialID)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Token exchange failed"})
		return
	}

	// Encrypt and store tokens
	if err := h.storeOAuthTokens(r.Context(), req.CredentialID, tokenResp); err != nil {
		h.logger.Error("store OAuth tokens from exchange", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to store tokens"})
		return
	}

	h.logger.Info("OAuth tokens stored via manual exchange", "credential_id", req.CredentialID)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "credential_id": req.CredentialID})
}

// Loopback starts an OAuth flow using a temporary loopback HTTP server.
// This is the same approach as `gh auth login`, `gcloud auth login`, etc.
// Works on localhost without any domain or public IP — the browser and
// server are on the same machine.
func (h *OAuthHandler) Loopback(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())
	if !canRole(role, "manage") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
		return
	}

	var req struct {
		CredentialID string `json:"credential_id"`
	}
	if err := readJSON(r, &req); err != nil || req.CredentialID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "credential_id is required"})
		return
	}

	// Load credential OAuth config
	cred, err := h.loadOAuthCredential(r.Context(), req.CredentialID, workspaceID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "OAuth credential not found"})
		return
	}
	if cred.ClientID == "" || cred.AuthURL == "" || cred.TokenURL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Credential missing OAuth configuration"})
		return
	}

	// Find a free port for the loopback server
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		h.logger.Error("find free port for OAuth loopback", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to start loopback server"})
		return
	}
	port := listener.Addr().(*net.TCPAddr).Port
	redirectURI := fmt.Sprintf("http://127.0.0.1:%d/callback", port)

	// Generate state token
	state, err := generateOAuthState()
	if err != nil {
		listener.Close()
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to generate state"})
		return
	}

	// Generate PKCE
	codeVerifier, codeChallenge, err := generatePKCE()
	if err != nil {
		listener.Close()
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to generate PKCE"})
		return
	}

	// Build auth URL with PKCE
	fullAuthURL := buildOAuthURL(cred.AuthURL, cred.ClientID, redirectURI, state, codeChallenge, cred.Scopes)

	// Start loopback callback server in background
	credID := req.CredentialID
	go h.runLoopbackServer(listener, state, credID, workspaceID, cred.ClientID, cred.ClientSecret, cred.TokenURL, redirectURI, codeVerifier)

	h.logger.Info("OAuth loopback started", "port", port, "credential_id", credID)

	writeJSON(w, http.StatusOK, map[string]any{
		"auth_url":      fullAuthURL,
		"loopback_port": port,
		"state":         state,
	})
}

// runLoopbackServer handles the OAuth callback on a temporary loopback server.
func (h *OAuthHandler) runLoopbackServer(
	listener net.Listener,
	expectedState string,
	credentialID string,
	workspaceID string,
	clientID string,
	clientSecret string,
	tokenURL string,
	redirectURI string,
	codeVerifier string,
) {
	defer listener.Close()

	done := make(chan struct{})
	mux := http.NewServeMux()

	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		defer close(done)

		code := r.URL.Query().Get("code")
		state := r.URL.Query().Get("state")
		errParam := r.URL.Query().Get("error")

		if errParam != "" {
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprintf(w, `<html><body><h2>Authorization failed</h2><p>%s</p><p>You can close this window.</p></body></html>`, html.EscapeString(errParam))
			return
		}

		if code == "" || state != expectedState {
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<html><body><h2>Invalid callback</h2><p>Missing code or invalid state. You can close this window.</p></body></html>`)
			return
		}

		// Exchange code for tokens (with PKCE verifier)
		tokenResp, err := exchangeOAuthCode(r.Context(), tokenURL, clientID, clientSecret, code, redirectURI, codeVerifier)
		if err != nil {
			h.logger.Error("OAuth loopback token exchange", "error", err, "credential_id", credentialID)
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<html><body><h2>Token exchange failed</h2><p>Please check the server logs for details.</p><p>You can close this window.</p></body></html>`)
			return
		}

		// Encrypt and store
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := h.storeOAuthTokens(ctx, credentialID, tokenResp); err != nil {
			h.logger.Error("store loopback tokens", "error", err)
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<html><body><h2>Failed to store token</h2><p>You can close this window.</p></body></html>`)
			return
		}

		h.logger.Info("OAuth loopback completed", "credential_id", credentialID)
		h.autoBindCredentialToMCPServers(ctx, credentialID, workspaceID)
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><body><script>window.close()</script><h2>Authorization successful!</h2><p>You can close this window.</p></body></html>`)
	})

	server := &http.Server{Handler: mux}
	go func() {
		select {
		case <-done:
			// Give browser time to receive the response
			time.Sleep(500 * time.Millisecond)
		case <-time.After(120 * time.Second):
			h.logger.Warn("OAuth loopback timed out", "credential_id", credentialID)
		}
		server.Close()
	}()

	server.Serve(listener)
}

// Discover handles OAuth metadata discovery for an MCP server URL.
// Returns auth_url, token_url, registration_endpoint, and capabilities.
func (h *OAuthHandler) Discover(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MCPURL string `json:"mcp_url"`
	}
	if err := readJSON(r, &req); err != nil || req.MCPURL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "mcp_url is required"})
		return
	}

	discovered, err := discoverOAuthFromMCPURL(r.Context(), req.MCPURL)
	if err != nil {
		h.logger.Debug("OAuth discovery failed, trying known providers", "url", req.MCPURL, "error", err)
		// Fallback: check if URL matches a known provider
		if provider := matchKnownProvider(req.MCPURL); provider != nil {
			writeJSON(w, http.StatusOK, map[string]any{
				"auth_url":    provider.AuthURL,
				"token_url":   provider.TokenURL,
				"scopes":      provider.DefaultScopes,
				"supports_dcr": false,
				"supports_pkce": true,
				"source":       "known_provider",
			})
			return
		}
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Could not discover OAuth endpoints: " + err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"auth_url":              discovered.AuthURL,
		"token_url":             discovered.TokenURL,
		"registration_endpoint": discovered.RegistrationEndpoint,
		"scopes":                discovered.Scopes,
		"supports_dcr":          discovered.SupportsDCR,
		"supports_pkce":         discovered.SupportsPKCE,
		"source":                "discovery",
	})
}

// AutoConnect performs the OAuth auto-connect flow for an MCP server:
// 1. Discover OAuth endpoints (or use known provider)
// 2. Dynamic Client Registration (if supported)
// 3. Create OAUTH2 credential in PENDING state
// 4. Return auth URL for browser redirect (uses backend /api/v1/oauth/callback)
func (h *OAuthHandler) AutoConnect(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())
	if !canRole(role, "manage") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
		return
	}

	var req struct {
		MCPURL       string `json:"mcp_url"`
		ServerName   string `json:"server_name"`
		ProviderHint string `json:"provider_hint"`
	}
	if err := readJSON(r, &req); err != nil || req.MCPURL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "mcp_url is required"})
		return
	}
	if req.ServerName == "" {
		req.ServerName = "mcp-server"
	}

	// Step 1: Resolve OAuth endpoints
	var authURL, tokenURL, scopes string
	var registrationEndpoint string

	if req.ProviderHint != "" {
		if p, ok := OAuthProviders[req.ProviderHint]; ok {
			authURL = p.AuthURL
			tokenURL = p.TokenURL
			scopes = p.DefaultScopes
		}
	}
	if authURL == "" {
		discovered, err := discoverOAuthFromMCPURL(r.Context(), req.MCPURL)
		if err != nil {
			if provider := matchKnownProvider(req.MCPURL); provider != nil {
				authURL = provider.AuthURL
				tokenURL = provider.TokenURL
				scopes = provider.DefaultScopes
			} else {
				writeJSON(w, http.StatusBadRequest, map[string]string{
					"error": "Cannot discover OAuth for this URL. This provider may require a Personal API Key instead.",
				})
				return
			}
		} else {
			authURL = discovered.AuthURL
			tokenURL = discovered.TokenURL
			scopes = discovered.Scopes
			registrationEndpoint = discovered.RegistrationEndpoint
		}
	}

	// Step 2: Build redirect URI from request host (backend callback)
	scheme := "http"
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		scheme = proto
	} else if r.TLS != nil {
		scheme = "https"
	}
	host := r.Host
	if fwdHost := r.Header.Get("X-Forwarded-Host"); fwdHost != "" {
		host = fwdHost
	}
	redirectURI := fmt.Sprintf("%s://%s/api/v1/oauth/callback", scheme, host)

	// Step 3: Dynamic Client Registration (if available)
	var clientID, clientSecret string
	if registrationEndpoint != "" {
		dcr, err := dynamicClientRegister(r.Context(), registrationEndpoint, redirectURI)
		if err != nil {
			h.logger.Warn("DCR failed, returning needs_client_id", "error", err)
			writeJSON(w, http.StatusOK, map[string]any{
				"status":   "needs_client_id",
				"auth_url": authURL,
				"token_url": tokenURL,
				"scopes":   scopes,
				"message":  "Automatic registration not available. Please create an OAuth app and provide Client ID.",
			})
			return
		}
		clientID = dcr.ClientID
		clientSecret = dcr.ClientSecret
		h.logger.Info("DCR succeeded", "client_id", clientID, "server", req.ServerName)
	} else {
		// No DCR — return info so frontend can ask for Client ID
		writeJSON(w, http.StatusOK, map[string]any{
			"status":   "needs_client_id",
			"auth_url": authURL,
			"token_url": tokenURL,
			"scopes":   scopes,
			"message":  "This provider requires a Client ID. Create an OAuth app in the provider's settings.",
		})
		return
	}

	// Step 4: Create OAUTH2 credential in PENDING state
	credID := generateCUID()
	// Use a unique name to avoid conflicts with previous OAuth attempts
	credName := fmt.Sprintf("%s-oauth-%s", req.ServerName, credID[len(credID)-5:])
	user := UserFromContext(r.Context())

	var encSecret string
	if clientSecret != "" {
		var err error
		encSecret, err = encryption.Encrypt(clientSecret)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to encrypt client secret"})
			return
		}
	}

	if _, err := h.db.ExecContext(r.Context(), `
		INSERT INTO credentials (id, workspace_id, name, type, encrypted_value, status,
			oauth_client_id, oauth_client_secret_enc, oauth_auth_url, oauth_token_url, oauth_scopes,
			created_by, created_at, updated_at)
		VALUES (?, ?, ?, 'OAUTH2', '', 'PENDING', ?, ?, ?, ?, ?, ?, datetime('now'), datetime('now'))`,
		credID, workspaceID, credName, clientID, encSecret, authURL, tokenURL, scopes, user.ID); err != nil {
		h.logger.Error("create auto-connect credential", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to create credential"})
		return
	}

	// Step 5: Store CSRF state + PKCE
	state, err := generateOAuthState()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to generate state"})
		return
	}

	codeVerifier, codeChallenge, err := generatePKCE()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to generate PKCE"})
		return
	}

	if err := h.storeStateWithPKCE(r.Context(), state, credID, workspaceID, redirectURI, codeVerifier); err != nil {
		h.logger.Error("store OAuth state", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to store state"})
		return
	}

	// Step 6: Build auth URL with PKCE
	fullAuthURL := buildOAuthURL(authURL, clientID, redirectURI, state, codeChallenge, scopes)

	h.logger.Info("OAuth auto-connect ready", "server", req.ServerName, "credential_id", credID)

	writeJSON(w, http.StatusOK, map[string]any{
		"status":        "authorize",
		"auth_url":      fullAuthURL,
		"credential_id": credID,
	})
}
