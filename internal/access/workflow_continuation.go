package access

import "context"

func workflowContinuationSlot(ctx context.Context, q queryer, parent Attempt, agent string) error {
	if parent.Parent != "" || parent.Agent != agent || parent.AdmissionOperation != "run" || parent.ContextAudience != "" {
		return ErrDenied
	}
	var one int
	if q.QueryRowContext(ctx, `SELECT 1 FROM restricted_workflow_delegate_slots s
 JOIN restricted_workflow_jobs j ON j.id=s.job_id JOIN access_attempts origin ON origin.id=j.origin_attempt_id
 JOIN restricted_workflow_provider_policies policy ON policy.job_id=s.job_id AND policy.agent_id=s.agent_id AND policy.policy_hash=s.policy_hash
 WHERE s.parent_attempt_id=? AND s.agent_id=? AND j.agent_id=? AND j.state IN ('running','completed')
 AND j.principal_id=? AND j.workspace_id=? AND j.member_id=? AND j.member_revision=? AND j.chat_id=? AND origin.revoked_at IS NULL`, parent.ID, agent, agent, parent.Principal, parent.Workspace, parent.Member, parent.Revision, parent.Chat).Scan(&one) != nil {
		return ErrDenied
	}
	return nil
}
