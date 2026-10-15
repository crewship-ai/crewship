package api

import "context"

// limitedChatToken rejects authority that would be widened when converted to
// a WebSocket ticket or queued chat context. Those transports currently carry
// a human identity, not the originating CLI token's scope fence. Only an
// unrestricted session/token or the explicit global wildcard is equivalent.
func limitedChatToken(ctx context.Context) bool {
	return ctx.Value(ctxTokenScopes) != nil && !canScope(ctx, "*")
}
