package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"unicode/utf8"

	"github.com/crewship-ai/crewship/internal/documents"
)

type DocumentDraftView struct {
	PageID     string                 `json:"page_id"`
	Schema     int                    `json:"schema"`
	Rev        int64                  `json:"rev"`
	Generation int64                  `json:"generation"`
	Epoch      string                 `json:"epoch"`
	Targets    []DocumentOutlineBlock `json:"targets"`
}

// Both public-to-sidecar operations infer the sole Page from the authenticated
// run's explicit consent. A caller cannot select another Page or principal.
func (h *PageHandler) documentDraftRequest(w http.ResponseWriter, r *http.Request) (*http.Request, *artifactRunAuthority, bool) {
	artifactCommentHeaders(w)
	checked, ok := h.artifactAgentRequest(w, r)
	if !ok {
		return r, nil, false
	}
	a, _ := documentDraftRun(checked.Context())
	if err := validateDocumentDraftQuery(checked, a.WorkspaceID); err != nil {
		replyError(w, 400, "only the authenticated workspace query is supported")
		return checked, nil, false
	}
	return checked, a, true
}

func validateDocumentDraftQuery(r *http.Request, workspace string) error {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return err
	}
	for name, values := range query {
		if name != "workspace_id" || len(values) != 1 || values[0] != workspace {
			return errors.New("unsupported query")
		}
	}
	return nil
}

// Release the fence before calling Node, whose callbacks acquire the SAME SQL
// writer fence and revalidate scope. No network computation holds this writer.
func (h *PageHandler) readDocumentDraftScope(r *http.Request, a *artifactRunAuthority) (documentDraftScope, error) {
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		return documentDraftScope{}, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(r.Context(), `UPDATE document_writer_fence SET seq=seq WHERE id=1`); err != nil {
		return documentDraftScope{}, err
	}
	ctx := context.WithValue(r.Context(), pageAuthReaderKey{}, tx)
	scope, err := h.documentDraftScope(ctx, a, "")
	if err != nil {
		return scope, err
	}
	return scope, tx.Commit()
}

