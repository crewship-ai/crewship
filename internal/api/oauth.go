package api

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

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
	rand.Read(stateBytes)
	state := hex.EncodeToString(stateBytes)

	// Default redirect URI = Crewship backend callback
	redirectURI := req.RedirectURI
	if redirectURI == "" {
		// Auto-detect from request Host
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		redirectURI = fmt.Sprintf("%s://%s/api/v1/oauth/callback", scheme, r.Host)
	}

	// Store state for CSRF validation
	h.db.ExecContext(r.Context(),
		"INSERT INTO oauth_states (state, credential_id, workspace_id, redirect_uri) VALUES (?, ?, ?, ?)",
		state, req.CredentialID, workspaceID, redirectURI)

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
		http.Error(w, fmt.Sprintf("OAuth error: %s", errParam), http.StatusBadRequest)
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

	// Decrypt client secret
	clientSecret := ""
	if clientSecretEnc != "" {
		if decrypted, err := encryption.Decrypt(clientSecretEnc); err == nil {
			clientSecret = decrypted
		}
	}

	// Exchange code for tokens
	tokenResp, err := exchangeOAuthCode(tokenURL, clientID, clientSecret, code, redirectURI)
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
		encRefreshToken, _ = encryption.Encrypt(tokenResp.RefreshToken)
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

// tokenResponse holds the OAuth token exchange response.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
}

func exchangeOAuthCode(tokenURL, clientID, clientSecret, code, redirectURI string) (*tokenResponse, error) {
	data := url.Values{
		"grant_type":   {"authorization_code"},
		"code":         {code},
		"redirect_uri": {redirectURI},
		"client_id":    {clientID},
	}
	if clientSecret != "" {
		data.Set("client_secret", clientSecret)
	}

	req, err := http.NewRequest(http.MethodPost, tokenURL, strings.NewReader(data.Encode()))
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

		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				refreshExpiringTokens(db, hub, logger)
			}
		}
	}()
}

func refreshExpiringTokens(db *sql.DB, hub *ws.Hub, logger *slog.Logger) {
	// Find tokens expiring within 10 minutes
	threshold := time.Now().Add(10 * time.Minute).UTC().Format(time.RFC3339)
	rows, err := db.Query(`
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
			if d, err := encryption.Decrypt(clientSecretEnc); err == nil {
				clientSecret = d
			}
		}
		refreshToken := ""
		if d, err := encryption.Decrypt(refreshTokenEnc); err == nil {
			refreshToken = d
		}
		if refreshToken == "" {
			continue
		}

		// Refresh the token
		newToken, err := refreshOAuthToken(tokenURL, clientID, clientSecret, refreshToken)
		if err != nil {
			logger.Error("OAuth token refresh failed", "credential_id", id, "error", err)
			db.Exec("UPDATE credentials SET status = 'EXPIRED', updated_at = datetime('now') WHERE id = ?", id)
			if hub != nil {
				hub.Broadcast("workspace:"+wsID, ws.ServerMessage{
					Type: "credential.expired", Channel: "workspace:" + wsID,
					Payload: map[string]string{"credential_id": id, "reason": "OAuth token refresh failed"},
				})
			}
			continue
		}

		encAccess, _ := encryption.Encrypt(newToken.AccessToken)
		expiresAt := ""
		if newToken.ExpiresIn > 0 {
			expiresAt = time.Now().Add(time.Duration(newToken.ExpiresIn) * time.Second).UTC().Format(time.RFC3339)
		}

		// Update refresh token only if a new one was issued
		if newToken.RefreshToken != "" {
			encRefresh, _ := encryption.Encrypt(newToken.RefreshToken)
			db.Exec("UPDATE credentials SET encrypted_value = ?, oauth_refresh_token_enc = ?, oauth_token_expires_at = ?, updated_at = datetime('now') WHERE id = ?",
				encAccess, encRefresh, expiresAt, id)
		} else {
			db.Exec("UPDATE credentials SET encrypted_value = ?, oauth_token_expires_at = ?, updated_at = datetime('now') WHERE id = ?",
				encAccess, expiresAt, id)
		}

		logger.Info("OAuth token refreshed", "credential_id", id)
	}
}

func refreshOAuthToken(tokenURL, clientID, clientSecret, refreshToken string) (*tokenResponse, error) {
	data := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {clientID},
	}
	if clientSecret != "" {
		data.Set("client_secret", clientSecret)
	}

	req, err := http.NewRequest(http.MethodPost, tokenURL, strings.NewReader(data.Encode()))
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
