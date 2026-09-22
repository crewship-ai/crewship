package work

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

// ErrRunResultUnstored means execution returned but its result could not be
// acknowledged durably. It is not evidence of execution failure or cancellation.
var ErrRunResultUnstored = errors.New("run result persistence unconfirmed")

// RunResult is captured output, not an authority to mark execution terminal.
// Its final status comes only from the fenced dispatcher transition.
type RunResult struct {
	ExitCode     *int           `json:"exit_code,omitempty"`
	ErrorMessage *string        `json:"error_message,omitempty"`
	Metadata     map[string]any `json:"metadata,omitempty"`
}

// RunProjection joins durable output to a confirmed attempt outcome.
type RunProjection struct {
	RunID       string
	WorkspaceID string
	Status      string
	Result      RunResult
	Ready       bool
}

// StageRunResult stores output once, including when execution's context ended.
// Binding remains mandatory even for late output from an already settled run.
func (s *Store) StageRunResult(ctx context.Context, workID, runID string, generation int64, result RunResult) error {
	if workID == "" || runID == "" || generation <= 0 {
		return fmt.Errorf("%w: run result requires work, run and generation", ErrNotBound)
	}
	data, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("work: encode run result: %w", err)
	}
	// Leave room for the status envelope in the internal API's 1 MiB body.
	if len(data) > 512*1024 {
		return fmt.Errorf("work: run result exceeds 512 KiB")
	}
	res, err := s.db.ExecContext(ctx, `UPDATE work_attempts SET run_result_json = COALESCE(run_result_json, ?)
 WHERE run_id = ? AND work_id = ? AND generation = ?`, string(data), runID, workID, generation)
	if err != nil {
		return fmt.Errorf("work: stage run result: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return fmt.Errorf("%w: run result binding", ErrNotBound)
	}
	return nil
}

// RunProjection returns owned=true even before a work run is ready. Callers
// must refuse premature terminal writes rather than fall back to caller status.
func (s *Store) RunProjection(ctx context.Context, runID string) (p RunProjection, owned bool, err error) {
	p.RunID = runID
	var data sql.NullString
	var reason string
	err = s.db.QueryRowContext(ctx, `SELECT w.workspace_id, a.run_status, a.run_result_json, a.end_reason
 FROM work_attempts a JOIN work_items w ON w.id = a.work_id WHERE a.run_id = ?`, runID).
		Scan(&p.WorkspaceID, &p.Status, &data, &reason)
	if err == sql.ErrNoRows {
		return p, false, nil
	}
	if err != nil {
		return p, false, fmt.Errorf("work: read run projection: %w", err)
	}
	p.Ready = p.Status != "" && data.Valid
	if !p.Ready {
		return p, true, nil
	}
	if err = json.Unmarshal([]byte(data.String), &p.Result); err != nil {
		return p, true, fmt.Errorf("work: decode run result: %w", err)
	}
	if p.Status == "CANCELLED" {
		p.Result.ExitCode = nil
		p.Result.ErrorMessage = &reason
	}
	return p, true, nil
}

// PendingRunProjections is a bounded durable outbox. Unconfirmed outcomes do
// not enter it, even if Run returned an error or a cancellation was requested.
func (s *Store) PendingRunProjections(ctx context.Context, limit int) ([]string, error) {
	if limit < 1 || limit > 100 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT run_id FROM work_attempts
 WHERE run_result_json IS NOT NULL AND run_status != '' AND run_projected_at IS NULL
 ORDER BY COALESCE(run_projection_attempted_at, started_at),run_id LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("work: list run projections: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// MarkRunProjectionAttempt rotates failures behind other pending outcomes, so a
// batch of permanently conflicting histories cannot starve later completions.
func (s *Store) MarkRunProjectionAttempt(ctx context.Context, runID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE work_attempts SET run_projection_attempted_at = ?
 WHERE run_id = ? AND run_projected_at IS NULL`, tsformat.Format(s.now()), runID)
	return err
}

// MarkRunProjected acknowledges only the immutable status which was delivered.
func (s *Store) MarkRunProjected(ctx context.Context, runID, status string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE work_attempts SET run_projected_at = ?
 WHERE run_id = ? AND run_status = ? AND run_result_json IS NOT NULL AND run_projected_at IS NULL`, tsformat.Format(s.now()), runID, status)
	return err
}

func runStatusForState(to State) string {
	switch to {
	case StateSucceeded:
		return "COMPLETED"
	case StateCancelled:
		return "CANCELLED"
	case StateFailed, StateRetryWait, StateQueued:
		return "FAILED"
	default:
		return ""
	}
}
