package api

// OAuth credential persistence (loadOAuthCredential / storeOAuthTokens
// / storeStateWithPKCE) plus the Discover + AutoConnect endpoints that
// run after a successful flow. Extracted from oauth.go.

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/encryption"
)

type oauthCredConfig struct {
	ClientID     string
	ClientSecret string // decrypted
	AuthURL      string
	TokenURL     string
	Scopes       string
}

// generateOAuthState produces a hex-encoded 16-byte random state token.

func (h *OAuthHandler) loadOAuthCredential(ctx context.Context, credID, wsID string) (*oauthCredConfig, error) {
	var clientID, clientSecretEnc, authURL, tokenURL, scopes string
	err := h.db.QueryRowContext(ctx, `
		SELECT oauth_client_id, COALESCE(oauth_client_secret_enc, ''),
			oauth_auth_url, oauth_token_url, COALESCE(oauth_scopes, '')
		FROM credentials WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`,
		credID, wsID).Scan(&clientID, &clientSecretEnc, &authURL, &tokenURL, &scopes)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("credential not found: %w", err)
		}
		return nil, fmt.Errorf("load oauth credential: %w", err)
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
// V-14: The code_verifier is encrypted before storage for defense-in-depth.

func (h *OAuthHandler) storeStateWithPKCE(ctx context.Context, state, credID, wsID, redirectURI, codeVerifier string) error {
	encVerifier, err := encryption.Encrypt(codeVerifier)
	if err != nil {
		return fmt.Errorf("encrypt code_verifier: %w", err)
	}
	// V-13: Clean up expired states (older than 15 minutes)
	h.db.ExecContext(ctx, "DELETE FROM oauth_states WHERE created_at < datetime('now', '-15 minutes')")

	_, err = h.db.ExecContext(ctx,
		"INSERT INTO oauth_states (state, credential_id, workspace_id, redirect_uri, code_verifier) VALUES (?, ?, ?, ?, ?)",
		state, credID, wsID, redirectURI, encVerifier)
	return err
}

// ListProviders returns pre-configured OAuth providers.

func (h *OAuthHandler) Discover(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MCPURL string `json:"mcp_url"`
	}
	if err := readJSON(r, &req); err != nil || req.MCPURL == "" {
		replyError(w, http.StatusBadRequest, "mcp_url is required")
		return
	}

	discovered, err := discoverOAuthFromMCPURL(r.Context(), req.MCPURL)
	if err != nil {
		h.logger.Debug("OAuth discovery failed, trying known providers", "url", req.MCPURL, "error", err)
		// Fallback: check if URL matches a known provider
		if provider := matchKnownProvider(req.MCPURL); provider != nil {
			writeJSON(w, http.StatusOK, map[string]any{
				"auth_url":      provider.AuthURL,
				"token_url":     provider.TokenURL,
				"scopes":        provider.DefaultScopes,
				"supports_dcr":  false,
				"supports_pkce": true,
				"source":        "known_provider",
			})
			return
		}
		h.logger.Warn("OAuth discovery failed", "error", err)
		replyError(w, http.StatusNotFound, "Could not discover OAuth endpoints for this issuer")
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
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}

	var req struct {
		MCPURL       string `json:"mcp_url"`
		ServerName   string `json:"server_name"`
		ProviderHint string `json:"provider_hint"`
	}
	if err := readJSON(r, &req); err != nil || req.MCPURL == "" {
		replyError(w, http.StatusBadRequest, "mcp_url is required")
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
				"status":    "needs_client_id",
				"auth_url":  authURL,
				"token_url": tokenURL,
				"scopes":    scopes,
				"message":   "Automatic registration not available. Please create an OAuth app and provide Client ID.",
			})
			return
		}
		clientID = dcr.ClientID
		clientSecret = dcr.ClientSecret
		h.logger.Info("DCR succeeded", "client_id", clientID, "server", req.ServerName)
	} else {
		// No DCR — return info so frontend can ask for Client ID
		writeJSON(w, http.StatusOK, map[string]any{
			"status":    "needs_client_id",
			"auth_url":  authURL,
			"token_url": tokenURL,
			"scopes":    scopes,
			"message":   "This provider requires a Client ID. Create an OAuth app in the provider's settings.",
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
			replyError(w, http.StatusInternalServerError, "Failed to encrypt client secret")
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
		replyError(w, http.StatusInternalServerError, "Failed to create credential")
		return
	}

	// Step 5: Store CSRF state + PKCE
	state, err := generateOAuthState()
	if err != nil {
		replyError(w, http.StatusInternalServerError, "Failed to generate state")
		return
	}

	codeVerifier, codeChallenge, err := generatePKCE()
	if err != nil {
		replyError(w, http.StatusInternalServerError, "Failed to generate PKCE")
		return
	}

	if err := h.storeStateWithPKCE(r.Context(), state, credID, workspaceID, redirectURI, codeVerifier); err != nil {
		h.logger.Error("store OAuth state", "error", err)
		replyError(w, http.StatusInternalServerError, "Failed to store state")
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
