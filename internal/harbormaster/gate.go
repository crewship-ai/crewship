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

// GateInput bundles the per-call context for Gate so the signature stays
// readable as we add fields. Mode is the only piece that controls flow:
// ModeNone short-circuits (rules ignored), ModeAsync enqueues and
// returns immediately, ModeSync blocks until decision/timeout.
type GateInput struct {
	Mode        Mode
	Tool        string
	Args        map[string]any
	WorkspaceID string
	CrewID      string
	AgentID     string
	MissionID   string
	RequestedBy string
	// TimeoutSecs overrides the default 1h timeout when > 0.
	TimeoutSecs int
	// PollInterval lets tests speed up the sync loop. Defaults to 1s.
	PollInterval time.Duration
}

// Gate is the package's main entry point. It checks the rules, optionally
// enqueues an approval, and returns a Decision describing what the caller
// should do next.
//
// Behaviour by Mode:
//   - ModeNone: returns Decision{NotGated:true, Approved:true} without
//     consulting rules. Used for trusted callers that opt out entirely.
//   - ModeAsync: if a rule matches, enqueue and return Pending=true. The
//     caller continues the agent loop and humans see the request in the UI;
//     the eventual decision is the auditable record, not a flow gate.
//   - ModeSync: if a rule matches, enqueue and poll the row until the
//     status leaves 'pending'. The poll respects ctx.Done(). On timeout
//     (server-side via SweepTimeouts OR client-side via TimeoutSecs) the
//     caller sees TimedOut=true.
//
// When no rule matches, Gate returns NotGated=true and Approved=true so
// callers can use a single boolean check (`if dec.Approved { ... }`).
func Gate(ctx context.Context, db *sql.DB, j journal.Emitter, eval *Evaluator, in GateInput) (Decision, error) {
	if in.Mode == ModeNone || eval == nil {
		return Decision{NotGated: true, Approved: true}, nil
	}

	required, reason, kind := eval.Evaluate(ctx, in.Tool, in.Args)
	if !required {
		return Decision{NotGated: true, Approved: true}, nil
	}

	req := Request{
		WorkspaceID: in.WorkspaceID,
		CrewID:      in.CrewID,
		AgentID:     in.AgentID,
		MissionID:   in.MissionID,
		RequestedBy: in.RequestedBy,
		Kind:        kind,
		Reason:      reason,
		Payload: map[string]any{
			"tool": in.Tool,
			"args": in.Args,
		},
		TimeoutSecs: in.TimeoutSecs,
	}
	id, err := Enqueue(ctx, db, j, req)
	if err != nil {
		return Decision{}, fmt.Errorf("harbormaster: gate enqueue: %w", err)
	}

	if in.Mode == ModeAsync {
		return Decision{
			Pending:   true,
			RequestID: id,
			Status:    StatusPending,
			Reason:    reason,
			Kind:      kind,
		}, nil
	}

	// ModeSync: poll until decided or timed out.
	interval := in.PollInterval
	if interval <= 0 {
		interval = time.Second
	}

	// Prepare the lookup statement once; every poll re-binds the same SQL.
	// timeout_at is no longer selected here — the client-side deadline
	// handles the timeout transition, and reading the row value just
	// left an unused Scan target (CodeRabbit flagged).
	const pollSQL = `SELECT status, decided_by, decision_comment
		FROM approvals_queue WHERE id = ?`
	stmt, err := db.PrepareContext(ctx, pollSQL)
	if err != nil {
		return Decision{}, fmt.Errorf("harbormaster: prepare poll: %w", err)
	}
	defer stmt.Close()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Local hard deadline so the gate caller fails even if SweepTimeouts
	// is not running (unit tests, for instance).
	var deadline <-chan time.Time
	if in.TimeoutSecs > 0 {
		deadline = time.After(time.Duration(in.TimeoutSecs) * time.Second)
	} else {
		deadline = time.After(time.Duration(defaultTimeoutSecs) * time.Second)
	}

	check := func() (Decision, bool, error) {
		var (
			status             string
			decidedBy, comment sql.NullString
		)
		err := stmt.QueryRowContext(ctx, id).Scan(&status, &decidedBy, &comment)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				// Row vanished — treat as denied so the agent fails closed.
				return Decision{Denied: true, Status: StatusDenied, RequestID: id, Reason: reason, Kind: kind}, true, nil
			}
			return Decision{}, false, fmt.Errorf("harbormaster: poll: %w", err)
		}
		st := Status(status)
		switch st {
		case StatusApproved:
			return Decision{Approved: true, Status: st, RequestID: id, DecidedBy: decidedBy.String, Comment: comment.String, Reason: reason, Kind: kind}, true, nil
		case StatusDenied:
			return Decision{Denied: true, Status: st, RequestID: id, DecidedBy: decidedBy.String, Comment: comment.String, Reason: reason, Kind: kind}, true, nil
		case StatusTimeout:
			return Decision{TimedOut: true, Status: st, RequestID: id, Reason: reason, Kind: kind}, true, nil
		case StatusCancelled:
			return Decision{Denied: true, Status: st, RequestID: id, Comment: comment.String, Reason: reason, Kind: kind}, true, nil
		}
		return Decision{}, false, nil
	}

	// Fast first poll so a same-process auto-approver is not penalized by
	// the ticker interval.
	if dec, done, err := check(); err != nil {
		return Decision{}, err
	} else if done {
		return dec, nil
	}

	for {
		select {
		case <-ctx.Done():
			return Decision{}, ctx.Err()
		case <-deadline:
			// Flip the row to timeout so subsequent reads are consistent
			// with the audit log. Errors get logged at debug so oncall
			// can grep for this string if both fail — the sweeper will
			// still catch up on the next tick so we don't escalate them.
			if _, err := db.ExecContext(context.Background(),
				`UPDATE approvals_queue SET status = 'timeout', decided_at = ?
				 WHERE id = ? AND status = 'pending'`,
				time.Now().UTC().Format(timeFmt), id); err != nil {
				slog.Debug("harbormaster: gate timeout update failed", "id", id, "kind", kind, "err", err)
			}
			if j != nil {
				if _, err := j.Emit(context.Background(), journal.Entry{
					WorkspaceID: in.WorkspaceID,
					CrewID:      in.CrewID,
					AgentID:     in.AgentID,
					MissionID:   in.MissionID,
					Type:        journal.EntryApprovalTimeout,
					Severity:    journal.SeverityWarn,
					ActorType:   journal.ActorSystem,
					ActorID:     "harbormaster",
					Summary:     fmt.Sprintf("approval timed out (sync gate): %s", reason),
					Payload:     map[string]any{"approval_id": id, "kind": string(kind)},
					Refs:        map[string]any{"approval_id": id},
				}); err != nil {
					slog.Debug("harbormaster: gate timeout emit failed", "id", id, "kind", kind, "err", err)
				}
			}
			return Decision{TimedOut: true, Status: StatusTimeout, RequestID: id, Reason: reason, Kind: kind}, nil
		case <-ticker.C:
			if dec, done, err := check(); err != nil {
				return Decision{}, err
			} else if done {
				return dec, nil
			}
		}
	}
}

// StartTimeoutSweeper runs SweepTimeouts on an interval until ctx is
// cancelled. Returns immediately; the goroutine exits on ctx.Done().
// Intended to be wired up once at process start.
func StartTimeoutSweeper(ctx context.Context, db *sql.DB, j journal.Emitter, interval time.Duration) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				// Log sweep failures so an operator notices if the
				// DB becomes unreachable or the update loop wedges;
				// transient errors are expected so debug level is
				// fine, an oncall wants to grep for this string.
				if _, err := SweepTimeouts(ctx, db, j); err != nil {
					slog.Debug("harbormaster: sweep error", "err", err)
				}
			}
		}
	}()
}
