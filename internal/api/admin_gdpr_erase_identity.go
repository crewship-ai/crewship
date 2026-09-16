package api

// Art. 17 erasure — unnaming the subject on every workspace-scoped table
// outside Pages that the schema sweep found (issue #2308; the Pages four are
// pages_erase_identity.go, #1976).
//
// # What this file is
//
// admin_gdpr.go's cascade enumerated seven tables and pages_erase_identity.go
// added four. subjectSightings (admin_gdpr_pages_identity_test.go) walks the
// live schema and found ~40 more columns that name a user and sit in rows
// this workspace owns — a credential the subject created, a routine they
// authored, the trust grants and invitations they issued, their own saved
// views. Every one of them kept naming a person whose SAR ticket had been
// closed as "erased". This file is the rest of the sentence the contract in
// admin_gdpr.go promises: UNNAMED IN THIS WORKSPACE, on every table listed
// here.
//
// # The three verbs, and how each column got its verb
//
// The split is the one #1976 and #2233 each settled for one table, applied
// once for all of them:
//
//   - The subject's OWN records — a saved view, their notification
//     preferences, the deliveries addressed to them, the personal channel
//     that carries their address, the onboarding proposal drafted in their
//     session, the invitations they sent — are DELETED. These are not
//     attribution on someone else's row; the row IS the data about them.
//
//   - A CAPABILITY the subject issued is REVOKED. A standing trust grant on a
//     routine gate is page_grants' twin (a human delegating authority, with a
//     NOT NULL issuer the schema will not let us blank), so it goes the way a
//     grant goes: deleted, and journalled with the same entry the ordinary
//     revoke path writes. A pending invitation is a live capability to join
//     the workspace in the subject's name — deleted. Credentials are the
//     exception, argued below.
//
//   - HISTORY is ANONYMISED. Where the row is worth keeping and the name is
//     not — who created this agent, who ran this routine, who approved this
//     task, who uploaded this file — the column is cleared and the row stays.
//     The FK actions the schema already declares mostly agree (SET NULL on
//     agents.created_by_user_id, pipelines.author_user_id,
//     mission_code_links.created_by_user_id, workspace_files.created_by,
//     attachments.uploaded_by_user_id, composio_settings.created_by,
//     keeper_governance_settings.*, credential_rotations.rotated_by); the
//     rest are nullable columns with no FK, which is the schema saying the
//     same thing less formally: a row without its author is still a row.
//
// Two columns cannot be nulled and are not worth deleting for: a version's
// author on pipeline_versions and a comment's author on mission_comments are
// NOT NULL beside an author_type. For author_type='user' the id becomes the
// empty string — the row keeps saying "a human wrote this" and stops saying
// which one. All three readers of mission_comments tolerate an author that
// resolves to nobody: the comment list and the internal issue view COALESCE
// the display name, and pendingFollowUpsFor scans it into a NullString.
//
// # Why the issue's "revoke" column ends up mostly anonymised
//
// #2308 lists hooks_config, automations, recurring_issues, notification
// channels and their agent grants under "capability — revoke", on the
// argument that they keep firing on the authority of a departed human. The
// schema disagrees, and the schema is what #1976 chose to follow: every one
// of those created_by / granted_by columns is NULLABLE and carries no FK
// ("Nullable so a seed or an import doesn't fail", notification_channel_
// agents' own words). A row the schema lets exist with no issuer is not
// authority delegated by one person — it is workspace configuration that any
// admin edits and any admin can delete, and deleting a team's cron issues or
// Slack channel because one member was erased would be a loss the operator
// did not ask for. The rule this file applies is the NOT NULL test: an
// issuer column the schema refuses to blank (trust grants, invitations) is a
// capability that dies with its issuer; a nullable one is attribution, and
// attribution is anonymised. What DOES go is the subject's personal channel
// (scope='user', owner_user_id): that row carries their own address and is
// theirs, not the team's.
//
// # Credentials: re-attributed to a custodian, not deleted
//
// credentials.created_by is NOT NULL REFERENCES users(id), so it can be
// neither nulled nor pointed at nobody, and the credential itself must stay
// usable — every agent bound to it would otherwise lose a secret the
// workspace still relies on, which #2308 rules out. escalation_credential.go
// already has the precedent for a credential whose human cannot be named
// (an agent-proposed one is attributed to the workspace OWNER until an
// approver takes it over). Here the custodian is the ADMIN WHO RAN THE
// ERASURE — the person who chose to keep the credential live is the person
// accountable for it from now on — falling back to the oldest OWNER when the
// admin is erasing themself. The polymorphic created_by_actor_id and
// approved_by_user_id, both nullable, are simply cleared. Each hand-over is
// written to the credential's own audit timeline as a REATTRIBUTED event
// carrying the gdpr_actions id, because a credential whose custodian changed
// with no trace on its timeline is exactly the row the timeline exists to
// prevent. Bindings on the credential (credential_bindings.created_by,
// nullable) are anonymised, not removed: deleting them would revoke the
// credential by the back door.
//
// # What is deliberately NOT here
//
//   - missions.owner_user_id — an issue with no owner may be a state the app
//     cannot render; it needs pages' transfer-or-refuse treatment (#1944),
//     not a blind NULL.
//   - chats, chat_participants, chat_read_cursors, captain_chats,
//     conversation_messages, message_reactions, message_feedback — chat
//     history is operator-handled per docs/security/gdpr.mdx; that note is a
//     placeholder awaiting a decision, and this file does not make it.
//   - user_peer_consent — the subject's recorded consent state, which
//     arguably must survive as proof of consent. Undecided.
//   - peer_card_audit — the peer_cards audit trail; whether it is
//     accountability (excluded) or content (erased) is #2233's argument
//     again. Undecided.
//   - workspace_members — membership is removed by RemoveMember, not by an
//     erasure (see admin_gdpr.go).
//   - skills, skill_reviews, rate_limit_overrides, keeper_runtime_settings,
//     keeper_aux_settings — instance-scoped, no workspace column. A
//     workspace erasure cannot speak for them (the issue's own rule).
//   - memory_proposals.decided_by_user_id — a CHECK requires a decided
//     proposal to name its decider ("HITL approval trail integrity", the
//     migration says). Nulling violates the constraint; re-attributing
//     falsifies an accountability record. Left for a decision.
//   - agent_config_history.changed_by — NOT NULL REFERENCES users(id) on a
//     versioned config log; needs a migration relaxing the constraint before
//     it can be anonymised.
//   - backup_locks.acquired_by — a transient lock row deleted on release and
//     evicted on expiry; it never outlives the backup that took it.
//   - mission_proposals.proposed_by_id — REFERENCES agents(id); it is an
//     agent, not a user, whatever the sweep's suffix rule thinks.
//   - journal_entries, journal_entries_archived, audit_logs, gdpr_actions,
//     keeper_request_events, mission_activity, journal_entry_priorities —
//     append-only accountability. A SAR does not erase the SAR.
//
// # Scoping, atomicity, receipts
//
// Every statement is scoped through workspace_id, or through a join to a
// parent that has one (pipelines, missions, credentials). All statements
// run in ONE transaction, like the Pages four and for the same reason: they
// are one promise, and a receipt that says the trust grants went while the
// invitation the same person sent is still live is worse than a refusal.
// Nothing here touches disk, so the stronger guarantee costs nothing.
//
// The receipt is one count per table, keyed by table AND verb, because
// "anonymised" and "removed" are different answers to give the person who
// filed the request. Every key is reported even at zero, and on failure
// every key is zero — the transaction rolled back, and an audit row is the
// last place to report work that was undone.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// identityArg names a bound parameter of an identityStep by role, so a step
// declares WHICH values it takes and the executor supplies them — a
// misordered literal cannot swap subject and workspace.
type identityArg int

