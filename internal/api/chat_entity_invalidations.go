package api

import (
	"context"
	"database/sql"
	"time"

	"github.com/crewship-ai/crewship/internal/ws"
)

const entityRefsContractVersion = 1

// EntityInvalidation carries no object identity, content, count or revision.
// Clients replace their authorized snapshot after either best-effort hint.
type EntityInvalidation struct {
	ContractVersion int `json:"contract_version"`
}

// Called only after a successful commit, never by mutation outbox retries.
// deletionReceipts is true only when deletion appended a receipt to every
// referenced chat. Ordinary issue mutations emit revision hints only.
// Trusted chat viewers have the same workspace-wide issue read boundary as
// entity-refs today. Keep the visibility predicate shared: a future narrower
// issue boundary must update both projections before these hints are emitted.
func broadcastIssueCardInvalidations(ctx context.Context, db *sql.DB, hub *ws.Hub, workspace, issue string, deletionReceipts bool) {
	if hub == nil || db == nil || workspace == "" || issue == "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	rows, err := db.QueryContext(ctx, `SELECT DISTINCT cr.chat_id FROM chat_entity_refs cr
 JOIN agent_operations op ON op.id=cr.operation_id
 JOIN chats c ON c.id=cr.chat_id AND c.workspace_id=op.workspace_id
 JOIN workspaces w ON w.id=c.workspace_id AND w.deleted_at IS NULL
 JOIN agents a ON a.id=c.agent_id AND a.workspace_id=c.workspace_id AND a.deleted_at IS NULL
 LEFT JOIN missions m ON m.id=op.entity_id AND m.workspace_id=op.workspace_id
 WHERE op.workspace_id=? AND op.entity_id=? AND op.kind='issue' AND op.status='completed'
 AND `+entityRefVisibilitySQL, workspace, issue)
	if err != nil {
		return
	}
	var chats []string
	for rows.Next() {
		var chat string
		if err = rows.Scan(&chat); err != nil {
			break
		}
		chats = append(chats, chat)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return
	}
	for _, chat := range chats {
		// Session subscription and socket-delivery gates recheck each viewer's
		// live trusted audience. No workspace-wide hint discloses private chat work.
		if deletionReceipts {
			if broadcastChatHint(ctx, hub, chat, "chat.entity_refs_changed") != nil {
				return
			}
		}
		if broadcastChatHint(ctx, hub, chat, "issue.revision_changed") != nil {
			return
		}
	}
}

// A completed chat operation changes only its originating reference list.
// Other chats may refresh the issue revision, but must not learn chat activity.
func broadcastChatEntityRefsInvalidation(ctx context.Context, hub *ws.Hub, chat string) {
	if hub != nil && chat != "" {
		_ = broadcastChatHint(ctx, hub, chat, "chat.entity_refs_changed")
	}
}

// Keep session publication for existing clients and NDJSON stream consumers.
// Card-only clients subscribe to chat-hints to avoid receiving run frames.
// Both channels use the exact same trusted chat audience at subscribe/delivery.
func broadcastChatHint(ctx context.Context, hub *ws.Hub, chat, event string) error {
	for _, channel := range []string{"session", "chat-hints"} {
		if err := hub.BroadcastChannelContext(ctx, channel, chat, event, EntityInvalidation{entityRefsContractVersion}); err != nil {
			return err
		}
	}
	return nil
}