func (h *PageHandler) GetDocumentDraftAgent(w http.ResponseWriter, r *http.Request) {
	r, a, ok := h.documentDraftRequest(w, r)
	if !ok {
		return
	}
	scope, err := h.readDocumentDraftScope(r, a)
	if err != nil {
		documentAuthorityError(w, err)
		return
	}
	if h.documentCall == nil {
		replyError(w, 503, "Docs are not enabled on this server")
		return
	}
	release, ok := acquireArtifactUpload(w, r)
	if !ok {
		return
	}
	defer release()
	raw, status, err := h.documentCall(r.Context(), a.WorkspaceID, "Bearer "+extractToken(r), scope.PageID, "read", map[string]any{})
	if err != nil {
		replyError(w, 503, "document draft view unavailable")
		return
	}
	if status >= 400 {
		forwardDocumentDraftError(w, status, raw)
		return
	}
	var remote struct {
		Schema     int                    `json:"schema"`
		Rev        int64                  `json:"rev"`
		Generation int64                  `json:"generation"`
		Epoch      string                 `json:"epoch"`
		Outline    []DocumentOutlineBlock `json:"outline"`
	}
	if json.Unmarshal(raw, &remote) != nil || remote.Schema != 3 || remote.Rev != scope.Rev || remote.Generation != scope.Generation || remote.Epoch == "" || len(remote.Epoch) > 256 {
		replyError(w, 502, "invalid document draft view")
		return
	}
	view := DocumentDraftView{PageID: scope.PageID, Schema: remote.Schema, Rev: remote.Rev, Generation: remote.Generation, Epoch: remote.Epoch, Targets: []DocumentOutlineBlock{}}
	for _, target := range remote.Outline {
		if target.Type != "paragraph" && target.Type != "heading" {
			continue
		}
		if !documentRichID.MatchString(target.ID) || !documentDraftHash(target.Hash) || !utf8.ValidString(target.Text) {
			replyError(w, 502, "invalid document draft target")
			return
		}
		view.Targets = append(view.Targets, target)
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		replyInternalError(w, h.logger, "encode document draft view", err)
		return
	}
	if len(encoded) > 64<<10 {
		replyError(w, 413, "document draft view exceeds 64 KiB; content unchanged")
		return
	}
	current, err := h.readDocumentDraftScope(r, a)
	if err != nil {
		documentAuthorityError(w, err)
		return
	}
	if current != scope {
		documentAuthorityError(w, documents.ErrConflict)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(encoded)
}

func (h *PageHandler) MutateDocumentDraftAgent(w http.ResponseWriter, r *http.Request) {
	r, a, ok := h.documentDraftRequest(w, r)
	if !ok {
		return
	}
	raw, ok := readCapped(w, r, 1<<20, "document draft request")
	if !ok {
		return
	}
	envelope, err := documentDraftObject(raw)
	if err != nil || len(envelope) != 3 || envelope["operation"] == nil || envelope["generation"] == nil || envelope["request"] == nil {
		replyError(w, 400, "closed operation, generation and request required")
		return
	}
	var operation string
	var generation int64
	if json.Unmarshal(envelope["operation"], &operation) != nil || json.Unmarshal(envelope["generation"], &generation) != nil || generation < 0 || (operation != "patch" && operation != "checkpoint") {
		replyError(w, 400, "draft patch or checkpoint and original generation required")
		return
	}
	request, err := documentDraftObject(envelope["request"])
	if err != nil || validateDocumentDraftRequest(operation, request) != nil {
		replyError(w, 400, "bounded original semantic draft request required")
		return
	}
	scope, err := h.readDocumentDraftScope(r, a)
	if err != nil {
		documentAuthorityError(w, err)
		return
	}
	var page string
	_ = json.Unmarshal(request["page_id"], &page)
	if page != scope.PageID || generation != scope.Generation {
		documentAuthorityError(w, documents.ErrConflict)
		return
	}
	if h.documentCall == nil {
		replyError(w, 503, "Docs are not enabled on this server")
		return
	}
	release, ok := acquireArtifactUpload(w, r)
	if !ok {
		return
	}
	defer release()
	result, status, err := h.documentCall(r.Context(), a.WorkspaceID, "Bearer "+extractToken(r), scope.PageID, operation, request)
	if err != nil {
		replyError(w, 503, "document draft outcome unavailable; retain the exact prepared request to check its outcome")
		return
	}
	if status >= 400 {
		forwardDocumentDraftError(w, status, result)
		return
	}
	if len(result) > 64<<10 {
		replyError(w, 503, "document draft outcome unavailable; retain the exact prepared request to check its outcome")
		return
	}
	var receipt DocumentMutationResponse
	var id, epoch string
	var revision int64
	_ = json.Unmarshal(request["id"], &id)
	_ = json.Unmarshal(request["epoch"], &epoch)
	_ = json.Unmarshal(request["ifRev"], &revision)
	if operation == "patch" {
		revision++
	}
	if json.Unmarshal(result, &receipt) != nil || receipt.ID != id || receipt.Rev != revision || receipt.Epoch != epoch {
		replyError(w, 502, "document draft confirmation unavailable; retain the exact prepared request")
		return
	}
	if operation == "checkpoint" {
		if receipt.Checkpoint != id && !(receipt.Checkpoint == "" && receipt.Duplicate && receipt.Kind == "checkpoint") {
			replyError(w, 502, "document draft checkpoint confirmation unavailable; retain the exact prepared request")
			return
		}
		receipt.Checkpoint = id
	} else if receipt.Kind != "agent" || receipt.Actor != "agent:"+a.AgentID+":"+a.RunID {
		replyError(w, 502, "document draft confirmation unavailable; retain the exact prepared request")
		return
	}
	// Only a receipt is returned. A failed later pin never rewrites this commit
	// outcome as unsaved, and confirmed exact retries keep their original revision.
	writeJSON(w, status, receipt)
}

func documentDraftObject(raw []byte) (map[string]json.RawMessage, error) {
	if !utf8.Valid(raw) {
		return nil, errors.New("invalid UTF-8")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errors.New("object required")
	}
	out := map[string]json.RawMessage{}
	for d.More() {
		token, err = d.Token()
		if err != nil {
			return nil, err
		}
		key, ok := token.(string)
		if !ok || out[key] != nil {
			return nil, errors.New("duplicate field")
		}
		var value json.RawMessage
		if err = d.Decode(&value); err != nil {
			return nil, err
		}
		if bytes.Equal(value, []byte("null")) {
			return nil, errors.New("null field")
		}
		out[key] = value
	}
	if _, err = d.Token(); err != nil {
		return nil, err
	}
	if d.Decode(new(any)) != io.EOF {
		return nil, errors.New("one object required")
	}
	return out, nil
}

func validateDocumentDraftRequest(operation string, request map[string]json.RawMessage) error {
	fields := []string{"id", "schema", "page_id", "epoch", "ifRev"}
	if operation == "patch" {
		fields = append(fields, "target", "ifHash", "text")
	}
	if len(request) != len(fields) {
		return errors.New("closed request required")
	}
	for _, name := range fields {
		if request[name] == nil {
			return errors.New("missing request field")
		}
	}
	var schema int
	var rev int64
	if json.Unmarshal(request["schema"], &schema) != nil || schema != 3 || json.Unmarshal(request["ifRev"], &rev) != nil || rev < 0 {
		return errors.New("original schema/revision required")
	}
	for _, name := range []string{"id", "page_id", "epoch"} {
		var value string
		if json.Unmarshal(request[name], &value) != nil || value == "" || len(value) > 256 {
			return errors.New("bounded request identity required")
		}
		if name == "id" && (documents.CreationIntent{SubmissionID: value}).Validate() != nil {
			return errors.New("operation ID required")
		}
	}
	if operation == "patch" {
		var target, hash, text string
		if json.Unmarshal(request["target"], &target) != nil || !documentRichID.MatchString(target) || json.Unmarshal(request["ifHash"], &hash) != nil || !documentDraftHash(hash) || json.Unmarshal(request["text"], &text) != nil || len(text) > documents.CreationMaxItemBytes {
			return errors.New("bounded semantic target required")
		}
	}
	return nil
}

func documentDraftHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, c := range value {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func forwardDocumentDraftError(w http.ResponseWriter, status int, raw []byte) {
	var response struct {
		Error string `json:"error"`
		Code  string `json:"code,omitempty"`
	}
	if len(raw) > 64<<10 || json.Unmarshal(raw, &response) != nil || response.Error == "" {
		replyError(w, 502, "document draft operation unavailable")
		return
	}
	writeJSON(w, status, response)
}