const (
	argSubject identityArg = iota
	argWorkspace
	argCustodian
)

// identityStep is one workspace-scoped statement of the erasure: the scope
// key it reports under (verb included), whether the rows it touches went AWAY
// (and so count toward rows_deleted), the statement, and its parameters.
type identityStep struct {
	key     string
	deletes bool
	sql     string
	args    []identityArg
}

// identitySteps is the whole list, in the order it runs. The order only
// matters twice: the subject's own notification preferences and deliveries
// go before their personal channel (the channel's FK would cascade the
// prefs anyway, but a count that depends on cascade order is not a count),
// and credentials are re-attributed before their bindings and rotations are
// anonymised, so a rolled-back custodian lookup leaves both untouched.
var identitySteps = []identityStep{
	// ── The subject's own records — deleted ────────────────────────────
	{key: "saved_views_removed", deletes: true,
		sql:  `DELETE FROM saved_views WHERE workspace_id = ? AND user_id = ?`,
		args: []identityArg{argWorkspace, argSubject}},
	{key: "user_notification_prefs_removed", deletes: true,
		sql:  `DELETE FROM user_notification_prefs WHERE workspace_id = ? AND user_id = ?`,
		args: []identityArg{argWorkspace, argSubject}},
	{key: "notification_deliveries_removed", deletes: true,
		sql:  `DELETE FROM notification_deliveries WHERE workspace_id = ? AND user_id = ?`,
		args: []identityArg{argWorkspace, argSubject}},
	// The personal channel: scope='user' rows carry the owner's own address
	// in config_json. Workspace-scoped channels the subject merely created
	// are anonymised further down.
	{key: "notification_channels_removed", deletes: true,
		sql:  `DELETE FROM notification_channels WHERE workspace_id = ? AND scope = 'user' AND owner_user_id = ?`,
		args: []identityArg{argWorkspace, argSubject}},
	// created_by is NOT NULL REFERENCES users(id) and the payload is the
	// proposal the Guide drafted in the subject's session — their content.
	{key: "onboarding_proposals_removed", deletes: true,
		sql:  `DELETE FROM onboarding_proposals WHERE workspace_id = ? AND created_by = ?`,
		args: []identityArg{argWorkspace, argSubject}},

	// ── Capabilities — revoked ─────────────────────────────────────────
	// invited_by is NOT NULL REFERENCES users(id): a pending invitation is
	// a live capability to join in the subject's name, and an accepted one
	// is a spent token whose durable outcome is the workspace_members row.
	{key: "workspace_invitations_revoked", deletes: true,
		sql:  `DELETE FROM workspace_invitations WHERE workspace_id = ? AND invited_by = ?`,
		args: []identityArg{argWorkspace, argSubject}},
	// page_grants' twin (internal/pipeline/trust_grants.go): NOT NULL
	// issuer, a human delegating authority. Journalled per row after
	// commit — see collectErasedTrustGrants.
	{key: "trust_grants_revoked", deletes: true,
		sql:  `DELETE FROM waitpoint_trust_grants WHERE workspace_id = ? AND granted_by_user_id = ?`,
		args: []identityArg{argWorkspace, argSubject}},
	{key: "trust_grants_anonymised",
		sql:  `UPDATE waitpoint_trust_grants SET revoked_by_user_id = NULL WHERE workspace_id = ? AND revoked_by_user_id = ?`,
		args: []identityArg{argWorkspace, argSubject}},
	// Credentials: created_by hands over to the custodian (see the file
	// header), the nullable attribution columns are cleared. One statement,
	// so a row naming the subject in two columns is counted once.
	{key: "credentials_reattributed",
		sql: `UPDATE credentials
		         SET created_by          = CASE WHEN created_by = ? THEN ? ELSE created_by END,
		             created_by_actor_id = CASE WHEN created_by_actor_id = ? THEN NULL ELSE created_by_actor_id END,
		             approved_by_user_id = CASE WHEN approved_by_user_id = ? THEN NULL ELSE approved_by_user_id END
		       WHERE workspace_id = ?
		         AND (created_by = ? OR created_by_actor_id = ? OR approved_by_user_id = ?)`,
		args: []identityArg{argSubject, argCustodian, argSubject, argSubject, argWorkspace, argSubject, argSubject, argSubject}},
	{key: "credential_bindings_anonymised",
		sql:  `UPDATE credential_bindings SET created_by = NULL WHERE workspace_id = ? AND created_by = ?`,
		args: []identityArg{argWorkspace, argSubject}},
	{key: "credential_rotations_anonymised",
		sql: `UPDATE credential_rotations SET rotated_by = NULL
		       WHERE rotated_by = ? AND credential_id IN (SELECT id FROM credentials WHERE workspace_id = ?)`,
		args: []identityArg{argSubject, argWorkspace}},
	// Workspace configuration the subject happened to create: nullable,
	// no FK — attribution, anonymised (file header, "Why the issue's
	// 'revoke' column ends up mostly anonymised").
	{key: "notification_channels_anonymised",
		sql: `UPDATE notification_channels
		         SET created_by    = CASE WHEN created_by = ? THEN NULL ELSE created_by END,
		             owner_user_id = CASE WHEN owner_user_id = ? THEN NULL ELSE owner_user_id END
		       WHERE workspace_id = ? AND (created_by = ? OR owner_user_id = ?)`,
		args: []identityArg{argSubject, argSubject, argWorkspace, argSubject, argSubject}},
	{key: "notification_channel_agents_anonymised",
		sql:  `UPDATE notification_channel_agents SET granted_by = NULL WHERE workspace_id = ? AND granted_by = ?`,
		args: []identityArg{argWorkspace, argSubject}},
	{key: "hooks_config_anonymised",
		sql:  `UPDATE hooks_config SET created_by = NULL WHERE workspace_id = ? AND created_by = ?`,
		args: []identityArg{argWorkspace, argSubject}},
	{key: "automations_anonymised",
		sql:  `UPDATE automations SET created_by = NULL WHERE workspace_id = ? AND created_by = ?`,
		args: []identityArg{argWorkspace, argSubject}},
	{key: "recurring_issues_anonymised",
		sql:  `UPDATE recurring_issues SET created_by = NULL WHERE workspace_id = ? AND created_by = ?`,
		args: []identityArg{argWorkspace, argSubject}},
	// default_user_id is a Composio-side identity, not ours; it is cleared
	// only when it literally equals the subject's id (an operator who keys
	// Composio users by Crewship id), and the default re-derives itself.
	{key: "composio_settings_anonymised",
		sql: `UPDATE composio_settings
		         SET created_by      = CASE WHEN created_by = ? THEN NULL ELSE created_by END,
		             default_user_id = CASE WHEN default_user_id = ? THEN NULL ELSE default_user_id END
		       WHERE workspace_id = ? AND (created_by = ? OR default_user_id = ?)`,
		args: []identityArg{argSubject, argSubject, argWorkspace, argSubject, argSubject}},

	// ── History — anonymised ───────────────────────────────────────────
	{key: "agents_anonymised",
		sql: `UPDATE agents
		         SET created_by_user_id           = CASE WHEN created_by_user_id = ? THEN NULL ELSE created_by_user_id END,
		             self_learning_set_by_user_id = CASE WHEN self_learning_set_by_user_id = ? THEN NULL ELSE self_learning_set_by_user_id END
		       WHERE workspace_id = ? AND (created_by_user_id = ? OR self_learning_set_by_user_id = ?)`,
		args: []identityArg{argSubject, argSubject, argWorkspace, argSubject, argSubject}},
	{key: "crews_anonymised",
		sql:  `UPDATE crews SET autonomy_set_by_user_id = NULL WHERE workspace_id = ? AND autonomy_set_by_user_id = ?`,
		args: []identityArg{argWorkspace, argSubject}},
	{key: "pipelines_anonymised",
		sql:  `UPDATE pipelines SET author_user_id = NULL WHERE workspace_id = ? AND author_user_id = ?`,
		args: []identityArg{argWorkspace, argSubject}},
	// NOT NULL beside author_type: the row keeps saying a human wrote it.
	{key: "pipeline_versions_anonymised",
		sql: `UPDATE pipeline_versions SET author_id = ''
		       WHERE author_type = 'user' AND author_id = ?
		         AND pipeline_id IN (SELECT id FROM pipelines WHERE workspace_id = ?)`,
		args: []identityArg{argSubject, argWorkspace}},
	{key: "pipeline_runs_anonymised",
		sql:  `UPDATE pipeline_runs SET invoking_user_id = NULL WHERE workspace_id = ? AND invoking_user_id = ?`,
		args: []identityArg{argWorkspace, argSubject}},
	{key: "pending_runs_anonymised",
		sql:  `UPDATE pending_runs SET invoking_user_id = NULL WHERE workspace_id = ? AND invoking_user_id = ?`,
		args: []identityArg{argWorkspace, argSubject}},
	{key: "pipeline_waitpoints_anonymised",
		sql:  `UPDATE pipeline_waitpoints SET decided_by_user_id = NULL WHERE workspace_id = ? AND decided_by_user_id = ?`,
		args: []identityArg{argWorkspace, argSubject}},
	{key: "agent_runs_archive_anonymised",
		sql:  `UPDATE agent_runs_archive SET triggered_by = NULL WHERE workspace_id = ? AND triggered_by = ?`,
		args: []identityArg{argWorkspace, argSubject}},
	// owner_user_id is deliberately absent — see the file header.
	{key: "missions_anonymised",
		sql:  `UPDATE missions SET created_by_user_id = NULL WHERE workspace_id = ? AND created_by_user_id = ?`,
		args: []identityArg{argWorkspace, argSubject}},
	{key: "mission_proposals_anonymised",
		sql:  `UPDATE mission_proposals SET reviewed_by = NULL WHERE workspace_id = ? AND reviewed_by = ?`,
		args: []identityArg{argWorkspace, argSubject}},
	{key: "mission_tasks_anonymised",
		sql: `UPDATE mission_tasks SET approved_by = NULL
		       WHERE approved_by = ? AND mission_id IN (SELECT id FROM missions WHERE workspace_id = ?)`,
		args: []identityArg{argSubject, argWorkspace}},
	{key: "mission_comments_anonymised",
		sql: `UPDATE mission_comments SET author_id = ''
		       WHERE author_type = 'user' AND author_id = ?
		         AND mission_id IN (SELECT id FROM missions WHERE workspace_id = ?)`,
		args: []identityArg{argSubject, argWorkspace}},
	{key: "mission_code_links_anonymised",
		sql:  `UPDATE mission_code_links SET created_by_user_id = NULL WHERE workspace_id = ? AND created_by_user_id = ?`,
		args: []identityArg{argWorkspace, argSubject}},
	{key: "checkpoints_anonymised",
		sql:  `UPDATE checkpoints SET created_by = NULL WHERE workspace_id = ? AND created_by = ?`,
		args: []identityArg{argWorkspace, argSubject}},
	// Instance-level backups have workspace_id NULL and are not this
	// workspace's to speak for; only rows that name this workspace match.
	{key: "backup_catalog_anonymised",
		sql:  `UPDATE backup_catalog SET created_by = NULL WHERE workspace_id = ? AND created_by = ?`,
		args: []identityArg{argWorkspace, argSubject}},
	// NOT NULL DEFAULT '' — the empty string is the schema's own "nobody".
	{key: "backup_restore_origins_anonymised",
		sql:  `UPDATE backup_restore_origins SET restored_by = '' WHERE workspace_id = ? AND restored_by = ?`,
		args: []identityArg{argWorkspace, argSubject}},
	{key: "eval_runs_anonymised",
		sql:  `UPDATE eval_runs SET created_by = NULL WHERE workspace_id = ? AND created_by = ?`,
		args: []identityArg{argWorkspace, argSubject}},
	{key: "workspace_files_anonymised",
		sql:  `UPDATE workspace_files SET created_by = NULL WHERE workspace_id = ? AND created_by = ?`,
		args: []identityArg{argWorkspace, argSubject}},
	{key: "attachments_anonymised",
		sql:  `UPDATE attachments SET uploaded_by_user_id = NULL WHERE workspace_id = ? AND uploaded_by_user_id = ?`,
		args: []identityArg{argWorkspace, argSubject}},
	{key: "escalations_anonymised",
		sql:  `UPDATE escalations SET resolved_by = NULL WHERE workspace_id = ? AND resolved_by = ?`,
		args: []identityArg{argWorkspace, argSubject}},
	{key: "gate_reward_history_anonymised",
		sql:  `UPDATE gate_reward_history SET decided_by = NULL WHERE workspace_id = ? AND decided_by = ?`,
		args: []identityArg{argWorkspace, argSubject}},
	// Both columns are ON DELETE SET NULL already. A cleared security
	// contact leaves keeper findings untargeted (visible to every admin
	// rather than one), and the erasure logs a warning so the operator
	// appoints a new one — see eraseSubjectIdentity.
	{key: "keeper_governance_settings_anonymised",
		sql: `UPDATE keeper_governance_settings
		         SET updated_by               = CASE WHEN updated_by = ? THEN NULL ELSE updated_by END,
		             security_contact_user_id = CASE WHEN security_contact_user_id = ? THEN NULL ELSE security_contact_user_id END
		       WHERE workspace_id = ? AND (updated_by = ? OR security_contact_user_id = ?)`,
		args: []identityArg{argSubject, argSubject, argWorkspace, argSubject, argSubject}},
	// The two cascade tables that were matched on data_subject_id only:
	// the OTHER columns on rows that survive that match.
	{key: "memory_versions_anonymised",
		sql:  `UPDATE memory_versions SET written_by = NULL WHERE workspace_id = ? AND written_by = ?`,
		args: []identityArg{argWorkspace, argSubject}},
	// A cleared target_user_id makes the item untargeted, i.e. visible to
	// everyone who can see the inbox — the right fallback for a work item
	// whose addressee is gone.
	{key: "inbox_items_anonymised",
		sql: `UPDATE inbox_items
		         SET target_user_id      = CASE WHEN target_user_id = ? THEN NULL ELSE target_user_id END,
		             read_by_user_id     = CASE WHEN read_by_user_id = ? THEN NULL ELSE read_by_user_id END,
		             resolved_by_user_id = CASE WHEN resolved_by_user_id = ? THEN NULL ELSE resolved_by_user_id END
		       WHERE workspace_id = ? AND (target_user_id = ? OR read_by_user_id = ? OR resolved_by_user_id = ?)`,
		args: []identityArg{argSubject, argSubject, argSubject, argWorkspace, argSubject, argSubject, argSubject}},
}

