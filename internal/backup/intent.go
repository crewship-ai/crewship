package backup

import "sort"

// intent.go — authoritative allowlist of workspace-scoped tables and
// what backup should do with each one.
//
// Every workspace-scoped table the SQLite schema contains must have an
// entry here, otherwise CategoriseScopedTables surfaces
// ErrDiscoveryDrift at test time. That makes "we forgot to back up a
// new table" a CI failure rather than a silent data loss after a real
// restore.
//
// Three intents:
//
//	IntentInclude              — round-trip in the bundle
//	IntentExcludeOperational   — instance-local state (audit_logs,
//	                             backup_locks, scheduling state) that
//	                             MUST NOT be carried across restores
//	IntentExcludeRuntime       — populated by running services
//	                             (sessions, pairings, rate buckets) and
//	                             regenerates naturally on restore
//
// Globally-namespaced tables (`users`, `skills`, both keyed by UNIQUE
// constraints on human-readable columns) are NOT in this map — they
// don't get discovered via FK walk from workspaces. They're handled
// separately in dump.go through reverse-FK lookup: "users referenced
// by anything we just dumped" → include.

// BackupTableIntent is the source-of-truth allowlist. Order of entries
// is irrelevant; iteration order for dump/restore is computed by
// resolveInsertOrder via the FK graph so parents always land before
// children.
//
// Add a new entry every time a migration creates a workspace-scoped
// table. Drift detection catches the omission in tests so an oversight
// surfaces before a bundle ships missing rows.
var BackupTableIntent = map[string]ScopedTableIntent{
	// === Core entities (round-trip) =========================
	"crews":              IntentInclude,
	"agents":             IntentInclude,
	"agent_skills":       IntentInclude,
	"crew_members":       IntentInclude,
	"chats":              IntentInclude,
	"agent_mcp_bindings": IntentInclude,
	"journal_entries":    IntentInclude,
	// journal_entry_priorities (v166) is the append-only ledger of operator
	// pin/permanent edits. It rides with journal_entries: without it a restored
	// bundle's live `priority` values would have no ledger to reconcile against,
	// and VerifyChain's priority check would read every historical edit as a
	// silent DB-level flip.
	"journal_entry_priorities": IntentInclude,
	// journal_chain_checkpoints (v152) records the HMAC-signed (seq, entry_hash)
	// of rows a legitimate compaction / pipeline-purge deleted mid-chain, so the
	// resulting seq gap verifies instead of reading as tampering. It is NOT
	// reconstructable (the removed rows are gone and the MAC needs the chain
	// key), so a bundle that round-trips journal_entries but drops the
	// checkpoints restores a chain whose every compacted gap VerifyChain now
	// reports as a break — the same failure mode journal_entry_priorities above
	// guards against. Direct workspace_id column, dumped via the generic filter.
	"journal_chain_checkpoints": IntentInclude,

	// === Credentials & secrets (round-trip; cipher preserved) ========
	"credentials":          IntentInclude,
	"agent_credentials":    IntentInclude,
	"credential_audit":     IntentInclude,
	"credential_rotations": IntentInclude,
	// Composio managed-integration provider config (encrypted API key per
	// workspace). Workspace-scoped; round-trips with the encrypted value.
	"composio_settings": IntentInclude,
	// Keeper watchdog governance (workspace toggle, security contact,
	// DENY-notify threshold). Workspace-scoped; plain columns, round-trips.
	"keeper_governance_settings": IntentInclude,

	// === Files & memory (round-trip) ==========================
	// attachments is the single table behind every attached file — issue,
	// issue comment and chat — replacing the never-written chat_attachments
	// (dropped by 20260806194500_attachments.sql, and gone from this map with
	// it). It round-trips the METADATA only: the blob itself lives under the
	// storage root at attachments/<workspace>/<sha[0:2]>/<sha>, which the file
	// half of a bundle carries, and the row is what makes a restored blob
	// findable again. A restored row whose blob is missing degrades to a 404 on
	// download rather than to a corrupt read — the sha256 column is what lets a
	// verify pass say which of the two happened.
	"attachments":             IntentInclude,
	"chat_branches":           IntentInclude,
	"chat_participants":       IntentInclude,
	"chat_read_cursors":       IntentInclude,
	"message_reactions":       IntentInclude,
	"workspace_files":         IntentInclude,
	"memory_relations":        IntentInclude,
	"memory_health_snapshots": IntentInclude,

	// === Scheduling, webhooks, port exposures (round-trip) ==========
	"port_exposures":      IntentInclude,
	"scheduled_jobs":      IntentInclude,
	"backup_destinations": IntentInclude,
	"webhooks":            IntentInclude,
	"routines":            IntentInclude,
	"schedules":           IntentInclude,
	"recurring_issues":    IntentInclude,
	"triage_rules":        IntentInclude,
	"workflow_templates":  IntentInclude,
	"saved_views":         IntentInclude,

	// === Pages (round-trip) ===================================
	// PRD docs/prd/pages.md §10b.5 draws the line here: `crewship
	// export` carries the page SPEC only, because export moves
	// configuration between installs. A BACKUP is a whole-instance
	// snapshot, so it carries spec, grants, versions AND panel data —
	// "a page whose numbers vanish on restore would be a page nobody
	// trusts afterwards".
	"pages":         IntentInclude,
	"page_panels":   IntentInclude,
	"page_versions": IntentInclude,
	// page_grants is the ACL. Dropping it on restore would silently
	// widen or narrow who can read a page, and `granted_by_user_id` is
	// NOT NULL precisely so a grant always names the human accountable
	// for it (§7.1b rule 1). A restore that loses that is a restore
	// that loses the audit trail.
	"page_grants": IntentInclude,
	// page_panel_data is the payload ring — the numbers themselves.
	// Bounded to 200 rows / 7 days per panel by internal/pages, so it
	// cannot make a bundle unbounded.
	"page_panel_data": IntentInclude,
	// page_public_tokens carries HASHES, never a usable secret, plus
	// each link's expiry and revocation. Carrying it keeps a published
	// link working across a restore; dropping it would silently break
	// every external reader an accountant or client was given, with no
	// error anyone would see until they clicked.
	"page_public_tokens": IntentInclude,
	// page_webhooks is the inbound-webhook table (§10b.5c) and it is the same
	// judgement as page_public_tokens above, reached the same way. It carries a
	// SHA-256 DIGEST and never a usable secret — there is no cleartext column in
	// the schema at all — so a bundle is not a credential store, and a reader of
	// the dump learns which integrations exist, never how to be one.
	//
	// Carrying it is the choice that makes a restore honest. A page webhook is
	// wired into something outside this instance: a cron on a box we do not own,
	// a Zapier step, a PLC gateway, a GitHub Action secret. None of those is
	// re-issued by a restore, and none of them reports an error anybody here
	// would see — dropping the table would leave every external producer POSTing
	// into a 404 while the panel quietly went stale, which is precisely the
	// failure §4's freshness contract exists to surface rather than to cause.
	// The row also carries created_by_user_id (the human accountable for the
	// capability, §7.1b rule 1) and revoked_at: a restore that lost those would
	// resurrect tokens somebody had deliberately pulled, and lose the audit
	// trail that says who issued what.
	//
	// Restoring a digest hands the restored instance no authority the source did
	// not have: the token still writes exactly one panel, and its authority is
	// re-derived from its issuer's CURRENT standing on every fire, so a bundle
	// restored into a workspace where that human is not a member yields a token
	// that holds nothing.
	"page_webhooks": IntentInclude,
	// page_panel_alerts is the edge a lapse was already reported on: one row
	// per (panel, gate) while an alert is open, deleted on recovery. It rides
	// the bundle because dropping it is not neutral in either direction — a
	// restore without it re-opens an issue on the next sweep for an outage a
	// human already closed by hand, and it carries the issue_id that makes the
	// recovery entry able to name what it is closing.
	"page_panel_alerts": IntentInclude,
	"hooks":             IntentInclude,
	"labels":            IntentInclude,
	"milestones":        IntentInclude,
	"projects":          IntentInclude,

	// === Eval / training (round-trip) =========================
	"eval_runs":           IntentInclude,
	"gate_reward_history": IntentInclude,
	"missions":            IntentInclude,
	"agent_runs":          IntentInclude,

	// === Operational state (DO NOT export) ==========================
	"audit_logs":     IntentExcludeOperational,
	"backup_locks":   IntentExcludeOperational,
	"backup_catalog": IntentExcludeOperational,
	// backup_restore_origins records "this workspace was created by
	// restoring that bundle" so the DR resume can be authorised on
	// evidence (#1716). It is a fact about THIS instance's history: a
	// bundle that carried it forward would assert to the next instance
	// a lineage that instance never had, and allowRestore would then
	// authorise a resume against it.
	"backup_restore_origins":   IntentExcludeOperational,
	"journal_entries_archived": IntentExcludeOperational,
	"journal_embeddings":       IntentExcludeOperational,
	"agent_runs_archive":       IntentExcludeOperational,
	// keeper_aux_settings is instance-global evaluator wiring — one row per
	// Keeper slot for the whole server, no workspace_id. It sat in
	// NonBackedUpTables until #1554 gave the table a credential_id FK into
	// `credentials`; because credentials ARE workspace-scoped, the reverse-FK
	// walk in DiscoverScopedTables now reaches this table from `workspaces` and
	// requires an entry here (the deny-list only covers tables discovery never
	// finds, and the two maps are pinned disjoint).
	//
	// The classification is unchanged by that move: still "do not export".
	// Carrying these rows across a restore would repoint the TARGET instance's
	// evaluators at the source's models and at a credential id that means
	// nothing in the target's vault — and these five slots are the PAID half of
	// the Keeper stack, so the damage is a silent spend against the wrong
	// subscription rather than a cosmetic drift.
	//
	// One thing to settle BEFORE anyone flips this to IntentInclude (#1973):
	// credential_id is the table's only route to a workspace and it is
	// nullable, so a scope filter built from it omits every slot not tied to a
	// credential — which is most of them. That filter is computed and thrown
	// away today precisely because the table is excluded, and
	// TestScopedFilters_NeverTraverseANullableFK starts failing on this table
	// the moment that stops being true. Including it means giving it a real
	// scope column, not just changing the constant here.
	"keeper_aux_settings": IntentExcludeOperational,

	// === Runtime state (regenerates on restore) =====================
	"user_sessions": IntentExcludeRuntime,
	"cli_pairings":  IntentExcludeRuntime,
	// keeper_requests carries the same caveat as keeper_aux_settings above
	// (#1973): its only FKs out are a nullable credential_id and a nullable
	// requesting_agent_id — requesting_crew_id is a bare TEXT column with no
	// foreign key, so the walk cannot see it — and there is no NOT NULL route
	// to a workspace to prefer. Safe while excluded; not safe to include as-is.
	"keeper_requests": IntentExcludeRuntime,
	// keeper_request_events (v166) is the append-only transition ledger behind
	// keeper_requests. It follows its projection: both are per-instance runtime
	// audit, not portable workspace configuration, and a restored bundle should
	// not carry another instance's keeper decision history.
	"keeper_request_events": IntentExcludeRuntime,
	"rate_buckets":          IntentExcludeRuntime,
	"agent_status":          IntentExcludeRuntime, // live status; agent boots IDLE
	// `notifications` sat here until #1751 dropped the table: it was the
	// entity-scoped in-app feed, and nothing outside a test ever inserted a
	// row. A classification for a table that no longer exists is a claim the
	// totality guard cannot check, so it goes with the table.

	// === Outbound notifications (#1412) =============================
	// These tables carry only a plain workspace_id column (no FK to
	// workspaces), so the FK-walk discovery never surfaces them — they
	// are dumped via their explicit BackupTables entries, not discovery.
	// notification_channels (v133) is the provider/route config a
	// workspace configured; user_notification_prefs (v161) is each
	// operator's per-category × channel routing matrix. Both are durable
	// user configuration that MUST survive a restore — losing them
	// silently unsubscribes everyone. notification_deliveries (v161) is
	// the outbox/delivery LOG (dedup keys, retry counters, sent_at) —
	// operational telemetry that regenerates as new events fire, so it
	// does NOT ride bundles.
	// notification_channel_agents (v170) is the agent↔channel grant table:
	// which agents a human allowed to post to which channel. Dropping it on
	// restore would silently revoke every agent's ability to notify, and the
	// symptom — an agent quietly failing to reach anyone — is exactly the kind
	// nobody notices until it matters.
	"notification_channels":       IntentInclude,
	"user_notification_prefs":     IntentInclude,
	"notification_channel_agents": IntentInclude,
	"notification_deliveries":     IntentExcludeOperational,

	// notification_templates is the operator's own wording for what those
	// notifications SAY — authored configuration in the same sense as the
	// channels themselves. Losing it on restore would silently revert every
	// message to the shipped default, and the only symptom would be that the
	// words changed, which no alert catches.
	//
	// Listed separately from the block above because it does NOT share that
	// block's premise: it declares workspace_id REFERENCES workspaces(id), so
	// the FK walk DOES reach it and a --replace restore clears it. Sitting
	// under a comment saying discovery never surfaces these tables read as a
	// claim about this one too. See TestReplace_DiscoversNotificationTemplates.
	"notification_templates": IntentInclude,

	// === Discovered via drift detection (2026-05-25) ===============
	// Every workspace-scoped table the FK walk currently surfaces.
	// Default classification leans toward IntentInclude because the
	// risk of silent data loss (admin restores expecting "everything"
	// and gets a partial state) outweighs the risk of carrying a
	// row across instances. Anything that's clearly operational
	// (audit-like, retry counters, telemetry) is excluded explicitly.
	"agent_config_history": IntentInclude,
	"approvals_queue":      IntentInclude,
	// automations are user-authored rules: "when this happens, run that".
	// Same class as pipelines — configuration a person wrote and would have to
	// write again, not derived state. A restore that silently comes back with
	// no automations is the worst version of this failure, because everything
	// still works and simply stops happening by itself.
	//
	// Soft-deleted rows ride along (deleted_at is a column, not a filter here).
	// That is deliberate: internal/chain reads deleted rules to explain runs
	// they caused, so dropping them on restore would make restored history
	// unexplainable.
	"automations":      IntentInclude,
	"assignments":      IntentInclude,
	"budget_limits":    IntentInclude,
	"captain_chats":    IntentInclude,
	"checkpoints":      IntentInclude,
	"cost_ledger":      IntentInclude,
	"credential_crews": IntentInclude,
	// Both new with the credentials-V2 work. They hold durable user content
	// and losing them on restore is silent: a multi-part credential comes
	// back with its primary value and no access key id or region, and every
	// agent loses the slot mapping that told it which account to use. The
	// credential itself would restore fine, which is what makes the loss hard
	// to notice.
	"credential_bindings":    IntentInclude,
	"credential_fields":      IntentInclude,
	"crew_connections":       IntentInclude,
	"crew_mcp_servers":       IntentInclude,
	"crew_templates":         IntentInclude,
	"feature_flag_overrides": IntentInclude,
	// gdpr_actions MUST roundtrip. The table records Art. 15
	// (access) / Art. 17 (deletion) compliance events with required
	// `reason` fields — a regulator audit reading "we lost the
	// GDPR log on a restore" is not a defensible posture.
	"gdpr_actions":     IntentInclude,
	"hooks_config":     IntentInclude,
	"inbox_items":      IntentInclude,
	"issue_counters":   IntentInclude,
	"memory_proposals": IntentInclude,
	// onboarding_proposals is the crew a setup agent proposed and a human
	// approved. Included rather than denied because the row IS the audit
	// record of that decision: the whole design turns on the card the human
	// read being byte-identical to what executed, and a restore that dropped
	// the proposals would keep the crews while losing the only evidence of
	// who agreed to them and on what terms.
	"onboarding_proposals": IntentInclude,
	"memory_versions":      IntentInclude,
	"message_feedback":     IntentInclude,
	"mission_activity":     IntentInclude,
	// mission_code_links is the issue → pull-request/merge-request link
	// (link-first Git integration). It round-trips: the link is a fact the
	// user asserted, the same class as mission_relations, and the fetched
	// state it carries is a cache that the next refresh rewrites anyway.
	// credential_id may dangle after a restore into an instance whose
	// credentials differ — the column is ON DELETE SET NULL and nothing
	// authorises on it, so a stale id costs a re-resolve, not a leak.
	"mission_code_links": IntentInclude,
	"mission_comments":   IntentInclude,
	// mission_comment_mentions is the RESOLVED @mention set of a comment —
	// which agents a comment named, in order, and what the dispatch trigger
	// did about each. It rides with mission_comments: the bodies round-trip,
	// and re-deriving the set on restore would mean re-parsing every comment
	// (and, worse, would silently invent mentions for agent ids that resolve
	// differently in the restored instance). The dispatch columns are history,
	// not live state — a restored 'refused' row records that a cap said no at
	// the time, which is exactly the fact an operator reads it for.
	"mission_comment_mentions": IntentInclude,
	"mission_labels":           IntentInclude,
	"mission_proposals":        IntentInclude,
	"mission_relations":        IntentInclude,
	"mission_tasks":            IntentInclude,
	"peer_card_audit":          IntentExcludeOperational, // audit trail
	"peer_cards":               IntentInclude,
	// pending_runs holds deferred/debounced triggers waiting to fire
	// (delay/ttl/priority). A pending row is a scheduled future run —
	// durable, like a waitpoint; dropping it on restore loses queued work.
	"pending_runs":  IntentInclude,
	"pipeline_runs": IntentInclude,
	// pipeline_routine_state = durable cross-run watermarks per (pipeline,
	// schedule) (v155); dropping it on restore makes routines reprocess from
	// scratch or lose their "since last run" cursor.
	"pipeline_routine_state": IntentInclude,
	// pipeline_run_step_outputs (v159) is the normalized per-step
	// projection that replaced pipeline_runs.step_outputs_json on the hot
	// write path — same durability class as pipeline_runs itself (it's
	// the run-detail waterfall's data), and cascade-deletes with its run.
	"pipeline_run_step_outputs": IntentInclude,
	"pipeline_schedules":        IntentInclude,
	// pipeline_tags = routine-DEFINITION discovery tags (v125).
	"pipeline_tags":     IntentInclude,
	"pipeline_versions": IntentInclude,
	// pipeline_waitpoints holds suspended-workflow state (pending
	// approval tokens, event-wait, decision_payload, timeout_at).
	// These are DURABLE state — a "pending" waitpoint is a real
	// suspended pipeline run with a token an approver still holds.
	// Dropping these on restore breaks every in-flight workflow.
	// Initial classification (IntentExcludeRuntime) was wrong.
	"pipeline_waitpoints": IntentInclude,
	"pipeline_webhooks":   IntentInclude,
	"pipelines":           IntentInclude,
	// waitpoint_trust_grants = standing "stop asking me" decisions an
	// operator made on a specific gate. Durable governance state, not
	// runtime: dropping these on restore silently re-arms every gate the
	// operator deliberately disarmed, and the restored instance starts
	// blocking runs that used to flow. Restoring them is safe because
	// each is pinned to a definition_hash — if the routine's definition
	// did not survive the restore identically, the grant matches nothing.
	"waitpoint_trust_grants": IntentInclude,
	// routine_step_overrides = per-step prompt/model overrides (v123);
	// run_tags = per-run labels (v122). Both durable workspace state.
	"routine_step_overrides": IntentInclude,
	"run_tags":               IntentInclude,
	"skill_invocations":      IntentExcludeOperational, // telemetry
	"subscriptions":          IntentInclude,
	"user_models":            IntentInclude, // durable per-operator model
	"user_peer_consent":      IntentInclude,
	"workflow_states":        IntentInclude,
	"workspace_invitations":  IntentInclude,
	"workspace_mcp_servers":  IntentInclude,
	"workspace_members":      IntentInclude, // who can access — must restore
}

