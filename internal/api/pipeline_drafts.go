package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

func (h *PipelineHandler) GetDraft(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "create") {
		return
	}
	d, err := h.store.GetDraft(r.Context(), WorkspaceIDFromContext(r.Context()), r.PathValue("slug"))
	if err != nil {
		h.logger.Error("load routine draft", "error", err)
		replyError(w, 500, "Could not load routine draft")
		return
	}
	writeJSON(w, 200, d)
}

type routineDraftListEntry struct {
	Slug      string `json:"slug"`
	Revision  int    `json:"revision"`
	UpdatedAt string `json:"updated_at"`
}

func (h *PipelineHandler) ListDrafts(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "create") {
		return
	}
	rows, err := h.db.QueryContext(r.Context(), `SELECT slug,revision,updated_at FROM pipeline_drafts WHERE workspace_id=? ORDER BY updated_at DESC LIMIT 100`, WorkspaceIDFromContext(r.Context()))
	if err != nil {
		replyError(w, 500, "Could not list routine drafts")
		return
	}
	defer rows.Close()
	out := []routineDraftListEntry{}
	for rows.Next() {
		var e routineDraftListEntry
		if err := rows.Scan(&e.Slug, &e.Revision, &e.UpdatedAt); err != nil {
			replyError(w, 500, "Could not read routine draft")
			return
		}
		out = append(out, e)
	}
	if rows.Err() != nil {
		replyError(w, 500, "Could not read routine drafts")
		return
	}
	writeJSON(w, 200, out)
}

func (h *PipelineHandler) SaveDraft(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "create") {
		return
	}
	user := UserFromContext(r.Context())
	if user == nil {
		replyError(w, 401, "auth required")
		return
	}
	var d pipeline.Draft
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxExecBodyBytes)).Decode(&d); err != nil {
		replyError(w, 400, "invalid draft body")
		return
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(d.Document, &doc); err != nil || doc == nil {
		replyError(w, 400, "draft document required")
		return
	}
	var body userSaveRequest
	if err := json.Unmarshal(d.Document, &body); err != nil || body.Slug == "" || strings.ContainsAny(body.Slug, "/?#\\") || body.Slug != d.Slug || len(body.Definition) == 0 || d.Revision < 0 {
		replyError(w, 400, "draft slug and definition required")
		return
	}
	if !draftDefinitionObject(body.Definition) {
		replyError(w, http.StatusBadRequest, "draft definition must be an object")
		return
	}
	// Saving incomplete work is not validation or an approval. Never retain a
	// short-lived validation token or legacy gate overrides in a durable draft.
	for _, key := range []string{"save_token", "skip_test_gate", "skip_governance_gate", "last_test_run_at", "last_test_run_passed"} {
		delete(doc, key)
	}
	encoded, err := encodeDraftDocument(doc)
	if err != nil {
		replyError(w, 500, "Could not prepare routine draft")
		return
	}
	d.Document = encoded
	d.WorkspaceID = WorkspaceIDFromContext(r.Context())
	d.UpdatedBy = user.ID
	saved, err := h.store.SaveDraft(r.Context(), d)
	if errors.Is(err, pipeline.ErrDraftConflict) {
		replyError(w, 409, err.Error())
		return
	}
	if err != nil {
		h.logger.Error("save routine draft", "error", err)
		replyError(w, 500, "Could not save routine draft")
		return
	}
	writeJSON(w, 200, saved)
}

func draftDefinitionObject(raw json.RawMessage) bool {
	var definition map[string]json.RawMessage
	return json.Unmarshal(raw, &definition) == nil && definition != nil
}

// Keep JSON.stringify's escaping inside the raw definition: publication proof
// is bound to its bytes. HTML escaping here would invalidate a valid test token.
func encodeDraftDocument(document map[string]json.RawMessage) ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(document); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

func (h *PipelineHandler) DeleteDraft(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "create") {
		return
	}
	var body struct {
		ID       string `json:"id"`
		Revision int    `json:"revision"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxExecBodyBytes)).Decode(&body); err != nil {
		replyError(w, 400, "invalid draft revision")
		return
	}
	err := h.store.DeleteDraft(r.Context(), WorkspaceIDFromContext(r.Context()), r.PathValue("slug"), body.ID, body.Revision)
	if errors.Is(err, pipeline.ErrDraftConflict) {
		replyError(w, 409, err.Error())
		return
	}
	if err != nil {
		replyError(w, 500, "Could not discard routine draft")
		return
	}
	w.WriteHeader(204)
}

func (h *PipelineHandler) PublishDraft(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "create") {
		return
	}
	var body struct {
		ID          string `json:"id"`
		Revision    int    `json:"revision"`
		SaveToken   string `json:"save_token"`
		ApproveRisk bool   `json:"approve_risk"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxExecBodyBytes)).Decode(&body); err != nil {
		replyError(w, 400, "invalid publication body")
		return
	}
	d, err := h.store.GetDraft(r.Context(), WorkspaceIDFromContext(r.Context()), r.PathValue("slug"))
	if err != nil {
		replyError(w, 500, "Could not load routine draft")
		return
	}
	if d.ID == "" || d.ID != body.ID || d.Revision != body.Revision {
		replyError(w, 409, pipeline.ErrDraftConflict.Error())
		return
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(d.Document, &document); err != nil {
		replyError(w, 500, "Draft document is unreadable")
		return
	}
	document["save_token"], _ = json.Marshal(body.SaveToken)
	encoded, err := encodeDraftDocument(document)
	if err != nil {
		replyError(w, 500, "Could not prepare publication")
		return
	}
	request := r.Clone(r.Context())
	request.Body = io.NopCloser(bytes.NewReader(encoded))
	request.ContentLength = int64(len(encoded))
	h.saveWithPublication(w, request, &pipeline.DraftPublication{ID: d.ID, Revision: d.Revision, Document: string(d.Document), ApproveRisk: body.ApproveRisk})
}
