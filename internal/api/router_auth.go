package api

// Authentication, session, CLI-token, NextAuth, and onboarding routes.
// All routes that participate in the login / signup / token-refresh
// flow live here so the auth surface can be audited in one file.

import (
	"net/http"

	"github.com/crewship-ai/crewship/internal/mailer"
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
	r.authedSelfMut("POST", "/api/v1/onboarding/complete", onboarding.Complete)
	r.authedSelfMut("POST", "/api/v1/onboarding/setup", onboarding.Setup)
	// Session-scoped like the three routes above, not workspace-scoped like
	// the proposal routes below: the workspace it acts on is the caller's
	// own first workspace, resolved inside the handler exactly the way
	// Status/Complete/Setup already do, never a workspace_id the request
	// supplies (see onboarding_setup_agent.go's own doc comment).
	r.authedSelfMut("POST", "/api/v1/onboarding/setup-agent/start", onboarding.StartSetupAgent)

	// Auth (no auth required)
	// Stash the handler on the Router so server.New can arm the bootstrap
	// setup token (Patch C) against the same instance the mux dispatches
	// to. /api/v1/bootstrap is the deploy-race vector — the token gate on
	// that handler is the single point of defence.
	// One mailer for both auth surfaces: signup needs it for the
	// "you already have an account" notice that replaced the 409,
	// recovery for the reset link.
	mail := mailer.NewFromEnv()
	authH := NewAuthHandler(r.db, r.logger, r.authMw.validator, r.sessionsStore, r.allowSignup)
	authH.mail = mail
	r.authHandler = authH
	r.mux.HandleFunc("POST /api/v1/bootstrap", authH.Bootstrap)
	r.mux.HandleFunc("POST /api/v1/auth/signup", authH.Signup)
	r.mux.Handle("GET /api/v1/ws-token", authed(http.HandlerFunc(authH.WsToken)))

	// Password recovery (no auth required — token IS the credential).
	// Mailer reads RESEND_API_KEY / RESEND_FROM at startup; falls back
	// to mailer.Disabled which returns ErrDisabled on Send. /forgot
	// returns 200 either way (no enumeration); /reset is the
	// token-redemption endpoint.
	recoveryH := NewRecoveryHandler(r.db, r.logger, mail, r.sessionsStore)
	r.mux.HandleFunc("POST /api/v1/auth/forgot", recoveryH.Forgot)
	r.mux.HandleFunc("POST /api/v1/auth/reset", recoveryH.Reset)

	// Google OAuth2 — SWITCHED OFF (2026-07-27, Pavel's call).
	//
	// The redirect and callback routes are deliberately NOT registered, so
	// the flow is unreachable even on a box that still carries
	// GOOGLE_CLIENT_ID / GOOGLE_CLIENT_SECRET. Gating on Enabled() would
	// have let leftover config quietly switch it back on.
	//
	// Beyond "we don't want it": the flow created users with no
	// hashed_password (auth_google.go's INSERT sets email_verified and
	// never a password). An account somebody genuinely controls, holding
	// no password, is the shape that made the provisioning takeover fix
	// incomplete — see workspaces_provision.go. Removing the source stops
	// new ones; the predicate there still has to handle the accounts real
	// deployments already created.
	//
	// auth_google.go is left in the tree rather than deleted so turning this
	// back on is re-registering NewGoogleAuthHandler against the redirect and
	// callback handlers, not recovering a file from git — but nothing
	// references it while nothing is registered here.
	//
	// The registrations are NOT kept here commented out. cmd/gen-openapi is a
	// regex scan over this file's source and does not strip comments, so a
	// commented-out mux registration still lands in openapi.gen.json —
	// publishing routes that answer 404 to anyone generating a client or
	// fuzzing the spec. (Nor can this comment quote the call shape it is
	// warning about: doing so put the redirect route straight back into the
	// generated spec.)
	//
	// The status route stays and answers a flat false. 404ing it would
	// leave an older frontend build waiting on a request that errors; the
	// login page renders its Google button from this and needs a definite
	// "off".
	r.mux.HandleFunc("GET /api/v1/auth/google/status", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]bool{"enabled": false})
	})

	// Active sessions (auth required) — backs the Settings → Sessions
	// UI. List shows the caller's own; revoke flips revoked_at on a
	// session owned by the caller (or 404 to avoid enumeration).
	sessionsH := NewSessionsHandler(r.db, r.logger, r.sessionsStore)
	r.mux.Handle("GET /api/v1/auth/sessions", authed(http.HandlerFunc(sessionsH.List)))
	r.authedSelfMut("POST", "/api/v1/auth/sessions/{id}/revoke", sessionsH.Revoke)

	// Self-service profile (auth required, no workspace context) — edit
	// own name + change own password (#867.1). Password change revokes
	// the caller's other active sessions.
	profileH := NewUserProfileHandler(r.db, r.logger, r.sessionsStore)
	profileH.SetAvatarRoot(r.storagePath)
	r.authedSelfMut("PATCH", "/api/v1/users/me", profileH.UpdateProfile)
	r.authedSelfMut("POST", "/api/v1/users/me/password", profileH.ChangePassword)
	// Avatar (#889): upload/clear are self-scoped mutations; the serve route
	// is authed-only (any signed-in user fetches a member's avatar by id, so
	// rosters render — an unauth request 401s at RequireAuth).
	r.authedSelfMut("POST", "/api/v1/users/me/avatar", profileH.UploadAvatar)
	r.authedSelfMut("DELETE", "/api/v1/users/me/avatar", profileH.DeleteAvatar)
	r.mux.Handle("GET /api/v1/users/{id}/avatar", authed(http.HandlerFunc(profileH.ServeAvatar)))

	// CLI token management (auth required)
	cliTokenH := NewCLITokenHandler(r.db, r.logger)
	r.authedSelfMut("POST", "/api/v1/auth/cli-token", cliTokenH.Create)
	r.mux.Handle("GET /api/v1/auth/cli-token/validate", authed(http.HandlerFunc(cliTokenH.Validate)))
	r.mux.Handle("GET /api/v1/auth/cli-tokens", authed(http.HandlerFunc(cliTokenH.List)))
	r.authedSelfMut("DELETE", "/api/v1/auth/cli-tokens/{tokenId}", cliTokenH.Revoke)

	// CLI pairing — device-code handoff. Mounted under /api/v1/auth/
	// so it inherits the auth-tier rate limit (10 req/min/IP). /start
	// + /poll are session-authed (user must be logged in to issue a
	// code); /redeem is intentionally unauthenticated — the code IS
	// the credential, single-use, 10-min TTL.
	pairH := NewCliPairHandler(r.db, r.logger)
	r.authedSelfMut("POST", "/api/v1/auth/pair/start", pairH.Start)
	// #1824. Poll 400s without ?code, but the emptiness is decided inside
	// normalizePairingCode — a helper the inference will not look into, because
	// `f("") == ""` is a fact about that function rather than a property of the
	// shape. Declared here instead, and pinned like every inferred one.
	// openapi: query code:string!
	r.mux.Handle("GET /api/v1/auth/pair/poll", authed(http.HandlerFunc(pairH.Poll)))
	r.mux.HandleFunc("POST /api/v1/auth/pair/redeem", pairH.Redeem)

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
