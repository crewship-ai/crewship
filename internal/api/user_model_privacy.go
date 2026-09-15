package api

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/crewship-ai/crewship/internal/consolidate"
	"github.com/crewship-ai/crewship/internal/memory"
)

// #1669 — self-service read and correction for the operator model.
//
// Three routes, all on the caller's OWN model, alongside the peer-card
// routes in user_peer_privacy.go:
//
//	GET    /api/v1/users/me/user-model             — what is stored about me
//	DELETE /api/v1/users/me/user-model             — forget all of it
//	DELETE /api/v1/users/me/user-model/facts/{key} — forget ONE field
//
// # Why this ships with the extractor rather than after it
//
// A memory about someone that they cannot see is a poor default whatever
// the technology, and until now the only escape the schema offered was
// user_peer_consent — "turn it all off", never "this one is wrong, drop
// it". An extractor that starts writing real facts into an unreadable
// file makes that gap load-bearing rather than theoretical.
//
// The per-field delete is possible only because the on-disk format is
// one "- key: value" bullet per line and the merge is keyed on it
// (consolidate.MergeUserModel). Forgetting one field is a line removal;
// it needs no parser and no schema.
//
// # Where each entry came from
//
// The read also says WHERE each fact came from (#1693): the verbatim span
// of the person's own words the extraction verified it against, the
// message it was found in, the source type and when it was recorded. That
// is not in the file — the file has a 1.5 KB cap read into every prompt,
// and carrying provenance inline would halve how much can be known — but
// in user_model_provenance beside it (consolidate.LoadUserModelProvenance).
// Every delete below purges that table with the file: the per-field forget
// takes the field's rows, the whole-model delete and the opt-out purge take
// them all, and the Art. 17 cascade (admin_gdpr_erase_identity.go) has its
// own step. Four paths, all four — or this becomes the fifth place a SAR
// erase misses.
//
// # A hole this closes on the way past
//
// PutConsent promised that opting out purged everything immediately and
// purged peer cards only, so the operator model survived to the next
// daily sweep. purgeUserModel below is now called from that path too.

// userModelPathsFor resolves the crew-shared memory directory holding one
// operator model.
//
// It delegates to the writer's own function rather than reimplementing
// it. Two functions that merely AGREE today about where a file lives is
// how a purge silently misses one — and a purge that misses is the exact
// class of bug this file exists to close.
func userModelPathsFor(basePath, crewID string) memory.UserModelPaths {
	return consolidate.UserModelPathsFor(basePath, crewID)
}

// userModelRow is one index row plus where its file lives.
type userModelRow struct {
	id       string
	crewID   string
	userSlug string
	bytes    int
	created  string
	updated  string
}

// loadMyUserModelRow reads the caller's index row for this workspace.
// Returns (row, false, nil) when there is none — a workspace that has
// never extracted a model for this person is the common case, not an
// error.
func (h *UserPeerPrivacyHandler) loadMyUserModelRow(r *http.Request, userID, wsID string) (userModelRow, bool, error) {
	var (
		row    userModelRow
		crewID sql.NullString
	)
	err := h.db.QueryRowContext(r.Context(), `
		SELECT id, crew_id, user_slug, bytes, created_at, updated_at
		FROM user_models
		WHERE user_id = ? AND workspace_id = ?
	`, userID, wsID).Scan(&row.id, &crewID, &row.userSlug, &row.bytes, &row.created, &row.updated)
	if errors.Is(err, sql.ErrNoRows) {
		return userModelRow{}, false, nil
	}
	if err != nil {
		return userModelRow{}, false, err
	}
	row.crewID = crewID.String
	return row, true, nil
}

// userModelFact is one "- key: value" bullet, exposed as a field so the
// caller can name the one they want forgotten — and, when the store beside
// the file has a row for it, where it came from.
type userModelFact struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	// Provenance is absent (not null, not empty) for a fact written before
	// the store existed; the client shows the fact without inventing an
	// origin for it.
	Provenance *userModelProvenance `json:"provenance,omitempty"`
}

// userModelProvenance is the newest evidence row for one fact (#1693).
type userModelProvenance struct {
	// Quote is the verbatim span of the person's own words the fact was
	// verified against.
	Quote string `json:"quote"`
	// MessageID is the conversation_messages.id the quote was found in.
	// Empty when the turn had none.
	MessageID string `json:"message_id"`
	// SourceType is usermodel.SourceType — "stated" under every shipped
	// profile.
	SourceType string `json:"source_type"`
	// At is when this evidence was recorded, fixed-width ISO millis UTC.
	At string `json:"at"`
}

