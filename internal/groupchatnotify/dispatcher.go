// Package groupchatnotify projects durable conversation events into the inbox
// and identity-scoped realtime invalidations. Conversation contents never enter
// the projection, so a revoked participant cannot read private text in history.
package groupchatnotify

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/crewship-ai/crewship/internal/groupchat"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

type Hub interface {
	BroadcastChannelContext(context.Context, string, string, string, any) error
}

type Dispatcher struct {
	db     *sql.DB
	store  *groupchat.Store
	hub    Hub
	logger *slog.Logger
}

func New(db *sql.DB, hub Hub, logger *slog.Logger) *Dispatcher {
	if logger == nil {
		logger = slog.Default()
	}
	return &Dispatcher{db: db, store: groupchat.New(db), hub: hub, logger: logger}
}

// Run drains on startup and periodically thereafter, recovering a process that
// died after accepting a message. The caller owns and joins this goroutine.
func (d *Dispatcher) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if err := d.store.ProjectActivity(ctx); err != nil && ctx.Err() == nil {
			d.logger.Warn("channel activity projection delayed", "error", err)
		}
		if err := d.Drain(ctx); err != nil && ctx.Err() == nil {
			d.logger.Warn("conversation notifications delayed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Drain is safe to replay. A recipient's last_sequence prevents old events
// from resurrecting read items or overwriting a newer notification.
func (d *Dispatcher) Drain(ctx context.Context) error {
	events, err := d.store.PendingEvents(ctx, 100)
	if err != nil {
		return err
	}
	for _, event := range events {
		recipients, err := d.project(ctx, event)
		if err != nil {
			return err
		}
		for _, uid := range recipients {
			if d.hub == nil {
				continue
			}
			payload := map[string]string{"conversation_id": event.ConversationID}
			if err := d.hub.BroadcastChannelContext(ctx, "user", uid, "conversation.updated", payload); err != nil {
				return err
			}
			if err := d.hub.BroadcastChannelContext(ctx, "user", uid, "inbox.updated", payload); err != nil {
				return err
			}
		}
		if err := d.store.AcknowledgeEvent(ctx, event.ID); err != nil {
			return err
		}
	}
	return nil
}

func (d *Dispatcher) project(ctx context.Context, event groupchat.Event) ([]string, error) {
	conn, err := d.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return nil, err
	}
	defer conn.ExecContext(context.Background(), "ROLLBACK")
	var workspaceID, authorID string
	var sequence int64
	err = conn.QueryRowContext(ctx, `SELECT c.workspace_id, COALESCE(m.author_user_id,''),m.sequence
 FROM workspace_conversations c JOIN workspaces w ON w.id=c.workspace_id
 JOIN workspace_conversation_messages m ON m.conversation_id=c.id AND m.id=?
 WHERE c.id=? AND c.deleted_at IS NULL AND w.deleted_at IS NULL`, event.MessageID, event.ConversationID).Scan(&workspaceID, &authorID, &sequence)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rows, err := conn.QueryContext(ctx, `SELECT wm.user_id, COALESCE(cm.last_read_sequence,0),COALESCE(cm.muted,0)
 FROM workspace_members wm JOIN workspace_conversations c ON c.workspace_id=wm.workspace_id
 LEFT JOIN workspace_conversation_members cm ON cm.conversation_id=c.id AND cm.user_id=wm.user_id
 WHERE c.id=? AND (c.kind='channel' OR cm.user_id IS NOT NULL)`, event.ConversationID)
	if err != nil {
		return nil, err
	}
	type recipient struct {
		id    string
		read  int64
		muted bool
	}
	var recipients []recipient
	for rows.Next() {
		var r recipient
		if err := rows.Scan(&r.id, &r.read, &r.muted); err != nil {
			rows.Close()
			return nil, err
		}
		recipients = append(recipients, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(map[string]any{"conversation_id": event.ConversationID, "chat_url": groupchat.URL(event.ConversationID), "last_sequence": sequence})
	if err != nil {
		return nil, err
	}
	now := tsformat.Format(time.Now())
	var userIDs []string
	for _, recipient := range recipients {
		userIDs = append(userIDs, recipient.id)
		if recipient.id == authorID || recipient.read >= sequence || recipient.muted {
			continue
		}
		source := "conversation_" + event.ConversationID + "_" + recipient.id
		id := "ibx_message_" + source
		result, err := conn.ExecContext(ctx, `INSERT INTO inbox_items
   (id,workspace_id,kind,source_id,target_user_id,title,body_md,sender_type,state,priority,blocking,payload_json,created_at,updated_at)
   VALUES (?,?,'message',?,?,'New conversation activity','Open the conversation to read new messages.','system','unread','medium',0,?,?,?)
   ON CONFLICT(kind,source_id) DO UPDATE SET
    payload_json=excluded.payload_json,state='unread',read_at=NULL,read_by_user_id=NULL,
    resolved_at=NULL,resolved_by_user_id=NULL,resolved_action=NULL,created_at=excluded.created_at,updated_at=excluded.updated_at
   WHERE COALESCE(CAST(json_extract(inbox_items.payload_json,'$.last_sequence') AS INTEGER),0) < ?`,
			id, workspaceID, source, recipient.id, string(payload), now, now, sequence)
		if err != nil {
			return nil, fmt.Errorf("project conversation inbox: %w", err)
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return nil, err
		}
		if changed > 0 {
			if _, err := conn.ExecContext(ctx, `DELETE FROM inbox_item_reads WHERE inbox_item_id=(SELECT id FROM inbox_items WHERE kind='message' AND source_id=?)`, source); err != nil {
				return nil, err
			}
		}
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return nil, err
	}
	return userIDs, nil
}
