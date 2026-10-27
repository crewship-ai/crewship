package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/mooring"
)

type craftNativeLeaseBody struct {
	Generation string `json:"generation"`
	RequestID  string `json:"request_id"`
}

func craftNativeGeneration(raw string) (int64, error) {
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v < 1 || strconv.FormatInt(v, 10) != raw {
		return 0, errCraftNativeAccess
	}
	return v, nil
}
func (m *craftNativeHTTP) availability(w http.ResponseWriter, r *http.Request) {
	enabled := false
	if m.enabled && m.pages != nil {
		source, err := craftNativeHuman(r.Context())
		if err == nil {
			tx, e := m.pages.db.BeginTx(r.Context(), nil)
			if e == nil {
				defer tx.Rollback()
				ctx := context.WithValue(r.Context(), pageAuthReaderKey{}, tx)
				ws := WorkspaceIDFromContext(ctx)
				rec, e := m.pages.loadPageForRead(ctx, ws, r.PathValue("slug"))
				if e == nil {
					_, e = m.pageAuthority(ctx, tx, source, mooring.EditLeaseScope{WorkspaceID: ws, ArtifactID: rec.ID}, false)
					enabled = e == nil
				}
			}
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]bool{"enabled": enabled})
}
func craftNativeHTTPError(w http.ResponseWriter, err error) {
	if errors.Is(err, mooring.ErrEditLeaseBusy) || errors.Is(err, mooring.ErrEditLeaseStale) {
		replyError(w, 409, "native editor lease changed")
		return
	}
	replyError(w, 403, "native Craft access denied")
}
func (m *craftNativeHTTP) leaseView(r *http.Request, entry *craftNativeSession, l mooring.EditLease) map[string]any {
	var holder any
	if l.Holder.ActorID != "" {
		var name string
		_ = m.pages.db.QueryRowContext(r.Context(), `SELECT COALESCE(NULLIF(full_name,''),'User') FROM users WHERE id=?`, strings.TrimPrefix(l.Holder.ActorID, "user:")).Scan(&name)
		holder = map[string]any{"actor_id": l.Holder.ActorID, "display_name": name, "kind": "human"}
	}
	mine := l.Holder == craftNativePrincipal(entry)
	requests := make([]map[string]any, 0)
	type candidate struct {
		request craftNativeRequest
		target  *craftNativeSession
	}
	var candidates []candidate
	m.mu.Lock()
	for _, request := range m.requests {
		target := m.sessionsByID[request.target]
		if mine && target != nil && target.scope == entry.scope && request.generation == l.Generation {
			candidates = append(candidates, candidate{request, target})
		}
	}
	m.mu.Unlock()
	for _, candidate := range candidates {
		request, target := candidate.request, candidate.target
		boot, now, err := m.clock.Now()
		if err != nil || target.boot != boot || now >= target.deadline {
			continue
		}
		var name string
		err = m.pages.db.QueryRowContext(r.Context(), `SELECT COALESCE(NULLIF(u.full_name,''),'User') FROM users u WHERE u.id=? AND EXISTS(SELECT 1 FROM mooring_edit_lease_requests WHERE page_id=? AND actor_id=? AND session_id=? AND generation=?)`, target.source.UserID, entry.scope.ArtifactID, "user:"+target.source.UserID, target.id, l.Generation).Scan(&name)
		if err != nil {
			continue
		}
		requests = append(requests, map[string]any{"request_id": request.id, "actor_id": "user:" + target.source.UserID, "display_name": name, "kind": "human"})
	}
	var expiry, generation any
	if !l.ExpiresAt.IsZero() {
		expiry = l.ExpiresAt.UTC().Format(time.RFC3339Nano)
	}
	if l.Generation > 0 {
		generation = strconv.FormatInt(l.Generation, 10)
	}
	var requestedKind any
	if len(requests) > 0 {
		requestedKind = "human"
	}
	// The lease core has no durable acquisition timestamp. Renewal time would
	// falsely describe custody age, so the dormant API reports unknown explicitly.
	return map[string]any{"acquired_at": nil, "handoff_requested_kind": requestedKind, "holder": holder, "mine": mine, "generation": generation, "expires_at": expiry, "can_write": mine, "handoff_requested_by": requests}
}
func (m *craftNativeHTTP) lease(w http.ResponseWriter, r *http.Request) {
	entry, err := m.bound(r)
	if err != nil {
		craftNativeHTTPError(w, err)
		return
	}
	l, err := m.leases.Read(r.Context(), entry.scope, craftNativePrincipal(entry))
	if err != nil {
		craftNativeHTTPError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, m.leaseView(r, entry, l))
}
func (m *craftNativeHTTP) mutateLease(w http.ResponseWriter, r *http.Request, operation string) {
	if !m.enabled {
		replyError(w, 503, "native Craft unavailable")
		return
	}
	entry, err := m.bound(r)
	if err != nil {
		craftNativeHTTPError(w, err)
		return
	}
	var body craftNativeLeaseBody
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048))
	d.DisallowUnknownFields()
	if err = d.Decode(&body); err != nil {
		replyError(w, 400, "invalid native lease request")
		return
	}
	p := craftNativePrincipal(entry)
	var l mooring.EditLease
	if operation != "acquire" && operation != "request" {
		generation, e := craftNativeGeneration(body.Generation)
		if e != nil {
			replyError(w, 400, "invalid generation")
			return
		}
		l = mooring.EditLease{Scope: entry.scope, Holder: p, Generation: generation}
	}
	switch operation {
	case "acquire":
		l, err = m.leases.Acquire(r.Context(), entry.scope, p)
	case "renew":
		l, err = m.leases.Renew(r.Context(), l)
	case "release":
		err = m.leases.Release(r.Context(), l)
		if err == nil {
			l, err = m.leases.Read(r.Context(), entry.scope, p)
		}
	case "request":
		l, err = m.leases.Read(r.Context(), entry.scope, p)
		if err != nil {
			break
		}
		err = m.leases.RequestHandoff(r.Context(), entry.scope, p)
		if err != nil {
			break
		}
		id, e := craftNativeOpaque()
		if e != nil {
			err = e
			break
		}
		request := craftNativeRequest{id: id, target: entry.id, generation: l.Generation}
		m.mu.Lock()
		for key, old := range m.requests {
			if old.target == entry.id {
				delete(m.requests, key)
			}
		}
		m.requests[id] = request
		m.mu.Unlock()
		writeJSON(w, 201, map[string]any{"request_id": id, "generation": strconv.FormatInt(l.Generation, 10)})
		return
	case "handoff":
		m.mu.Lock()
		request, ok := m.requests[body.RequestID]
		target := m.sessionsByID[request.target]
		m.mu.Unlock()
		if !ok || target == nil || target.scope != entry.scope || target.application != entry.application || request.generation != l.Generation {
			err = errCraftNativeAccess
			break
		}
		l, err = m.leases.Handoff(r.Context(), l, craftNativePrincipal(target))
		if err == nil {
			m.mu.Lock()
			for key, old := range m.requests {
				if registered := m.sessionsByID[old.target]; registered != nil && registered.scope == entry.scope && old.generation == request.generation {
					delete(m.requests, key)
				}
			}
			m.mu.Unlock()
		}
	default:
		err = errCraftNativeAccess
	}
	if err != nil {
		craftNativeHTTPError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, m.leaseView(r, entry, l))
}
func (m *craftNativeHTTP) acquire(w http.ResponseWriter, r *http.Request) {
	m.mutateLease(w, r, "acquire")
}
func (m *craftNativeHTTP) renew(w http.ResponseWriter, r *http.Request) { m.mutateLease(w, r, "renew") }
func (m *craftNativeHTTP) release(w http.ResponseWriter, r *http.Request) {
	m.mutateLease(w, r, "release")
}
func (m *craftNativeHTTP) requestHandoff(w http.ResponseWriter, r *http.Request) {
	m.mutateLease(w, r, "request")
}
func (m *craftNativeHTTP) handoff(w http.ResponseWriter, r *http.Request) {
	m.mutateLease(w, r, "handoff")
}
