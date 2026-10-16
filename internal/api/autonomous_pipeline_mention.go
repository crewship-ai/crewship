package api

import (
	"context"

	"github.com/crewship-ai/crewship/internal/runauthority"
)

// autonomousPipelineRunSQL uses the fixed pr/v aliases. A NULL origin alone
// is insufficient: any recorded human, missing authority or malformed start
// payload disqualifies the run, including legacy agent-run evidence.
const autonomousPipelineRunSQL = `pr.chat_origin_id IS NULL
 AND (pr.pipeline_version IS NULL OR (v.id IS NOT NULL AND v.chat_origin_id IS NULL))
 AND NOT EXISTS(SELECT 1 FROM journal_entries j WHERE j.trace_id=pr.id AND j.workspace_id=pr.workspace_id AND j.entry_type='run.started'
 AND CASE WHEN json_valid(j.payload) THEN
 (COALESCE(json_extract(j.payload,'$.requester_authority_missing'),0)<>0 OR json_type(j.payload,'$.human_authority') IS NOT NULL OR COALESCE(json_extract(j.payload,'$.chat_origin_id'),'')<>'')
 ELSE 1 END)`

// The marker and actor crew are frozen by trusted mutation admission, never
// decoded from tool arguments. A committed intent may outlive its source run
// and a later recipe-head edit must not change the actor that admitted it.
func validateAutonomousPipelineMentionSource(ctx context.Context, q issueOperationQueryer, mc mentionContext) error {
	if q == nil || !mc.AutonomousPipeline || mc.SourceRunID == "" || mc.AutonomousPipelineCrewID == "" || mc.AuthorType != "agent" || mc.AuthorID == "" || mc.WorkspaceID == "" {
		return runauthority.ErrUnavailable
	}
	var valid bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(
 SELECT 1 FROM pipeline_runs pr
 JOIN agents a ON a.id=? AND a.workspace_id=pr.workspace_id AND a.crew_id=? AND a.deleted_at IS NULL
 JOIN crews c ON c.id=a.crew_id AND c.workspace_id=a.workspace_id AND c.deleted_at IS NULL
 JOIN workspaces ws ON ws.id=pr.workspace_id AND ws.deleted_at IS NULL
 LEFT JOIN pipeline_versions v ON v.pipeline_id=pr.pipeline_id AND v.version=pr.pipeline_version
 WHERE pr.id=? AND pr.workspace_id=? AND (`+autonomousPipelineRunSQL+`)
 )`, mc.AuthorID, mc.AutonomousPipelineCrewID, mc.SourceRunID, mc.WorkspaceID).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return runauthority.ErrIdentityMismatch
	}
	return nil
}
