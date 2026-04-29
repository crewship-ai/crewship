package harbormaster

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

func SweepTimeouts(ctx context.Context, db *sql.DB, j journal.Emitter) (int, error) {
	now := time.Now().UTC().Format(timeFmt)

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
		res, err := db.ExecContext(ctx, `UPDATE approvals_queue
			SET status = 'timeout', decided_at = ?
			WHERE id = ? AND status = 'pending' AND timeout_at IS NOT NULL AND timeout_at <= ?`,
			now, s.id, now)
		if err != nil {
			return 0, fmt.Errorf("harbormaster: sweep update %s: %w", s.id, err)
		}
		rowsAffected, err := res.RowsAffected()
		if err != nil {
			return 0, fmt.Errorf("harbormaster: sweep rows %s: %w", s.id, err)
		}
		if rowsAffected == 1 {
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

// List returns approvals for a workspace, optionally filtered by status.
// Newest-first. The cap is enforced server-side so a buggy caller can't
// pull the entire table by passing limit=MaxInt.
