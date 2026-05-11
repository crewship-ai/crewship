package api

// Authentication, session, CLI-token, NextAuth, and onboarding routes.
// All routes that participate in the login / signup / token-refresh
// flow live here so the auth surface can be audited in one file.

import (
	"net/http"

	"github.com/crewship-ai/crewship/internal/services"
)

// registerAuthRoutes wires every authentication-adjacent endpoint —
// public bootstrap, Google OAuth2, active sessions, CLI tokens, the
// NextAuth-compatible /api/auth/* routes, and onboarding status.
func (r *Router) registerAuthRoutes() {
	authed := r.authMw.RequireAuth

	// Onboarding (require auth, no workspace context needed)
	onboardingSvc := services.NewOnboardingService(r.db, r.logger, generateCUID)
	onboarding := NewOnboardingHandler(r.db, onboardingSvc, r.logger)
	r.mux.Handle("GET /api/v1/onboarding/status", authed(http.HandlerFunc(onboarding.Status)))
	r.mux.Handle("POST /api/v1/onboarding/complete", authed(http.HandlerFunc(onboarding.Complete)))
	r.mux.Handle("POST /api/v1/onboarding/setup", authed(http.HandlerFunc(onboarding.Setup)))

	// Auth (no auth required)
	authH := NewAuthHandler(r.db, r.logger, r.authMw.validator, r.sessionsStore, r.allowSignup)
	r.mux.HandleFunc("POST /api/v1/bootstrap", authH.Bootstrap)
	r.mux.HandleFunc("POST /api/v1/auth/signup", authH.Signup)
	r.mux.Handle("GET /api/v1/ws-token", authed(http.HandlerFunc(authH.WsToken)))

	// Google OAuth2
	googleAuth := NewGoogleAuthHandler(r.db, r.logger, r.authMw.validator, r.sessionsStore, r.googleClientID, r.googleSecret, r.authBaseURL)
	if googleAuth.Enabled() {
		r.mux.HandleFunc("GET /api/v1/auth/google/redirect", googleAuth.Redirect)
		r.mux.HandleFunc("GET /api/v1/auth/google/callback", googleAuth.Callback)
	}
	r.mux.HandleFunc("GET /api/v1/auth/google/status", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]bool{"enabled": googleAuth.Enabled()})
	})

	// Active sessions (auth required) — backs the Settings → Sessions
	// UI. List shows the caller's own; revoke flips revoked_at on a
	// session owned by the caller (or 404 to avoid enumeration).
	sessionsH := NewSessionsHandler(r.db, r.logger, r.sessionsStore)
	r.mux.Handle("GET /api/v1/auth/sessions", authed(http.HandlerFunc(sessionsH.List)))
	r.mux.Handle("POST /api/v1/auth/sessions/{id}/revoke", authed(http.HandlerFunc(sessionsH.Revoke)))

	// CLI token management (auth required)
	cliTokenH := NewCLITokenHandler(r.db, r.logger)
	r.mux.Handle("POST /api/v1/auth/cli-token", authed(http.HandlerFunc(cliTokenH.Create)))
	r.mux.Handle("GET /api/v1/auth/cli-token/validate", authed(http.HandlerFunc(cliTokenH.Validate)))
	r.mux.Handle("GET /api/v1/auth/cli-tokens", authed(http.HandlerFunc(cliTokenH.List)))
	r.mux.Handle("DELETE /api/v1/auth/cli-tokens/{tokenId}", authed(http.HandlerFunc(cliTokenH.Revoke)))

	// Auth endpoints (no RBAC -- public access required for login/signup flow).
	// These intentionally bypass RequireAuth as they are the authentication
	// bootstrap endpoints that establish the session cookie.
	nextAuth := NewNextAuthHandler(r.db, r.logger, r.authMw.validator, r.sessionsStore)
	r.mux.HandleFunc("GET /api/auth/csrf", nextAuth.CSRF)
	r.mux.HandleFunc("GET /api/auth/providers", nextAuth.Providers)
	r.mux.HandleFunc("GET /api/auth/session", nextAuth.Session)
	r.mux.HandleFunc("POST /api/auth/callback/credentials", nextAuth.CallbackCredentials)
	r.mux.HandleFunc("POST /api/auth/token/refresh", nextAuth.RefreshToken)
	r.mux.HandleFunc("GET /api/auth/signin", nextAuth.SignIn)
	r.mux.HandleFunc("POST /api/auth/signout", nextAuth.SignOut)
	r.mux.HandleFunc("GET /api/auth/error", nextAuth.Error)
}
