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

	// === Credentials & secrets (round-trip; cipher preserved) ========
	"credentials":          IntentInclude,
	"agent_credentials":    IntentInclude,
	"credential_audit":     IntentInclude,
	"credential_rotations": IntentInclude,
	// Composio managed-integration provider config (encrypted API key per
	// workspace). Workspace-scoped; round-trips with the encrypted value.
	"composio_settings": IntentInclude,

	// === Files & memory (round-trip) ==========================
	"chat_branches":           IntentInclude,
	"chat_attachments":        IntentInclude,
	"chat_participants":       IntentInclude,
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
	"hooks":               IntentInclude,
	"labels":              IntentInclude,
	"milestones":          IntentInclude,
	"projects":            IntentInclude,

	// === Eval / training (round-trip) =========================
	"eval_runs":           IntentInclude,
	"gate_reward_history": IntentInclude,
	"missions":            IntentInclude,
	"agent_runs":          IntentInclude,

	// === Operational state (DO NOT export) ==========================
	"audit_logs":               IntentExcludeOperational,
	"backup_locks":             IntentExcludeOperational,
	"backup_catalog":           IntentExcludeOperational,
	"journal_entries_archived": IntentExcludeOperational,
	"journal_embeddings":       IntentExcludeOperational,
	"agent_runs_archive":       IntentExcludeOperational,

	// === Runtime state (regenerates on restore) =====================
	"user_sessions":   IntentExcludeRuntime,
	"cli_pairings":    IntentExcludeRuntime,
	"keeper_requests": IntentExcludeRuntime,
	"rate_buckets":    IntentExcludeRuntime,
	"agent_status":    IntentExcludeRuntime, // live status; agent boots IDLE
	"notifications":   IntentExcludeRuntime, // transient; resend on the new instance

	// === Discovered via drift detection (2026-05-25) ===============
	// Every workspace-scoped table the FK walk currently surfaces.
	// Default classification leans toward IntentInclude because the
	// risk of silent data loss (admin restores expecting "everything"
	// and gets a partial state) outweighs the risk of carrying a
	// row across instances. Anything that's clearly operational
	// (audit-like, retry counters, telemetry) is excluded explicitly.
	"agent_config_history":   IntentInclude,
	"approvals_queue":        IntentInclude,
	"assignments":            IntentInclude,
	"budget_limits":          IntentInclude,
	"captain_chats":          IntentInclude,
	"checkpoints":            IntentInclude,
	"cost_ledger":            IntentInclude,
	"credential_crews":       IntentInclude,
	"crew_connections":       IntentInclude,
	"crew_mcp_servers":       IntentInclude,
	"crew_templates":         IntentInclude,
	"feature_flag_overrides": IntentInclude,
	// gdpr_actions MUST roundtrip. The table records Art. 15
	// (access) / Art. 17 (deletion) compliance events with required
	// `reason` fields — a regulator audit reading "we lost the
	// GDPR log on a restore" is not a defensible posture.
	"gdpr_actions":      IntentInclude,
	"hooks_config":      IntentInclude,
	"inbox_items":       IntentInclude,
	"issue_counters":    IntentInclude,
	"memory_proposals":  IntentInclude,
	"memory_versions":   IntentInclude,
	"message_feedback":  IntentInclude,
	"mission_activity":  IntentInclude,
	"mission_comments":  IntentInclude,
	"mission_labels":    IntentInclude,
	"mission_proposals": IntentInclude,
	"mission_relations": IntentInclude,
	"mission_tasks":     IntentInclude,
	"peer_card_audit":   IntentExcludeOperational, // audit trail
	"peer_cards":        IntentInclude,
	// pending_runs holds deferred/debounced triggers waiting to fire
	// (delay/ttl/priority). A pending row is a scheduled future run —
	// durable, like a waitpoint; dropping it on restore loses queued work.
	"pending_runs":       IntentInclude,
	"pipeline_runs":      IntentInclude,
	"pipeline_schedules": IntentInclude,
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
