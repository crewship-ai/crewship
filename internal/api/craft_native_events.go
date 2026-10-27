package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

// events is an authenticated server heartbeat. It never depends on browser
// visibility, never acquires custody, and renews only this exact live holder.
func (m *craftNativeHTTP) events(w http.ResponseWriter, r *http.Request) {
	entry, err := m.bound(r)
	if err != nil {
		craftNativeHTTPError(w, err)
		return
	}
	r = r.WithContext(context.WithValue(r.Context(), craftNativeStreamKey{}, entry.id))
	m.mu.Lock()
	if m.live[entry.id] {
		m.mu.Unlock()
		replyError(w, 409, "native connection already live")
		return
	}
	m.live[entry.id] = true
	m.mu.Unlock()
	defer func() { m.mu.Lock(); delete(m.live, entry.id); m.mu.Unlock() }()
	controller := http.NewResponseController(w)
	if err = controller.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		replyError(w, 503, "bounded native stream unavailable")
		return
	}
	defer func() { _ = controller.SetWriteDeadline(time.Time{}) }()
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	send := func(event string, value any) bool {
		raw, e := json.Marshal(value)
		if e != nil {
			return false
		}
		if controller.SetWriteDeadline(time.Now().Add(5*time.Second)) != nil {
			return false
		}
		if _, e = w.Write(append([]byte("event: "+event+"\ndata: "), append(raw, []byte("\n\n")...)...)); e != nil {
			return false
		}
		return controller.Flush() == nil
	}
	retire := func() {
		draft := "retain"
		tx, e := m.pages.db.BeginTx(r.Context(), nil)
		if e == nil {
			_, e = m.pageAuthority(r.Context(), tx, entry.source, entry.scope, false)
			_ = tx.Rollback()
			if errors.Is(e, errCraftNativeAccess) || errors.Is(e, errContentCredentialNotCurrent) || errors.Is(e, errContentCredentialScope) || errors.Is(e, sql.ErrNoRows) {
				draft = "purge"
			}
		}
		send("session_retired", map[string]any{"delivery": "stopped", "reason": "authority_changed", "draft": draft})
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		// Repeat original-source, scope and current Page read authority immediately
		// before each metadata delivery. No document bytes flow through this stream.
		freshEntry, e := m.bound(r)
		if e != nil {
			retire()
			return
		}
		entry = freshEntry
		lease, e := m.leases.Read(r.Context(), entry.scope, craftNativePrincipal(entry))
		if e != nil {
			retire()
			return
		}
		readonly := false
		if lease.Holder == craftNativePrincipal(entry) {
			renewed, e := m.leases.Renew(r.Context(), lease)
			if e != nil {
				readonly = true
			} else {
				lease = renewed
			}
		}
		view := m.leaseView(r, entry, lease)
		if readonly {
			view["can_write"] = false
			view["draft"] = "retain"
		}
		freshEntry, e = m.bound(r)
		if e != nil {
			retire()
			return
		}
		entry = freshEntry
		if !send("lease", view) {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}
