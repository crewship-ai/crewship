package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/artifacts"
)

// craftNativeEventInterval is the server's authority recheck cadence on an open
// metadata stream: on a healthy connection read loss reaches the client within
// one interval plus delivery.
const craftNativeEventInterval = 5 * time.Second

// retirementFinding says why a stream ends and whether the browser may erase its
// draft. Erasing needs proof that this still signed-in person lost read of the
// Page, and the proof is the one draft-key gives, localDraftRead, so the two cannot
// disagree. Losing Edit alone is no such proof, even down to Comment: it takes
// latest away, and Read and Comment keep the same shared version. Neither is a
// refusal by native admission or application eligibility. An ended, revoked or
// missing credential says nothing about the Page either, and the browser keeps
// its draft through it (docs/specs/pages.md, Private browser draft recovery). A
// moved user revision alone is no proof, it also moves for other workspaces, so
// the Page is judged on the current revision.
func (m *craftNativeHTTP) retirementFinding(ctx context.Context, entry *craftNativeSession) (draft, reason string) {
	draft, reason = "retain", "authority_changed"
	tx, err := m.pages.db.BeginTx(ctx, nil)
	if err != nil {
		return draft, reason
	}
	defer tx.Rollback()
	source := entry.source
	current, err := loadOriginalCredential(ctx, tx, source.UserID, source.Kind, source.ID)
	if err != nil || current.reference.Binding != source.Binding {
		return draft, reason
	}
	if expiry, _ := source.Expiry(); !expiry.IsZero() && !time.Now().Before(expiry) {
		return draft, reason
	}
	source.UserAuthorityRevision = current.reference.UserAuthorityRevision
	if _, err = validateOriginalHumanCredential(ctx, tx, source, "pages:read"); err != nil {
		return draft, reason
	}
	read := context.WithValue(context.WithValue(ctx, pageAuthReaderKey{}, tx), ctxUser, &AuthUser{ID: source.UserID})
	err = m.pages.localDraftRead(read, artifacts.VersionKey{WorkspaceID: entry.scope.WorkspaceID, PageID: entry.scope.ArtifactID}, tx)
	if errors.Is(err, errLocalDraftReadDenied) {
		return "purge", reason
	}
	if err == nil {
		// Read still holds but Page write is explicitly denied (#4348). The
		// reason only lets the client explain itself and ask for a FRESH read
		// admission; this admission and its lease generation stay retired.
		if _, denied := m.pageAuthority(ctx, tx, source, entry.scope, true); errors.Is(denied, errCraftNativeAccess) {
			reason = "write_revoked"
		}
	}
	return draft, reason
}

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
		draft, reason := m.retirementFinding(r.Context(), entry)
		send("session_retired", map[string]any{"delivery": "stopped", "reason": reason, "draft": draft})
	}
	ticker := time.NewTicker(craftNativeEventInterval)
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
			view.CanWrite = false
			view.Draft = "retain"
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
