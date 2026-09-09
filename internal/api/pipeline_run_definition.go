package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// Run history must never resolve a slug to its current HEAD. A content hash
// also supports older runs that did not stamp a numeric version. If neither
// identifies an archived definition, the outputs remain useful on their own.
func (h *PipelineHandler) enrichRunDefinition(ctx context.Context, workspaceID, runID string, resp map[string]interface{}) {
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
		return
	}
	resp["definition_hash"] = hash.String
	if version.Valid {
		resp["pipeline_version"] = version.Int64
	}
	if definition.Valid && json.Valid([]byte(definition.String)) {
		resp["definition"] = json.RawMessage(definition.String)
		resp["definition_status"] = "available"
	}
}
