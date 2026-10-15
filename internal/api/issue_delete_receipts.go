package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/crewship-ai/crewship/internal/chatentities"
)

// Deletion receipts disclose no issue content. Their audience stays the existing
// trusted chat boundary, regardless of which authorized human deleted the issue.
func appendHumanIssueDeletionReceipts(ctx context.Context, tx *sql.Tx, workspace, issue, actor string, revision, aggregate int64) error {
	var referenced bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM chat_entity_refs cr JOIN agent_operations op ON op.id=cr.operation_id WHERE op.entity_id=? AND op.workspace_id=?)`, issue, workspace).Scan(&referenced); err != nil {
		return err
	}
	if referenced {
		operationID := generateCUID()
		op, _, err := chatentities.BeginOperation(ctx, tx, chatentities.OperationSpec{
			ID: operationID, WorkspaceID: workspace, Key: "delete:" + operationID,
			RequestHash: fmt.Sprintf("%x", sha256.Sum256([]byte(issue))),
			ActorType:   "human", ActorID: actor, Provenance: "human", Kind: "issue", Verb: "deleted",
		})
		if err != nil {
			return err
		}
		snapshot, _ := json.Marshal(chatentities.DeletionAccessSnapshot{Version: 1, WorkspaceID: workspace, Visibility: "trusted_workspace"})
		if err := chatentities.CompleteOperation(ctx, tx, op.ID, chatentities.ResultSpec{
			EntityID: issue, Revision: revision, AggregateRevision: aggregate,
			AccessSnapshot: snapshot, Result: json.RawMessage(`{"deleted":true}`),
		}); err != nil {
			return err
		}
		if _, err := chatentities.AppendDeletionRefs(ctx, tx, op.ID, workspace, issue); err != nil {
			return err
		}
	}
	return nil
}
