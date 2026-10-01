package api

import (
	"context"
	"database/sql"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/backup"
)

// LandServices imports the verified service images staged by offline recover.
// Clearing maintenance and recording the audit are one database transaction.
func (h *InstanceBackupsHandler) LandServices(w http.ResponseWriter, r *http.Request) {
	var req landEnvironmentsRequest
	if r.ContentLength != 0 && !decodeInstanceBody(w, r, &req) {
		return
	}
	cfg := h.recoveryConfig()
	if cfg.DataDir == "" {
		replyError(w, http.StatusServiceUnavailable, "recovered data directory is not configured")
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 30*time.Minute)
	defer cancel()
	opts := backup.ServiceLandingOptions{DryRun: req.DryRun, Finalize: func(ctx context.Context, tx *sql.Tx, count int) error {
		return auditInstance(ctx, r, tx, "instance.services_landed", "backup_services", "", "", map[string]any{"images": count})
	}}
	count, err := backup.LandRecoveredServices(ctx, h.db, cfg.DataDir, cfg.ServiceSnapshots, opts)
	if err != nil {
		h.fail(w, "land service data", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"images": count, "dry_run": req.DryRun})
}