// attachUserModelProvenance decorates each fact with the newest evidence
// row for its key. Facts without a row are left as they are.
func attachUserModelProvenance(facts []userModelFact, rows map[string]consolidate.UserModelProvenance) {
	for i := range facts {
		p, ok := rows[facts[i].Key]
		if !ok {
			continue
		}
		facts[i].Provenance = &userModelProvenance{
			Quote:      p.Quote,
			MessageID:  p.MessageID,
			SourceType: p.SourceType,
			At:         p.RecordedAt,
		}
	}
}

// parseUserModelFacts splits a model body into its bullets, preserving
// order. Non-bullet lines (the optional trailing narrative) are not
// facts and are not returned; DeleteMyUserModel is the escape for those.
func parseUserModelFacts(body string) []userModelFact {
	out := []userModelFact{}
	for _, raw := range strings.Split(body, "\n") {
		key, value, ok := splitUserModelBullet(raw)
		if !ok {
			continue
		}
		out = append(out, userModelFact{Key: key, Value: value})
	}
	return out
}

// splitUserModelBullet parses one line as "- key: value". Keys are
// lower-cased and trimmed, matching consolidate.splitFields so a key
// named here is the same key the merge would overwrite.
func splitUserModelBullet(raw string) (key, value string, ok bool) {
	line := strings.TrimSpace(raw)
	if !strings.HasPrefix(line, "-") {
		return "", "", false
	}
	bullet := strings.TrimSpace(strings.TrimPrefix(line, "-"))
	idx := strings.Index(bullet, ":")
	if idx <= 0 {
		return "", "", false
	}
	key = strings.ToLower(strings.TrimSpace(bullet[:idx]))
	if key == "" {
		return "", "", false
	}
	return key, strings.TrimSpace(bullet[idx+1:]), true
}

// GetMyUserModel returns the operator model stored about the caller in
// this workspace, both as the raw file and split per field.
//
// GET /api/v1/users/me/user-model
func (h *UserPeerPrivacyHandler) GetMyUserModel(w http.ResponseWriter, r *http.Request) {
	userID, wsID, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	row, found, err := h.loadMyUserModelRow(r, userID, wsID)
	if err != nil {
		h.logger.Error("user model read failed", "user_id", userID, "workspace_id", wsID, "error", err)
		replyError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	payload := map[string]any{
		"user_id":      userID,
		"workspace_id": wsID,
		"exists":       found,
		"facts":        []userModelFact{},
	}
	if found {
		payload["user_slug"] = row.userSlug
		payload["bytes"] = row.bytes
		payload["created_at"] = row.created
		payload["updated_at"] = row.updated
		if h.outputBasePath == "" {
			replyError(w, http.StatusServiceUnavailable, "personal memory storage unavailable")
			return
		}
		if h.outputBasePath != "" {
			body, err := memory.LoadUserModelBySlug(userModelPathsFor(h.outputBasePath, row.crewID), row.userSlug)
			if err != nil || (body == "" && row.bytes > 0) {
				replyError(w, http.StatusServiceUnavailable, "saved preferences could not be read")
				return
			}
			facts := parseUserModelFacts(body)
			// Where each fact came from (#1693). A failed read here is a
			// failed read of the person's own record, not a degraded one:
			// answering with facts and no origins would look exactly like
			// a model written before provenance existed.
			prov, err := consolidate.LoadUserModelProvenance(r.Context(), h.db, wsID, row.userSlug)
			if err != nil {
				h.logger.Error("user model provenance read failed", "user_id", userID, "workspace_id", wsID, "error", err)
				replyError(w, http.StatusInternalServerError, "internal server error")
				return
			}
			attachUserModelProvenance(facts, prov)
			payload["content"] = body
			payload["facts"] = facts
		}
		// Auditing the read keyed on the actor — who here IS the data
		// subject — keeps "everything logged about this user" one query,
		// the same way GetMyCards does it.
		insertPeerAudit(r.Context(), h.db, h.logger, peerAuditInsert{
			WorkspaceID:  wsID,
			ActorUserID:  userID,
			ActorKind:    "user",
			Action:       "read",
			TargetUserID: userID,
			Metadata:     `{"kind":"user_model"}`,
		})
	}
	writeJSON(w, http.StatusOK, payload)
}

// DeleteMyUserModel forgets the whole model. It does NOT opt the caller
// out — a person may want the current picture dropped while still
// allowing a new one to accrete, the same split DeleteMyCards makes.
//
// DELETE /api/v1/users/me/user-model
func (h *UserPeerPrivacyHandler) DeleteMyUserModel(w http.ResponseWriter, r *http.Request) {
	userID, wsID, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	purged, err := h.purgeUserModel(r, userID, wsID, "self_service_delete")
	if err != nil {
		h.logger.Error("user model delete failed", "user_id", userID, "workspace_id", wsID, "error", err)
		replyError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user_id": userID,
		"purged":  purged,
	})
}