// identityErasure is the receipt eraseSubjectIdentity hands back to the
// cascade: one count per step key, and the journal entries to emit once the
// transaction has committed.
type identityErasure struct {
	Counts map[string]int

	// RowsDeleted is the sum of the counts whose step deletes — what
	// rows_deleted absorbs. Anonymised and re-attributed rows are not in it:
	// nothing went away.
	RowsDeleted int

	revoked []pageCapabilityRevocation
}

// zeroIdentityErasure returns a receipt with every key present at zero — the
// shape a failed (rolled-back) run reports, and the starting point of a
// successful one. A key that is absent would be indistinguishable from a
// step that never existed; a key at zero says "ran, or was undone".
func zeroIdentityErasure() identityErasure {
	out := identityErasure{Counts: make(map[string]int, len(identitySteps))}
	for _, s := range identitySteps {
		out.Counts[s.key] = 0
	}
	return out
}

// identityScopeKeys lists every key the receipt reports, sorted, for callers
// that need the set without a run (tests, the audit-row shape).
func identityScopeKeys() []string {
	keys := make([]string, 0, len(identitySteps))
	for _, s := range identitySteps {
		keys = append(keys, s.key)
	}
	sort.Strings(keys)
	return keys
}

// errNoCredentialCustodian is returned when the subject created credentials
// in this workspace and nobody can take them over: the admin running the
// erasure IS the subject and the workspace has no other OWNER. The whole
// identity step refuses rather than half-applying — see the file header.
var errNoCredentialCustodian = errors.New("gdpr erasure: the subject created credentials in this workspace and no other OWNER exists to take custody of them")

