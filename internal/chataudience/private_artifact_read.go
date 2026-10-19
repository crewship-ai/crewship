package chataudience

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

// CanReadArtifactConversation is a read-delivery boundary, not transaction/run
// admission. One SELECT verifies current audience and classification together.
// Only foreign OWNER inspection adds an audit INSERT; its failure denies bytes.
// Use outside caller-owned transactions (including on single-connection DBs).
// An empty workspace is permitted for globally identified WS chat channels.
func CanReadArtifactConversation(ctx context.Context, db *sql.DB, chatID, userID, workspaceID string, trustedOnly bool) (bool, error) {
	if db == nil || chatID == "" || userID == "" {
		return false, nil
	}
	source := `FROM chats c JOIN workspace_members wm ON wm.workspace_id=c.workspace_id AND wm.user_id=?
 JOIN users u ON u.id=wm.user_id AND u.suspended_at IS NULL
 JOIN workspaces w ON w.id=c.workspace_id AND w.deleted_at IS NULL
 JOIN agents a ON a.id=c.agent_id AND a.workspace_id=c.workspace_id AND a.deleted_at IS NULL
 WHERE c.id=? AND (` + VisibleSQL + `) AND (` + PrivateArtifactContextSQL + `)`
	args := []any{userID, chatID}
	args = append(args, Args(userID)...)
	if workspaceID != "" {
		source += ` AND c.workspace_id=?`
		args = append(args, workspaceID)
	}
	if trustedOnly {
		source += ` AND NOT EXISTS(SELECT 1 FROM workspace_members restricted WHERE restricted.user_id=wm.user_id AND restricted.access_mode='restricted')`
	}
	var value PrivateArtifactContext
	var role string
	err := db.QueryRowContext(ctx, `SELECT c.workspace_id,c.id,COALESCE(c.private_artifact_author_user_id,c.created_by,''),(`+privateArtifactSourceSQL+`),wm.role `+source, args...).Scan(&value.WorkspaceID, &value.ChatID, &value.AuthorUserID, &value.Sealed, &role)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if value.Decide(userID, role) == ArtifactContextOwner {
		// Recheck within the audit write statement. The earlier read is not
		// authority to inspect bytes after a concurrent role/custody change.
		auditArgs := []any{"c" + rand.Text(), tsformat.Format(time.Now().UTC())}
		auditArgs = append(auditArgs, args...)
		auditArgs = append(auditArgs, value.WorkspaceID, value.ChatID, value.AuthorUserID)
		result, err := db.ExecContext(ctx, `INSERT INTO audit_logs(id,workspace_id,user_id,action,entity_type,entity_id,metadata,created_at)
 SELECT ?,c.workspace_id,wm.user_id,'artifact.private_owner_accessed','chat',c.id,
 json_object('owner_user_id',c.created_by,'output_source','conversation'),? `+source+`
 AND c.workspace_id=? AND c.id=? AND c.created_by=? AND wm.user_id<>c.created_by
 AND wm.role='OWNER' AND wm.access_mode='trusted' AND (`+privateArtifactSourceSQL+`)
 AND NOT EXISTS(SELECT 1 FROM workspace_members restricted WHERE restricted.user_id=wm.user_id AND restricted.access_mode='restricted')`, auditArgs...)
		if err != nil {
			return false, err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return false, err
		}
		if count != 1 {
			return false, ErrPrivateArtifactContext
		}
	}
	return true, nil
}

// FilterReadableArtifactConversations refreshes a bounded catalog in one SQL
// snapshot after enrichment. Only protected rows require individual delivery
// checks/audits; ordinary rows retain a batched read. Callers preserve order
// by projecting this ID set onto their already-loaded rows.
func FilterReadableArtifactConversations(ctx context.Context, db *sql.DB, workspace, user string, ids []string) (map[string]bool, error) {
	result := map[string]bool{}
	if len(ids) == 0 {
		return result, nil
	}
	if db == nil || workspace == "" || user == "" || len(ids) > 100 {
		return nil, ErrPrivateArtifactContext
	}
	args := []any{user, workspace}
	placeholders := make([]string, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args = append(args, id)
	}
	args = append(args, Args(user)...)
	rows, err := db.QueryContext(ctx, `SELECT c.id,(`+privateArtifactSourceSQL+`)
 FROM chats c JOIN workspace_members wm ON wm.workspace_id=c.workspace_id AND wm.user_id=?
 JOIN users u ON u.id=wm.user_id AND u.suspended_at IS NULL
 JOIN workspaces w ON w.id=c.workspace_id AND w.deleted_at IS NULL
 JOIN agents a ON a.id=c.agent_id AND a.workspace_id=c.workspace_id AND a.deleted_at IS NULL
 WHERE c.workspace_id=? AND c.id IN (`+strings.Join(placeholders, ",")+`) AND (`+VisibleSQL+`) AND (`+PrivateArtifactContextSQL+`)`, args...)
	if err != nil {
		return nil, err
	}
	var protected []string
	for rows.Next() {
		var id string
		var sealed bool
		if err := rows.Scan(&id, &sealed); err != nil {
			rows.Close()
			return nil, err
		}
		if sealed {
			protected = append(protected, id)
		} else {
			result[id] = true
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for _, id := range protected {
		allowed, err := CanReadArtifactConversation(ctx, db, id, user, workspace, false)
		if err != nil {
			return nil, err
		}
		if allowed {
			result[id] = true
		}
	}
	return result, nil
}
