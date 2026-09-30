//go:build linux

package restricteddispatch

import (
	"context"
	"database/sql"
	"errors"

	"github.com/crewship-ai/crewship/internal/access"
)

// accountingMission uses only the immutable host workflow declaration. An
// input string, chat name or worker-supplied identifier never selects a payer.
func (a Authority) accountingMission(ctx context.Context, attempt access.Attempt) (string, error) {
	if attempt.Parent == "" {
		return "", nil
	}
	var facet string
	var mission sql.NullString
	var valid int
	err := a.Store.DB.QueryRowContext(ctx, `SELECT job.source_facet, reservation.issue_id,
 CASE WHEN job.state='running' AND job.workspace_id=leaf.workspace_id
 AND job.principal_id=leaf.principal_id AND job.member_id=leaf.member_id
 AND job.member_revision=leaf.member_revision AND job.chat_id=leaf.chat_id
 AND origin.revoked_at IS NULL AND origin.completed_at IS NOT NULL
 AND reservation.workflow_id=job.id AND reservation.origin_attempt_id=origin.id
 AND reservation.workspace_id=leaf.workspace_id AND reservation.principal_id=leaf.principal_id
 AND reservation.member_id=leaf.member_id AND reservation.member_revision=leaf.member_revision
 AND reservation.agent_id=job.agent_id AND reservation.recipe_hash=job.recipe_hash
 AND mission.workspace_id=leaf.workspace_id AND mission.mission_type='issue'
 AND mission.project_id=reservation.project_id
 AND EXISTS(SELECT 1 FROM access_context_dependencies d WHERE d.attempt_id=leaf.parent_id AND d.source_attempt_id=origin.id)
 THEN 1 ELSE 0 END
 FROM access_attempts leaf
 JOIN restricted_workflow_delegate_slots slot ON slot.parent_attempt_id=leaf.parent_id AND slot.agent_id=leaf.agent_id
 JOIN restricted_workflow_jobs job ON job.id=slot.job_id
 JOIN access_attempts origin ON origin.id=job.origin_attempt_id
 LEFT JOIN restricted_preflight_reservations reservation ON reservation.workflow_id=job.id
 LEFT JOIN missions mission ON mission.id=reservation.issue_id
 WHERE leaf.id=?`, attempt.ID).Scan(&facet, &mission, &valid)
	if errors.Is(err, sql.ErrNoRows) {
		// Generic delegation inherits a fully bound provider from its parent.
		// Workflow orchestration parents have no provider binding: a lost slot
		// must never downgrade their leaf to an unattributed paid request.
		var slot bool
		if err := a.Store.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM restricted_workflow_delegate_slots WHERE parent_attempt_id=? AND agent_id=?)`, attempt.Parent, attempt.Agent).Scan(&slot); err != nil {
			return "", err
		}
		if slot {
			return "", access.ErrDenied
		}
		parent, e := loadProvider(ctx, a.Store.DB, attempt.Parent)
		child, childError := loadProvider(ctx, a.Store.DB, attempt.ID)
		if e != nil || childError != nil || parent.Credential != child.Credential || parent.Revision != child.Revision || parent.Model != child.Model || parent.Profile != child.Profile || parent.MaxOutputTokens < child.MaxOutputTokens {
			return "", access.ErrDenied
		}
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if facet == "" {
		if mission.Valid {
			return "", access.ErrDenied
		}
		return "", nil
	}
	if facet != "issue" || valid != 1 || !mission.Valid || mission.String == "" {
		return "", access.ErrDenied
	}
	return mission.String, nil
}
