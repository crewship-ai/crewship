package api

// OAuth flow handlers — Initiate / Callback / Exchange / Loopback +
// runLoopbackServer. Extracted from oauth.go for readability; the
// shared handler struct, provider table, and crypto helpers stay
// in the main file.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"html"

	"github.com/crewship-ai/crewship/internal/encryption"
)

func (h *OAuthHandler) Initiate(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())
	if !canRole(role, "manage") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}

	var req struct {
		CredentialID string `json:"credential_id"`
		RedirectURI  string `json:"redirect_uri"`
	}
	if err := readJSON(r, &req); err != nil {
		replyError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}
	if req.CredentialID == "" {
		replyError(w, http.StatusBadRequest, "credential_id is required")
		return
	}

	// Load credential OAuth config
	cred, err := h.loadOAuthCredential(r.Context(), req.CredentialID, workspaceID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			replyError(w, http.StatusNotFound, "OAuth credential not found")
		} else {
			h.logger.Error("load OAuth credential", "error", err)
			replyError(w, http.StatusInternalServerError, "Failed to load credential")
		}
		return
	}
	if cred.ClientID == "" || cred.AuthURL == "" {
		replyError(w, http.StatusBadRequest, "Credential missing oauth_client_id or oauth_auth_url")
		return
	}

	// Generate state token
	state, err := generateOAuthState()
	if err != nil {
		h.logger.Error("generate OAuth state", "error", err)
		replyError(w, http.StatusInternalServerError, "Failed to generate state token")
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
		replyError(w, http.StatusInternalServerError, "Failed to generate PKCE")
		return
	}

	// Store state + PKCE verifier for CSRF validation and token exchange
	if err := h.storeStateWithPKCE(r.Context(), state, req.CredentialID, workspaceID, redirectURI, codeVerifier); err != nil {
		h.logger.Error("store OAuth state", "error", err)
		replyError(w, http.StatusInternalServerError, "Failed to store OAuth state")
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

	// Atomically consume the state token (SELECT + DELETE in one operation to prevent race conditions).
	var credentialID, workspaceID, redirectURI, codeVerifier, createdAt string
	err := h.db.QueryRowContext(r.Context(),
		"DELETE FROM oauth_states WHERE state = ? RETURNING credential_id, workspace_id, redirect_uri, code_verifier, created_at", state).
		Scan(&credentialID, &workspaceID, &redirectURI, &codeVerifier, &createdAt)
	if err != nil {
		if err == sql.ErrNoRows {
			http.Error(w, "Invalid or expired state token", http.StatusBadRequest)
		} else {
			h.logger.Error("query oauth_states", "error", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
		}
		return
	}
	// V-13: Reject states older than 15 minutes
	if t, parseErr := time.Parse(time.RFC3339, createdAt); parseErr == nil {
		if time.Since(t) > 15*time.Minute {
			http.Error(w, "OAuth state expired", http.StatusBadRequest)
			return
		}
	}

	// V-14: Decrypt PKCE code_verifier
	if codeVerifier != "" {
		decrypted, decErr := encryption.Decrypt(codeVerifier)
		if decErr != nil {
			h.logger.Error("decrypt code_verifier", "error", decErr)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
		codeVerifier = decrypted
	}

	// Load credential OAuth config for token exchange
	cred, loadErr := h.loadOAuthCredential(r.Context(), credentialID, workspaceID)
	if loadErr != nil {
		if errors.Is(loadErr, sql.ErrNoRows) {
			http.Error(w, "Credential not found", http.StatusNotFound)
		} else {
			h.logger.Error("load OAuth credential in callback", "error", loadErr)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
		}
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
		replyError(w, http.StatusForbidden, "Forbidden")
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
		replyError(w, http.StatusBadRequest, "credential_id and code are required")
		return
	}

	// If no code_verifier provided but state is available, recover the
	// server-side PKCE verifier that was stored during Initiate().
	codeVerifier := req.CodeVerifier
	redirectURI := req.RedirectURI
	if codeVerifier == "" && req.State != "" {
		// Atomically consume the state (same as Callback handler)
		var storedVerifier, storedRedirectURI, storedCredentialID string
		err := h.db.QueryRowContext(r.Context(),
			"DELETE FROM oauth_states WHERE state = ? RETURNING code_verifier, redirect_uri, credential_id", req.State).
			Scan(&storedVerifier, &storedRedirectURI, &storedCredentialID)
		if err != nil {
			replyError(w, http.StatusBadRequest, "Invalid or expired OAuth state")
			return
		}
		// Validate that the state belongs to this credential
		if storedCredentialID != req.CredentialID {
			replyError(w, http.StatusBadRequest, "OAuth state does not match credential")
			return
		}
		// V-14: Decrypt stored PKCE verifier
		if storedVerifier != "" {
			decrypted, decErr := encryption.Decrypt(storedVerifier)
			if decErr != nil {
				replyError(w, http.StatusInternalServerError, "Failed to decrypt state")
				return
			}
			storedVerifier = decrypted
		}
		codeVerifier = storedVerifier
		if redirectURI == "" {
			redirectURI = storedRedirectURI
		}
	}

	// Load credential OAuth config
	cred, err := h.loadOAuthCredential(r.Context(), req.CredentialID, workspaceID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			replyError(w, http.StatusNotFound, "Credential not found")
		} else {
			h.logger.Error("load OAuth credential in exchange", "error", err)
			replyError(w, http.StatusInternalServerError, "Failed to load credential")
		}
		return
	}

	// Exchange code for tokens (with PKCE verifier if available)
	tokenResp, err := exchangeOAuthCode(r.Context(), cred.TokenURL, cred.ClientID, cred.ClientSecret, req.Code, redirectURI, codeVerifier)
	if err != nil {
		h.logger.Error("OAuth manual code exchange failed", "error", err, "credential_id", req.CredentialID)
		replyError(w, http.StatusBadGateway, "Token exchange failed")
		return
	}

	// Encrypt and store tokens
	if err := h.storeOAuthTokens(r.Context(), req.CredentialID, tokenResp); err != nil {
		h.logger.Error("store OAuth tokens from exchange", "error", err)
		replyError(w, http.StatusInternalServerError, "Failed to store tokens")
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
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}

	var req struct {
		CredentialID string `json:"credential_id"`
	}
	if err := readJSON(r, &req); err != nil || req.CredentialID == "" {
		replyError(w, http.StatusBadRequest, "credential_id is required")
		return
	}

	// Load credential OAuth config
	cred, err := h.loadOAuthCredential(r.Context(), req.CredentialID, workspaceID)
	if err != nil {
		replyError(w, http.StatusNotFound, "OAuth credential not found")
		return
	}
	if cred.ClientID == "" || cred.AuthURL == "" || cred.TokenURL == "" {
		replyError(w, http.StatusBadRequest, "Credential missing OAuth configuration")
		return
	}

	// Find a free port for the loopback server
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		h.logger.Error("find free port for OAuth loopback", "error", err)
		replyError(w, http.StatusInternalServerError, "Failed to start loopback server")
		return
	}
	port := listener.Addr().(*net.TCPAddr).Port
	redirectURI := fmt.Sprintf("http://127.0.0.1:%d/callback", port)

	// Generate state token
	state, err := generateOAuthState()
	if err != nil {
		listener.Close()
		replyError(w, http.StatusInternalServerError, "Failed to generate state")
		return
	}

	// Generate PKCE
	codeVerifier, codeChallenge, err := generatePKCE()
	if err != nil {
		listener.Close()
		replyError(w, http.StatusInternalServerError, "Failed to generate PKCE")
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
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
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
