package api

import (
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"golang.org/x/crypto/bcrypt"
)

// NextAuthHandler implements the endpoints that next-auth/react client SDK expects.
// This allows the static-exported Next.js frontend to use signIn(), signOut(), useSession().
type NextAuthHandler struct {
	db        *sql.DB
	logger    *slog.Logger
	validator *auth.JWTValidator
}

// NewNextAuthHandler creates a NextAuthHandler for compatibility with the next-auth client SDK.
func NewNextAuthHandler(db *sql.DB, logger *slog.Logger, validator *auth.JWTValidator) *NextAuthHandler {
	return &NextAuthHandler{db: db, logger: logger, validator: validator}
}

func (h *NextAuthHandler) csrfCookieName(r *http.Request) string {
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		return "__Host-authjs.csrf-token"
	}
	return "authjs.csrf-token"
}

func (h *NextAuthHandler) csrfToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("csrfToken: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func (h *NextAuthHandler) sessionCookieName(r *http.Request) string {
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		return "__Secure-authjs.session-token"
	}
	return "authjs.session-token"
}

// CSRF returns a CSRF token (GET /api/auth/csrf)
func (h *NextAuthHandler) CSRF(w http.ResponseWriter, r *http.Request) {
	token, err := h.csrfToken()
	if err != nil {
		h.logger.Error("generate csrf token", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	cookieName := h.csrfCookieName(r)
	isSecure := strings.HasPrefix(cookieName, "__Host-")
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   isSecure,
		SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, http.StatusOK, map[string]string{"csrfToken": token})
}

// Providers returns available auth providers (GET /api/auth/providers)
func (h *NextAuthHandler) Providers(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"credentials": map[string]interface{}{
			"id":          "credentials",
			"name":        "Credentials",
			"type":        "credentials",
			"signinUrl":   "/api/auth/callback/credentials",
			"callbackUrl": "/api/auth/callback/credentials",
		},
	})
}

// Session returns the current session (GET /api/auth/session)
func (h *NextAuthHandler) Session(w http.ResponseWriter, r *http.Request) {
	cookieName := h.sessionCookieName(r)
	cookie, err := r.Cookie(cookieName)
	if err != nil || cookie.Value == "" {
		writeJSON(w, http.StatusOK, map[string]interface{}{})
		return
	}

	claims, err := h.validator.Validate(cookie.Value)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{})
		return
	}

	expires := time.Unix(claims.Exp, 0).UTC().Format(time.RFC3339)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"user": map[string]interface{}{
			"id":    claims.ID,
			"name":  claims.Name,
			"email": claims.Email,
		},
		"expires": expires,
	})
}

// CallbackCredentials handles login (POST /api/auth/callback/credentials)
func (h *NextAuthHandler) CallbackCredentials(w http.ResponseWriter, r *http.Request) {
	csrfCookie, _ := r.Cookie(h.csrfCookieName(r))
	if csrfCookie == nil || csrfCookie.Value == "" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Missing CSRF token"})
		return
	}

	isJSON := strings.Contains(r.Header.Get("Content-Type"), "json")

	var email, password, csrfToken string
	if isJSON {
		var body map[string]interface{}
		if err := readJSON(r, &body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid request"})
			return
		}
		if v, ok := body["email"].(string); ok {
			email = v
		}
		if v, ok := body["password"].(string); ok {
			password = v
		}
		if v, ok := body["csrfToken"].(string); ok {
			csrfToken = v
		}
	} else {
		r.ParseForm()
		email = r.FormValue("email")
		password = r.FormValue("password")
		csrfToken = r.FormValue("csrfToken")
	}

	// Respond with JSON when any of these conditions are met:
	// - Content-Type is JSON
	// - form field json=true
	// - form field redirect=false (next-auth/react SDK convention)
	wantJSON := isJSON ||
		r.FormValue("json") == "true" ||
		r.FormValue("redirect") == "false"

	if subtle.ConstantTimeCompare([]byte(csrfToken), []byte(csrfCookie.Value)) != 1 {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Invalid CSRF token"})
		return
	}

	if email == "" || password == "" {
		if wantJSON {
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"error": "CredentialsSignin",
				"ok":    false,
				"url":   "/api/auth/error?error=CredentialsSignin",
			})
		} else {
			http.Redirect(w, r, "/login?error=CredentialsSignin", http.StatusFound)
		}
		return
	}

	var userID, fullName, hashedPw string
	err := h.db.QueryRowContext(r.Context(),
		"SELECT id, full_name, hashed_password FROM users WHERE email = ?", email,
	).Scan(&userID, &fullName, &hashedPw)
	if err != nil || bcrypt.CompareHashAndPassword([]byte(hashedPw), []byte(password)) != nil {
		if wantJSON {
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"error": "CredentialsSignin",
				"ok":    false,
				"url":   "/api/auth/error?error=CredentialsSignin",
			})
		} else {
			http.Redirect(w, r, "/login?error=CredentialsSignin", http.StatusFound)
		}
		return
	}

	token, err := h.validator.CreateToken(&auth.Claims{
		ID:    userID,
		Name:  fullName,
		Email: email,
	})
	if err != nil {
		h.logger.Error("create token", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	cookieName := h.sessionCookieName(r)
	isSecure := cookieName == "__Secure-authjs.session-token"
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   isSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   30 * 24 * 60 * 60, // 30 days
	})

	callbackUrl := r.FormValue("callbackUrl")
	if callbackUrl == "" {
		callbackUrl = "/"
	}
	// V-06: Prevent open redirect — only allow relative paths
	if !strings.HasPrefix(callbackUrl, "/") || strings.HasPrefix(callbackUrl, "//") {
		callbackUrl = "/"
	}

	if wantJSON {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"ok":     true,
			"url":    callbackUrl,
			"status": 200,
		})
	} else {
		http.Redirect(w, r, callbackUrl, http.StatusFound)
	}
}

// SignOut handles logout (POST /api/auth/signout)
func (h *NextAuthHandler) SignOut(w http.ResponseWriter, r *http.Request) {
	cookieName := h.sessionCookieName(r)
	isSecure := cookieName == "__Secure-authjs.session-token"
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   isSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})

	isJSON := strings.Contains(r.Header.Get("Accept"), "json") ||
		strings.Contains(r.Header.Get("Content-Type"), "json")

	if isJSON || r.Method == "POST" {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"url": "/login",
		})
	} else {
		http.Redirect(w, r, "/login", http.StatusFound)
	}
}

// SignIn redirects to the login page (GET /api/auth/signin)
func (h *NextAuthHandler) SignIn(w http.ResponseWriter, r *http.Request) {
	callbackUrl := r.URL.Query().Get("callbackUrl")
	if callbackUrl == "" {
		callbackUrl = "/"
	}
	// V-06: Prevent open redirect — only allow relative paths
	if !strings.HasPrefix(callbackUrl, "/") || strings.HasPrefix(callbackUrl, "//") {
		callbackUrl = "/"
	}
	http.Redirect(w, r, "/login?callbackUrl="+url.QueryEscape(callbackUrl), http.StatusFound)
}

// Error shows auth error (GET /api/auth/error)
func (h *NextAuthHandler) Error(w http.ResponseWriter, r *http.Request) {
	errType := r.URL.Query().Get("error")
	if errType == "" {
		errType = "Default"
	}
	msg := fmt.Sprintf("Authentication error: %s", errType)
	writeJSON(w, http.StatusOK, map[string]string{"error": errType, "message": msg})
}
