package api

import (
	"database/sql"
	"log/slog"
	"net/http"

	"github.com/crewship-ai/crewship/internal/crashreport"
)

// TelemetryStatusHandler exposes the operator's consent state to the
// browser so the Next.js bundle can decide whether to initialise its own
// Sentry client. The frontend's sentry.client.config.ts fetches this
// before calling Sentry.init and bails out if enabled=false.
//
// Unauthenticated by design — Sentry init runs before login on the very
// first page paint (the login form itself can crash, and we want to see
// it). The response carries no secrets: enabled is a bool, install_id
// is the same anonymous random hex the Go side ships in ServerName, and
// the operator has already consented to that being attached to crash
// events.
//
// Returns `{enabled: false, install_id: ""}` on any DB error so a flaky
// transient state defaults to the privacy-preserving outcome (no client
// init). Never 5xx — the frontend would treat that the same as "no
// consent" anyway, and a 200 with enabled=false keeps the response
// shape predictable for the consumer.
type TelemetryStatusHandler struct {
	db     *sql.DB
	logger *slog.Logger
}

// NewTelemetryStatusHandler builds the handler with the DB the consent
// row lives in.
func NewTelemetryStatusHandler(db *sql.DB, logger *slog.Logger) *TelemetryStatusHandler {
	return &TelemetryStatusHandler{db: db, logger: logger}
}

type telemetryStatusResponse struct {
	// Enabled mirrors crashreport.IsEnabled() at the time of the call.
	// True only when the operator has opted in AND a DSN is wired in
	// AND the backend init succeeded. The browser uses this to gate
	// Sentry.init.
	Enabled bool `json:"enabled"`
	// InstallID is the anonymous identifier the backend already ships
	// as Sentry ServerName. Exposed here so the frontend's events can
	// be grouped with the backend's events for the same install —
	// otherwise we'd have to invent a separate frontend ID and lose
	// the cross-stack correlation.
	InstallID string `json:"install_id"`
}

// Status — GET /api/v1/system/telemetry (no auth)
//
// The endpoint is read-only. Operators flip consent via the CLI
// (`crewship telemetry on/off`); the browser cannot change consent
// state — making it changeable here would create a CSRF surface (any
// site could navigate the user's browser to /api/v1/system/telemetry
// and flip the bit if it were POST-able).
func (h *TelemetryStatusHandler) Status(w http.ResponseWriter, r *http.Request) {
	enabled, _, installID, err := crashreport.Status(r.Context(), h.db)
	if err != nil {
		h.logger.Debug("telemetry status: read consent", "error", err)
		writeJSON(w, http.StatusOK, telemetryStatusResponse{Enabled: false, InstallID: ""})
		return
	}
	writeJSON(w, http.StatusOK, telemetryStatusResponse{
		Enabled:   enabled,
		InstallID: installID,
	})
}
