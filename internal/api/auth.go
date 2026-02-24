package api

import (
	"database/sql"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/crewship-ai/crewship/internal/auth"
)

type AuthHandler struct {
	db        *sql.DB
	logger    *slog.Logger
	validator *auth.JWTValidator
}

func NewAuthHandler(db *sql.DB, logger *slog.Logger, validator *auth.JWTValidator) *AuthHandler {
	return &AuthHandler{db: db, logger: logger, validator: validator}
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

func (h *AuthHandler) Signup(w http.ResponseWriter, r *http.Request) {
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

	// Return the session cookie value as the ws token.
	// The WebSocket hub validates it using the same JWTValidator.
	for _, name := range []string{"__Secure-authjs.session-token", "authjs.session-token"} {
		if c, err := r.Cookie(name); err == nil && c.Value != "" {
			writeJSON(w, http.StatusOK, map[string]string{"token": c.Value})
			return
		}
	}
	writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Unauthorized"})
}
