package harbormaster

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

func SweepTimeouts(ctx context.Context, db *sql.DB, j journal.Emitter) (int, error) {
	at := time.Now().UTC()
	now := at.Format(timeFmt)
	// agents.* timestamps are RFC3339 everywhere (migrations, the
	// hire handlers, internal/ephemeral). Same instant, that table's
	// format — a sweeper-written expired_at must sort next to the
	// ones the deny path and the TTL sweeper write.
	nowAgents := at.Format(time.RFC3339)

	// First snapshot the soon-to-be-timed-out IDs so the journal entries
	// know which scope to emit under. Doing the SELECT before the UPDATE
	// gives us a small race (a human could approve in between) but the
	// UPDATE's status='pending' guard is the source of truth — we just
	// emit a stale audit entry, which is preferable to skipping audit.
	const selectSQL = `SELECT id, workspace_id, crew_id, agent_id, mission_id,
			requested_by, kind, reason
		FROM approvals_queue
		WHERE status = 'pending' AND timeout_at IS NOT NULL AND timeout_at <= ?`
	rows, err := db.QueryContext(ctx, selectSQL, now)
	if err != nil {
		return 0, fmt.Errorf("harbormaster: sweep select: %w", err)
	}
	type stale struct {
		id, ws, crew, agent, mission, requestedBy, reason string
		kind                                              Kind
	}
	var pending []stale
	for rows.Next() {
		var (
			s                             stale
			crew, agent, mission, kindStr sql.NullString
		)
		if err := rows.Scan(&s.id, &s.ws, &crew, &agent, &mission, &s.requestedBy, &kindStr, &s.reason); err != nil {
			rows.Close()
			return 0, fmt.Errorf("harbormaster: sweep scan: %w", err)
		}
		s.crew = crew.String
		s.agent = agent.String
		s.mission = mission.String
		s.kind = Kind(kindStr.String)
		pending = append(pending, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	if len(pending) == 0 {
		return 0, nil
	}

	// Per-row UPDATE instead of a bulk UPDATE so we can tell exactly
	// which rows actually flipped. If an approve/deny raced between
	// the SELECT above and the UPDATE, the row stayed resolved — we
	// must NOT emit EntryApprovalTimeout for it or the journal
	// disagrees with the canonical status. Previously the bulk
	// UPDATE + unconditional emit loop corrupted the audit trail in
	// exactly that race window.
	var n int64
	flipped := make([]stale, 0, len(pending))
	for _, s := range pending {
		ok, err := flipTimedOutRow(ctx, db, s.id, s.agent, s.kind, now, nowAgents)
		if err != nil {
			return 0, err
		}
		if ok {
			n++
			flipped = append(flipped, s)
		}
	}

	if j != nil {
		for _, s := range flipped {
			_, _ = j.Emit(ctx, journal.Entry{
				WorkspaceID: s.ws,
				CrewID:      s.crew,
				AgentID:     s.agent,
				MissionID:   s.mission,
				Type:        journal.EntryApprovalTimeout,
				Severity:    journal.SeverityWarn,
				ActorType:   journal.ActorSystem,
				ActorID:     "harbormaster",
				Summary:     fmt.Sprintf("approval timed out: %s", s.reason),
				Payload:     map[string]any{"approval_id": s.id, "kind": string(s.kind)},
				Refs:        map[string]any{"approval_id": s.id},
			})
		}
	}

	return int(n), nil
}

// flipTimedOutRow moves one pending row to 'timeout' and, for a
// kind=ephemeral_hire row, ghosts the staged agent in the SAME
// transaction. Reports whether the row actually flipped.
//
// Why the agent row has to move too (#1304): approvals_queue rows are
// per-enqueue, not per-agent, so approve-hire deliberately ignores a
// terminal queue row — `crewship rehire` reopens a hire cycle without
// enqueuing a new one, and 409'ing on the previous cycle's verdict
// bricked the agent (#1272). agents.expired_at is therefore the only
// per-cycle decidability guard, and it is the only one rehire resets.
// A deny writes it inside its own decision transaction
// (api.applyEphemeralHireDecisionTx); a timeout that skipped it left
// the hire fully approvable after its window had lapsed, contradicting
// docs/guides/ephemeral-agents.mdx. Same marker, same tx shape — the
// deny path is the reference implementation here, not a parallel one.
//
// The UPDATE is deliberately raw SQL against `agents` rather than a
// call into internal/api: the sweeper runs below the handler layer and
// importing it would be a cycle. Its guards mirror the deny path
// exactly, so a hire that was approved or ghosted between the SELECT
// and here is left alone.
//
// The blocking inbox waitpoint is NOT resolved. It stays in the
// operator's inbox as the actionable "this hire lapsed, rehire or drop
// it" item, and the approve/deny that eventually follows a rehire
// resolves it the way it always did.
func flipTimedOutRow(ctx context.Context, db *sql.DB, approvalID, agentID string, kind Kind, now, nowAgents string) (bool, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("harbormaster: sweep begin %s: %w", approvalID, err)
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, `UPDATE approvals_queue
		SET status = 'timeout', decided_at = ?
		WHERE id = ? AND status = 'pending' AND timeout_at IS NOT NULL AND timeout_at <= ?`,
		now, approvalID, now)
	if err != nil {
		return false, fmt.Errorf("harbormaster: sweep update %s: %w", approvalID, err)
	}
	rowsAffected, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("harbormaster: sweep rows %s: %w", approvalID, err)
	}
	if rowsAffected != 1 {
		// Approved or denied between the SELECT and here. Nothing to
		// commit — and nothing to ghost, since that decision already
		// settled the agent.
		return false, nil
	}

	if kind == KindEphemeralHire && agentID != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE agents
			SET expired_at = ?, updated_at = ?
			WHERE id = ? AND ephemeral = 1 AND status = 'PENDING_REVIEW'
			  AND expired_at IS NULL AND deleted_at IS NULL`,
			nowAgents, nowAgents, agentID); err != nil {
			return false, fmt.Errorf("harbormaster: sweep ghost agent %s: %w", agentID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("harbormaster: sweep commit %s: %w", approvalID, err)
	}
	return true, nil
}

// List returns approvals for a workspace, optionally filtered by status.
// Newest-first. The cap is enforced server-side so a buggy caller can't
// pull the entire table by passing limit=MaxInt.