// NonBackedUpTables is the explicit deny-list: every real table that is NOT
// workspace-scoped bundle data and therefore has no BackupTableIntent entry.
//
// It is the other half of the totality guard (intent_totality_test.go): that
// test asserts EVERY table in the real migrated schema is a deliberate decision
// — either "back up" (a BackupTableIntent entry) or "do not" (an entry here).
// A new migration whose table lands in neither list fails the build, which is
// what turns the twice-seen silent-data-loss class (#1437, #1444) into a red
// build instead of a missing-rows surprise after a real restore.
//
// Adding a table HERE is a decision that it must not ride workspace bundles. If
// a table carries workspace_id / crew_id and holds durable USER content, it
// almost certainly belongs in BackupTableIntent (IntentInclude) + BackupTables
// instead — this guard's own audit moved journal_chain_checkpoints out of here
// for exactly that reason.
//
// Pure-mechanical tables (sqlite_*, the _migrations ledger, FTS5 shadow tables)
// are skipped by pattern in the guard and deliberately NOT listed here.
var NonBackedUpTables = map[string]struct{}{
	// ── Global / cross-workspace identities. No workspace_id; keyed by global
	//    unique columns. users/workspaces/skills are dumped (if at all) via the
	//    special-cased head of BackupTables, not the workspace-scoped intent map.
	"users":         {}, // global identity (dumped via reverse-FK in BackupTables)
	"workspaces":    {}, // the anchor itself, not a scoped child
	"skills":        {}, // global catalog, re-seeded on boot (SeedBundledSkills)
	"skill_reviews": {}, // reviews OF global skills — global, not per-workspace
	"plans":         {}, // global billing-tier catalog (Stripe price ids, limits)

	// ── Auth / session / token state. Per-user or per-device; never part of a
	//    portable workspace bundle, regenerated by signing in again.
	"accounts":            {}, // linked auth accounts
	"sessions":            {}, // web sessions (user_sessions is separately IntentExcludeRuntime)
	"verification_tokens": {}, // email/verification nonces
	"oauth_states":        {}, // OAuth CSRF nonce, ephemeral
	"user_preferences":    {}, // per-user UI prefs, not workspace content
	"cli_tokens":          {}, // CLI auth tokens (device-local secrets)
	"cli_token_uses":      {}, // CLI token usage log

	// ── Instance-global configuration & operational state. Local to THIS
	//    instance; carrying it across a restore would clobber the target's own.
	"app_settings":            {},
	"instance_config":         {},
	"rate_limit_overrides":    {}, // instance-global limiter tuning (v168); must not clobber the target's own on restore
	"keeper_runtime_settings": {}, // instance-global judge wiring; a restored workspace must not repoint the target's gatekeeper at the source's model server
	// keeper_aux_settings moved to BackupTableIntent (IntentExcludeOperational)
	// in #1554: its new credential_id FK makes the reverse-FK walk discover it,
	// and a discovered table must be classified there, not here. Same verdict —
	// instance-global evaluator wiring, never exported.
	"feature_flags":            {}, // global flag defaults (per-ws feature_flag_overrides IS backed up)
	"scheduler_leader":         {}, // leader-election lease
	"pipeline_run_idempotency": {}, // dispatch dedup keys, runtime
	"pipeline_signal_waits":    {}, // in-flight signal waits, runtime

	// ── MCP tool registry & call log. Instance-level tool wiring + telemetry.
	//    (Per-workspace MCP servers ARE backed up: workspace_mcp_servers /
	//    crew_mcp_servers / agent_mcp_bindings.)
	"mcp_registry_servers": {},
	"mcp_tool_bindings":    {},
	"mcp_tool_calls":       {}, // call telemetry

	// ── Inter-agent runtime history. These carry workspace/crew/agent scope but
	//    are the instance's live comms / governance LOG, not authored user
	//    content, and have never ridden bundles (kept instance-local like
	//    audit_logs). If a product decision makes any of these portable, move it
	//    to BackupTableIntent (IntentInclude) + BackupTables + a workspaceFilterSQL
	//    scope — do not leave it half-wired.
	"conversation_messages": {}, // agent transcript log (session_id / agent_id)
	"crew_messages":         {}, // cross-crew message delivery log
	"peer_conversations":    {}, // sidecar peer Q&A history
	"escalations":           {}, // keeper escalation history
	"crew_audit_log":        {}, // crew action audit trail (operational)
}

// IncludedTables returns the names of tables the bundle should
// include, derived from BackupTableIntent. Sorted alphabetically
// — map iteration order is nondeterministic so the explicit sort
// pins the contract callers rely on (drift-detection test fixtures
// compare ordered slices). Runtime ordering for FK-safe INSERT is
// computed elsewhere via DiscoverScopedTables.
func IncludedTables() []string {
	out := make([]string, 0, len(BackupTableIntent))
	for name, intent := range BackupTableIntent {
		if intent == IntentInclude {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}
