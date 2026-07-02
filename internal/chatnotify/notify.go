// Package chatnotify projects persisted assistant chat replies into the
// unified inbox ("your agent replied") for users who are NOT currently
// watching the session live over WebSocket. It implements
// chatbridge.ReplyNotifier and is wired into the Bridge at server boot
// (cmd_start.go), keeping the bridge decoupled from the DB + ws.Hub.
//
// Semantics:
//
//   - Recipients are the chat's creator plus every chat_participants
//     row. In a group chat the human whose message triggered the run is
//     excluded — they asked, they don't need a bell about the answer.
//   - A user with a live subscription on "session:<chatId>" is skipped:
//     they watched the reply stream in.
//   - Dedupe is per (user, chat): source_id "chat_reply_<chat>_<user>"
//     with inbox.UpsertMessage, so repeated replies refresh ONE unread
//     bell item (timestamp + preview) instead of stacking.
//   - The preview is credential-scrubbed and truncated to ~120 runes.
//   - Marking the chat read (PUT /agents/{id}/chats/{id}/read) clears
//     the item — see api.MarkChatRead, which shares the source_id shape.
package chatnotify

import (
	"context"
	"database/sql"
	"log/slog"
	"strings"

	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/inbox"
	"github.com/crewship-ai/crewship/internal/scrubber"
)

// previewRunes caps the reply preview embedded in the inbox item body.
const previewRunes = 120

// Hub is the slice of ws.Hub the notifier needs: presence ("is this
// user watching the session channel right now?") and the workspace
// broadcast that repaints the bell badge. *ws.Hub satisfies it.
type Hub interface {
	IsUserSubscribed(channel, userID string) bool
	BroadcastWorkspace(wsID, eventType string, payload any)
}

// Notifier is the production ReplyNotifier. Safe for concurrent use.
type Notifier struct {
	db     *sql.DB
	hub    Hub
	scrub  *scrubber.Scrubber
	logger *slog.Logger
}

// Compile-time check: Notifier satisfies the bridge-side contract.
var _ chatbridge.ReplyNotifier = (*Notifier)(nil)

// New builds a Notifier. logger may be nil (falls back to slog default);
// hub may be nil (presence then reports "not watching" for everyone,
// which degrades to always-notify rather than never-notify).
func New(db *sql.DB, hub Hub, logger *slog.Logger) *Notifier {
	if logger == nil {
		logger = slog.Default()
	}
	return &Notifier{db: db, hub: hub, scrub: scrubber.New(), logger: logger}
}

// NotifyAssistantReply implements chatbridge.ReplyNotifier. Best-effort:
// failures are logged, never propagated — the reply itself is already
// durably persisted by the bridge and must not be affected.
func (n *Notifier) NotifyAssistantReply(ctx context.Context, rn chatbridge.ReplyNotification) {
	if n == nil || n.db == nil || rn.ChatID == "" || rn.WorkspaceID == "" {
		return
	}
	preview := n.preview(rn.ReplyText)
	if preview == "" {
		return
	}

	// Chat metadata — also revalidates the (chat, workspace) pairing so
	// a confused caller can't write a notification into the wrong tenant.
	var createdBy, title sql.NullString
	err := n.db.QueryRowContext(ctx,
		`SELECT created_by, title FROM chats WHERE id = ? AND workspace_id = ?`,
		rn.ChatID, rn.WorkspaceID).Scan(&createdBy, &title)
	if err != nil {
		if err != sql.ErrNoRows {
			n.logger.Warn("chatnotify: chat lookup", "error", err, "chat_id", rn.ChatID)
		}
		return
	}

	agentName := rn.AgentSlug
	if rn.AgentID != "" {
		var name sql.NullString
		if err := n.db.QueryRowContext(ctx,
			`SELECT name FROM agents WHERE id = ?`, rn.AgentID).Scan(&name); err == nil && name.String != "" {
			agentName = name.String
		}
	}

	recipients := map[string]bool{}
	if createdBy.String != "" {
		recipients[createdBy.String] = true
	}
	rows, err := n.db.QueryContext(ctx,
		`SELECT user_id FROM chat_participants WHERE chat_id = ?`, rn.ChatID)
	if err != nil {
		n.logger.Warn("chatnotify: participants lookup", "error", err, "chat_id", rn.ChatID)
	} else {
		defer rows.Close()
		for rows.Next() {
			var uid string
			if err := rows.Scan(&uid); err == nil && uid != "" {
				recipients[uid] = true
			}
		}
		_ = rows.Err()
	}

	// Group chats: the author of the triggering message asked the
	// question — don't bell them about the answer. In a private 1:1 the
	// author IS the chat's only user; presence alone decides there
	// (walked-away-mid-run is exactly the case this feature exists for).
	if rn.Visibility == "group" && rn.AuthorUserID != "" {
		delete(recipients, rn.AuthorUserID)
	}

	sessionChannel := "session:" + rn.ChatID
	chatURL := "/chat/" + rn.AgentSlug + "?session=" + rn.ChatID
	itemTitle := agentName + " replied"
	if title.String != "" {
		itemTitle += " · " + n.preview(title.String)
	}

	notified := 0
	for uid := range recipients {
		if n.hub != nil && n.hub.IsUserSubscribed(sessionChannel, uid) {
			continue // watching live — no bell needed
		}
		err := inbox.UpsertMessage(ctx, n.db, n.logger, inbox.Item{
			WorkspaceID:  rn.WorkspaceID,
			Kind:         inbox.KindMessage,
			SourceID:     "chat_reply_" + rn.ChatID + "_" + uid,
			TargetUserID: uid,
			Title:        itemTitle,
			BodyMD:       preview,
			SenderType:   "agent",
			SenderID:     rn.AgentID,
			SenderName:   agentName,
			Priority:     "medium",
			Blocking:     false,
			Payload: map[string]interface{}{
				"chat_id":    rn.ChatID,
				"agent_id":   rn.AgentID,
				"agent_slug": rn.AgentSlug,
				"chat_title": title.String,
				"chat_url":   chatURL,
			},
		})
		if err != nil {
			n.logger.Warn("chatnotify: inbox upsert", "error", err, "chat_id", rn.ChatID, "user_id", uid)
			continue
		}
		notified++
	}

	if notified > 0 && n.hub != nil {
		n.hub.BroadcastWorkspace(rn.WorkspaceID, "inbox.updated", map[string]string{
			"source": "chat_reply",
			"chat":   rn.ChatID,
		})
	}
}

// preview scrubs credentials out of the reply text, collapses it onto a
// single line, and truncates to previewRunes runes with an ellipsis.
func (n *Notifier) preview(text string) string {
	s := strings.TrimSpace(n.scrub.Scrub(text))
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > previewRunes {
		return string(r[:previewRunes]) + "…"
	}
	return s
}
