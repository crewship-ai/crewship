package api

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"github.com/crewship-ai/crewship/internal/auth"
)

// GoogleAuthHandler handles Google OAuth2 sign-in for users.
type GoogleAuthHandler struct {
	db        *sql.DB
	logger    *slog.Logger
	validator *auth.JWTValidator
	oauthCfg  *oauth2.Config
}

func NewGoogleAuthHandler(db *sql.DB, logger *slog.Logger, validator *auth.JWTValidator, clientID, clientSecret, baseURL string) *GoogleAuthHandler {
	return &GoogleAuthHandler{
		db:        db,
		logger:    logger,
		validator: validator,
		oauthCfg: &oauth2.Config{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			Endpoint:     google.Endpoint,
			RedirectURL:  baseURL + "/api/v1/auth/google/callback",
			Scopes:       []string{"openid", "email", "profile"},
		},
	}
}

// Enabled returns true if Google OAuth is configured.
func (h *GoogleAuthHandler) Enabled() bool {
	return h.oauthCfg.ClientID != "" && h.oauthCfg.ClientSecret != ""
}

// Redirect initiates the Google OAuth flow.
func (h *GoogleAuthHandler) Redirect(w http.ResponseWriter, r *http.Request) {
	if !h.Enabled() {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Google sign-in not configured"})
		return
	}

	b := make([]byte, 16)
	_, _ = rand.Read(b)
	state := base64.URLEncoding.EncodeToString(b)

	// Store state in DB for CSRF protection (single-use, validated on callback)
	redirect := r.URL.Query().Get("redirect")
	if redirect == "" {
		redirect = "/"
	}
	_, err := h.db.ExecContext(r.Context(),
		`INSERT INTO oauth_states (state, credential_id, workspace_id, redirect_uri) VALUES (?, '', '', ?)`,
		state, redirect)
	if err != nil {
		h.logger.Error("store oauth state", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	url := h.oauthCfg.AuthCodeURL(state, oauth2.AccessTypeOffline)
	http.Redirect(w, r, url, http.StatusTemporaryRedirect)
}

// googleUserInfo represents the response from Google's userinfo endpoint.
type googleUserInfo struct {
	Sub           string `json:"sub"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	Name          string `json:"name"`
	Picture       string `json:"picture"`
}

// Callback handles the Google OAuth callback.
func (h *GoogleAuthHandler) Callback(w http.ResponseWriter, r *http.Request) {
	if !h.Enabled() {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Google sign-in not configured"})
		return
	}

	state := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")

	if state == "" || code == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Missing state or code"})
		return
	}

	// Atomically consume the state token (prevents replay attacks)
	var redirectURI, createdAt string
	err := h.db.QueryRowContext(r.Context(),
		`DELETE FROM oauth_states WHERE state = ? RETURNING redirect_uri, created_at`,
		state).Scan(&redirectURI, &createdAt)
	if err != nil {
		h.logger.Warn("invalid oauth state", "error", err)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid or expired state"})
		return
	}

	// Reject states older than 15 minutes
	if t, parseErr := time.Parse(time.RFC3339, createdAt); parseErr == nil {
		if time.Since(t) > 15*time.Minute {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "OAuth state expired"})
			return
		}
	}

	// Exchange code for token
	token, err := h.oauthCfg.Exchange(r.Context(), code)
	if err != nil {
		h.logger.Error("oauth exchange failed", "error", err)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Failed to exchange authorization code"})
		return
	}

	// Fetch user info
	client := h.oauthCfg.Client(r.Context(), token)
	resp, err := client.Get("https://www.googleapis.com/oauth2/v3/userinfo")
	if err != nil {
		h.logger.Error("fetch google userinfo", "error", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Failed to fetch user info from Google"})
		return
	}
	defer resp.Body.Close()

	var userInfo googleUserInfo
	if err := json.NewDecoder(resp.Body).Decode(&userInfo); err != nil {
		h.logger.Error("decode google userinfo", "error", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Failed to decode user info"})
		return
	}

	// Find or create user
	userID, err := h.findOrCreateUser(r, userInfo, token)
	if err != nil {
		h.logger.Error("find or create user", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	// Set session cookie
	sessToken, err := h.validator.CreateToken(&auth.Claims{
		ID:    userID,
		Name:  userInfo.Name,
		Email: userInfo.Email,
	})
	if err != nil {
		h.logger.Error("create session token", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	cookieName := "authjs.session-token"
	isSecure := false
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		cookieName = "__Secure-authjs.session-token"
		isSecure = true
	}

	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    sessToken,
		Path:     "/",
		HttpOnly: true,
		Secure:   isSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   30 * 24 * 60 * 60,
	})

	// Redirect to dashboard
	target := "/"
	if redirectURI != "" {
		target = redirectURI
	}
	http.Redirect(w, r, target, http.StatusTemporaryRedirect)
}

func (h *GoogleAuthHandler) findOrCreateUser(r *http.Request, info googleUserInfo, token *oauth2.Token) (string, error) {
	ctx := r.Context()
	now := time.Now().UTC().Format(time.RFC3339)

	// Check if account already exists
	var existingUserID string
	err := h.db.QueryRowContext(ctx,
		`SELECT userId FROM accounts WHERE provider = 'google' AND providerAccountId = ?`,
		info.Sub).Scan(&existingUserID)
	if err == nil {
		// Update tokens
		h.db.ExecContext(ctx,
			`UPDATE accounts SET access_token = ?, refresh_token = ?, expires_at = ? WHERE provider = 'google' AND providerAccountId = ?`,
			token.AccessToken, token.RefreshToken, token.Expiry.Unix(), info.Sub)
		return existingUserID, nil
	}

	// Check if user with same email exists
	var userID string
	err = h.db.QueryRowContext(ctx, `SELECT id FROM users WHERE email = ?`, info.Email).Scan(&userID)
	if err == sql.ErrNoRows {
		// Create new user
		b := make([]byte, 16)
		_, _ = rand.Read(b)
		userID = hex.EncodeToString(b)
		_, err = h.db.ExecContext(ctx,
			`INSERT INTO users (id, email, full_name, avatar_url, email_verified, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			userID, info.Email, info.Name, info.Picture, now, now, now)
		if err != nil {
			return "", fmt.Errorf("create user: %w", err)
		}
	} else if err != nil {
		return "", fmt.Errorf("check user: %w", err)
	}

	// Link Google account
	accID := make([]byte, 16)
	_, _ = rand.Read(accID)
	_, err = h.db.ExecContext(ctx,
		`INSERT INTO accounts (id, userId, type, provider, providerAccountId, access_token, refresh_token, expires_at, token_type, scope) VALUES (?, ?, 'oauth', 'google', ?, ?, ?, ?, ?, ?)`,
		hex.EncodeToString(accID), userID, info.Sub, token.AccessToken, token.RefreshToken, token.Expiry.Unix(), token.TokenType, "openid email profile")
	if err != nil {
		return "", fmt.Errorf("link account: %w", err)
	}

	return userID, nil
}
