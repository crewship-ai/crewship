package ws

import (
	"context"
	"time"
)

// RevokePageSubscribers re-checks, now, every live subscriber of one Page's
// channel ("page:<id>") and ends each subscription the authorizer no longer
// allows. It is the push half of an audience change the host has just
// committed (a Page returned to its owner's private audience), so former
// readers stop receiving at once instead of on the next periodic sweep.
//
// Unlike the periodic sweep, this runs because access was deliberately taken
// away, so it fails closed: a client whose re-check errors or times out is
// disconnected (it re-subscribes through the ordinary subscribe-time check),
// and an HTTP observer is revoked. A definitive deny unsubscribes only that
// channel and sends the same "access revoked" error frame the sweep sends; the
// socket and the client's other channels stay. Returns how many socket
// subscriptions and observers were ended. Nil-safe.
func (h *Hub) RevokePageSubscribers(ctx context.Context, pageID string) int {
	if h == nil || pageID == "" || h.channelAuth == nil {
		return 0
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	channel := "page:" + pageID
	h.mu.RLock()
	clients := make([]*Client, 0, len(h.channels[channel]))
	for c := range h.channels[channel] {
		clients = append(clients, c)
	}
	observers := make([]*Observer, 0, len(h.observers[channel]))
	for o := range h.observers[channel] {
		observers = append(observers, o)
	}
	h.mu.RUnlock()

	type verdict struct{ allowed, failed bool }
	decided := map[string]verdict{}
	check := func(user string) verdict {
		if v, ok := decided[user]; ok {
			return v
		}
		ok, err := h.channelAuth.CanSubscribe(ctx, user, channel)
		v := verdict{allowed: err == nil && ctx.Err() == nil && ok, failed: err != nil || ctx.Err() != nil}
		decided[user] = v
		return v
	}
	ended := 0
	for _, c := range clients {
		v := check(c.userID)
		if v.allowed {
			continue
		}
		c.unsubscribe(channel)
		ended++
		if v.failed {
			c.forceDisconnect()
			continue
		}
		c.trySendError(channel, "access revoked")
	}
	for _, o := range observers {
		if check(o.userID).allowed {
			continue
		}
		h.revokeObserver(channel, o)
		ended++
	}
	if ended > 0 {
		h.logger.Info("ws page subscriptions revoked after audience change", "page_id", pageID, "ended", ended)
	}
	return ended
}
