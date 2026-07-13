package api

// Admin-only routes: workspace stats, user / workspace listing,
// keeper request audit log, audit log, and the backup admin surface.
// All require workspace context and (per-handler) OWNER role.

import (
	"os"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/provider"
)

// registerAdminRoutes wires admin + audit + backup endpoints.
// audit is grouped here because the audit_logs surface is a
// workspace admin tool, not part of any feature domain.
func (r *Router) registerAdminRoutes() {
	// Every admin READ route flows through authedAdmin (ADMIN+ floor, #865);
	// every admin MUTATION through authedMut (roleManage). Neither the raw
	// authed/wsCtx chain nor an inline-only role check is used here anymore —
	// the floor is declared at registration and enforced from the route table,
	// so a new admin route that forgets its gate fails the floor invariant.

	// Audit logs
	audit := NewAuditHandler(r.db, r.logger)
	r.authedAdmin("GET", "/api/v1/audit", audit.List)

	// Admin
	admin := NewAdminHandler(r.db, r.logger)
	r.authedAdmin("GET", "/api/v1/admin/stats", admin.Stats)
	r.authedAdmin("GET", "/api/v1/admin/users", admin.ListUsers)
	r.authedAdmin("GET", "/api/v1/admin/workspaces", admin.ListWorkspaces)

	// Admin observability: runtime log-level toggle + disk/health read.
	obs := NewAdminObservabilityHandler(r.db, r.logger)
	r.authedAdmin("GET", "/api/v1/admin/log-level", obs.GetLogLevel)
	r.authedMut("PUT", "/api/v1/admin/log-level", roleManage, obs.SetLogLevel)
	r.authedAdmin("GET", "/api/v1/admin/health", obs.Health)

	// Master-key re-encryption (E1). Instance-wide walk of every stored
	// AES-256-GCM envelope, re-encrypted to the current key version — the
	// missing half of ENCRYPTION_KEY rotation (decrypt-old always worked;
	// this moves rows forward so the old key can be retired). Mutation →
	// roleManage, same gate as the other instance-scoped admin operations
	// (backups, prune-legacy-resources).
	reencryptH := NewReencryptHandler(r.db, r.logger)
	r.authedMut("POST", "/api/v1/admin/reencrypt", roleManage, reencryptH.Reencrypt)

	// Keeper admin log
	keeperLog := NewKeeperLogHandler(r.db, r.logger)
	r.authedAdmin("GET", "/api/v1/admin/keeper/requests", keeperLog.List)

	// Keeper watchdog governance (issue #1001 M0): workspace toggle, named
	// security contact, DENY-notify threshold. Read ADMIN+, write OWNER/ADMIN.
	keeperGov := NewKeeperGovernanceHandler(r.db, r.logger, r.Journal())
	r.authedAdmin("GET", "/api/v1/admin/keeper/governance", keeperGov.Get)
	r.authedMut("PUT", "/api/v1/admin/keeper/governance", roleManage, keeperGov.Put)

	// PR-F F6: Admin GDPR cascade endpoints — Art. 15 access +
	// Art. 17 erasure across the four cascadable tables
	// (peer_cards, memory_versions, inbox_items; keeper_requests
	// is intentionally excluded — see admin_gdpr.go header). Both
	// routes require ADMIN+ in the current workspace; the handler
	// enforces the role check internally so middleware stays
	// uniform with the rest of /api/v1/admin/*. Every invocation
	// writes a gdpr_actions audit row (v107) recording who acted
	// on whom, scope, and operator-supplied reason.
	gdprH := NewAdminGDPRHandler(r.db, r.logger, r.outputBasePath)
	r.authedAdmin("GET", "/api/v1/admin/users/{userId}/data", gdprH.ExportUserData)
	r.authedMut("DELETE", "/api/v1/admin/users/{userId}/data", roleManage, gdprH.DeleteUserData)

	// Memory stats — operator observability for the memory subsystem.
	// Reads memory_versions directly; the audit watcher (Iter 1 of
	// the memory-hardening series) keeps that table honest about
	// both sidecar-mediated and direct-filesystem writes, so the
	// dashboard numbers stay correct regardless of which path the
	// agent took.
	memStats := NewMemoryStatsHandler(r.db, r.logger)
	r.authedAdmin("GET", "/api/v1/admin/memory/stats", memStats.Stats)

	// Memory versions list — row-level drill-down into
	// memory_versions. Stats (above) is the aggregate; this
	// endpoint is the detail. Filters by tier / agent_slug /
	// path prefix / time range, paginated via keyset cursor.
	// Iter 7 of the memory-hardening series.
	memVer := NewMemoryVersionsListHandler(r.db, r.logger)
	r.authedAdmin("GET", "/api/v1/admin/memory/versions", memVer.List)

	// Memory version content — operator drill-down into the
	// actual bytes of a specific memory_versions row. Pairs
	// with the stats + list endpoints above so the dashboard
	// can render a paginated table that drills into any row's
	// literal body for audit / PII review. Iter 8 of the
	// memory-hardening series. The blob root is the same
	// content-addressed path the rest of the memory pipeline
	// uses; empty disables the endpoint (503).
	memContent := NewMemoryVersionsContentHandler(r.db, r.logger, r.memoryVersionsBlobRoot)
	r.authedAdmin("GET", "/api/v1/admin/memory/versions/{id}/content", memContent.Content)

	// Memory config — read + partial write of
	// workspaces.memory_config. Iter 4 wired the per-workspace
	// retention sweep that consumes the column; this endpoint
	// (Iter 6 of the memory-hardening series) is the write
	// surface so operators can adjust retention without editing
	// SQLite by hand. PATCH emits memory.config_updated to the
	// journal so compliance audits can trace policy changes
	// over time.
	memCfg := NewMemoryConfigHandler(r.db, r.logger)
	memCfg.SetJournal(r.Journal())
	r.authedAdmin("GET", "/api/v1/admin/memory/config", memCfg.Get)
	r.authedMut("PATCH", "/api/v1/admin/memory/config", roleManage, memCfg.Patch)

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
	r.authedMut("POST", "/api/v1/admin/backups", roleManage, backupH.Create)
	r.authedAdmin("GET", "/api/v1/admin/backups", backupH.List)
	r.authedAdmin("GET", "/api/v1/admin/backups/status", backupH.Status)
	r.authedAdmin("GET", "/api/v1/admin/backups/metrics", backupH.Metrics)
	r.authedMut("DELETE", "/api/v1/admin/backups/status", roleManage, backupH.Unlock)
	r.authedAdmin("GET", "/api/v1/admin/backups/inspect", backupH.Inspect)
	r.authedAdmin("GET", "/api/v1/admin/backups/verify", backupH.Verify)
	r.authedMut("POST", "/api/v1/admin/backups/rotate", roleManage, backupH.Rotate)
	r.authedAdmin("GET", "/api/v1/admin/backups/download", backupH.Download)
	r.authedMut("POST", "/api/v1/admin/backups/restore", roleManage, backupH.Restore)
	r.authedMut("POST", "/api/v1/admin/backups/self-test", roleManage, backupH.SelfTest)
	r.authedMut("DELETE", "/api/v1/admin/backups", roleManage, backupH.Delete)

	// Legacy C1 resources (admin-only). Detect/remove pre-C1 slug-only crew
	// docker resources that survive nuke+reseed and block agent container
	// start. Nil pruner/detector (non-docker provider) → handler 503s.
	var legacyPruner provider.LegacyResourcePruner
	var legacyDetector provider.LegacyResourceDetector
	if r.keeperContainer != nil {
		if lp, ok := r.keeperContainer.(provider.LegacyResourcePruner); ok {
			legacyPruner = lp
		}
		if ld, ok := r.keeperContainer.(provider.LegacyResourceDetector); ok {
			legacyDetector = ld
		}
	}
	legacyH := NewLegacyResourceHandler(r.db, r.logger, legacyPruner, legacyDetector)
	r.authedAdmin("GET", "/api/v1/admin/legacy-resources", legacyH.Detect)
	r.authedMut("POST", "/api/v1/admin/prune-legacy-resources", roleManage, legacyH.Prune)

	// Crew runtime teardown (admin-only). Removes the LIVE id-scoped docker
	// containers+volumes of every crew in the workspace — the docker half of a
	// full `seed --nuke` (crew DB delete is a soft-delete that never touches
	// docker). Cached devcontainer images are preserved so a reseed doesn't
	// force a rebuild. Nil pruner (non-docker provider) → handler 503s.
	var runtimePruner provider.CrewRuntimePruner
	if r.keeperContainer != nil {
		if rp, ok := r.keeperContainer.(provider.CrewRuntimePruner); ok {
			runtimePruner = rp
		}
	}
	crewRuntimeH := NewCrewRuntimeHandler(r.db, r.logger, runtimePruner)
	r.authedMut("POST", "/api/v1/admin/prune-crew-runtimes", roleManage, crewRuntimeH.Prune)
}
