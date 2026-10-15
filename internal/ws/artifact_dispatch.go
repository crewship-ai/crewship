package ws

import (
	"context"
	"errors"
)

// Server-only dispatch keeps the same recording and bridge admission as chat;
// no untrusted WebSocket metadata can set ArtifactRequestID.
func (h *Hub) DispatchArtifactChat(ctx context.Context, user, chat, content string, opts ChatMessageOption) error {
	if h.chatHandler == nil {
		return errors.New("chat runtime unavailable")
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	key := user + ":" + chat
	h.cancelMu.Lock()
	if _, busy := h.cancelFns[key]; busy {
		h.cancelMu.Unlock()
		return ErrAgentBusy
	}
	h.cancelFns[key] = cancel
	h.cancelMu.Unlock()
	defer func() { h.cancelMu.Lock(); delete(h.cancelFns, key); h.cancelMu.Unlock() }()
	run := h.beginSessionRun(chat, nil)
	defer run.End()
	return h.chatHandler.HandleChatMessage(runCtx, user, chat, content, run.Emit, opts)
}
