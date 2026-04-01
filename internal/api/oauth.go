package api

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"html"

	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/ws"
)

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
	var clientID, authURL, scopes string
	err := h.db.QueryRowContext(r.Context(), `
		SELECT oauth_client_id, oauth_auth_url, COALESCE(oauth_scopes, '')
		FROM credentials WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`,
		req.CredentialID, workspaceID).Scan(&clientID, &authURL, &scopes)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "OAuth credential not found"})
		return
	}
	if clientID == "" || authURL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Credential missing oauth_client_id or oauth_auth_url"})
		return
	}

	// Generate state token
	stateBytes := make([]byte, 16)
	if _, err := rand.Read(stateBytes); err != nil {
		h.logger.Error("generate OAuth state", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to generate state token"})
		return
	}
	state := hex.EncodeToString(stateBytes)

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

	// Store state for CSRF validation
	if _, err := h.db.ExecContext(r.Context(),
		"INSERT INTO oauth_states (state, credential_id, workspace_id, redirect_uri) VALUES (?, ?, ?, ?)",
		state, req.CredentialID, workspaceID, redirectURI); err != nil {
		h.logger.Error("store OAuth state", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to store OAuth state"})
		return
	}

	// Build auth URL
	params := url.Values{
		"client_id":     {clientID},
		"redirect_uri":  {redirectURI},
		"response_type": {"code"},
		"state":         {state},
		"access_type":   {"offline"}, // Request refresh token
		"prompt":        {"consent"},
	}
	if scopes != "" {
		params.Set("scope", scopes)
	}

	fullURL := authURL + "?" + params.Encode()

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

	// Validate state token
	var credentialID, workspaceID, redirectURI string
	err := h.db.QueryRowContext(r.Context(),
		"SELECT credential_id, workspace_id, redirect_uri FROM oauth_states WHERE state = ?", state).
		Scan(&credentialID, &workspaceID, &redirectURI)
	if err != nil {
		http.Error(w, "Invalid or expired state token", http.StatusBadRequest)
		return
	}

	// Delete used state
	h.db.ExecContext(r.Context(), "DELETE FROM oauth_states WHERE state = ?", state)

	// Load credential OAuth config for token exchange
	var clientID, clientSecretEnc, tokenURL string
	err = h.db.QueryRowContext(r.Context(), `
		SELECT oauth_client_id, COALESCE(oauth_client_secret_enc, ''), oauth_token_url
		FROM credentials WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`,
		credentialID, workspaceID).Scan(&clientID, &clientSecretEnc, &tokenURL)
	if err != nil {
		http.Error(w, "Credential not found", http.StatusNotFound)
		return
	}

	// Decrypt client secret — fail closed if decryption fails
	clientSecret := ""
	if clientSecretEnc != "" {
		decrypted, decErr := encryption.Decrypt(clientSecretEnc)
		if decErr != nil {
			h.logger.Error("decrypt OAuth client secret", "error", decErr, "credential_id", credentialID)
			http.Error(w, "Failed to decrypt client secret", http.StatusInternalServerError)
			return
		}
		clientSecret = decrypted
	}

	// Exchange code for tokens
	tokenResp, err := exchangeOAuthCode(r.Context(), tokenURL, clientID, clientSecret, code, redirectURI)
	if err != nil {
		h.logger.Error("OAuth token exchange failed", "error", err, "credential_id", credentialID)
		http.Error(w, fmt.Sprintf("Token exchange failed: %v", err), http.StatusBadGateway)
		return
	}

	// Encrypt and store tokens
	encAccessToken, err := encryption.Encrypt(tokenResp.AccessToken)
	if err != nil {
		http.Error(w, "Failed to encrypt token", http.StatusInternalServerError)
		return
	}

	var encRefreshToken string
	if tokenResp.RefreshToken != "" {
		encRefreshToken, err = encryption.Encrypt(tokenResp.RefreshToken)
		if err != nil {
			h.logger.Error("encrypt refresh token", "error", err)
			http.Error(w, "Failed to encrypt refresh token", http.StatusInternalServerError)
			return
		}
	}

	expiresAt := ""
	if tokenResp.ExpiresIn > 0 {
		expiresAt = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second).UTC().Format(time.RFC3339)
	}

	_, err = h.db.ExecContext(r.Context(), `
		UPDATE credentials SET
			encrypted_value = ?,
			oauth_refresh_token_enc = CASE WHEN ? != '' THEN ? ELSE oauth_refresh_token_enc END,
			oauth_token_expires_at = ?,
			status = 'ACTIVE',
			updated_at = datetime('now')
		WHERE id = ?`,
		encAccessToken, encRefreshToken, encRefreshToken, expiresAt, credentialID)
	if err != nil {
		h.logger.Error("store OAuth tokens", "error", err)
		http.Error(w, "Failed to store tokens", http.StatusInternalServerError)
		return
	}

	h.logger.Info("OAuth tokens stored", "credential_id", credentialID)

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
	}
	if err := readJSON(r, &req); err != nil || req.CredentialID == "" || req.Code == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "credential_id and code are required"})
		return
	}

	// Load credential OAuth config
	var clientID, clientSecretEnc, tokenURL string
	err := h.db.QueryRowContext(r.Context(), `
		SELECT oauth_client_id, COALESCE(oauth_client_secret_enc, ''), oauth_token_url
		FROM credentials WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`,
		req.CredentialID, workspaceID).Scan(&clientID, &clientSecretEnc, &tokenURL)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Credential not found"})
		return
	}

	clientSecret := ""
	if clientSecretEnc != "" {
		decrypted, decErr := encryption.Decrypt(clientSecretEnc)
		if decErr != nil {
			h.logger.Error("decrypt OAuth client secret", "error", decErr, "credential_id", req.CredentialID)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to decrypt client secret"})
			return
		}
		clientSecret = decrypted
	}

	// Exchange code for tokens
	tokenResp, err := exchangeOAuthCode(r.Context(), tokenURL, clientID, clientSecret, req.Code, req.RedirectURI)
	if err != nil {
		h.logger.Error("OAuth manual code exchange failed", "error", err, "credential_id", req.CredentialID)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Token exchange failed: " + err.Error()})
		return
	}

	// Encrypt and store tokens
	encAccessToken, err := encryption.Encrypt(tokenResp.AccessToken)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to encrypt token"})
		return
	}

	var encRefreshToken string
	if tokenResp.RefreshToken != "" {
		encRefreshToken, err = encryption.Encrypt(tokenResp.RefreshToken)
		if err != nil {
			h.logger.Error("encrypt refresh token", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to encrypt refresh token"})
			return
		}
	}

	expiresAt := ""
	if tokenResp.ExpiresIn > 0 {
		expiresAt = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second).UTC().Format(time.RFC3339)
	}

	_, err = h.db.ExecContext(r.Context(), `
		UPDATE credentials SET
			encrypted_value = ?,
			oauth_refresh_token_enc = CASE WHEN ? != '' THEN ? ELSE oauth_refresh_token_enc END,
			oauth_token_expires_at = ?,
			status = 'ACTIVE',
			updated_at = datetime('now')
		WHERE id = ?`,
		encAccessToken, encRefreshToken, encRefreshToken, expiresAt, req.CredentialID)
	if err != nil {
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
	var clientID, clientSecretEnc, authURL, tokenURL, scopes string
	err := h.db.QueryRowContext(r.Context(), `
		SELECT oauth_client_id, COALESCE(oauth_client_secret_enc, ''),
			oauth_auth_url, oauth_token_url, COALESCE(oauth_scopes, '')
		FROM credentials WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`,
		req.CredentialID, workspaceID).Scan(&clientID, &clientSecretEnc, &authURL, &tokenURL, &scopes)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "OAuth credential not found"})
		return
	}
	if clientID == "" || authURL == "" || tokenURL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Credential missing OAuth configuration"})
		return
	}

	// Decrypt client secret — fail closed
	clientSecret := ""
	if clientSecretEnc != "" {
		dec, decErr := encryption.Decrypt(clientSecretEnc)
		if decErr != nil {
			h.logger.Error("decrypt OAuth client secret for loopback", "error", decErr, "credential_id", req.CredentialID)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to decrypt client secret"})
			return
		}
		clientSecret = dec
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
	stateBytes := make([]byte, 16)
	if _, err := rand.Read(stateBytes); err != nil {
		listener.Close()
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to generate state"})
		return
	}
	state := hex.EncodeToString(stateBytes)

	// Build auth URL
	params := url.Values{
		"client_id":     {clientID},
		"redirect_uri":  {redirectURI},
		"response_type": {"code"},
		"state":         {state},
		"access_type":   {"offline"},
		"prompt":        {"consent"},
	}
	if scopes != "" {
		params.Set("scope", scopes)
	}
	fullAuthURL := authURL + "?" + params.Encode()

	// Start loopback callback server in background
	credID := req.CredentialID
	go h.runLoopbackServer(listener, state, credID, workspaceID, clientID, clientSecret, tokenURL, redirectURI)

	h.logger.Info("OAuth loopback started", "port", port, "credential_id", credID)

	writeJSON(w, http.StatusOK, map[string]interface{}{
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

		// Exchange code for tokens
		tokenResp, err := exchangeOAuthCode(r.Context(), tokenURL, clientID, clientSecret, code, redirectURI)
		if err != nil {
			h.logger.Error("OAuth loopback token exchange", "error", err, "credential_id", credentialID)
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprintf(w, `<html><body><h2>Token exchange failed</h2><p>%s</p><p>You can close this window.</p></body></html>`, html.EscapeString(err.Error()))
			return
		}

		// Encrypt and store
		encAccess, err := encryption.Encrypt(tokenResp.AccessToken)
		if err != nil {
			h.logger.Error("encrypt loopback token", "error", err)
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<html><body><h2>Failed to store token</h2><p>You can close this window.</p></body></html>`)
			return
		}

		var encRefresh string
		if tokenResp.RefreshToken != "" {
			encRefresh, err = encryption.Encrypt(tokenResp.RefreshToken)
			if err != nil {
				h.logger.Error("encrypt loopback refresh token", "error", err)
				w.Header().Set("Content-Type", "text/html")
				fmt.Fprint(w, `<html><body><h2>Failed to store refresh token</h2><p>You can close this window.</p></body></html>`)
				return
			}
		}

		expiresAt := ""
		if tokenResp.ExpiresIn > 0 {
			expiresAt = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second).UTC().Format(time.RFC3339)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		_, err = h.db.ExecContext(ctx, `
			UPDATE credentials SET
				encrypted_value = ?,
				oauth_refresh_token_enc = CASE WHEN ? != '' THEN ? ELSE oauth_refresh_token_enc END,
				oauth_token_expires_at = ?,
				status = 'ACTIVE',
				updated_at = datetime('now')
			WHERE id = ?`,
			encAccess, encRefresh, encRefresh, expiresAt, credentialID)
		if err != nil {
			h.logger.Error("store loopback tokens", "error", err)
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<html><body><h2>Failed to store token</h2><p>You can close this window.</p></body></html>`)
			return
		}

		h.logger.Info("OAuth loopback completed", "credential_id", credentialID)
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

// tokenResponse holds the OAuth token exchange response.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
}

func exchangeOAuthCode(ctx context.Context, tokenURL, clientID, clientSecret, code, redirectURI string) (*tokenResponse, error) {
	data := url.Values{
		"grant_type":   {"authorization_code"},
		"code":         {code},
		"redirect_uri": {redirectURI},
		"client_id":    {clientID},
	}
	if clientSecret != "" {
		data.Set("client_secret", clientSecret)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token request: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token endpoint returned %d: %s", resp.StatusCode, string(body))
	}

	var tokenResp tokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("parse token response: %w", err)
	}
	if tokenResp.AccessToken == "" {
		return nil, fmt.Errorf("empty access_token in response")
	}
	return &tokenResp, nil
}

