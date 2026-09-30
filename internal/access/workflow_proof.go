package access

import (
	"context"
	"database/sql"
)

// CheckWorkflowContextAttempt is read-only provenance validation for an exact
// completed leaf. A public job or attempt identifier is never a capability.
func (s Store) CheckWorkflowContextAttempt(ctx context.Context, handle, job string) error {
	if s.DB == nil || job == "" {
		return ErrDenied
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	a, err := resolveState(ctx, tx, digest(handle), true, map[string]bool{}, true)
	if err != nil || a.Parent == "" || a.AdmissionOperation != "run" || a.ContextAudience != "" {
		return ErrDenied
	}
	var one int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM access_attempts leaf
 JOIN restricted_workflow_delegate_slots slot ON slot.parent_attempt_id=leaf.parent_id AND slot.agent_id=leaf.agent_id
 JOIN restricted_workflow_jobs job ON job.id=slot.job_id
 JOIN access_attempts origin ON origin.id=job.origin_attempt_id
 JOIN access_context_dependencies dependency ON dependency.attempt_id=leaf.parent_id AND dependency.source_attempt_id=origin.id
 WHERE leaf.id=? AND leaf.completed_at IS NOT NULL AND slot.job_id=? AND job.state IN ('running','completed')
 AND job.workspace_id=? AND job.principal_id=? AND job.member_id=? AND job.member_revision=? AND job.chat_id=? AND origin.revoked_at IS NULL`, a.ID, job, a.Workspace, a.Principal, a.Member, a.Revision, a.Chat).Scan(&one)
	if err != nil {
		return ErrDenied
	}
	return tx.Commit()
}
