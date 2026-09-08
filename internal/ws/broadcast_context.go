package ws

import "context"

// BroadcastChannelContext queues an identity-scoped invalidation while allowing
// a server-owned dispatcher to stop when the hub shuts down. Delivery remains
// best effort, like BroadcastChannel; clients refresh authoritative state.
func (h *Hub) BroadcastChannelContext(ctx context.Context, prefix, id, eventType string, payload any) error {
	if h == nil {
		return nil
	}
	channel := prefix + ":" + id
	data, ok := h.marshalFrame(ServerMessage{Type: eventType, Channel: channel, Payload: payload})
	if !ok {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case h.broadcast <- ChannelMessage{Channel: channel, Data: data}:
		return nil
	}
}