// eraseSubjectIdentity runs every identityStep for targetID inside wsID, in
// one transaction, and journals the capabilities it revoked after commit.
// actorID is the admin running the erasure and the default custodian for
// the subject's credentials.
//
// On ANY error the receipt is zeroed, not partial — the transaction rolled
// back, and counts accumulated before the failure describe work that no
// longer exists.
func (h *AdminGDPRHandler) eraseSubjectIdentity(ctx context.Context, actionID, actorID, wsID, targetID string) (identityErasure, error) {
	out := zeroIdentityErasure()

	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return zeroIdentityErasure(), fmt.Errorf("begin identity erasure: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	custodian, err := credentialCustodian(ctx, tx, wsID, targetID, actorID)
	if err != nil {
		return zeroIdentityErasure(), err
	}

	// Everything that has to be READ before the statement that removes it:
	// the trust grants (journalled per row, and the row is gone after), the
	// credentials changing custodian (an audit event each), and whether the
	// security contact is about to be cleared (a warning the operator needs).
	grants, err := collectErasedTrustGrants(ctx, tx, wsID, targetID)
	if err != nil {
		return zeroIdentityErasure(), err
	}
	credIDs, err := collectReattributedCredentials(ctx, tx, wsID, targetID)
	if err != nil {
		return zeroIdentityErasure(), err
	}
	var contactCleared bool
	if err := tx.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM keeper_governance_settings WHERE workspace_id = ? AND security_contact_user_id = ?)`,
		wsID, targetID).Scan(&contactCleared); err != nil {
		return zeroIdentityErasure(), fmt.Errorf("read keeper security contact: %w", err)
	}

	bound := map[identityArg]any{argSubject: targetID, argWorkspace: wsID, argCustodian: custodian}
	for _, step := range identitySteps {
		args := make([]any, len(step.args))
		for i, a := range step.args {
			args[i] = bound[a]
		}
		res, err := tx.ExecContext(ctx, step.sql, args...)
		if err != nil {
			return zeroIdentityErasure(), fmt.Errorf("%s: %w", step.key, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return zeroIdentityErasure(), fmt.Errorf("%s: rows affected: %w", step.key, err)
		}
		out.Counts[step.key] = int(n)
		if step.deletes {
			out.RowsDeleted += int(n)
		}
	}

	// The custodian hand-over lands on each credential's own timeline,
	// inside the same transaction: a credential must not change hands
	// without its audit row, for the same reason it must not be created
	// without one (credentials_mutate.go).
	for _, id := range credIDs {
		if err := RecordCredentialEventTx(ctx, tx, id, AuditEventReattributed, "", "", map[string]any{
			"cause":             "gdpr_erasure",
			"gdpr_action_id":    actionID,
			"custodian_user_id": custodian,
		}); err != nil {
			return zeroIdentityErasure(), fmt.Errorf("record credential re-attribution: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return zeroIdentityErasure(), fmt.Errorf("commit identity erasure: %w", err)
	}

	out.revoked = grants
	h.journalRevocations(ctx, actionID, actorID, wsID, out.revoked)
	if contactCleared {
		h.logger.Warn("gdpr delete: the erased subject was the keeper security contact — the workspace now has none; appoint a new one",
			"action_id", actionID, "workspace_id", wsID)
	}
	if len(credIDs) > 0 {
		h.logger.Info("gdpr delete: credentials the subject created were handed to a custodian",
			"action_id", actionID, "workspace_id", wsID, "custodian_user_id", custodian, "count", len(credIDs))
	}
	return out, nil
}

// credentialCustodian picks who takes over credentials.created_by: the admin
// running the erasure, unless they are erasing themself, in which case the
// oldest OWNER of the workspace who is not the subject. Returns
// errNoCredentialCustodian only when a custodian is actually NEEDED — a
// workspace where the subject created nothing does not care.
func credentialCustodian(ctx context.Context, tx *sql.Tx, wsID, targetID, actorID string) (string, error) {
	if actorID != "" && actorID != targetID {
		return actorID, nil
	}
	var owner string
	err := tx.QueryRowContext(ctx, `
		SELECT user_id FROM workspace_members
		 WHERE workspace_id = ? AND role = 'OWNER' AND user_id <> ?
		 ORDER BY created_at ASC LIMIT 1`, wsID, targetID).Scan(&owner)
	switch {
	case err == nil:
		return owner, nil
	case errors.Is(err, sql.ErrNoRows):
		var needed bool
		if err := tx.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM credentials WHERE workspace_id = ? AND created_by = ?)`,
			wsID, targetID).Scan(&needed); err != nil {
			return "", fmt.Errorf("read credentials needing a custodian: %w", err)
		}
		if needed {
			return "", errNoCredentialCustodian
		}
		// Nothing to re-attribute; the CASE never fires. Bind the subject
		// itself so the statement stays well-formed — the predicate cannot
		// match a row, so the value is never written.
		return targetID, nil
	default:
		return "", fmt.Errorf("find credential custodian: %w", err)
	}
}

