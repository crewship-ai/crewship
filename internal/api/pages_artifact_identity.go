package api

import (
	"context"
	"database/sql"
	"fmt"
)

// Attribution is retained without identifying an erased human. Live grants are
// removed rather than transplanted to a successor. This function participates
// in the existing workspace identity-erasure transaction and is safe on a
// database predating this optional pilot's tables.
func anonymiseArtifactProjectIdentity(ctx context.Context, tx *sql.Tx, workspace, user string) error {
	for _, step := range []struct{ table, statement string }{
		{"artifact_project_message_intents", `DELETE FROM artifact_project_message_intents WHERE workspace_id=? AND human_user_id=?`},
		{"artifact_project_run_consents", `DELETE FROM artifact_project_run_consents WHERE workspace_id=? AND human_user_id=?`},
		{"artifact_versions", `UPDATE artifact_versions SET actor='erased' WHERE workspace_id=?1 AND actor IN (?2,'user:'||?2)`},
		{"artifact_data_documents", `UPDATE artifact_data_documents SET updated_by='erased' WHERE workspace_id=?1 AND updated_by IN (?2,'user:'||?2)`},
		{"artifact_project_comment_replies", `UPDATE artifact_project_comment_replies SET author='erased' WHERE comment_id IN(SELECT id FROM artifact_project_comments WHERE workspace_id=?) AND author=?`},
		{"artifact_project_comments", `UPDATE artifact_project_comments SET author='erased' WHERE workspace_id=? AND author=?`},
		{"artifact_project_origins", `UPDATE artifact_project_origins SET human_user_id='erased',chat_id=NULL,agent_id=NULL,message_id='' WHERE workspace_id=? AND human_user_id=?`},
	} {
		var installed bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_schema WHERE name=? AND type='table')`, step.table).Scan(&installed); err != nil {
			return err
		}
		if !installed {
			continue
		}
		if _, err := tx.ExecContext(ctx, step.statement, workspace, user); err != nil {
			return fmt.Errorf("erase %s attribution: %w", step.table, err)
		}
	}
	return nil
}
