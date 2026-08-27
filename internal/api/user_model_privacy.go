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
// # The one thing this cannot do yet
//
// It shows the person WHAT is stored, not WHERE it came from. The
// extraction verifies each fact against a verbatim span of the person's
// own words, but that span is not persisted — the file has a 1.5 KB cap
// read into every prompt, and carrying provenance inline would halve how
// much can be known. Per-fact provenance ("you said this on the 12th,
// here is the message") needs a store beside the file. Filed rather than
// bolted on.
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
// caller can name the one they want forgotten.
type userModelFact struct {
	Key   string `json:"key"`
	Value string `json:"value"`
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
		if h.outputBasePath != "" {
			body, _ := memory.LoadUserModelBySlug(userModelPathsFor(h.outputBasePath, row.crewID), row.userSlug)
			payload["content"] = body
			payload["facts"] = parseUserModelFacts(body)
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
func (h *UserPeerPrivacyHandler) purgeUserModel(r *http.Request, userID, wsID, reason string) (int, error) {
	row, found, err := h.loadMyUserModelRow(r, userID, wsID)
	if err != nil {
		return 0, err
	}
	if !found {
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
