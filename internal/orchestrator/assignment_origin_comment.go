package orchestrator

import (
	"context"
	"database/sql"
	"errors"

	"github.com/crewship-ai/crewship/internal/runauthority"
)

// WriteAssignmentResultComment publishes agent-authored result text only while
// its original requester can still comment. Lifecycle settlement itself remains
// independent: revoked work must still become terminal and release its slot.
func WriteAssignmentResultComment(ctx context.Context, db *sql.DB, assignmentID, workspaceID, missionID, agentID, commentID, body, stamp string) (bool, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var originID string
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(a.chat_origin_id,m.chat_origin_id,'') FROM missions m LEFT JOIN assignments a ON a.id=? AND a.workspace_id=m.workspace_id WHERE m.id=? AND m.workspace_id=?`, assignmentID, missionID, workspaceID).Scan(&originID)
	if err != nil {
		return false, err
	}
	if originID != "" {
		authority, err := runauthority.ValidateOrigin(ctx, tx, originID, agentID, workspaceID)
		if err != nil {
			return false, err
		}
		// Same existing public issue-comment create gate (OWNER/ADMIN/MANAGER),
		// evaluated in the insert transaction rather than from the run snapshot.
		var allowed bool
		err = tx.QueryRowContext(ctx, `SELECT role IN ('OWNER','ADMIN','MANAGER') AND access_mode!='restricted' FROM workspace_members WHERE workspace_id=? AND user_id=?`, workspaceID, authority.HumanAuthority.UserID).Scan(&allowed)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && !allowed) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO mission_comments(id,mission_id,author_type,author_id,body,created_at,updated_at,chat_origin_id) VALUES(?,?,'agent',?,?,?,?,NULLIF(?,''))`, commentID, missionID, agentID, body, stamp, stamp, originID)
	if err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}
