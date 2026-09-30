package api

// Admin-only routes: workspace stats, user / workspace listing,
// keeper request audit log, audit log, and the backup admin surface.
// All require workspace context and (per-handler) OWNER role.

import (
	"os"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/backupplan"
	"github.com/crewship-ai/crewship/internal/mailer"
	"github.com/crewship-ai/crewship/internal/notify"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/usermodel"
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
	// openapi: query page:integer limit:integer source:string action:string entity_type:string entity_id:string user_id:string date_from:string date_to:string search:string; responses 200,400,401,403,500
	r.authedAdmin("GET", "/api/v1/audit", audit.List)

	// Admin
	admin := NewAdminHandler(r.db, r.logger)
	r.authedAdminAny("GET", "/api/v1/admin/stats", admin.Stats)
	r.authedAdminPeople("GET", "/api/v1/admin/users", admin.ListUsers)
	r.authedAdminPeople("GET", "/api/v1/admin/workspaces", admin.ListWorkspaces)

	// Per-person admin actions (Admin › Users): a person's signed-in
	// devices and CLI tokens, signing one or all of them out, and lifting a
	// sign-in lockout. Same session store the auth middleware checks, so a
	// revoke takes effect on the next request.
	people := NewAdminUsersHandler(r.db, r.logger, r.sessionsStore)
	// openapi: responses 200,400,401,403,404,500
	r.authedAdminPeople("GET", "/api/v1/admin/users/{userId}/sessions", people.Sessions)
	// openapi: responses 204,400,401,403,404,500
	r.authedAdminPeople("POST", "/api/v1/admin/users/{userId}/sessions/{sessionId}/revoke", people.RevokeSession)
	// openapi: responses 200,400,401,403,404,500
	r.authedAdminPeople("POST", "/api/v1/admin/users/{userId}/sessions/revoke-all", people.RevokeAllSessions)
	// openapi: responses 204,400,401,403,404,500
	r.authedAdminPeople("POST", "/api/v1/admin/users/{userId}/unlock", people.Unlock)

	// Instance administration (Admin › People & workspaces): add a person,
	// give or take access in any workspace, create / hand over / delete
	// workspaces, suspend accounts, reissue setup links and name the other
	// instance admins. authedInstance: an instance admin, and no workspace in
	// the request — they need not belong to what they manage.
	inst := NewInstanceAdminHandler(r.db, r.logger, r.sessionsStore, r.Journal(), r.hub)
	// openapi: responses 201,400,401,403,404,409,500,503
	r.authedInstance("POST", "/api/v1/admin/instance/people", inst.CreatePerson)
	// openapi: responses 200,400,401,403,404,409,500
	r.authedInstance("POST", "/api/v1/admin/instance/people/{userId}/suspend", inst.Suspend)
	// openapi: responses 204,401,403,404,500
	r.authedInstance("POST", "/api/v1/admin/instance/people/{userId}/reactivate", inst.Reactivate)
	// openapi: responses 200,401,403,404,409,500,503
	r.authedInstance("POST", "/api/v1/admin/instance/people/{userId}/setup-link", inst.IssueSetupLink)
	// openapi: responses 204,401,403,404,500
	r.authedInstance("DELETE", "/api/v1/admin/instance/people/{userId}/setup-link", inst.RevokeSetupLink)
	// openapi: responses 204,401,403,404,409,500
	r.authedInstance("PUT", "/api/v1/admin/instance/admins/{userId}", inst.GrantAdmin)
	// openapi: responses 204,401,403,404,409,500
	r.authedInstance("DELETE", "/api/v1/admin/instance/admins/{userId}", inst.RevokeAdmin)
	// openapi: responses 200,201,400,401,403,404,409,500
	r.authedInstance("PUT", "/api/v1/admin/instance/workspaces/{workspaceId}/members/{userId}", inst.SetMembership)
	// openapi: responses 204,401,403,404,409,500
	r.authedInstance("DELETE", "/api/v1/admin/instance/workspaces/{workspaceId}/members/{userId}", inst.RemoveMembership)
	// openapi: responses 201,400,401,403,409,500
	r.authedInstance("POST", "/api/v1/admin/instance/workspaces", inst.CreateWorkspace)
	// openapi: responses 200,400,401,403,404,409,500
	r.authedInstance("POST", "/api/v1/admin/instance/workspaces/{workspaceId}/transfer-ownership", inst.TransferOwnership)
	// openapi: responses 204,400,401,403,404,500
	r.authedInstance("DELETE", "/api/v1/admin/instance/workspaces/{workspaceId}", inst.DeleteWorkspace)
	// openapi: responses 200,401,403,500
	r.authedInstance("GET", "/api/v1/admin/instance/audit", inst.AuditLog)

	// Admin › Security across workspaces: the Keeper of every workspace, set for
	// one, several or all of them (dry_run previews what a save overwrites),
	// and its decision log and health windows with each row's workspace.
	ik := NewInstanceKeeperHandler(r.db, r.logger, r.Journal())
	// openapi: responses 200,401,403,500
	r.authedInstance("GET", "/api/v1/admin/instance/keeper/governance", ik.ListGovernance)
	// openapi: responses 200,400,401,403,404,409,500
	r.authedInstance("PUT", "/api/v1/admin/instance/keeper/governance", ik.PutGovernance)
	// openapi: responses 200,401,403,500
	r.authedInstance("GET", "/api/v1/admin/instance/keeper/governance/defaults", ik.GetDefaults)
	// openapi: responses 200,400,401,403,409,500
	r.authedInstance("PUT", "/api/v1/admin/instance/keeper/governance/defaults", ik.PutDefaults)
	// openapi: responses 200,401,403,404,500
	r.authedInstance("GET", "/api/v1/admin/instance/keeper/requests", ik.ListRequests)
	// openapi: responses 200,401,403,500
	r.authedInstance("GET", "/api/v1/admin/instance/keeper/health", ik.Health)

	// Admin › Data retention: how long every workspace keeps each kind of
	// data, set for one, several or every existing workspace (dry_run
	// previews what the next sweep would delete), the fixed instance limits,
	// and separately the defaults a workspace created later starts with.
	ret := NewInstanceRetentionHandler(r.db, r.logger)
	// openapi: responses 200,401,403,404,500
	r.authedInstance("GET", "/api/v1/admin/instance/retention", ret.Get)
	// openapi: responses 200,400,401,403,404,409,500
	r.authedInstance("PUT", "/api/v1/admin/instance/retention", ret.Put)
	// openapi: responses 200,401,403,500
	r.authedInstance("GET", "/api/v1/admin/instance/retention/defaults", ret.GetDefaults)
	// openapi: responses 200,400,401,403,500
	r.authedInstance("PUT", "/api/v1/admin/instance/retention/defaults", ret.PutDefaults)
	// Admin › Backups across workspaces: the catalog of every workspace's
	// bundles (proof level, pin, recorded gaps), pins no retention rule may
	// override, and every restore / dry run the server has run.
	ib := NewInstanceBackupsHandler(r.db, r.logger)
	r.instanceBackups = ib
	// openapi: responses 200,401,403,404,500
	r.authedInstance("GET", "/api/v1/admin/instance/backups/bundles", ib.ListBundles)
	// openapi: responses 200,400,401,403,404,500
	r.authedInstance("POST", "/api/v1/admin/instance/backups/bundles/pin", ib.Pin)
	// openapi: responses 200,400,401,403,404,500
	r.authedInstance("POST", "/api/v1/admin/instance/backups/bundles/unpin", ib.Unpin)
	// openapi: responses 200,400,401,403,500
	r.authedInstance("GET", "/api/v1/admin/instance/backups/restores", ib.ListRestores)
	// One catalogued bundle of any scope, instance bundles included: the
	// manifest, the checksum, the bytes.
	// openapi: responses 200,400,401,403,404,422,500
	r.authedInstance("GET", "/api/v1/admin/instance/backups/bundles/inspect", ib.InspectBundle)
	// openapi: responses 200,400,401,403,404,500
	r.authedInstance("GET", "/api/v1/admin/instance/backups/bundles/verify", ib.VerifyBundle)
	// openapi: responses 200,400,401,403,404,500
	r.authedInstance("GET", "/api/v1/admin/instance/backups/bundles/download", ib.DownloadBundle)

	// Whole-instance backup and recovery: an instance bundle (the whole
	// database, every file store and crew container, one quiet window), the
	// recovery kit, contents checks, restore checks, drills recorded by the
	// offline `crewship backup drill`, and the automations an offline
	// `crewship recover` left held.
	ib.SetRecovery(r.instanceRecoveryConfig())
	// openapi: responses 200,401,403,500
	r.authedInstance("GET", "/api/v1/admin/instance/backups/vault-keys", ib.VaultKeys)
	// openapi: responses 200,400,401,403,500
	r.authedInstance("PUT", "/api/v1/admin/instance/backups/settings/recovery-kit", ib.SetRecoveryKit)
	// openapi: responses 200,400,401,403,404,422,500
	r.authedInstance("POST", "/api/v1/admin/instance/backups/bundles/check", ib.CheckBundle)
	// openapi: responses 200,400,401,403,404,422,500
	r.authedInstance("POST", "/api/v1/admin/instance/backups/restore/checks", ib.RestoreChecks)
	// openapi: responses 201,400,401,403,404,409,500
	r.authedInstance("POST", "/api/v1/admin/instance/backups/drills", ib.RecordDrill)
	// openapi: responses 200,401,403,500
	r.authedInstance("GET", "/api/v1/admin/instance/backups/drills", ib.ListDrills)
	// openapi: responses 200,400,401,403,500
	r.authedInstance("POST", "/api/v1/admin/instance/backups/environments/land", ib.LandEnvironments)
	// openapi: responses 200,401,403,500
	r.authedInstance("GET", "/api/v1/admin/instance/holds", ib.ListHolds)
	// openapi: responses 200,400,401,403,404,500
	r.authedInstance("POST", "/api/v1/admin/instance/holds/resume", ib.ResumeHold)

	// Admin observability: runtime log-level toggle + disk/health read.
	obs := NewAdminObservabilityHandler(r.db, r.logger)
	r.authedAdminAny("GET", "/api/v1/admin/log-level", obs.GetLogLevel)
	r.authedInstanceMut("PUT", "/api/v1/admin/log-level", obs.SetLogLevel)
	r.authedAdminAny("GET", "/api/v1/admin/health", obs.Health)

	// Master-key re-encryption (E1). Instance-wide walk of every stored
	// AES-256-GCM envelope, re-encrypted to the current key version — the
	// missing half of ENCRYPTION_KEY rotation (decrypt-old always worked;
	// this moves rows forward so the old key can be retired). Mutation →
	// roleManage, same gate as the other instance-scoped admin operations
	// (backups, prune-legacy-resources).
	reencryptH := NewReencryptHandler(r.db, r.logger)
	r.authedInstanceMut("POST", "/api/v1/admin/reencrypt", reencryptH.Reencrypt)

	// Keeper admin log
	keeperLog := NewKeeperLogHandler(r.db, r.logger)
	r.authedAdmin("GET", "/api/v1/admin/keeper/requests", keeperLog.List)
	// Append-only transition history for one request (issue #1369). The List
	// route above returns the CURRENT decision; this returns how the request got
	// there, which the in-place UPDATE used to overwrite.
	r.authedAdmin("GET", "/api/v1/admin/keeper/requests/{requestId}/events", keeperLog.ListEvents)

	// Journal integrity (issue #1369): walk the workspace's audit
	// hash-chain and report the first broken link (mutation / reorder /
	// mid-chain deletion). Read-only, ADMIN+ — the tamper-evidence check
	// that the whole ex-post accountability model leans on.
	journalIntegrity := NewJournalIntegrityHandler(r.db, r.logger)
	r.authedAdmin("GET", "/api/v1/admin/journal/verify", journalIntegrity.Verify)

	// Instance security posture (issue #1379). Read-only, ADMIN+. The
	// env-driven instance flags stay env-driven — this only makes their STATE
	// visible, so an admin can answer "are we storing secrets in plaintext? is
	// signup open? is the limiter off?" without shell access to the box.
	// Booleans only; no secret value is ever serialized.
	posture := NewSecurityPostureHandler(r.allowSignup, r.googleClientID != "" && r.googleSecret != "", r.db, r.logger)
	r.authedAdminAny("GET", "/api/v1/admin/security-posture", posture.Get)

	// Runtime rate-limiter tuning (#1505 follow-up). Read ADMIN+, write
	// OWNER/ADMIN. Lists every tunable limiter with its current value; an
	// override applies instance-wide and takes effect immediately (per-IP HTTP
	// buckets retune live, the rest read their value on next use). Replaces
	// the removed "Rate Limits" placeholder tab with a real backend.
	rateLimits := NewAdminRateLimitsHandler(r.ratelimitStore, r.logger)
	r.authedAdminAny("GET", "/api/v1/admin/rate-limits", rateLimits.List)
	r.authedInstanceMut("PUT", "/api/v1/admin/rate-limits/{key}", rateLimits.Set)
	r.authedInstanceMut("DELETE", "/api/v1/admin/rate-limits/{key}", rateLimits.Reset)

	// Keeper instance judge configuration. Read ADMIN+, write OWNER/ADMIN. This
	// is the INSTANCE layer (keeper_runtime_settings) that the per-workspace
	// governance model below overrides: whether Keeper runs, and what the
	// credential-access judge is wired to. Before it, all three values were
	// boot-time env — the console could diagnose a dead judge but not fix it,
	// and an operator with no shell access could not turn Keeper on at all.
	// A change takes effect on the next credential request; no restart.
	keeperCfg := NewAdminKeeperConfigHandler(r.keeperSettings, r.Journal(), r.logger)
	r.authedAdmin("GET", "/api/v1/admin/keeper/health", NewAdminKeeperHealthHandler(r.logger).Get)
	r.authedAdminAny("GET", "/api/v1/admin/keeper/config", keeperCfg.Get)
	r.authedInstanceMut("PUT", "/api/v1/admin/keeper/config", keeperCfg.Put)
	r.authedInstanceMut("DELETE", "/api/v1/admin/keeper/config", keeperCfg.Reset)

	// Judge verification + model discovery. The pair that makes configuring a
	// local judge a one-minute job: paste an address, see what it serves, prove
	// it can actually return a verdict. OWNER/ADMIN on both — they dial an
	// address the caller can supply, which is a write-class capability even
	// though one of them only returns a model list.
	keeperJudge := NewAdminKeeperJudgeHandler(r.keeperSettings, r.logger).WithGovJudge(r.govModelJudge)
	r.authedInstanceMut("POST", "/api/v1/admin/keeper/judge/test", keeperJudge.Test)
	r.authedAdminAny("GET", "/api/v1/admin/keeper/judge/models", keeperJudge.Models)
	// The same check for a HOSTED judge (Anthropic / OpenAI-compatible built from
	// a vault key). Separate route rather than a mode flag on /test: the stages
	// differ because the failure modes do — there is no endpoint to reach and no
	// model to pull, but there IS a key that can be missing, revoked or of the
	// wrong type.
	r.authedInstanceMut("POST", "/api/v1/admin/keeper/judge/test-hosted", keeperJudge.TestHosted)

	// Keeper evaluator models. Same instance layer, for the OTHER half of the
	// Keeper model stack: the five aux slots behind the watchdog and the Reviews
	// sweeps. They bill per token where the judge does not, so this is where the
	// cost decision gets made — including pointing them all at the local judge.
	keeperAux := NewAdminKeeperAuxHandler(r.keeperAuxSettings, r.keeperSettings, r.Journal(), r.logger).
		WithProbe(keeperJudge.ProbeModel).
		// Which vault key each evaluator spends (#1554), validated against the
		// caller's own workspace before it is stored.
		WithCredentials(newAuxCredentialCheck(r.db))
	r.authedAdminAny("GET", "/api/v1/admin/keeper/aux", keeperAux.Get)
	r.authedInstanceMut("PUT", "/api/v1/admin/keeper/aux/{slot}", keeperAux.Put)
	r.authedInstanceMut("DELETE", "/api/v1/admin/keeper/aux/{slot}", keeperAux.Reset)
	// The collection-scoped DELETE is "reset every slot"; {slot} is empty there,
	// which is exactly what AuxStore.Reset("") means.
	r.authedInstanceMut("DELETE", "/api/v1/admin/keeper/aux", keeperAux.Reset)
	r.authedInstanceMut("POST", "/api/v1/admin/keeper/aux/use-judge", keeperAux.UseJudge)
	// One real evaluation against a slot's model, on request. The card's default
	// stays "not probed" — rendering a status page must not spend money — but an
	// operator who asks explicitly should be able to find out.
	r.authedInstanceMut("POST", "/api/v1/admin/keeper/aux/{slot}/probe", keeperAux.Probe)

	// Manual runs for the four Reviews evaluators (issue #1555). The
	// evaluators were reachable only by the scheduler and by sidecars holding
	// an internal token — both machine paths — so "check my agents' skills
	// now" was not expressible, and the behaviour watchdog (which only fires
	// on a tool call) had never run outside its unit tests. This is the
	// operator's trigger; it calls the same Phase 2 handler the internal
	// routes do, so a manual run writes the same audit row a scheduled one
	// does. OWNER/ADMIN: it spends model tokens and can escalate to the inbox.
	keeperReview := NewAdminKeeperReviewHandler(r.db, r.keeperPhase2Handler(), r.logger)
	r.authedAdminWrite("POST", "/api/v1/admin/keeper/review/{slot}/run", keeperReview.Run)

	// Keeper watchdog governance (issue #1001 M0): workspace toggle, named
	// security contact, DENY-notify threshold. Read ADMIN+, write OWNER/ADMIN.
	keeperGov := NewKeeperGovernanceHandler(r.db, r.logger, r.Journal())
	r.authedAdmin("GET", "/api/v1/admin/keeper/governance", keeperGov.Get)
	r.authedAdminWrite("PUT", "/api/v1/admin/keeper/governance", keeperGov.Put)

	// Findings routing check. Sends ONE synthetic finding through the real inbox
	// writer with real target resolution and returns who it reached. Whether a
	// security control can actually reach a human is not something to discover
	// during the incident it was bought for. Costs nothing — no model is called.
	// The hub may be absent in a bare test router; the handler is nil-safe about
	// the broadcast, so the write still happens and only the live badge push is
	// skipped.
	var keeperBcast KeeperBroadcaster
	if r.hub != nil {
		keeperBcast = &keeperWSBroadcaster{hub: r.hub}
	}
	keeperFindings := NewAdminKeeperFindingsHandler(r.db, r.Journal(), keeperBcast, r.logger)
	r.authedAdminWrite("POST", "/api/v1/admin/keeper/findings/test", keeperFindings.SendTest)

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
	gdprH.SetJournal(r.Journal())
	r.authedAdmin("GET", "/api/v1/admin/users/{userId}/data", gdprH.ExportUserData)
	r.authedAdminWrite("DELETE", "/api/v1/admin/users/{userId}/data", gdprH.DeleteUserData)

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
	r.authedAdminWrite("PATCH", "/api/v1/admin/memory/config", memCfg.Patch)

	// Manual runs of the two daily memory sweeps (#1702). The operator-model
	// sweep fires at 05:00 UTC and the peer-card sweep at 04:00, and until
	// this pair of routes there was no other way to see either run — which,
	// for a feature whose every failure mode is silent by design, meant no
	// way at all. Each route is the worker's own sweep over the worker's own
	// workspace set, with the worker's own extractor wiring (the user-model
	// extractor is built here exactly as cmd_start.go builds it for the
	// worker: same curator slot, same profile setting, resolved per call),
	// returning the per-workspace summary the worker only ever logged.
	// `dry_run` reports without writing. roleManage: it spends the curator
	// slot's tokens and writes under the storage root.
	memSync := NewAdminMemorySyncHandler(r.db, r.logger, r.outputBasePath)
	if r.db != nil {
		memSync.WithUserModelExtractor(usermodel.New(
			r.db, r.UserModelAux, usermodel.ProfileFromSettings(r.db, r.logger), r.logger))
	}
	// openapi: query dry_run:boolean; responses 200,400,401,403,404,500,503
	r.authedMut("POST", "/api/v1/admin/memory/user-model-sync", roleManage, memSync.UserModelSync)
	// openapi: query dry_run:boolean; responses 200,400,401,403,404,500,503
	r.authedMut("POST", "/api/v1/admin/memory/peer-card-sync", roleManage, memSync.PeerCardSync)

	// Backups (admin-only; require workspace context for scoping).
	// Adapt the concrete Docker client to backup.DockerOps so the
	// admin-backup HTTP layer doesn't see the Moby SDK directly.
	var backupDockerOps backup.DockerOps
	if r.dockerClient != nil {
		backupDockerOps = &backup.MobyDockerOps{Client: r.dockerClient}
	}
	backupH := NewBackupHandler(r.db, r.logger, backupDockerOps, os.Getenv("CREWSHIP_VERSION"))
	// Same content-addressed blob root the memory-versions content
	// endpoint uses (memContent above) — wiring it here lets
	// Create/Restore carry memory_versions blobs through the bundle
	// instead of leaving the DB rows pointing at nothing after a
	// restore. Empty when memory versioning isn't configured, which
	// disables the section (see internal/backup/memoryblobs.go).
	backupH.SetMemoryBlobRoot(r.memoryVersionsBlobRoot)
	// Attachment blobs live under the same storage root AttachmentHandler
	// writes to (attachments/<workspace>/<sha[:2]>/<sha>).
	backupH.SetAttachmentRoot(r.storagePath)
	backupH.pageProjectsPath = r.pageProjectsPath
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
	// Admin › Backups plans, runs and overview. The scheduler service runs
	// every backup in a server goroutine (never through an agent), writes
	// workspace bundles with the same wiring as backupH, and is started by
	// the boot path through BackupPlans().
	// Instance runs write Track B's instance bundle inside the quiet window;
	// scheduled runs are skipped while an instance restore's schedule hold
	// is set (admin_instance_backup_bridge.go).
	svc := backupplan.New(r.db, r.logger)
	svc.Workspace = &backupplan.WorkspaceExecutor{
		DB: r.db, DockerOps: backupDockerOps, CrewContainerName: backupH.resolveCrewContainerName(),
		BlobRoot: r.memoryVersionsBlobRoot, AttachmentRoot: r.storagePath, PageProjectsPath: r.pageProjectsPath,
		CrewshipVersion: os.Getenv("CREWSHIP_VERSION"),
		EnvInline:       backup.EnvironmentInlineDefault(),
	}
	svc.Instance = &instanceExecutor{h: ib, db: r.db}
	svc.Quiesce = backupQuiescer{h: ib, db: r.db}
	svc.Pause = holdsPauser{db: r.db}
	// Incidents reach every instance admin's inbox and the notification
	// channels on the alert route (admin_instance_backup_alerts.go), and a
	// recorded drill raises or clears the plan's drill incident. The channel
	// alerter sends through the same gated dispatcher as every notification
	// and registers its outbox deriver, so the recovery sweep retries it.
	backupAlerts := backupplan.NewChannelAlerter(r.db,
		newGatedDispatcher(notify.NewChannelStore(r.db), mailer.NewFromEnv(), r.logger, r.db), r.logger)
	backupAlerts.Begin = beginBackgroundWork
	svc.Alerts = backupIncidentAlerter{db: r.db, logger: r.logger, channels: backupAlerts}
	ib.onDrill = svc.RecordDrillOutcome
	r.backupPlans = svc
	bp := NewInstanceBackupPlansHandler(ib, svc)
	bp.alerts = backupAlerts
	// openapi: responses 200,401,403,500
	r.authedInstance("GET", "/api/v1/admin/instance/backups/plans", bp.ListPlans)
	// openapi: responses 201,400,401,403,500
	r.authedInstance("POST", "/api/v1/admin/instance/backups/plans", bp.CreatePlan)
	// openapi: responses 200,400,401,403,500
	r.authedInstance("POST", "/api/v1/admin/instance/backups/plans/preview-contents", bp.PreviewContents)
	// openapi: responses 200,401,403,404,500
	r.authedInstance("GET", "/api/v1/admin/instance/backups/plans/{id}", bp.GetPlan)
	// openapi: responses 200,400,401,403,404,500
	r.authedInstance("PUT", "/api/v1/admin/instance/backups/plans/{id}", bp.UpdatePlan)
	// openapi: responses 204,401,403,404,500
	r.authedInstance("DELETE", "/api/v1/admin/instance/backups/plans/{id}", bp.DeletePlan)
	// openapi: query n:integer; responses 200,400,401,403,404,500
	r.authedInstance("GET", "/api/v1/admin/instance/backups/plans/{id}/next", bp.NextRuns)
	// openapi: query from:string to:string; responses 200,400,401,403,404,500
	r.authedInstance("GET", "/api/v1/admin/instance/backups/plans/{id}/calendar", bp.Calendar)
	// openapi: query plan:string scope:string ws:string limit:integer; responses 200,400,401,403,404,500
	r.authedInstance("GET", "/api/v1/admin/instance/backups/runs", bp.ListRuns)
	// openapi: responses 202,400,401,403,404,500
	r.authedInstance("POST", "/api/v1/admin/instance/backups/run", bp.StartRun)
	// openapi: responses 200,401,403,404,500
	r.authedInstance("GET", "/api/v1/admin/instance/backups/run/{runId}", bp.GetRun)
	// openapi: query scope:string ws:string; responses 200,400,401,403,404,500
	r.authedInstance("GET", "/api/v1/admin/instance/backups/overview", bp.Overview)
	// Settings, backup keys, off-site destinations, incidents and the
	// recovery sheet (admin_instance_backup_settings.go).
	// openapi: responses 200,401,403,500
	r.authedInstance("GET", "/api/v1/admin/instance/backups/settings", bp.GetSettings)
	// openapi: responses 200,400,401,403,500
	r.authedInstance("PUT", "/api/v1/admin/instance/backups/settings", bp.PutSettings)
	// openapi: responses 200,400,401,403,404,500
	r.authedInstance("POST", "/api/v1/admin/instance/backups/settings/test-alert", bp.TestAlert)
	// openapi: responses 200,401,403,500
	r.authedInstance("GET", "/api/v1/admin/instance/backups/recipients", bp.ListRecipients)
	// openapi: responses 201,400,401,403,409,500
	r.authedInstance("POST", "/api/v1/admin/instance/backups/recipients", bp.CreateRecipient)
	// openapi: responses 204,401,403,404,409,500
	r.authedInstance("DELETE", "/api/v1/admin/instance/backups/recipients/{id}", bp.DeleteRecipient)
	// openapi: responses 200,401,403,500
	r.authedInstance("GET", "/api/v1/admin/instance/backups/destinations", bp.ListDestinations)
	// openapi: responses 201,400,401,403,409,422,500
	r.authedInstance("POST", "/api/v1/admin/instance/backups/destinations", bp.CreateDestination)
	// openapi: responses 200,401,403,404,500
	r.authedInstance("POST", "/api/v1/admin/instance/backups/destinations/{id}/test", bp.TestDestination)
	// openapi: responses 204,401,403,404,409,500
	r.authedInstance("DELETE", "/api/v1/admin/instance/backups/destinations/{id}", bp.DeleteDestination)
	// openapi: query state:string limit:integer; responses 200,400,401,403,500
	r.authedInstance("GET", "/api/v1/admin/instance/backups/incidents", bp.ListIncidents)
	// openapi: responses 200,401,403,500
	r.authedInstance("GET", "/api/v1/admin/instance/backups/recovery-sheet", bp.RecoverySheet)
	// Restore from an off-site copy: what a destination holds, and fetching a
	// bundle back as a job (admin_instance_backup_copies.go).
	copies := newOffsiteCopiesHandler(bp)
	// openapi: query destination:string; responses 200,400,401,403,404,502
	r.authedInstance("GET", "/api/v1/admin/instance/backups/copies", copies.List)
	// openapi: responses 202,400,401,403,404,409,500,502
	r.authedInstance("POST", "/api/v1/admin/instance/backups/copies/fetch", copies.Fetch)
	// openapi: responses 200,401,403,404
	r.authedInstance("GET", "/api/v1/admin/instance/backups/copies/fetch/{id}", copies.FetchStatus)

	// One workspace's backups: OWNER/ADMIN of the workspace named, or an
	// instance admin, who need not be a member of it (authedAdmin /
	// authedAdminWrite). The handlers scope everything to that workspace.
	r.authedAdminWrite("POST", "/api/v1/admin/backups", backupH.Create)
	r.authedAdmin("GET", "/api/v1/admin/backups", backupH.List)
	r.authedAdmin("GET", "/api/v1/admin/backups/status", backupH.Status)
	// Instance-wide counters: no workspace needed; the handler admits only
	// an instance admin.
	r.authedAdminAny("GET", "/api/v1/admin/backups/metrics", backupH.Metrics)
	r.authedAdminWrite("DELETE", "/api/v1/admin/backups/status", backupH.Unlock)
	r.authedAdmin("GET", "/api/v1/admin/backups/inspect", backupH.Inspect)
	r.authedAdmin("GET", "/api/v1/admin/backups/verify", backupH.Verify)
	r.authedAdminWrite("POST", "/api/v1/admin/backups/rotate", backupH.Rotate)
	r.authedAdmin("GET", "/api/v1/admin/backups/download", backupH.Download)
	r.authedAdminWrite("POST", "/api/v1/admin/backups/restore", backupH.Restore)
	r.authedAdminWrite("POST", "/api/v1/admin/backups/self-test", backupH.SelfTest)
	r.authedAdminWrite("DELETE", "/api/v1/admin/backups", backupH.Delete)

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
	r.authedAdminAny("GET", "/api/v1/admin/legacy-resources", legacyH.Detect)
	r.authedInstanceMut("POST", "/api/v1/admin/prune-legacy-resources", legacyH.Prune)

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
	r.authedAdminWrite("POST", "/api/v1/admin/prune-crew-runtimes", crewRuntimeH.Prune)

	// #1385: reap crew containers orphaned by an internal-token master rotation
	// across a restart — they hold a crew-bound token the new process rejects
	// forever ("invalid crew-bound token") with no self-healing. Dry-run by
	// default (report which containers are orphaned); ?apply=true stops+removes
	// them so the next dispatch re-mints a valid token. Nil provider (non-docker)
	// or a provider without crew-container lookup → handler 503s.
	orphanH := NewOrphanContainerHandler(r.db, r.logger, r.keeperContainer, r.internalToken)
	r.authedAdminWrite("POST", "/api/v1/admin/reap-orphan-containers", orphanH.Reap)
}
