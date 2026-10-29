package api

import (
	"context"
	"database/sql"
	"errors"
)

// Called only within the existing VersionStore writer's exact-run authority
// callback, after principal, original credential, policy and consent checks.
// Model arguments never select the marker. A manual consent without a typed
// human creation intent retains the historical unmarked behavior.
func (h *PageHandler) artifactProjectCreationTestOrigin(ctx context.Context, run *artifactRunAuthority) (bool, error) {
	if run == nil {
		return false, errArtifactProjectAccess
	}
	var marked bool
	err := h.pageAuthReader(ctx).QueryRowContext(ctx, `SELECT created_by_test FROM artifact_project_message_intents WHERE run_id=? AND workspace_id=? AND human_user_id=? AND chat_id=? AND message_id=? AND page_id IS NULL AND comment_id IS NULL AND base_version=0 AND revoked_at IS NULL`, run.RunID, run.WorkspaceID, run.HumanUserID, run.ChatID, run.MessageID).Scan(&marked)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return marked, err
}
