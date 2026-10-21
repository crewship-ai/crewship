package api

import (
	"context"
	"github.com/crewship-ai/crewship/internal/ws"
)

// BroadcastSourceEntityHint reuses the existing identity-free chat hint. The
// caller must establish current original source generation; existing session
// subscribe/delivery gates enforce each recipient's live trusted chat audience.
func BroadcastSourceEntityHint(ctx context.Context, hub *ws.Hub, chat string) {
	broadcastChatEntityRefsInvalidation(ctx, hub, chat)
}
