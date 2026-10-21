package api

import (
	"context"
	"database/sql"
	"errors"

	"github.com/crewship-ai/crewship/internal/runauthority"
)

func permanentRecurringAuthorityError(err error) bool {
	return errors.Is(err, runauthority.ErrRequesterStale) || errors.Is(err, runauthority.ErrIdentityMismatch) || errors.Is(err, runauthority.ErrUnavailable)
}

// Terminal authority failures leave the due queue and notify a current member.
// A database failure instead rolls back and remains retryable.
func pauseRecurringAuthorityTx(ctx context.Context, tx *sql.Tx, row recurringDueRow, source runauthority.Origin, now, nextRun string) error {
	if _, err := tx.ExecContext(ctx, `UPDATE recurring_issues SET enabled=0,next_run=?,updated_at=? WHERE id=? AND workspace_id=?`, nextRun, now, row.id, row.workspaceID); err != nil {
		return err
	}
	return notifyRecurringAuthorityTx(ctx, tx, row, source, "recurring-authority:", "Recurring schedule paused", "was paused because its execution authority is no longer valid", now)
}

// Stable source IDs make repeated pauses and legacy fires idempotent without
// rolling back the schedule when an earlier notice already exists.
func notifyRecurringAuthorityTx(ctx context.Context, tx *sql.Tx, row recurringDueRow, source runauthority.Origin, prefix, title, reason, now string) error {
	requester := row.createdBy.String
	if source.HumanAuthority != nil && source.HumanAuthority.UserID != "" {
		requester = source.HumanAuthority.UserID
	}
	var target string
	err := tx.QueryRowContext(ctx, `SELECT wm.user_id FROM workspace_members wm JOIN users u ON u.id=wm.user_id AND u.suspended_at IS NULL WHERE wm.workspace_id=? AND wm.access_mode='trusted' AND NOT EXISTS(SELECT 1 FROM workspace_members blocked WHERE blocked.user_id=wm.user_id AND blocked.access_mode='restricted') AND (wm.user_id=? OR wm.role IN ('OWNER','ADMIN')) ORDER BY (wm.user_id=?) DESC,wm.id LIMIT 1`, row.workspaceID, requester, requester).Scan(&target)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO inbox_items(id,workspace_id,kind,source_id,target_user_id,title,body_md,sender_type,sender_id,blocking,payload_json,created_at,updated_at) VALUES(?,?,'message',?,?,?,?,'system','recurring-dispatcher',0,'{}',?,?)`, generateCUID(), row.workspaceID, prefix+row.id+source.ID, target, title, "The schedule “"+row.title+"” "+reason+". An authorized person must save and enable it again.", now, now)
	return err
}
