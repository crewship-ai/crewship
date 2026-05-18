package api

// Admin-only routes: workspace stats, user / workspace listing,
// keeper request audit log, audit log, and the backup admin surface.
// All require workspace context and (per-handler) OWNER role.

import (
	"net/http"
	"os"

	"github.com/crewship-ai/crewship/internal/backup"
)

// registerAdminRoutes wires admin + audit + backup endpoints.
// audit is grouped here because the audit_logs surface is a
// workspace admin tool, not part of any feature domain.
func (r *Router) registerAdminRoutes() {
	authed := r.authMw.RequireAuth
	wsCtx := r.authMw.RequireWorkspace

	// Audit logs (require workspace context + manage role)
	audit := NewAuditHandler(r.db, r.logger)
	r.mux.Handle("GET /api/v1/audit", authed(wsCtx(http.HandlerFunc(audit.List))))

	// Admin (require workspace context + OWNER)
	admin := NewAdminHandler(r.db, r.logger)
	r.mux.Handle("GET /api/v1/admin/stats", authed(wsCtx(http.HandlerFunc(admin.Stats))))
	r.mux.Handle("GET /api/v1/admin/users", authed(wsCtx(http.HandlerFunc(admin.ListUsers))))
	r.mux.Handle("GET /api/v1/admin/workspaces", authed(wsCtx(http.HandlerFunc(admin.ListWorkspaces))))

	// Keeper admin log
	keeperLog := NewKeeperLogHandler(r.db, r.logger)
	r.mux.Handle("GET /api/v1/admin/keeper/requests", authed(wsCtx(http.HandlerFunc(keeperLog.List))))

	// Memory stats — operator observability for the memory subsystem.
	// Reads memory_versions directly; the audit watcher (Iter 1 of
	// the memory-hardening series) keeps that table honest about
	// both sidecar-mediated and direct-filesystem writes, so the
	// dashboard numbers stay correct regardless of which path the
	// agent took.
	memStats := NewMemoryStatsHandler(r.db, r.logger)
	r.mux.Handle("GET /api/v1/admin/memory/stats", authed(wsCtx(http.HandlerFunc(memStats.Stats))))

	// Memory versions list — row-level drill-down into
	// memory_versions. Stats (above) is the aggregate; this
	// endpoint is the detail. Filters by tier / agent_slug /
	// path prefix / time range, paginated via keyset cursor.
	// Iter 7 of the memory-hardening series.
	memVer := NewMemoryVersionsListHandler(r.db, r.logger)
	r.mux.Handle("GET /api/v1/admin/memory/versions", authed(wsCtx(http.HandlerFunc(memVer.List))))

	// Memory version content — operator drill-down into the
	// actual bytes of a specific memory_versions row. Pairs
	// with the stats + list endpoints above so the dashboard
	// can render a paginated table that drills into any row's
	// literal body for audit / PII review. Iter 8 of the
	// memory-hardening series. The blob root is the same
	// content-addressed path the rest of the memory pipeline
	// uses; empty disables the endpoint (503).
	memContent := NewMemoryVersionsContentHandler(r.db, r.logger, r.memoryVersionsBlobRoot)
	r.mux.Handle("GET /api/v1/admin/memory/versions/{id}/content",
		authed(wsCtx(http.HandlerFunc(memContent.Content))))

	// Backups (admin-only; require workspace context for scoping).
	// Adapt the concrete Docker client to backup.DockerOps so the
	// admin-backup HTTP layer doesn't see the Moby SDK directly.
	var backupDockerOps backup.DockerOps
	if r.dockerClient != nil {
		backupDockerOps = &backup.MobyDockerOps{Client: r.dockerClient}
	}
	backupH := NewBackupHandler(r.db, r.logger, backupDockerOps, os.Getenv("CREWSHIP_VERSION"))
	// Wire the slug→container-name mapping from the active container
	// provider so the backup runner uses the per-instance prefix
	// (e.g. "crewship-3-team-research") instead of the hardcoded
	// default "crewship-team-research" — multi-instance setups would
	// otherwise fail with "No such container" on docker pause.
	if r.keeperContainer != nil {
		backupH.SetCrewContainerName(r.keeperContainer.CrewContainerName)
	}
	// Dual-emit backup admin actions (create / delete / unlock / rotate
	// / download / restore) into the unified Crew Journal alongside the
	// audit_logs row that WriteAuditLog already writes. Skipped silently
	// when the router has no journal emitter (early bring-up paths).
	if r.journal != nil {
		backupH.SetJournal(r.journal)
	}
	r.mux.Handle("POST /api/v1/admin/backups", authed(wsCtx(http.HandlerFunc(backupH.Create))))
	r.mux.Handle("GET /api/v1/admin/backups", authed(wsCtx(http.HandlerFunc(backupH.List))))
	r.mux.Handle("GET /api/v1/admin/backups/status", authed(wsCtx(http.HandlerFunc(backupH.Status))))
	r.mux.Handle("GET /api/v1/admin/backups/metrics", authed(wsCtx(http.HandlerFunc(backupH.Metrics))))
	r.mux.Handle("DELETE /api/v1/admin/backups/status", authed(wsCtx(http.HandlerFunc(backupH.Unlock))))
	r.mux.Handle("GET /api/v1/admin/backups/inspect", authed(wsCtx(http.HandlerFunc(backupH.Inspect))))
	r.mux.Handle("GET /api/v1/admin/backups/verify", authed(wsCtx(http.HandlerFunc(backupH.Verify))))
	r.mux.Handle("POST /api/v1/admin/backups/rotate", authed(wsCtx(http.HandlerFunc(backupH.Rotate))))
	r.mux.Handle("GET /api/v1/admin/backups/download", authed(wsCtx(http.HandlerFunc(backupH.Download))))
	r.mux.Handle("POST /api/v1/admin/backups/restore", authed(wsCtx(http.HandlerFunc(backupH.Restore))))
	r.mux.Handle("POST /api/v1/admin/backups/self-test", authed(wsCtx(http.HandlerFunc(backupH.SelfTest))))
	r.mux.Handle("DELETE /api/v1/admin/backups", authed(wsCtx(http.HandlerFunc(backupH.Delete))))
}
