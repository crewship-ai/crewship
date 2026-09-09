package api

import (
	"context"
	"database/sql"
	"strings"
)

// Use the same durable run record in the explorer and recipe detail. Journal
// invocation counters remain counts, never a substitute for an execution ID.
func (h *PipelineHandler) enrichRecordedRoutineState(ctx context.Context, workspaceID string, out []pipelineResponse) {
	if h.db == nil || len(out) == 0 {
		return
	}
	byID := make(map[string]*pipelineResponse, len(out))
	for i := range out {
		byID[out[i].ID] = &out[i]
	}
	rows, err := h.db.QueryContext(ctx, `SELECT p.id, p.head_version, r.id, r.status, r.outcome, r.started_at
 FROM pipelines p LEFT JOIN pipeline_runs r ON r.id = (
 SELECT rr.id FROM pipeline_runs rr WHERE rr.pipeline_id=p.id AND rr.workspace_id=p.workspace_id
 ORDER BY rr.started_at DESC, rr.id DESC LIMIT 1)
 WHERE p.workspace_id=? AND p.deleted_at IS NULL`, workspaceID)
	if err != nil {
		h.logger.Warn("routine recorded state unavailable", "error", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var version sql.NullInt64
		var runID, status, outcome, started sql.NullString
		if err := rows.Scan(&id, &version, &runID, &status, &outcome, &started); err != nil {
			h.logger.Warn("routine recorded state scan", "error", err)
			return
		}
		row := byID[id]
		if row == nil {
			continue
		}
		if version.Valid {
			v := int(version.Int64)
			row.HeadVersion = &v
		}
		if !runID.Valid {
			continue
		}
		row.LastRecordedRunID = runID.String
		row.LastRunOutcome = outcome.String
		row.LastInvocationStatus = strings.ToLower(status.String)
		if outcome.String == "FAILED" {
			row.LastInvocationStatus = "failed"
		}
		if outcome.String == "NEEDS_HUMAN" {
			row.LastInvocationStatus = "waiting"
		}
		if started.Valid {
			t := started.String
			row.LastInvokedAt = &t
		}
	}
	if err := rows.Err(); err != nil {
		h.logger.Warn("routine recorded state query", "error", err)
	}
}
