package chatbridge

import (
	"context"
	"strings"
	"time"
)

// ReplyNotification describes one persisted assistant reply. The bridge
// emits it after the reply has durably landed in the conversation store;
// the notifier (internal/chatnotify) decides who — if anyone — needs an
// inbox "your agent replied" item, using WS presence to skip users who
// watched the reply stream live.
type ReplyNotification struct {
	ChatID      string
	WorkspaceID string
	AgentID     string
	AgentSlug   string
	// Visibility is the chat's visibility at resolve time ("group" or
	// "private"/empty). In a group chat the human whose message
	// triggered the run is not re-notified about the answer to their
	// own prompt.
	Visibility string
	// AuthorUserID is the human whose message triggered this run.
	AuthorUserID string
	// ReplyText is the full flattened assistant text; the notifier owns
	// preview truncation + credential scrubbing.
	ReplyText string
	// RepliedAt is the timestamp the assistant message was persisted
	// with. The notifier compares it against each recipient's
	// chat_read_cursors row: a cursor at or past this instant means a
	// racing mark-read already covered the reply, so no bell item is
	// (re-)raised for it.
	RepliedAt time.Time
}

// ReplyNotifier receives a notification once per persisted assistant
// reply. Implementations must be fast or fire-and-forget internally —
// the bridge calls it inline on the run-completion path.
type ReplyNotifier interface {
	NotifyAssistantReply(ctx context.Context, n ReplyNotification)
}

// SetReplyNotifier wires the "never miss a reply" projection after
// Bridge construction (the notifier needs the ws.Hub + DB, both built
// later in the server boot sequence — same pattern as
// SetSteerBroadcaster). Nil (the default) disables notification.
func (b *Bridge) SetReplyNotifier(rn ReplyNotifier) {
	b.replyNotifier = rn
}

// notifyReply announces a persisted assistant reply to the wired
// notifier. repliedAt must be the timestamp the assistant message was
// persisted with (not "now" — the notifier compares it against read
// cursors written by a racing mark-read). Nil-safe and empty-safe: no
// notifier or a blank reply (e.g. a run cancelled before any text) is a
// no-op.
func (b *Bridge) notifyReply(ctx context.Context, chatID, authorUserID string, info *ChatInfo, replyText string, repliedAt time.Time) {
	if b.replyNotifier == nil || strings.TrimSpace(replyText) == "" {
		return
	}
	b.replyNotifier.NotifyAssistantReply(ctx, ReplyNotification{
		ChatID:       chatID,
		WorkspaceID:  info.WorkspaceID,
		AgentID:      info.AgentID,
		AgentSlug:    info.AgentSlug,
		Visibility:   info.Visibility,
		AuthorUserID: authorUserID,
		ReplyText:    replyText,
		RepliedAt:    repliedAt,
	})
}
