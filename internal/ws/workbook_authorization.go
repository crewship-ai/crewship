package ws

import (
	"context"
	"encoding/json"
	"time"
)

// WorkbookSessionRechecker is the host's canonical writer-session authority.
// It closes persisted editor sessions transactionally and returns affected users.
type WorkbookSessionRechecker func(context.Context, string) ([]string, error)

// TrackWorkbookPage records resource kind only. It never caches permission.
// Successful editor-session issuance calls this before any editor is shown.
func (h *Hub) TrackWorkbookPage(pageID string, recheck ...WorkbookSessionRechecker) {
	if h == nil || pageID == "" {
		return
	}
	h.mu.Lock()
	if h.workbookPages == nil {
		h.workbookPages = map[string]WorkbookSessionRechecker{}
	}
	if len(recheck) > 0 {
		h.workbookPages[pageID] = recheck[0]
	} else if _, ok := h.workbookPages[pageID]; !ok {
		h.workbookPages[pageID] = nil
	}
	h.mu.Unlock()
}
func (h *Hub) isWorkbookPage(pageID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	_, known := h.workbookPages[pageID]
	return known
}

// RecheckWorkbookAccess runs after a committed rights change. closedUsers are
// trusted host decisions made in the grant transaction (loss of editor access,
// including a participant who retains an auditor's read-only access).
// The terminal control has no document content and no protected channel, so
// loss of content permission cannot prevent it from reaching that participant.
func (h *Hub) RecheckWorkbookAccess(ctx context.Context, pageID string, closedUsers ...string) {
	if h == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	channel := "page:" + pageID
	h.mu.RLock()
	var clients []*Client
	for c := range h.channels[channel] {
		clients = append(clients, c)
	}
	var observers []*Observer
	for o := range h.observers[channel] {
		observers = append(observers, o)
	}
	h.mu.RUnlock()
	forced := map[string]bool{}
	for _, user := range closedUsers {
		forced[user] = true
	}
	checked, allowed, failed := map[string]bool{}, map[string]bool{}, map[string]bool{}
	canKeep := func(user string) bool {
		if ctx.Err() != nil {
			failed[user] = true
			return false
		}
		if forced[user] {
			return false
		}
		if !checked[user] {
			checked[user] = true
			if h.channelAuth != nil {
				ok, err := h.channelAuth.CanSubscribe(ctx, user, channel)
				allowed[user] = err == nil && ctx.Err() == nil && ok
				failed[user] = err != nil || ctx.Err() != nil
			}
		}
		return allowed[user]
	}
	frame, _ := json.Marshal(ServerMessage{Type: "session_closed", Payload: map[string]string{"artifact_id": pageID, "reason": "access_revoked"}})
	for _, c := range clients {
		if canKeep(c.userID) {
			continue
		}
		c.unsubscribe(channel)
		if sent := c.trySend(frame); !sent || failed[c.userID] {
			c.forceDisconnect()
		}
	}
	for _, o := range observers {
		if canKeep(o.userID) {
			continue
		}
		// RemoveObserver cannot close the channel while this read lock protects
		// its membership. Never send to an already detached observer.
		h.mu.RLock()
		if h.observers[channel][o] {
			select {
			case o.ch <- frame:
			default:
				o.dropped.Store(true)
			}
		}
		h.mu.RUnlock()
		h.revokeObserver(channel, o)
	}
}

func (h *Hub) maybeStartWorkbookSweep(ctx context.Context) {
	if !h.workbookSweepInFlight.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer h.workbookSweepInFlight.Store(false)
		// One deadline for the entire sweep. Once exhausted, all later checks
		// fail closed rather than leaving the end of the list authorized forever.
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		h.mu.RLock()
		var pages []string
		for page := range h.workbookPages {
			channel := "page:" + page
			if len(h.channels[channel]) > 0 || len(h.observers[channel]) > 0 {
				pages = append(pages, page)
			}
		}
		h.mu.RUnlock()
		for _, page := range pages {
			h.mu.RLock()
			recheck := h.workbookPages[page]
			h.mu.RUnlock()
			var closed []string
			if recheck != nil {
				var err error
				closed, err = recheck(ctx, page)
				if err != nil {
					// Writer authority or persisted session closure could not be verified.
					// A canceled check forces every later participant closed, not just the
					// subscriber whose query first exhausted the shared budget.
					denied, cancel := context.WithCancel(ctx)
					cancel()
					h.RecheckWorkbookAccess(denied, page)
					continue
				}
			}
			h.RecheckWorkbookAccess(ctx, page, closed...)
		}
	}()
}
