package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/crewship-ai/crewship/internal/scrubber"
)

// Run history must never resolve a slug to its current HEAD. A content hash
// also supports older runs that did not stamp a numeric version. If neither
// identifies an archived definition, the outputs remain useful on their own.
//
// Returns the parsed executed definition when one is available (nil
// otherwise) so the failure projection can name steps from the same recipe
// the run actually executed.
func (h *PipelineHandler) enrichRunDefinition(ctx context.Context, workspaceID, runID string, resp map[string]interface{}) *pipeline.DSL {
	resp["definition"] = nil
	resp["definition_status"] = "unavailable"
	var version sql.NullInt64
	var hash, definition sql.NullString
	err := h.db.QueryRowContext(ctx, `SELECT COALESCE(v.version,r.pipeline_version), r.definition_hash, COALESCE(r.executed_definition_json,v.definition_json)
		FROM pipeline_runs r LEFT JOIN pipeline_versions v ON v.pipeline_id = r.pipeline_id
		AND ((COALESCE(r.definition_hash, '') <> '' AND v.definition_hash = r.definition_hash)
		OR (COALESCE(r.definition_hash, '') = '' AND r.pipeline_version > 0 AND v.version = r.pipeline_version))
		WHERE r.id = ? AND r.workspace_id = ?`, runID, workspaceID).Scan(&version, &hash, &definition)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			resp["definition_status"] = "error"
		}
		return nil
	}
	resp["definition_hash"] = hash.String
	if version.Valid {
		resp["pipeline_version"] = version.Int64
	}
	var parsed *pipeline.DSL
	if definition.Valid && json.Valid([]byte(definition.String)) {
		resp["definition"] = json.RawMessage(definition.String)
		resp["definition_status"] = "available"
		if dsl, err := pipeline.Parse([]byte(definition.String)); err == nil {
			parsed = dsl
			resp["behavior"] = pipeline.DescribeBehavior(dsl)
		}
	}
	return parsed
}

// runFailureApplies reports whether a run detail carries the `failure`
// projection: a failed or interrupted status, or a FAILED outcome verdict
// whatever the status column says.
func runFailureApplies(status, outcome string) bool {
	switch strings.ToLower(status) {
	case string(pipeline.RunStatusFailed), string(pipeline.RunStatusInterrupted):
		return true
	}
	return strings.EqualFold(outcome, "FAILED")
}

// loadExecutedStepIDs returns the distinct top-level step ids that have an
// execution record for the run — the evidence behind `not_done_step_ids`.
// Best-effort: an error yields nil, which classifies every later step as
// not done, the conservative reading.
func (h *PipelineHandler) loadExecutedStepIDs(ctx context.Context, runID string) []string {
	if h.db == nil {
		return nil
	}
	rows, err := h.db.QueryContext(ctx, `SELECT DISTINCT step_id FROM pipeline_step_executions WHERE run_id = ? AND parent_execution_id IS NULL`, runID)
	if err != nil {
		h.logger.Warn("run failure: load executed steps", "error", err, "run_id", runID)
		return nil
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	return ids
}

// enrichRunFailure attaches the `failure` projection (#2560) to a run that
// did not finish: the kind classified from the engine's own error text, a
// templated summary, and the kept / not-done step lists. The raw
// failed_at_step stays untouched; credential patterns in error_message are redacted.
func (h *PipelineHandler) enrichRunFailure(ctx context.Context, runID, status, outcome, errorMessage, failedAtStep, currentStepID string, dsl *pipeline.DSL, stepOutputs map[string]string, resp map[string]interface{}) {
	// Older persisted diagnostics may predate redaction at write time.
	if raw, ok := resp["error_message"].(string); ok {
		resp["error_message"] = scrubber.New().Scrub(raw)
	}
	if !runFailureApplies(status, outcome) {
		return
	}
	stepID := failedAtStep
	if stepID == "" {
		// A run interrupted by a restart never recorded a failed step;
		// the cursor says where it stopped.
		stepID = currentStepID
	}
	f := pipeline.ClassifyFailure(errorMessage, stepID, dsl, stepOutputs, h.loadExecutedStepIDs(ctx, runID))
	if strings.TrimSpace(errorMessage) == "" && strings.EqualFold(status, string(pipeline.RunStatusInterrupted)) {
		f.Summary = "The run was interrupted before it finished."
	}
	resp["failure"] = f
}
