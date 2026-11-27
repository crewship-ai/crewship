package api

import (
	"context"
	"database/sql"

	"github.com/crewship-ai/crewship/internal/mooring"
)

// Only personal workbook pages linked to a private chat are in the pilot.
// Check the stored references so an ordinary session or CLI cannot bypass
// the preview-only profile by omitting the chat ID from a request.
func (m *craftNativeHTTP) checkChatSheetWrite(ctx context.Context, tx *sql.Tx, scope mooring.EditLeaseScope, application string) error {
	if application != "gridcraft" || m.agents == nil {
		return nil
	}
	profile, ok := m.agents.documents.application(application)
	if !ok || !profile.ChatSheetPreviewOnly {
		return nil
	}
	var linked bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM pages p WHERE p.workspace_id=?1 AND p.id=?2 AND p.private_audience=1
		AND p.owner_user_id IS NOT NULL AND p.owner_crew_id IS NULL AND p.folder_id IS NULL AND (
			EXISTS (SELECT 1 FROM artifact_chat_refs r JOIN chats c ON c.id=r.chat_id AND c.workspace_id=r.workspace_id
				WHERE r.workspace_id=p.workspace_id AND r.page_id=p.id AND c.visibility='private' AND c.created_by=p.owner_user_id)
			OR EXISTS (SELECT 1 FROM artifact_message_refs r JOIN chats c ON c.id=r.chat_id AND c.workspace_id=r.workspace_id
				WHERE r.workspace_id=p.workspace_id AND r.page_id=p.id AND c.visibility='private' AND c.created_by=p.owner_user_id)
		)
	)`, scope.WorkspaceID, scope.ArtifactID).Scan(&linked)
	if err != nil {
		return err
	}
	if linked {
		return errCraftNativeAccess
	}
	return nil
}
