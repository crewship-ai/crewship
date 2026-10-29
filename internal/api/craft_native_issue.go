package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"math"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/mooring"
)

func (m *craftNativeHTTP) issue(w http.ResponseWriter, r *http.Request) {
	if !m.enabled || m.pages == nil || m.clock == nil {
		replyError(w, 503, "native Craft unavailable")
		return
	}
	source, err := craftNativeHuman(r.Context())
	if err != nil {
		replyError(w, 403, "native Craft access denied")
		return
	}
	var body struct{}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&body); err != nil {
		replyError(w, 400, "invalid native session request")
		return
	}
	ws := WorkspaceIDFromContext(r.Context())
	if ws == "" {
		replyError(w, 403, "native Craft access denied")
		return
	}
	tx, err := m.pages.db.BeginTx(r.Context(), nil)
	if err != nil {
		replyError(w, 503, "native Craft unavailable")
		return
	}
	defer tx.Rollback()
	ctx := context.WithValue(r.Context(), pageAuthReaderKey{}, tx)
	rec, err := m.pages.loadPageForRead(ctx, ws, r.PathValue("slug"))
	if err != nil {
		replyError(w, 403, "native Craft access denied")
		return
	}
	scope := mooring.EditLeaseScope{WorkspaceID: ws, ArtifactID: rec.ID}
	proof, err := m.pageAuthority(ctx, tx, source, scope, false)
	if err != nil {
		replyError(w, 403, "native Craft access denied")
		return
	}
	token, err := craftNativeOpaque()
	if err != nil {
		replyError(w, 503, "native Craft unavailable")
		return
	}
	id, err := craftNativeOpaque()
	if err != nil {
		replyError(w, 503, "native Craft unavailable")
		return
	}
	boot, now, err := m.clock.Now()
	if err != nil || boot == "" || now < 0 {
		replyError(w, 503, "native Craft unavailable")
		return
	}
	cap := 24 * time.Hour
	if !proof.expiry.IsZero() && time.Until(proof.expiry) < cap {
		cap = time.Until(proof.expiry)
	}
	if cap <= 0 || now > time.Duration(math.MaxInt64)-cap {
		replyError(w, 403, "native Craft access denied")
		return
	}
	entry := &craftNativeSession{id: id, tokenHash: sha256.Sum256([]byte(token)), source: source, scope: scope, application: proof.application, workspaceEpoch: proof.workspaceEpoch, memberID: proof.memberID, memberRevision: proof.memberRevision, boot: boot, deadline: now + cap}
	if _, err = m.checkEntry(ctx, tx, entry, false); err != nil {
		replyError(w, 403, "native Craft access denied")
		return
	}
	if err = tx.Commit(); err != nil {
		replyError(w, 503, "native Craft unavailable")
		return
	}
	m.mu.Lock()
	for hash, old := range m.sessionsByHash {
		if old.boot != boot || now >= old.deadline {
			delete(m.sessionsByHash, hash)
			delete(m.sessionsByID, old.id)
		}
	}
	for key, request := range m.requests {
		if m.sessionsByID[request.target] == nil {
			delete(m.requests, key)
		}
	}
	if len(m.sessionsByID) >= 4096 {
		m.mu.Unlock()
		replyError(w, 503, "native Craft session limit reached")
		return
	}
	m.sessionsByHash[entry.tokenHash] = entry
	m.sessionsByID[id] = entry
	m.mu.Unlock()
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, map[string]any{"session_token": token, "session_id": id, "application_id": proof.application, "page_id": scope.ArtifactID})
}
