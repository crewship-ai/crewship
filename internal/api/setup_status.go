package api

import (
	"database/sql"
	"log/slog"
	"net/http"
)

// SetupStatusHandler answers a single question: does this Crewship
// install need to be bootstrapped, or is there already an admin?
// Unauthenticated by design — the answer is what tells the browser
// whether to render /login or /bootstrap, so it must be reachable
// before any session exists.
//
// The endpoint also surfaces whether public signup is enabled so the
// frontend can hide or show the "Don't have an account? Sign up" link
// without a second round-trip.
type SetupStatusHandler struct {
	db          *sql.DB
	logger      *slog.Logger
	allowSignup bool
}

// NewSetupStatusHandler builds a handler. allowSignup mirrors the
// flag the AuthHandler is constructed with so a single env-var
// (CREWSHIP_ALLOW_SIGNUP) is the source of truth.
func NewSetupStatusHandler(db *sql.DB, logger *slog.Logger, allowSignup bool) *SetupStatusHandler {
	return &SetupStatusHandler{db: db, logger: logger, allowSignup: allowSignup}
}

type setupStatusResponse struct {
	// NeedsBootstrap is true when the users table is empty. The
	// frontend routes the visitor to /bootstrap in that state
	// instead of /login.
	NeedsBootstrap bool `json:"needs_bootstrap"`
	// AllowSignup mirrors the server flag — when false, /login
	// hides the "Sign up" link because /api/v1/auth/signup would
	// reject the call anyway.
	AllowSignup bool `json:"allow_signup"`
}

// Status — GET /api/v1/system/setup-status (no auth)
//
// Errors are surfaced as needs_bootstrap=false so a transient DB
// blip doesn't ship the user into the bootstrap flow on an already-
// initialised server (the bootstrap POST itself enforces "no users
// exist", so a false positive here can't actually create a second
// admin — but the UX of bouncing the user into a setup wizard on a
// healthy install would still be confusing).
func (h *SetupStatusHandler) Status(w http.ResponseWriter, r *http.Request) {
	var count int
	if err := h.db.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM users").Scan(&count); err != nil {
		h.logger.Error("setup-status: count users", "error", err)
		writeJSON(w, http.StatusOK, setupStatusResponse{NeedsBootstrap: false, AllowSignup: h.allowSignup})
		return
	}
	writeJSON(w, http.StatusOK, setupStatusResponse{
		NeedsBootstrap: count == 0,
		AllowSignup:    h.allowSignup,
	})
}
