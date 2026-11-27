package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/craftapps"
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
	var body struct {
		ChatID string `json:"chat_id"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	decoder.DisallowUnknownFields()
	var trailing any
	if err = decoder.Decode(&body); err != nil || decoder.Decode(&trailing) != io.EOF ||
		body.ChatID != "" && !craftChatID.MatchString(body.ChatID) {
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
	generations, err := captureCraftNativeGenerations(ctx, tx, scope, source.UserID)
	if err != nil {
		replyError(w, 503, "native Craft unavailable")
		return
	}
	entry := &craftNativeSession{id: id, tokenHash: sha256.Sum256([]byte(token)), source: source, scope: scope, application: proof.application, memberID: proof.memberID, memberRevision: proof.memberRevision, generations: generations, boot: boot, deadline: now + cap}
	if _, err = m.checkEntry(ctx, tx, entry, false); err != nil {
		replyError(w, 403, "native Craft access denied")
		return
	}
	canWrite := false
	if body.ChatID != "" && source.Kind == AuthKindSession && proof.privateRead && proof.application == craftapps.Sheet && m.agents != nil {
		if app, ok := m.agents.documents.application(proof.application); ok && app.Store.Format == mooring.NativeFormatWorkbook {
			linked, linkErr := craftRealtimeChatLinked(ctx, tx, scope, body.ChatID, source.UserID)
			if linkErr != nil {
				replyError(w, 503, "native Craft unavailable")
				return
			}
			if linked && now <= time.Duration(math.MaxInt64)-24*time.Hour {
				entry.chatID = body.ChatID
				entry.realtimeDeadline = now + 24*time.Hour
				_, writeErr := m.checkEntry(ctx, tx, entry, true)
				canWrite = !app.ChatSheetPreviewOnly && writeErr == nil && craftNativePersonalSpace(proof.record, source.UserID) && m.apps.allows(ctx, entry.application, craftapps.OpStartEdit)
				entry.realtimeWrite = canWrite
			}
		}
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
	out := craftNativeSessionResponse{
		SessionToken: token, SessionID: id, ApplicationID: proof.application, PageID: scope.ArtifactID,
		RealtimeSheet: entry.chatID != "", CanWrite: canWrite,
	}
	if canWrite {
		out.Generation = craftRealtimeGeneration
	}
	writeJSON(w, http.StatusCreated, out)
}

// An admission has one generation. The opaque session ID and the original
// authority epochs fence it across revocation, reauthorization and reissue.
const craftRealtimeGeneration = "1"

type craftNativeSessionResponse struct {
	SessionToken  string `json:"session_token"`
	SessionID     string `json:"session_id"`
	ApplicationID string `json:"application_id"`
	PageID        string `json:"page_id"`
	RealtimeSheet bool   `json:"realtime_sheet"`
	CanWrite      bool   `json:"can_write"`
	Generation    string `json:"generation,omitempty"`
}

func craftRealtimeChatLinked(ctx context.Context, tx *sql.Tx, scope mooring.EditLeaseScope, chat, user string) (bool, error) {
	var linked bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM chats c
		WHERE c.id=?1 AND c.workspace_id=?2 AND c.created_by=?3 AND c.visibility='private'
		AND (
			EXISTS(SELECT 1 FROM artifact_chat_refs r WHERE r.chat_id=c.id AND r.workspace_id=c.workspace_id AND r.human_user_id=c.created_by AND r.page_id=?4)
			OR EXISTS(SELECT 1 FROM artifact_message_refs r WHERE r.chat_id=c.id AND r.workspace_id=c.workspace_id AND r.human_user_id=c.created_by AND r.page_id=?4)
		)
	)`, chat, scope.WorkspaceID, user, scope.ArtifactID).Scan(&linked)
	return linked, err
}
