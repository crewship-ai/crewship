package api

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"regexp"
	"time"

	"github.com/crewship-ai/crewship/internal/inbox"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/ws"
)

// autoResolveEscalationsForCredential closes out any PENDING escalation
// raised by agentID that was clearly asking for the credential just
// assigned to it (issue #1198). Without this, a human who grants the
// underlying need via `credential create` + `credential assign` — instead
// of `escalation resolve --action approve` on the specific escalation
// record — never touches the escalation row, so it stays PENDING forever
// even though the agent's need has been satisfied. That leaves
// `escalation list`/`pending-count` accumulating stale, functionally-done
// rows.
//
// Matching is deliberately conservative to avoid closing unrelated
// escalations: a PENDING escalation only auto-resolves when
//  1. it was raised BY THE SAME AGENT the credential was just assigned to
//     (same agent_id, implicitly the same crew — an agent belongs to one
//     crew), and
//  2. it is a CREDENTIAL escalation — the only kind that a credential
//     grant can possibly satisfy. TEXT/LINK rows (e.g. the confidence
//     gate's auto-escalation in confidence_handler.go, hardcoded to
//     'TEXT') are not credential requests and must never be flipped to
//     action='approve' just because their free-form prose names a
//     credential. Agents are instructed to raise credential asks as
//     type='CREDENTIAL' (see the escalation contract in
//     orchestrator/exec.go), so this costs the feature nothing, and
//  3. it carries no structured proposal (credential_id IS NULL). A row
//     WITH credential_id (v119) has a dedicated resolve path that
//     activates that exact PENDING_APPROVAL credential; name-matching it
//     here would mark the escalation approved while the proposed
//     credential stayed stuck in PENDING_APPROVAL — approved on paper,
//     never actually granted, and
//  4. the credential's name appears as a whole word (case-insensitive) in
//     the escalation's reason or context text, and
//  5. the credential's name is specific enough to be a meaningful signal —
//     see minAutoResolveNameLen below, and
//  6. it is the ONLY such match — ambiguity leaves everything PENDING.
//
// Escalation reason/context is free-form text, but credential names are
// specific, distinctive tokens (e.g. "HARNESS_PAGER_NFQ93I",
// "STRIPE_API_KEY") — test-credentials.sh's own documented flow has the
// agent "raise a credential escalation that names exactly the credential
// you need" — so a whole-word match against the same agent's PENDING
// escalations is a strong, low-false-positive signal without having to
// unify the agent-proposed (structured `credential_id` + approve) and
// human-supplied (create+assign) grant paths.
//
// CodeRabbit flagged (correctly) that a free-form name match could close
// an escalation for a completely different need the same agent happens to
// also be waiting on, auto-approving the wrong thing.
//
// minAutoResolveNameLen alone does NOT answer that: it only rejects short,
// generic names ("API", "TOKEN", "KEY"), while a perfectly ordinary name
// like GITHUB_TOKEN (12 chars) clears the bar and still matches every
// pending escalation that mentions it. The type/credential_id filters and
// the ambiguity bail-out above are what actually bound the blast radius —
// the length guard is now just a cheap first cut at obvious noise.
//
// Requiring credential_id for a positive match would defeat the feature
// (it is NULL on exactly the human create+assign path this exists to
// handle), which is why correlation is achieved by narrowing what is
// ELIGIBLE rather than by demanding a structured key. The remaining
// single-match case is a deliberate, bounded trade: worst case it closes
// one stale row early, and a human can still see the resolution text.
//
// Best-effort: this runs after the credential assignment has already
// committed, so a failure here is logged and swallowed rather than failing
// the parent request — the assignment itself must not roll back because a
// housekeeping side-effect failed.
const minAutoResolveNameLen = 8

