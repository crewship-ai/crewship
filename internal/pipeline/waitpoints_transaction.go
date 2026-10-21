package pipeline

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/crewship-ai/crewship/internal/inbox"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

// DecisionDBTX is the caller-owned transaction used by the canonical resolve
// door. Callers reserve the writer before live authorization and source reads.
type DecisionDBTX interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

// WaitpointDecision describes effects to deliver only after a committed CAS.
// It is not another persisted answer state; the existing waitpoint owns that.
type WaitpointDecision struct {
	WorkspaceID, Token, PipelineRunID, Status, DeciderID string
	Approved, Applied, Expired                           bool
}

// DecideTx preserves the same decider, typed-form and expiry restrictions as
// Decide while allowing a source adapter to check live access in this transaction.
// A timed-out transition returns ErrAlreadyDecided with Applied=true: commit it
// and run AfterDecide. Other errors must roll back. Refused actors arriving via
// the public Decide door retain its existing refusal audit.
func (s *SQLWaitpointStore) DecideTx(ctx context.Context, tx DecisionDBTX, workspaceID, token string, approved bool, decider WaitpointDecider, payload string) (*WaitpointDecision, error) {
	return s.decideTxClock(ctx, tx, workspaceID, token, approved, decider, payload, time.Now)
}

func (s *SQLWaitpointStore) decideTxClock(ctx context.Context, tx DecisionDBTX, workspaceID, token string, approved bool, decider WaitpointDecider, payload string, clock func() time.Time) (*WaitpointDecision, error) {
	switch decider.Kind {
	case DeciderUser:
	case DeciderExternal:
		var form string
		if err := tx.QueryRowContext(ctx, `SELECT decision_form_json FROM pipeline_waitpoints WHERE workspace_id=? AND token=?`, workspaceID, token).Scan(&form); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, ErrAlreadyDecided
			}
			return nil, err
		}
		if form != "" {
			return nil, fmt.Errorf("%w: typed decisions require an authenticated workspace user", ErrDeciderNotAllowed)
		}
	default:
		return nil, ErrDeciderNotAllowed
	}
	now := tsformat.Format(clock())
	var formJSON, runID string
	var expired bool
	if err := tx.QueryRowContext(ctx, `SELECT decision_form_json,pipeline_run_id,julianday(timeout_at)<=julianday(?) FROM pipeline_waitpoints WHERE token=? AND workspace_id=? AND status='pending'`, now, token, workspaceID).Scan(&formJSON, &runID, &expired); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrAlreadyDecided
		}
		return nil, fmt.Errorf("waitpoints: update: read decision form: %w", err)
	}
	result := &WaitpointDecision{WorkspaceID: workspaceID, Token: token, PipelineRunID: runID, DeciderID: decider.ID, Approved: approved}
	if expired {
		return s.expireDecisionTx(ctx, tx, result, now)
	}
	if formJSON != "" {
		var form DecisionForm
		if err := json.Unmarshal([]byte(formJSON), &form); err != nil {
			return nil, fmt.Errorf("read decision form: %w", err)
		}
		normalized, err := NormalizeDecisionAnswer(&form, approved, payload)
		if err != nil {
			return nil, err
		}
		payload = normalized
	}
	result.Status = "approved"
	if !approved {
		result.Status = "denied"
	}
	now = tsformat.Format(clock())
	res, err := tx.ExecContext(ctx, `UPDATE pipeline_waitpoints SET status=?,decided_at=?,decided_by_user_id=?,decision_payload=? WHERE token=? AND workspace_id=? AND status='pending' AND julianday(timeout_at)>julianday(?)`, result.Status, now, nullableStr(decider.ID), nullableStr(payload), token, workspaceID, now)
	if err != nil {
		return nil, fmt.Errorf("waitpoints: update: %w", err)
	}
	count, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if count != 1 {
		return s.expireDecisionTx(ctx, tx, result, now)
	}
	result.Applied = true
	return result, nil
}

func (s *SQLWaitpointStore) expireDecisionTx(ctx context.Context, tx DecisionDBTX, result *WaitpointDecision, now string) (*WaitpointDecision, error) {
	var runID string
	err := tx.QueryRowContext(ctx, `UPDATE pipeline_waitpoints SET status='timed_out',decided_at=? WHERE workspace_id=? AND token=? AND status='pending' AND julianday(timeout_at)<=julianday(?) RETURNING pipeline_run_id`, now, result.WorkspaceID, result.Token, now).Scan(&runID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrAlreadyDecided
	}
	if err != nil {
		return nil, fmt.Errorf("waitpoints: settle expiry: %w", err)
	}
	result.PipelineRunID, result.Status, result.DeciderID = runID, "timed_out", ""
	result.Applied, result.Expired, result.Approved = true, true, false
	return result, ErrAlreadyDecided
}

// AfterDecide reuses the existing Inbox projection and listener delivery. Call
// exactly once for the committed winner; retries do not repeat these effects.
func (s *SQLWaitpointStore) AfterDecide(ctx context.Context, result *WaitpointDecision) {
	if result == nil || !result.Applied {
		return
	}
	inbox.ResolveBySource(ctx, s.db, slog.Default(), "waitpoint", result.Token, result.Status, result.DeciderID)
	s.mu.Lock()
	if channel, ok := s.listeners[result.Token]; ok {
		select {
		case channel <- waitDecision{approved: result.Approved}:
		default:
		}
	}
	s.mu.Unlock()
	if result.Expired {
		s.resumeRun(result.PipelineRunID)
	}
}
