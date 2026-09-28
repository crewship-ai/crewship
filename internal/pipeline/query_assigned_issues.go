package pipeline

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// hasAssignedIssues is a cheap signal, not a claim or an execution grant.
// It uses the persisted routine author's identity; query DSL cannot select a
// different agent. Actual issue start must still pass its current authority,
// budget and atomic ownership gates. Only a boolean leaves this read.
func (s *RunStore) hasAssignedIssues(ctx context.Context, workspace, crew, agent string) (bool, error) {
	if workspace == "" || crew == "" || agent == "" {
		return false, fmt.Errorf("assigned_issues requires an author agent and crew")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var result sql.NullBool
	err := s.db.QueryRowContext(ctx, `SELECT CASE WHEN EXISTS (
 SELECT 1 FROM agents a JOIN crews c ON c.id=a.crew_id JOIN workspaces w ON w.id=a.workspace_id
 WHERE a.id=? AND a.crew_id=? AND a.workspace_id=? AND c.workspace_id=a.workspace_id
 AND a.deleted_at IS NULL AND c.deleted_at IS NULL AND w.deleted_at IS NULL
 ) THEN EXISTS (
 SELECT 1 FROM missions m LEFT JOIN issue_work iw ON iw.mission_id=m.id
 WHERE m.workspace_id=? AND m.crew_id=? AND m.delegate_agent_id=?
 AND m.mission_type='issue' AND m.status='TODO' AND COALESCE(iw.mode,'agent')='agent'
 AND NOT EXISTS (SELECT 1 FROM mission_relations rel JOIN missions blocker
 ON blocker.id=CASE WHEN rel.relation_type='blocks' THEN rel.source_id ELSE rel.target_id END
 WHERE ((rel.relation_type='blocks' AND rel.target_id=m.id) OR
 (rel.relation_type='blocked_by' AND rel.source_id=m.id)) AND blocker.status NOT IN ('DONE','CANCELLED'))
 AND NOT EXISTS (SELECT 1 FROM assignments a WHERE a.mission_id=m.id
 AND a.status NOT IN ('COMPLETED','FAILED','CANCELLED','TIMEOUT'))
 AND NOT EXISTS (SELECT 1 FROM issue_executions x JOIN pipeline_runs pr ON pr.id=x.routine_run_id
 WHERE x.mission_id=m.id AND pr.status IN ('queued','running','waiting'))
 ) ELSE NULL END`, agent, crew, workspace, workspace, crew, agent).Scan(&result)
	if err != nil {
		return false, fmt.Errorf("assigned issue preflight: %w", err)
	}
	if !result.Valid {
		return false, fmt.Errorf("assigned issue preflight author is unavailable in this workspace")
	}
	return result.Bool, nil
}
