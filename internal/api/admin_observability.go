package api

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/diskusage"
	"github.com/crewship-ai/crewship/internal/logging"
	"github.com/crewship-ai/crewship/internal/secrets"
)

// AdminObservabilityHandler serves the operator-facing observability surface:
// a runtime log-level toggle (flip a live instance to debug to catch a repro,
// then let it auto-revert) and a health read that surfaces disk headroom —
// the signal that would have flagged the log-volume fill before it hit 100%.
type AdminObservabilityHandler struct {
	logger    *slog.Logger
	db        *sql.DB // for the Health DB-liveness probe; may be nil in unit tests that don't exercise it
	startedAt time.Time
	// diskUsage and dataDir are injected so Health depends on provider
	// functions rather than reaching into the filesystem directly — keeps
	// the handler testable/mockable and matches the repo's "no direct infra
	// access from handlers" convention. Defaulted to the real implementations
	// in the constructor.
	diskUsage func(path string) (diskusage.Stats, error)
	dataDir   func() (string, error)
}

func NewAdminObservabilityHandler(db *sql.DB, logger *slog.Logger) *AdminObservabilityHandler {
	return &AdminObservabilityHandler{
		logger:    logger,
		db:        db,
		startedAt: time.Now(),
		diskUsage: diskusage.Usage,
		dataDir: func() (string, error) {
			d, err := database.DefaultDataDir()
			if err != nil {
				return "", err
			}
			return d.Root, nil
		},
	}
}

// logLevelResponse is the wire shape for GET/PUT /api/v1/admin/log-level.
type logLevelResponse struct {
	Level     string  `json:"level"`
	Baseline  string  `json:"baseline"`
	ExpiresAt *string `json:"expires_at,omitempty"` // RFC3339; nil when no timed override is active
}

func currentLogLevel() logLevelResponse {
	cur, base, exp := logging.LevelState()
	resp := logLevelResponse{Level: cur, Baseline: base}
	if !exp.IsZero() {
		s := exp.UTC().Format(time.RFC3339)
		resp.ExpiresAt = &s
	}
	return resp
}

// GetLogLevel returns the live level, the configured baseline, and any
// override expiry. GET /api/v1/admin/log-level.
func (h *AdminObservabilityHandler) GetLogLevel(w http.ResponseWriter, r *http.Request) {
	if !canRole(RoleFromContext(r.Context()), "manage") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}
	writeJSON(w, http.StatusOK, currentLogLevel())
}

// SetLogLevel overrides the process log level at runtime. PUT
// /api/v1/admin/log-level with {"level":"debug","ttl_seconds":900}. A
// positive ttl auto-reverts to the baseline (capped so a forgotten debug
// switch can't firehose the logs indefinitely — itself a disk-fill risk).
func (h *AdminObservabilityHandler) SetLogLevel(w http.ResponseWriter, r *http.Request) {
	if !canRole(RoleFromContext(r.Context()), "manage") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}
	var body struct {
		Level      string `json:"level"`
		TTLSeconds int    `json:"ttl_seconds"`
	}
	if err := readJSON(r, &body); err != nil {
		replyError(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	// Clamp the raw seconds BEFORE converting: time.Duration(n)*time.Second
	// overflows int64 for extreme n and can wrap to a small/negative value
	// that slips past a post-multiply cap. 0 = no expiry (until next change).
	const maxTTLSeconds = int(24 * time.Hour / time.Second)
	if body.TTLSeconds < 0 {
		body.TTLSeconds = 0
	} else if body.TTLSeconds > maxTTLSeconds {
		body.TTLSeconds = maxTTLSeconds
	}
	ttl := time.Duration(body.TTLSeconds) * time.Second
	prev, err := logging.SetLevel(body.Level, ttl)
	if err != nil {
		replyError(w, http.StatusBadRequest, err.Error())
		return
	}
	actor := ""
	if u := UserFromContext(r.Context()); u != nil {
		actor = u.ID
	}
	// Log the change at WARN so it lands even when the instance was just
	// quieted to warn/error — an operator flipping verbosity is an event
	// worth an audit line regardless of the level they set.
	h.logger.Warn("log level changed via admin API",
		"new_level", body.Level, "previous", prev,
		"ttl_seconds", int(ttl.Seconds()), "actor", actor)
	writeJSON(w, http.StatusOK, currentLogLevel())
}

// Health reports process uptime, the current log level, and disk headroom for
// the data-dir volume. GET /api/v1/admin/health.
//
// Disk is resolved from the default data dir; a custom DATABASE_URL on a
// different volume isn't reflected, but the default location is the volume
// that fills in practice (DB + agent outputs + logs all live under it).
func (h *AdminObservabilityHandler) Health(w http.ResponseWriter, r *http.Request) {
	if !canRole(RoleFromContext(r.Context()), "manage") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}
	resp := map[string]any{
		"uptime_seconds": int(time.Since(h.startedAt).Seconds()),
		"log_level":      currentLogLevel(),
		// E2 (master-key ops): where ENCRYPTION_KEY came from. "generated"
		// means the auto-bootstrapped key sits in <dataDir>/secrets.env next
		// to the database — a copied disk carries both ciphertext and key.
		// "external" = operator-injected env; "unknown" = bootstrap didn't
		// run in this process (embedded/test harnesses).
		"encryption_key_source": secrets.EncryptionKeySource(),
	}
	// Real DB liveness probe. The admin overview's health dots were
	// hardcoded green (#868); a bounded ping means a wedged/locked SQLite
	// file actually shows red instead of lying "connected".
	if h.db != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		if err := h.db.PingContext(ctx); err != nil {
			resp["db"] = map[string]any{"connected": false, "error": err.Error()}
		} else {
			resp["db"] = map[string]any{"connected": true}
		}
		cancel()
	}
	// Surface every failure mode distinctly so an operator polling during an
	// incident can tell "disk info intentionally absent" from "data-dir
	// resolution broke" from "statfs failed".
	if dir, err := h.dataDir(); err != nil {
		resp["disk"] = map[string]any{"error": err.Error()}
	} else if du, derr := h.diskUsage(dir); derr == nil {
		resp["disk"] = du
	} else {
		resp["disk"] = map[string]any{"path": dir, "error": derr.Error()}
	}
	writeJSON(w, http.StatusOK, resp)
}
