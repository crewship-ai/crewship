package harbormaster

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

func Enqueue(ctx context.Context, db *sql.DB, j journal.Emitter, req Request) (string, error) {
	if req.WorkspaceID == "" {
		return "", errors.New("harbormaster: workspace_id required")
	}
	if req.RequestedBy == "" {
		return "", errors.New("harbormaster: requested_by required")
	}
	if req.Kind == "" {
		return "", errors.New("harbormaster: kind required")
	}
	if req.Reason == "" {
		return "", errors.New("harbormaster: reason required")
	}

	if req.ID == "" {
		req.ID = newRequestID()
	}
	if req.CreatedAt.IsZero() {
		req.CreatedAt = time.Now().UTC()
	}
	if req.TimeoutAt == nil {
		secs := req.TimeoutSecs
		if secs <= 0 {
			secs = defaultTimeoutSecs
		}
		t := req.CreatedAt.Add(time.Duration(secs) * time.Second)
		req.TimeoutAt = &t
	}
	// Enqueue always writes a pending row — a caller shouldn't be able
	// to persist a pre-resolved approval via the public enqueue path,
	// because the matching `approval.requested` journal emit below
	// would lie about its state. Decide / Cancel / SweepTimeouts are
	// the only ways to leave pending, and each emits its own terminal
	// entry. Force pending regardless of what the caller set.
	req.Status = StatusPending

	payload, err := encodeJSON(req.Payload)
	if err != nil {
		return "", fmt.Errorf("harbormaster: marshal payload: %w", err)
	}

	const insertSQL = `INSERT INTO approvals_queue
		(id, workspace_id, crew_id, agent_id, mission_id, requested_by, kind, reason,
		 payload, status, timeout_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	_, err = db.ExecContext(ctx, insertSQL,
		req.ID,
		req.WorkspaceID,
		nullable(req.CrewID),
		nullable(req.AgentID),
		nullable(req.MissionID),
		req.RequestedBy,
		string(req.Kind),
		req.Reason,
		payload,
		string(req.Status),
		req.TimeoutAt.UTC().Format(timeFmt),
		req.CreatedAt.UTC().Format(timeFmt),
	)
	if err != nil {
		return "", fmt.Errorf("harbormaster: insert approval: %w", err)
	}

	if j != nil {
		_, _ = j.Emit(ctx, journal.Entry{
			WorkspaceID: req.WorkspaceID,
			CrewID:      req.CrewID,
			AgentID:     req.AgentID,
			MissionID:   req.MissionID,
			Type:        journal.EntryApprovalRequest,
			Severity:    journal.SeverityNotice,
			ActorType:   journal.ActorAgent,
			ActorID:     req.RequestedBy,
			Summary:     fmt.Sprintf("approval requested: %s — %s", req.Kind, req.Reason),
			Payload:     map[string]any{"approval_id": req.ID, "kind": string(req.Kind)},
			Refs:        map[string]any{"approval_id": req.ID},
		})
	}

	return req.ID, nil
}

// Decide moves a pending row to approved/denied. The status check happens
// inside the same UPDATE so two concurrent deciders can't both win — the
// second sees rowsAffected == 0 and gets ErrNotPending.
//
// ErrNotPending is also returned when the row exists but is already
// approved/denied/timed out; the caller should treat that as a no-op.
//
// This is the autocommit wrapper, so there is no transaction to roll
// back: a reload failure surfaces as an error even though the CAS
// itself committed. Loud is right — the caller must not assume the
// post-decision tail ran.

func Decide(ctx context.Context, db *sql.DB, j journal.Emitter, workspaceID, id string, status Status, decidedBy, comment string) error {
	row, err := DecideTx(ctx, db, workspaceID, id, status, decidedBy, comment)
	if err != nil {
		return err
	}
	AfterDecide(ctx, db, j, row, status, decidedBy, comment)
	return nil
}

// DBTX is the subset of *sql.DB / *sql.Tx the decision writers need —
// it lets DecideTx ride a caller-owned transaction while Decide keeps
// managing its own autocommit statement. Same shape (and rationale) as
// auditExecer in internal/api/credential_audit.go: the caller owns
// commit/rollback, so a handler that must apply side effects
// atomically with the decision can enlist the CAS in its transaction
// instead of leaving a committed decision behind when a later step
// fails (issue #1247).
type DBTX interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// DecideTx performs ONLY the compare-and-swap half of a decision: the
// conditional UPDATE plus the reload that tells the caller what it just
// won. It writes no journal entry and no reward row — those are
// post-commit concerns the caller hands to AfterDecide once the
// enclosing transaction is durable. Broadcasting or journaling an
// uncommitted decision is its own bug.
//
// Returns the reloaded row on success. A non-nil error means the
// decision must NOT be committed — including a reload failure, which
// leaves the caller unable to apply the side effects the decision
// implies. On success the row is always non-nil.
func DecideTx(ctx context.Context, tx DBTX, workspaceID, id string, status Status, decidedBy, comment string) (*Request, error) {
	if status != StatusApproved && status != StatusDenied {
		return nil, ErrBadStatus
	}
	if id == "" {
		return nil, ErrNotFound
	}
	// Fail closed on empty workspaceID: an empty value would make the
	// scoped UPDATE a no-op and then the Get fallback below would have
	// to branch on `workspaceID == ""` to avoid an unscoped lookup —
	// easier to refuse the call here so there's exactly one safe path.
	if workspaceID == "" {
		return nil, ErrNotFound
	}

	now := time.Now().UTC()
	const updateSQL = `UPDATE approvals_queue
		SET status = ?, decided_by = ?, decided_at = ?, decision_comment = ?
		WHERE id = ? AND workspace_id = ? AND status = 'pending'`
	res, err := tx.ExecContext(ctx, updateSQL,
		string(status),
		nullable(decidedBy),
		now.Format(timeFmt),
		nullable(comment),
		id,
		workspaceID,
	)
	if err != nil {
		return nil, fmt.Errorf("harbormaster: update decision: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("harbormaster: rows affected: %w", err)
	}
	if n == 0 {
		// Distinguish missing vs. not-pending so the caller can render
		// the right error to the operator. The Get below is scoped to
		// the caller's workspace, so a cross-tenant ID looks identical
		// to a nonexistent one (ErrNotFound) — no existence leak.
		row, err := getTx(ctx, tx, workspaceID, id)
		if err != nil {
			return nil, err
		}
		if row == nil {
			return nil, ErrNotFound
		}
		return nil, ErrNotPending
	}

	// Reload so the journal entry carries the canonical scope. Scoped
	// to the caller's workspace, matching the UPDATE above.
	//
	// A failed reload MUST fail the whole call. Callers gate real work
	// on the returned row — POST /approvals/{id}/decide applies the
	// ephemeral-hire agent transition, the waitpoint resolve, the audit
	// row and the WS broadcast only `if row != nil`. Returning
	// (nil, nil) here therefore committed the queue CAS and skipped
	// every side effect while answering 200: a terminal approval
	// describing a transition that never happened, which is exactly the
	// drift #1247 exists to eliminate. Returning the error instead lets
	// the caller roll its transaction back, so the row stays pending
	// and decidable.
	row, err := getTx(ctx, tx, workspaceID, id)
	if err != nil {
		return nil, fmt.Errorf("harbormaster: reload decided approval %s: %w", id, err)
	}
	if row == nil {
		// Unreachable in practice — the UPDATE above just matched this
		// row inside the caller's transaction. Fail closed rather than
		// hand back a nil row the caller would read as "nothing to do".
		return nil, fmt.Errorf("harbormaster: reload decided approval %s: row vanished mid-transaction", id)
	}
	return row, nil
}

// AfterDecide performs the post-commit tail of a decision: the journal
// entry and the reward-history row that feeds AdjustMode. Both are
// best-effort and must run only once the decision is durable — a
// journal entry for a rolled-back decision is a lie. A nil row means
// the caller has nothing durable to describe (DecideTx never returns
// one alongside a nil error), so there is nothing to emit.
func AfterDecide(ctx context.Context, db *sql.DB, j journal.Emitter, row *Request, status Status, decidedBy, comment string) {
	if row == nil {
		return
	}

	if j != nil {
		entryType := journal.EntryApprovalGranted
		if status == StatusDenied {
			entryType = journal.EntryApprovalDenied
		}
		_, _ = j.Emit(ctx, journal.Entry{
			WorkspaceID: row.WorkspaceID,
			CrewID:      row.CrewID,
			AgentID:     row.AgentID,
			MissionID:   row.MissionID,
			Type:        entryType,
			Severity:    journal.SeverityNotice,
			ActorType:   journal.ActorUser,
			ActorID:     decidedBy,
			Summary:     fmt.Sprintf("approval %s by %s", status, decidedBy),
			Payload: map[string]any{
				"approval_id": row.ID,
				"kind":        string(row.Kind),
				"comment":     comment,
			},
			Refs: map[string]any{"approval_id": row.ID},
		})
	}

	// Feed the outcome into the reward-history table so AdjustMode
	// can converge gate behaviour from repeated operator decisions.
	// The tool + args live in the original request payload — we pull
	// them from the reloaded row so this works regardless of caller.
	// Failures here are non-fatal: auto-tuning is best-effort and
	// shouldn't cause a human decision to return an error. But we DO
	// log so an oncall engineer can see why auto-tuning stops working
	// if the reward table is having issues.
	tool, args := extractToolArgs(row.Payload)
	if tool != "" {
		outcome := OutcomeDenied
		if status == StatusApproved {
			outcome = OutcomeApproved
		}
		if err := RecordOutcome(ctx, db, row.WorkspaceID, tool, args, outcome, decidedBy, row.ID); err != nil {
			slog.Default().Warn("harbormaster: reward history insert failed",
				"err", err, "tool", tool, "outcome", outcome, "approval_id", row.ID)
		}
	} else {
		// No tool on the stored payload → no reward signal for auto-tuning.
		// Usually means an upstream enqueue path changed shape or a legacy
		// row predates the tool-field convention. Logged so drift is visible
		// in the audit log instead of silently degrading gate learning.
		slog.Default().Warn("harbormaster: reward history skipped — missing tool",
			"approval_id", row.ID, "workspace_id", row.WorkspaceID)
	}
}

// extractToolArgs pulls the tool name + args back out of the stored
// request payload. Gate() writes them as top-level map keys; if
// something else is inserting rows the lookup fails gracefully and
// AdjustMode just never tunes the affected calls.

func extractToolArgs(payload map[string]any) (string, map[string]any) {
	if payload == nil {
		return "", nil
	}
	tool, _ := payload["tool"].(string)
	args, _ := payload["args"].(map[string]any)
	return tool, args
}

// Cancel withdraws a still-pending request. Used when the agent that
// requested approval terminates / aborts before a human responds. Cancel
// is a no-op on already-resolved requests and returns ErrNotPending so
// the caller can log loudly if that wasn't expected.
// Cancel withdraws an approval on behalf of the requesting agent.
// workspaceID scope is load-bearing for tenant isolation — the same class
// of bug Decide had before round 2. Without it a caller who learned
// another workspace's approval ID could cancel it cross-tenant, and the
// ErrNotPending vs ErrNotFound distinction would leak whether that
// foreign ID existed at all.

func Cancel(ctx context.Context, db *sql.DB, j journal.Emitter, workspaceID, id, reason string) error {
	if id == "" {
		return ErrNotFound
	}
	if workspaceID == "" {
		return ErrNotFound
	}
	now := time.Now().UTC()
	const updateSQL = `UPDATE approvals_queue
		SET status = 'cancelled', decided_at = ?, decision_comment = ?
		WHERE id = ? AND workspace_id = ? AND status = 'pending'`
	res, err := db.ExecContext(ctx, updateSQL, now.Format(timeFmt), nullable(reason), id, workspaceID)
	if err != nil {
		return fmt.Errorf("harbormaster: cancel: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("harbormaster: rows affected: %w", err)
	}
	if n == 0 {
		row, err := Get(ctx, db, workspaceID, id)
		if err != nil {
			return err
		}
		if row == nil {
			return ErrNotFound
		}
		return ErrNotPending
	}
	if j != nil {
		row, _ := Get(ctx, db, workspaceID, id)
		if row != nil {
			_, _ = j.Emit(ctx, journal.Entry{
				WorkspaceID: row.WorkspaceID,
				CrewID:      row.CrewID,
				AgentID:     row.AgentID,
				MissionID:   row.MissionID,
				// Distinct from approval.denied: cancelled = agent
				// withdrew the request on its own, denied = human
				// said no. Consumers (UI filters, audit queries)
				// need to distinguish these to report the right
				// "who made the call" story.
				Type:      journal.EntryApprovalCancelled,
				Severity:  journal.SeverityNotice,
				ActorType: journal.ActorAgent,
				ActorID:   row.RequestedBy,
				Summary:   fmt.Sprintf("approval cancelled: %s", reason),
				Payload:   map[string]any{"approval_id": row.ID, "cancelled": true, "reason": reason},
				Refs:      map[string]any{"approval_id": row.ID},
			})
		}
	}
	return nil
}

// SweepTimeouts moves any pending row whose timeout_at is in the past to
// 'timeout' and emits one EntryApprovalTimeout per row. Designed to be
// called from a 30s ticker; safe to invoke concurrently because the
// UPDATE is conditional on status='pending'.
