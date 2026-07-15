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
//  2. the credential's name appears as a whole word (case-insensitive) in
//     the escalation's reason or context text, and
//  3. the credential's name is specific enough to be a meaningful signal —
//     see minAutoResolveNameLen below.
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
// CodeRabbit flagged (correctly) that a short, generic name — "API",
// "TOKEN", "KEY" — could whole-word-match a PENDING escalation for a
// completely different need the same agent happens to also be waiting on,
// auto-approving the wrong thing. Full fix is structured correlation via
// the escalation's own credential_id, but that field is only ever set when
// the AGENT proposed a credential inline — never on the human
// create+assign path this function exists to handle, so requiring it would
// defeat the feature. minAutoResolveNameLen is the cheap mitigation:
// real-world secret names are near-universally longer than 8 chars
// (env-var/API-key naming conventions), so this closes the common
// false-positive case without a redesign.
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