// --- Token Refresh Worker ---

// StartOAuthRefreshWorker runs a background goroutine that refreshes expiring OAuth tokens.
func StartOAuthRefreshWorker(db *sql.DB, hub *ws.Hub, logger *slog.Logger, stop <-chan struct{}, wg *sync.WaitGroup) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()

		// Derive a context that cancels when stop is closed
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			<-stop
			cancel()
		}()

		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				refreshExpiringTokens(ctx, db, hub, logger)
			}
		}
	}()
}

func refreshExpiringTokens(ctx context.Context, db *sql.DB, hub *ws.Hub, logger *slog.Logger) {
	// Find tokens expiring within 10 minutes
	threshold := time.Now().Add(10 * time.Minute).UTC().Format(time.RFC3339)
	rows, err := db.QueryContext(ctx, `
		SELECT id, workspace_id, oauth_client_id, oauth_client_secret_enc, oauth_token_url, oauth_refresh_token_enc
		FROM credentials
		WHERE type = 'OAUTH2' AND status = 'ACTIVE'
			AND oauth_token_expires_at != '' AND oauth_token_expires_at < ?
			AND oauth_refresh_token_enc != '' AND deleted_at IS NULL`, threshold)
	if err != nil {
		logger.Error("query expiring OAuth tokens", "error", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var id, wsID, clientID, clientSecretEnc, tokenURL, refreshTokenEnc string
		if err := rows.Scan(&id, &wsID, &clientID, &clientSecretEnc, &tokenURL, &refreshTokenEnc); err != nil {
			continue
		}

		clientSecret := ""
		if clientSecretEnc != "" {
			d, decErr := encryption.Decrypt(clientSecretEnc)
			if decErr != nil {
				logger.Error("decrypt OAuth client secret during refresh", "credential_id", id, "error", decErr)
				continue
			}
			clientSecret = d
		}
		refreshToken := ""
		if d, err := encryption.Decrypt(refreshTokenEnc); err == nil {
			refreshToken = d
		}
		if refreshToken == "" {
			continue
		}

		// Refresh the token
		newToken, err := refreshOAuthToken(ctx, tokenURL, clientID, clientSecret, refreshToken)
		if err != nil {
			logger.Error("OAuth token refresh failed", "credential_id", id, "error", err)
			if _, dbErr := db.ExecContext(ctx, "UPDATE credentials SET status = 'EXPIRED', updated_at = datetime('now') WHERE id = ?", id); dbErr != nil {
				logger.Error("mark credential expired", "credential_id", id, "error", dbErr)
			}
			if hub != nil {
				hub.Broadcast("workspace:"+wsID, ws.ServerMessage{
					Type: "credential.expired", Channel: "workspace:" + wsID,
					Payload: map[string]string{"credential_id": id, "reason": "OAuth token refresh failed"},
				})
			}
			continue
		}

		encAccess, err := encryption.Encrypt(newToken.AccessToken)
		if err != nil {
			logger.Error("encrypt refreshed access token", "credential_id", id, "error", err)
			continue
		}
		expiresAt := ""
		if newToken.ExpiresIn > 0 {
			expiresAt = time.Now().Add(time.Duration(newToken.ExpiresIn) * time.Second).UTC().Format(time.RFC3339)
		}

		// Update refresh token only if a new one was issued
		if newToken.RefreshToken != "" {
			encRefresh, err := encryption.Encrypt(newToken.RefreshToken)
			if err != nil {
				logger.Error("encrypt refreshed refresh token", "credential_id", id, "error", err)
				continue
			}
			if _, err := db.ExecContext(ctx, "UPDATE credentials SET encrypted_value = ?, oauth_refresh_token_enc = ?, oauth_token_expires_at = ?, updated_at = datetime('now') WHERE id = ?",
				encAccess, encRefresh, expiresAt, id); err != nil {
				logger.Error("update refreshed tokens", "credential_id", id, "error", err)
				continue
			}
		} else {
			if _, err := db.ExecContext(ctx, "UPDATE credentials SET encrypted_value = ?, oauth_token_expires_at = ?, updated_at = datetime('now') WHERE id = ?",
				encAccess, expiresAt, id); err != nil {
				logger.Error("update refreshed token", "credential_id", id, "error", err)
				continue
			}
		}

		logger.Info("OAuth token refreshed", "credential_id", id)
	}
}

func refreshOAuthToken(ctx context.Context, tokenURL, clientID, clientSecret, refreshToken string) (*tokenResponse, error) {
	data := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {clientID},
	}
	if clientSecret != "" {
		data.Set("client_secret", clientSecret)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("refresh request: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("refresh endpoint returned %d: %s", resp.StatusCode, string(body))
	}

	var tokenResp tokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("parse refresh response: %w", err)
	}
	if tokenResp.AccessToken == "" {
		return nil, fmt.Errorf("empty access_token in refresh response")
	}
	return &tokenResp, nil
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
			writeJSON(w, http.StatusOK, map[string]interface{}{
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

	writeJSON(w, http.StatusOK, map[string]interface{}{
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
			writeJSON(w, http.StatusOK, map[string]interface{}{
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
		writeJSON(w, http.StatusOK, map[string]interface{}{
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

	// Step 5: Store CSRF state
	stateBytes := make([]byte, 16)
	if _, err := rand.Read(stateBytes); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to generate state"})
		return
	}
	state := hex.EncodeToString(stateBytes)

	if _, err := h.db.ExecContext(r.Context(),
		"INSERT INTO oauth_states (state, credential_id, workspace_id, redirect_uri) VALUES (?, ?, ?, ?)",
		state, credID, workspaceID, redirectURI); err != nil {
		h.logger.Error("store OAuth state", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to store state"})
		return
	}

	// Step 6: Build auth URL
	params := url.Values{
		"client_id":     {clientID},
		"redirect_uri":  {redirectURI},
		"response_type": {"code"},
		"state":         {state},
		"access_type":   {"offline"},
		"prompt":        {"consent"},
	}
	if scopes != "" {
		params.Set("scope", scopes)
	}
	fullAuthURL := authURL + "?" + params.Encode()

	h.logger.Info("OAuth auto-connect ready", "server", req.ServerName, "credential_id", credID)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":        "authorize",
		"auth_url":      fullAuthURL,
		"credential_id": credID,
	})
}

// matchKnownProvider checks if an MCP server URL matches a known OAuth provider.
func matchKnownProvider(mcpURL string) *OAuthProvider {
	urlPatterns := map[string]string{
		"linear.app":    "linear",
		"gitlab.com":    "gitlab",
		"cloudflare.com": "cloudflare",
		"stripe.com":    "stripe",
		"notion.com":    "notion",
		"github.com":    "github",
		"googleapis.com": "google",
	}
	lower := strings.ToLower(mcpURL)
	for domain, providerKey := range urlPatterns {
		if strings.Contains(lower, domain) {
			if p, ok := OAuthProviders[providerKey]; ok {
				return &p
			}
		}
	}
	return nil
}