// ForgetUserModelFact removes ONE field and leaves the rest standing.
//
// This is the answer to "the agent saved a wrong assumption about me"
// that does not require throwing away everything correct alongside it.
// A key that is not stored is a 404 rather than a silent success: the
// caller asked for a specific thing to stop being true of their record,
// and reporting that it worked when it was never there would be a lie
// about their own data.
//
// DELETE /api/v1/users/me/user-model/facts/{key}
func (h *UserPeerPrivacyHandler) ForgetUserModelFact(w http.ResponseWriter, r *http.Request) {
	userID, wsID, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	key := strings.ToLower(strings.TrimSpace(r.PathValue("key")))
	if key == "" {
		replyError(w, http.StatusBadRequest, "field key required")
		return
	}
	if h.outputBasePath == "" {
		replyError(w, http.StatusServiceUnavailable, "memory storage is not configured on this server")
		return
	}
	row, found, err := h.loadMyUserModelRow(r, userID, wsID)
	if err != nil {
		h.logger.Error("user model read failed", "user_id", userID, "workspace_id", wsID, "error", err)
		replyError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if !found {
		replyError(w, http.StatusNotFound, "no operator model is stored about you in this workspace")
		return
	}

	paths := userModelPathsFor(h.outputBasePath, row.crewID)
	body, err := memory.LoadUserModelBySlug(paths, row.userSlug)
	if err != nil {
		h.logger.Error("user model file read failed", "user_id", userID, "error", err)
		replyError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	kept, removed := dropUserModelField(body, key)
	if !removed {
		replyError(w, http.StatusNotFound, fmt.Sprintf("no field %q is stored about you", key))
		return
	}

	// Losing the last field means there is no model left. Writing an
	// empty body is rejected by memory.WriteUserModel by design (empty
	// content is a delete, and it wants the caller to say so), so take
	// the purge path rather than leaving a zero-byte file behind.
	if strings.TrimSpace(kept) == "" {
		if _, err := h.purgeUserModel(r, userID, wsID, "self_service_forget_last_field"); err != nil {
			h.logger.Error("user model purge failed", "user_id", userID, "error", err)
			replyError(w, http.StatusInternalServerError, "internal server error")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"user_id": userID, "forgot": key, "remaining": []userModelFact{}, "exists": false,
		})
		return
	}

	// Delete path 3 of 4 (#1693): the field's evidence goes with the
	// field — every row for the key, not only the newest. Forgetting a
	// field is the person saying the record about it is wrong, and how it
	// got there is part of that record.
	//
	// BEFORE the file is rewritten, so a failure here is a clean 500 with
	// nothing changed, and a failure of the write after it leaves a fact
	// with no origin rather than an origin with no fact. Once the file has
	// lost the field a retry answers 404 and never reaches this line, so
	// the order is what makes the purge retryable at all.
	if _, err := consolidate.PurgeUserModelProvenanceKey(r.Context(), h.db, wsID, row.userSlug, key); err != nil {
		h.logger.Error("user model provenance purge failed", "user_id", userID, "workspace_id", wsID, "field", key, "error", err)
		replyError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if err := memory.WriteUserModel(paths, userID, wsID, kept); err != nil {
		h.logger.Error("user model write failed", "user_id", userID, "error", err)
		replyError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	// The index carries the byte count the sweep reconciles against; a
	// file that shrank behind an unchanged row is a row that lies.
	if _, err := h.db.ExecContext(r.Context(), `
		UPDATE user_models SET bytes = ?, updated_at = ? WHERE id = ?
	`, len(kept), isoMillisNow(), row.id); err != nil {
		h.logger.Warn("user model index update failed after forget",
			"user_id", userID, "error", err)
	}
	insertPeerAudit(r.Context(), h.db, h.logger, peerAuditInsert{
		WorkspaceID:  wsID,
		ActorUserID:  userID,
		ActorKind:    "user",
		Action:       "delete",
		TargetUserID: userID,
		Metadata:     fmt.Sprintf(`{"kind":"user_model_field","field":%q}`, key),
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"user_id":   userID,
		"forgot":    key,
		"exists":    true,
		"remaining": parseUserModelFacts(kept),
	})
}

// dropUserModelField removes every bullet whose key matches, leaving all
// other lines — including any trailing narrative — byte-identical.
func dropUserModelField(body, key string) (string, bool) {
	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	kept := make([]string, 0, len(lines))
	removed := false
	for _, raw := range lines {
		if k, _, ok := splitUserModelBullet(raw); ok && k == key {
			removed = true
			continue
		}
		kept = append(kept, raw)
	}
	return strings.TrimRight(strings.Join(kept, "\n"), "\n"), removed
}

// purgeUserModel deletes the caller's model from disk AND drops the index
// row, returning how many models were removed (0 or 1 — the index is
// UNIQUE on (workspace_id, user_slug)).
//
// The on-disk delete is best-effort in the sense that it tries every crew
// directory rather than stopping at the first miss (DeleteUserModelEverywhere
// itself is best-effort across directories), but a FAILURE from it is not
// swallowed here any more. It used to be: the DB row went unconditionally
// and the file error was only logged, so a caller who could not delete one
// of N crew copies still got back "deleted" — the exact erasure-reports-
// success bug the exhaustive, multi-directory delete was supposed to fix,
// one level up. Two things follow from treating it as a real failure:
//
//  1. The error propagates to the caller, which already turns a non-nil
//     error into a 500 (DeleteMyUserModel) rather than 200.
//  2. The index row is NOT deleted on that failure. Deleting it anyway
//     would make the orphan permanently unfindable: this function is
//     reached by looking the row up first (loadMyUserModelRow), so once
//     the row is gone nothing ever asks "does this slug still have files
//     anywhere" again — not a retry of this same endpoint, not a future
//     sweep. Leaving the row in place means a second DELETE call retries
//     the file cleanup (DeleteUserModelEverywhere is idempotent: crews it
//     already cleared are silent no-ops) instead of silently giving up.
//
// The provenance store (#1693) is purged here too — delete paths 1 and 2
// of 4, the opt-out purge and the self-service delete, both arrive here.
// It goes AFTER the index row for the same reason the row goes after the
// files: a failed file delete keeps everything findable for a retry. And
// it goes even when there is no index row, keyed on the slug the row
// would have carried: evidence with no model is still evidence about a
// person who asked for it gone.
func (h *UserPeerPrivacyHandler) purgeUserModel(r *http.Request, userID, wsID, reason string) (int, error) {
	row, found, err := h.loadMyUserModelRow(r, userID, wsID)
	if err != nil {
		return 0, err
	}
	if !found {
		if _, err := consolidate.PurgeUserModelProvenance(r.Context(), h.db, wsID, memory.UserSlug(userID, wsID)); err != nil {
			return 0, err
		}
		return 0, nil
	}
	if h.outputBasePath != "" {
		// Deletes from EVERY crew directory on disk, not just row.crewID.
		// A prior sweep's crew reassignment moves the row's crew_id
		// forward without removing the file it left behind in the crew
		// the operator has since left, and a purge that reconstructed a
		// single "expected" path from row.crewID would miss exactly that
		// orphan (#1701).
		if _, err := memory.DeleteUserModelEverywhere(h.outputBasePath, row.userSlug); err != nil {
			h.logger.Warn("user model file delete failed",
				"user_id", userID, "reason", reason, "err", err)
			return 0, fmt.Errorf("delete user model files: %w", err)
		}
	}
	if _, err := h.db.ExecContext(r.Context(),
		`DELETE FROM user_models WHERE id = ?`, row.id); err != nil {
		return 0, err
	}
	if _, err := consolidate.PurgeUserModelProvenance(r.Context(), h.db, wsID, row.userSlug); err != nil {
		return 0, err
	}
	insertPeerAudit(r.Context(), h.db, h.logger, peerAuditInsert{
		WorkspaceID:  wsID,
		ActorUserID:  userID,
		ActorKind:    "user",
		Action:       "delete",
		TargetUserID: userID,
		Metadata:     fmt.Sprintf(`{"kind":"user_model","reason":%q}`, reason),
	})
	return 1, nil
}
