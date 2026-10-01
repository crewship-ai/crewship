package api

// Admin › Backups: land the complete container environments an offline
// `crewship recover` staged (Track E).
//
//	POST /api/v1/admin/instance/backups/environments/land   {dry_run}
//
// recover runs without a server and usually without Docker, so it keeps the
// crews' environments under <data dir>/restore-staging/environments. Once
// the recovered server runs with Docker, this loads each image back (a
// managed crew's under its crew image tag, so Crewship recreates the crew
// from the captured environment), recreates any container Crewship does not
// manage with its sanitized settings, and reports each one restored, rebuilt
// or skipped. Settings recorded as unsafe stay off.

import (
	"context"
	"net/http"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/database"
)

type landEnvironmentsRequest struct {
	DryRun bool `json:"dry_run"`
}

// LandEnvironments is POST /api/v1/admin/instance/backups/environments/land.
func (h *InstanceBackupsHandler) LandEnvironments(w http.ResponseWriter, r *http.Request) {
	var req landEnvironmentsRequest
	if r.ContentLength != 0 && !decodeInstanceBody(w, r, &req) {
		return
	}
	cfg := h.recoveryConfig()
	dataDir := cfg.DataDir
	if dataDir == "" {
		dd, err := database.ResolveDefaultDataDir()
		if err != nil {
			h.fail(w, "resolve data dir", err)
			return
		}
		dataDir = dd.Root
	}
	opts := backup.LandOptions{DataDir: dataDir, DryRun: req.DryRun}
	if dir := cfg.OutputDir; dir != "" {
		opts.Store = backup.EnvironmentStoreFor(dir)
	} else if dir, err := backup.DefaultBackupsDir(); err == nil {
		opts.Store = backup.EnvironmentStoreFor(dir)
	}
	// Loading images takes minutes; a client that gives up must not tear
	// the landing down halfway, so the work runs on its own deadline.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), backup.LandTimeout())
	defer cancel()
	rep, err := backup.LandStagedEnvironments(ctx, cfg.DockerOps, opts)
	if err != nil {
		h.fail(w, "land environments", err)
		return
	}
	if !req.DryRun && len(rep.Environments) > 0 {
		counts := map[string]int{}
		for _, e := range rep.Environments {
			counts[e.Result]++
		}
		tx, err := h.db.BeginTx(ctx, nil)
		if err != nil {
			h.fail(w, "begin", err)
			return
		}
		defer func() { _ = tx.Rollback() }()
		if err := auditInstance(ctx, r, tx, "instance.environments_landed", "backup_environments", rep.Dir, "", map[string]any{
			"restored": counts[backup.EnvRestored], "rebuilt": counts[backup.EnvRebuilt], "skipped": counts[backup.EnvSkipped],
		}); err != nil {
			h.fail(w, "audit", err)
			return
		}
		if err := tx.Commit(); err != nil {
			h.fail(w, "commit", err)
			return
		}
	}
	writeJSON(w, http.StatusOK, rep)
}
