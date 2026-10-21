package backup

import (
	"context"
	"database/sql"
	"fmt"
	"math"
)

// A restore may bring back membership IDs/epochs previously revoked. Advance
// imported memberships past both live and archived epochs; preserve receipts
// as history, but require fresh authenticated admission for web execution.
func webAuthorityEpoch(ctx context.Context, tx *sql.Tx) (int64, error) {
	has, err := tableHasColumn(ctx, tx, "workspace_members", "web_execution_revision")
	if err != nil || !has {
		return 0, err
	}
	var epoch int64
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(web_execution_revision),0) FROM workspace_members`).Scan(&epoch); err != nil {
		return 0, err
	}
	exists, err := tableExistsTx(ctx, tx, "execution_origins")
	if err != nil || !exists {
		return epoch, err
	}
	var archived int64
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(CASE WHEN json_valid(human_authority) THEN CASE WHEN json_extract(human_authority,'$.kind') IN ('web_issue','web_recurring') AND json_type(human_authority,'$.member_revision')='integer' THEN json_extract(human_authority,'$.member_revision') ELSE 0 END ELSE 0 END),0) FROM execution_origins`).Scan(&archived)
	if archived > epoch {
		epoch = archived
	}
	return epoch, err
}

func fenceRestoredWebAuthority(ctx context.Context, tx *sql.Tx, dump *DBDump, before int64) error {
	if len(dump.Tables["workspace_members"]) == 0 {
		return nil
	}
	current, err := webAuthorityEpoch(ctx, tx)
	if err != nil {
		return err
	}
	if current == 0 {
		return nil
	} // Older target schema without web admissions.
	if before > current {
		current = before
	}
	if current == math.MaxInt64 {
		return fmt.Errorf("web execution epoch exhausted")
	}
	for _, row := range dump.Tables["workspace_members"] {
		id, _ := row["id"].(string)
		workspace, _ := row["workspace_id"].(string)
		if id == "" || workspace == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE workspace_members SET web_execution_revision=? WHERE id=? AND workspace_id=?`, current+1, id, workspace); err != nil {
			return err
		}
	}
	return nil
}