// collectReattributedCredentials reads the ids of the credentials whose
// created_by is about to change hands, so each gets its audit event.
func collectReattributedCredentials(ctx context.Context, tx *sql.Tx, wsID, targetID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT id FROM credentials WHERE workspace_id = ? AND created_by = ? ORDER BY id`, wsID, targetID)
	if err != nil {
		return nil, fmt.Errorf("read credentials to re-attribute: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("read credentials to re-attribute: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read credentials to re-attribute: %w", err)
	}
	return ids, nil
}

// collectErasedTrustGrants reads the standing approvals the trust_grants_
// revoked step is about to delete, shaped as the entry the ordinary revoke
// path (pipeline_trust.go) writes. Only grants that were still LIVE get an
// entry — approval.trust_revoked's own contract is that an attempt which
// changed nothing must not read as a withdrawal that happened. The
// definition hash travels with the entry because a grant only ever fired
// against that exact routine body, and an entry without it cannot say what
// was trusted.
//
// Never the subject's id: journal_entries is on the excluded list, and an
// entry naming them would have the erasure re-name the subject in a table
// nothing will ever clean. The actor is the admin, the link is the action id.
func collectErasedTrustGrants(ctx context.Context, tx *sql.Tx, wsID, targetID string) ([]pageCapabilityRevocation, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT g.id, g.pipeline_id, p.slug, g.step_id, g.definition_hash,
		       COALESCE(g.revoked_at, ''), COALESCE(g.expires_at, ''), g.uses, g.max_uses
		  FROM waitpoint_trust_grants g
		  JOIN pipelines p ON p.id = g.pipeline_id
		 WHERE g.workspace_id = ? AND g.granted_by_user_id = ?`, wsID, targetID)
	if err != nil {
		return nil, fmt.Errorf("read waitpoint_trust_grants to revoke: %w", err)
	}
	defer func() { _ = rows.Close() }()

	now := time.Now()
	var out []pageCapabilityRevocation
	for rows.Next() {
		var (
			id, pipelineID, slug, stepID, hash, revokedAt, expiresAt string
			uses                                                     int
			maxUses                                                  sql.NullInt64
		)
		if err := rows.Scan(&id, &pipelineID, &slug, &stepID, &hash, &revokedAt, &expiresAt, &uses, &maxUses); err != nil {
			return nil, fmt.Errorf("read waitpoint_trust_grants to revoke: %w", err)
		}
		if revokedAt != "" {
			continue // already withdrawn; the row goes, the gate did not change
		}
		if expiresAt != "" {
			if exp, perr := time.Parse(time.RFC3339, expiresAt); perr == nil && !exp.After(now) {
				continue
			}
		}
		if maxUses.Valid && int64(uses) >= maxUses.Int64 {
			continue
		}
		out = append(out, pageCapabilityRevocation{
			Type:    journal.EntryTrustRevoked,
			Summary: fmt.Sprintf("erasure revoked standing approval on gate %q of routine %q", stepID, slug),
			Payload: map[string]any{
				"definition_hash": hash,
				"reason":          "issuer erased (Art. 17)",
				"uses":            uses,
			},
			Refs: map[string]any{
				"trust_grant_id": id,
				"pipeline_id":    pipelineID,
				"pipeline_slug":  slug,
				"step_id":        stepID,
			},
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read waitpoint_trust_grants to revoke: %w", err)
	}
	return out, nil
}