func autoResolveEscalationsForCredential(ctx context.Context, db *sql.DB, logger *slog.Logger, hub *ws.Hub, j journal.Emitter, workspaceID, agentID, credentialName string) {
	if credentialName == "" || agentID == "" || workspaceID == "" {
		return
	}
	if len(credentialName) < minAutoResolveNameLen {
		logger.Info("auto-resolve escalations: credential name too generic to safely match, skipping",
			"agent_id", agentID, "credential_name", credentialName)
		return
	}
	pattern, err := regexp.Compile(`(?i)\b` + regexp.QuoteMeta(credentialName) + `\b`)
	if err != nil {
		logger.Warn("auto-resolve escalations: compile match pattern", "error", err, "credential_name", credentialName)
		return
	}

	rows, err := db.QueryContext(ctx, `
		SELECT id, reason, COALESCE(context, ''), crew_id, chat_id
		FROM escalations
		WHERE from_agent_id = ? AND workspace_id = ? AND status = 'PENDING'
		  AND type = 'CREDENTIAL'
		  AND credential_id IS NULL
	`, agentID, workspaceID)
	if err != nil {
		logger.Warn("auto-resolve escalations: query pending", "error", err, "agent_id", agentID)
		return
	}

	type pendingMatch struct{ id, crewID, chatID string }
	var matches []pendingMatch
	for rows.Next() {
		var id, reason, escCtx, crewID, chatID string
		if scanErr := rows.Scan(&id, &reason, &escCtx, &crewID, &chatID); scanErr != nil {
			logger.Warn("auto-resolve escalations: scan", "error", scanErr)
			continue
		}
		if pattern.MatchString(reason) || pattern.MatchString(escCtx) {
			matches = append(matches, pendingMatch{id: id, crewID: crewID, chatID: chatID})
		}
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		logger.Warn("auto-resolve escalations: rows iteration", "error", rowsErr)
	}
	rows.Close()

	if len(matches) == 0 {
		return
	}
	// Ambiguity is not a tie to be broken — it's a signal to stay out.
	// The name match cannot tell WHICH of two same-named asks the human
	// actually granted ("GITHUB_TOKEN with repo:read for CI" vs
	// "GITHUB_TOKEN needs admin:org so I can rotate org secrets"), and
	// guessing wrong records action='approve'/resolved_by='system' on a
	// request no human ever approved, then hides it from `escalation
	// list`. Leave every candidate PENDING for a human to resolve
	// explicitly — the stale-row cleanup this function exists for is
	// worth far less than a spurious approval in the audit trail.
	if len(matches) > 1 {
		ids := make([]string, 0, len(matches))
		for _, m := range matches {
			ids = append(ids, m.id)
		}
		logger.Info("auto-resolve escalations: multiple pending escalations match the credential name, leaving all PENDING for a human",
			"agent_id", agentID, "credential_name", credentialName, "escalation_ids", ids)
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	resolution := fmt.Sprintf("auto-resolved: matching credential %q assigned", credentialName)
	for _, m := range matches {
		result, execErr := db.ExecContext(ctx, `
			UPDATE escalations SET status = 'RESOLVED', resolution = ?, action = 'approve', resolved_at = ?, resolved_by = 'system'
			WHERE id = ? AND workspace_id = ? AND status = 'PENDING'
		`, resolution, now, m.id, workspaceID)
		if execErr != nil {
			logger.Warn("auto-resolve escalation: update", "error", execErr, "escalation_id", m.id)
			continue
		}
		if n, _ := result.RowsAffected(); n == 0 {
			// Lost a race with a concurrent resolve — nothing to do.
			continue
		}

		inbox.ResolveBySource(ctx, db, logger, "escalation", m.id, "approve", "")

		_, _ = j.Emit(ctx, journal.Entry{
			WorkspaceID: workspaceID,
			CrewID:      m.crewID,
			AgentID:     agentID,
			Type:        journal.EntryPeerEscalation,
			Severity:    journal.SeverityNotice,
			ActorType:   journal.ActorSystem,
			Summary:     fmt.Sprintf("escalation %s auto-resolved: matching credential %q assigned", m.id, credentialName),
			Payload: map[string]any{
				"resolution":      resolution,
				"action":          "approve",
				"state":           "resolved",
				"auto_resolved":   true,
				"credential_name": credentialName,
			},
			Refs: map[string]any{"escalation_id": m.id},
		})

		broadcastChannelEvent(hub, "session", m.chatID, "escalation_resolved",
			map[string]string{"id": m.id, "resolution": resolution, "action": "approve"})
		broadcastWorkspaceEvent(hub, workspaceID, "escalation.resolved",
			map[string]string{"id": m.id, "crew_id": m.crewID, "action": "approve"})

		logger.Info("escalation auto-resolved by matching credential",
			"escalation_id", m.id, "agent_id", agentID, "credential_name", credentialName)
	}
}
