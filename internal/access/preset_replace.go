package access

import (
	"context"
	"database/sql"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

// ReplacePresetIn changes only the selected definition's managed rows in the
// caller transaction. The caller must verify fresh consent first, append audit,
// record informational selection last and commit once. It never commits itself.
// Complete-document ReplaceIn intentionally retains its independent contract.
func (s Store) ReplacePresetIn(ctx context.Context, tx *sql.Tx, actor, user, workspace string, req PresetRequest) (Membership, error) {
	plan, err := s.PlanPresetIn(ctx, tx, actor, user, workspace, req)
	if err != nil {
		return Membership{}, err
	}
	for _, change := range plan.Changes {
		r := change.Right
		if change.Action == "remove" || change.Action == "reissue" {
			result, err := tx.ExecContext(ctx, `DELETE FROM access_grants WHERE member_id=? AND resource_kind=? AND COALESCE(agent_id,project_id,routine_id)=? AND operation=?`, req.MembershipID, r.Kind, r.ID, r.Operation)
			if err != nil {
				return Membership{}, err
			}
			n, err := result.RowsAffected()
			if err != nil {
				return Membership{}, err
			}
			if n != 1 {
				return Membership{}, ErrConflict
			}
		}
		if change.Action == "add" || change.Action == "reissue" {
			var projectID, routineID any
			if r.Kind == "project" {
				projectID = r.ID
			} else {
				routineID = r.ID
			}
			result, err := tx.ExecContext(ctx, `INSERT INTO access_grants(id,member_id,resource_kind,project_id,routine_id,operation,created_by,created_at) VALUES(?,?,?,?,?,?,?,?)`, randomID(), req.MembershipID, r.Kind, projectID, routineID, r.Operation, actor, tsformat.Format(time.Now()))
			if err != nil {
				return Membership{}, err
			}
			n, err := result.RowsAffected()
			if err != nil {
				return Membership{}, err
			}
			if n != 1 {
				return Membership{}, ErrConflict
			}
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE workspace_members SET access_revision=access_revision+1 WHERE id=? AND workspace_id=? AND user_id=? AND access_mode='restricted'`, req.MembershipID, workspace, user)
	if err != nil {
		return Membership{}, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return Membership{}, err
	}
	if n != 1 {
		return Membership{}, ErrConflict
	}
	return member(ctx, tx, user, workspace)
}
