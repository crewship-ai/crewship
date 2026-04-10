package api

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/crewship-ai/crewship/internal/auth"
)

// AuthHandler provides user authentication endpoints including signup, login, and WebSocket token exchange.
type AuthHandler struct {
	db          *sql.DB
	logger      *slog.Logger
	validator   *auth.JWTValidator
	allowSignup bool
}

// NewAuthHandler creates an AuthHandler with the given dependencies and signup configuration.
func NewAuthHandler(db *sql.DB, logger *slog.Logger, validator *auth.JWTValidator, allowSignup bool) *AuthHandler {
	return &AuthHandler{db: db, logger: logger, validator: validator, allowSignup: allowSignup}
}

func (h *AuthHandler) setSessionCookie(w http.ResponseWriter, r *http.Request, userID, fullName, email string) error {
	token, err := h.validator.CreateToken(&auth.Claims{
		ID:    userID,
		Name:  fullName,
		Email: email,
	})
	if err != nil {
		return err
	}

	cookieName := "authjs.session-token"
	isSecure := false
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		cookieName = "__Secure-authjs.session-token"
		isSecure = true
	}

	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   isSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   30 * 24 * 60 * 60,
	})
	return nil
}

type signupRequest struct {
	FullName string `json:"full_name"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

var emailRegex = regexp.MustCompile(`^[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}$`)

// Signup registers a new user, creates their default workspace, and sets a session cookie.
// POST /api/v1/auth/signup — disabled when CREWSHIP_ALLOW_SIGNUP is false.
func (h *AuthHandler) Signup(w http.ResponseWriter, r *http.Request) {
	if !h.allowSignup {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Registration is disabled. Set CREWSHIP_ALLOW_SIGNUP=true to enable."})
		return
	}

	var req signupRequest
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON body"})
		return
	}

	if len(req.FullName) < 2 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Name must be at least 2 characters"})
		return
	}
	if !emailRegex.MatchString(req.Email) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid email address"})
		return
	}
	if len(req.Password) < 8 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Password must be at least 8 characters"})
		return
	}

	var existingID string
	err := h.db.QueryRowContext(r.Context(), "SELECT id FROM users WHERE email = ?", req.Email).Scan(&existingID)
	if err == nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "Email already registered"})
		return
	}
	if err != sql.ErrNoRows {
		h.logger.Error("check existing email", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	hashed, err := bcrypt.GenerateFromPassword([]byte(req.Password), 12)
	if err != nil {
		h.logger.Error("hash password", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	slugBase := strings.Split(req.Email, "@")[0]
	slugBase = regexp.MustCompile(`[^a-z0-9-]`).ReplaceAllString(strings.ToLower(slugBase), "-")

	now := time.Now().UTC().Format(time.RFC3339)
	userID := generateCUID()
	workspaceID := generateCUID()
	memberID := generateCUID()

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		h.logger.Error("begin tx", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(r.Context(),
		"INSERT INTO users (id, full_name, email, hashed_password, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)",
		userID, req.FullName, req.Email, string(hashed), now, now)
	if err != nil {
		h.logger.Error("insert user", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	slug := slugBase + "-" + workspaceID[:8]
	_, err = tx.ExecContext(r.Context(),
		"INSERT INTO workspaces (id, name, slug, created_at, updated_at) VALUES (?, ?, ?, ?, ?)",
		workspaceID, req.FullName+"'s Workspace", slug, now, now)
	if err != nil {
		h.logger.Error("insert workspace", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	_, err = tx.ExecContext(r.Context(),
		"INSERT INTO workspace_members (id, workspace_id, user_id, role, created_at) VALUES (?, ?, ?, ?, ?)",
		memberID, workspaceID, userID, "OWNER", now)
	if err != nil {
		h.logger.Error("insert membership", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	if err := tx.Commit(); err != nil {
		h.logger.Error("commit tx", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	if err := h.setSessionCookie(w, r, userID, req.FullName, req.Email); err != nil {
		h.logger.Error("set session cookie after signup", "error", err)
	}

	writeJSON(w, http.StatusCreated, map[string]string{"id": userID, "email": req.Email})
}

// Bootstrap creates the first admin user on an empty database.
// This endpoint is unauthenticated but only works when no users exist.
func (h *AuthHandler) Bootstrap(w http.ResponseWriter, r *http.Request) {
	var req signupRequest
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON body"})
		return
	}
	if len(req.FullName) < 2 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Name must be at least 2 characters"})
		return
	}
	if !emailRegex.MatchString(req.Email) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid email address"})
		return
	}
	if len(req.Password) < 8 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Password must be at least 8 characters"})
		return
	}

	hashed, err := bcrypt.GenerateFromPassword([]byte(req.Password), 12)
	if err != nil {
		h.logger.Error("bootstrap: hash password", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	slugBase := strings.Split(req.Email, "@")[0]
	slugBase = regexp.MustCompile(`[^a-z0-9-]`).ReplaceAllString(strings.ToLower(slugBase), "-")

	now := time.Now().UTC().Format(time.RFC3339)
	userID := generateCUID()
	workspaceID := generateCUID()
	memberID := generateCUID()

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		h.logger.Error("bootstrap: begin tx", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	defer tx.Rollback()

	// Check inside tx to eliminate TOCTOU race
	var userCount int
	if err := tx.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM users").Scan(&userCount); err != nil {
		h.logger.Error("bootstrap: count users", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	if userCount > 0 {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Already initialized — bootstrap is only available on an empty database"})
		return
	}

	_, err = tx.ExecContext(r.Context(),
		"INSERT INTO users (id, full_name, email, hashed_password, onboarding_completed, created_at, updated_at) VALUES (?, ?, ?, ?, 1, ?, ?)",
		userID, req.FullName, req.Email, string(hashed), now, now)
	if err != nil {
		h.logger.Error("bootstrap: insert user", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	slug := slugBase + "-" + workspaceID[:8]
	_, err = tx.ExecContext(r.Context(),
		"INSERT INTO workspaces (id, name, slug, created_at, updated_at) VALUES (?, ?, ?, ?, ?)",
		workspaceID, req.FullName+"'s Workspace", slug, now, now)
	if err != nil {
		h.logger.Error("bootstrap: insert workspace", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	_, err = tx.ExecContext(r.Context(),
		"INSERT INTO workspace_members (id, workspace_id, user_id, role, created_at) VALUES (?, ?, ?, ?, ?)",
		memberID, workspaceID, userID, "OWNER", now)
	if err != nil {
		h.logger.Error("bootstrap: insert membership", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	// Generate CLI token for immediate CLI access
	tokenBytes := make([]byte, 20)
	if _, err := rand.Read(tokenBytes); err != nil {
		h.logger.Error("bootstrap: generate token", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	cliToken := cliTokenPrefix + hex.EncodeToString(tokenBytes)
	tokenHash := sha256.Sum256([]byte(cliToken))
	tokenHashHex := hex.EncodeToString(tokenHash[:])
	tokenID := generateCUID()

	_, err = tx.ExecContext(r.Context(),
		"INSERT INTO cli_tokens (id, user_id, name, token_hash, created_at) VALUES (?, ?, ?, ?, ?)",
		tokenID, userID, "bootstrap", tokenHashHex, now)
	if err != nil {
		h.logger.Error("bootstrap: insert cli_token", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	if err := tx.Commit(); err != nil {
		h.logger.Error("bootstrap: commit", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	h.logger.Info("bootstrap: admin created", "email", req.Email, "workspace", slug)

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"user_id":      userID,
		"email":        req.Email,
		"workspace_id": workspaceID,
		"cli_token":    cliToken,
	})
}

// WsToken generates a short-lived JWT for authenticating WebSocket connections.
// POST /api/v1/auth/ws-token — works with both session cookies and CLI tokens.
func (h *AuthHandler) WsToken(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Unauthorized"})
		return
	}

	// If auth came from a CLI token (no session cookie), generate a short-lived JWE.
	token := extractToken(r)
	if IsCLIToken(token) {
		jweToken, err := h.validator.CreateToken(&auth.Claims{
			ID:    user.ID,
			Name:  user.Name,
			Email: user.Email,
			Exp:   time.Now().Add(15 * time.Minute).Unix(),
		})
		if err != nil {
			h.logger.Error("create WS token for CLI", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"token": jweToken})
		return
	}

	// V-11: Always generate a short-lived JWE for WebSocket auth,
	// instead of exposing the long-lived session cookie in query params.
	jweToken, err := h.validator.CreateToken(&auth.Claims{
		ID:    user.ID,
		Name:  user.Name,
		Email: user.Email,
		Exp:   time.Now().Add(15 * time.Minute).Unix(),
	})
	if err != nil {
		h.logger.Error("create WS token for browser session", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"token": jweToken})
}
