package quartermaster

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// RunRecord is the shape the eval_runs handler returns for the list
// endpoint. One row per replay/regression invocation; a thin index over
// the much richer eval.run_started / eval.metric journal entries.
type RunRecord struct {
	ID                 string    `json:"id"`
	WorkspaceID        string    `json:"workspace_id"`
	Kind               string    `json:"kind"` // "replay" or "regression"
	MissionID          string    `json:"mission_id,omitempty"`
	BaselineMissionID  string    `json:"baseline_mission_id,omitempty"`
	CandidateMissionID string    `json:"candidate_mission_id,omitempty"`
	Status             string    `json:"status"` // queued|running|completed|failed
	Result             string    `json:"result,omitempty"`
	Seed               int64     `json:"seed"`
	Signature          string    `json:"signature,omitempty"`
	TotalTokens        int64     `json:"total_tokens"`
	TotalCostUSD       float64   `json:"total_cost_usd"`
	Regressed          bool      `json:"regressed"`
	CreatedBy          string    `json:"created_by,omitempty"`
	CreatedAt          time.Time `json:"created_at"`
	CompletedAt        *time.Time `json:"completed_at,omitempty"`
}

// InsertReplayRun writes a queued replay row. Returns the row after the
// insert so the handler can echo it back to the caller; the background
// goroutine then updates it with the final metrics via UpdateRun.
func InsertReplayRun(ctx context.Context, db *sql.DB, r RunRecord) error {
	if r.ID == "" || r.WorkspaceID == "" || r.MissionID == "" {
		return fmt.Errorf("quartermaster: replay run requires id, workspace_id, mission_id")
	}
	_, err := db.ExecContext(ctx, `INSERT INTO eval_runs
		(id, workspace_id, kind, mission_id, status, seed, created_by, created_at)
		VALUES (?, ?, 'replay', ?, 'queued', ?, ?, ?)`,
		r.ID, r.WorkspaceID, r.MissionID, r.Seed, nullStr(r.CreatedBy),
		time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

// InsertRegressionRun writes a queued regression row.
func InsertRegressionRun(ctx context.Context, db *sql.DB, r RunRecord) error {
	if r.ID == "" || r.WorkspaceID == "" || r.BaselineMissionID == "" || r.CandidateMissionID == "" {
		return fmt.Errorf("quartermaster: regression run requires id, workspace_id, baseline_mission_id, candidate_mission_id")
	}
	_, err := db.ExecContext(ctx, `INSERT INTO eval_runs
		(id, workspace_id, kind, baseline_mission_id, candidate_mission_id, status, created_by, created_at)
		VALUES (?, ?, 'regression', ?, ?, 'queued', ?, ?)`,
		r.ID, r.WorkspaceID, r.BaselineMissionID, r.CandidateMissionID,
		nullStr(r.CreatedBy), time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

// UpdateRunStatus flips the run to running/completed/failed and stamps
// the result, token totals, and completed_at. Called by the goroutine
// after Replay / DetectRegression returns.
func UpdateRunStatus(ctx context.Context, db *sql.DB, id, status, result, signature string, totalTokens int64, totalCostUSD float64, regressed bool) error {
	completedAt := sql.NullString{}
	if status == "completed" || status == "failed" {
		completedAt = sql.NullString{Valid: true, String: time.Now().UTC().Format(time.RFC3339Nano)}
	}
	_, err := db.ExecContext(ctx, `UPDATE eval_runs SET
		status = ?,
		result = ?,
		signature = ?,
		total_tokens = ?,
		total_cost_usd = ?,
		regressed = ?,
		completed_at = ?
		WHERE id = ?`,
		status, result, signature, totalTokens, totalCostUSD, boolInt(regressed), completedAt, id)
	return err
}

// ListRuns returns the most recent eval runs for a workspace, newest
// first. Cross-workspace reads are prevented by the WHERE clause —
// every call site must pass the caller's workspace ID.
func ListRuns(ctx context.Context, db *sql.DB, workspaceID string, limit int) ([]RunRecord, error) {
	if workspaceID == "" {
		return nil, fmt.Errorf("quartermaster: workspace_id required")
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := db.QueryContext(ctx, `SELECT id, workspace_id, kind,
		COALESCE(mission_id,''), COALESCE(baseline_mission_id,''),
		COALESCE(candidate_mission_id,''), status, COALESCE(result,''),
		seed, COALESCE(signature,''), total_tokens, total_cost_usd,
		regressed, COALESCE(created_by,''), created_at, completed_at
		FROM eval_runs WHERE workspace_id = ? ORDER BY created_at DESC LIMIT ?`,
		workspaceID, limit)
	if err != nil {
		return nil, fmt.Errorf("quartermaster: list runs: %w", err)
	}
	defer rows.Close()
	out := make([]RunRecord, 0, 16)
	for rows.Next() {
		var r RunRecord
		var regressedInt int
		var createdAt string
		var completedAt sql.NullString
		if err := rows.Scan(
			&r.ID, &r.WorkspaceID, &r.Kind,
			&r.MissionID, &r.BaselineMissionID, &r.CandidateMissionID,
			&r.Status, &r.Result,
			&r.Seed, &r.Signature, &r.TotalTokens, &r.TotalCostUSD,
			&regressedInt, &r.CreatedBy, &createdAt, &completedAt,
		); err != nil {
			return nil, err
		}
		r.Regressed = regressedInt != 0
		if t, err := parseRunTS(createdAt); err == nil {
			r.CreatedAt = t
		}
		if completedAt.Valid {
			if t, err := parseRunTS(completedAt.String); err == nil {
				r.CompletedAt = &t
			}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// parseRunTS accepts the same timestamp forms SQLite writes + the
// RFC3339Nano form our own INSERTs use.
func parseRunTS(s string) (time.Time, error) {
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("quartermaster: unparseable timestamp %q", s)
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
