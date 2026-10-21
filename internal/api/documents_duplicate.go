package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/documents"
	"github.com/crewship-ai/crewship/internal/pages"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

type duplicateDocumentRequest struct {
	MutationID     string `json:"mutation_id"`
	ExpectedSource struct {
		ID           string `json:"id"`
		PagesVersion *int64 `json:"pages_version"`
	} `json:"expected_source"`
}
type duplicateDocumentPage struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
}
type duplicateDocumentResponse struct {
	Page duplicateDocumentPage `json:"page"`
}
type duplicateDocumentHead struct {
	Schema          int
	Rev, Generation int64
}

// duplicateDocumentSource is called behind the writer fence both before pure
// preparation and before commit. OWNER custody never substitutes for ownership.
func (h *PageHandler) duplicateDocumentSource(r *http.Request, tx *sql.Tx, id string) (*pageRecord, duplicateDocumentHead, int, error) {
	var head duplicateDocumentHead
	ctx := context.WithValue(r.Context(), pageAuthReaderKey{}, tx)
	r = r.WithContext(ctx)
	for _, scope := range []string{"pages:read", "pages:write"} {
		if err := validateContentHumanCredential(r, tx, scope); err != nil {
			status := 401
			if errors.Is(err, errContentCredentialScope) {
				status = 403
			}
			return nil, head, status, errors.New("current pages:read and pages:write human credential required")
		}
	}
	u := UserFromContext(ctx)
	if _, err := h.liveContentHumanRole(ctx, WorkspaceIDFromContext(ctx), u.ID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, head, 404, errDocumentAccess
		}
		return nil, head, 500, err
	}
	rec, err := h.loadPage(ctx, WorkspaceIDFromContext(ctx), "@"+id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, head, 404, errDocumentAccess
	}
	if err != nil {
		return nil, head, 500, err
	}
	if rec.OwnerUserID != u.ID || rec.OwnerCrewID != "" || rec.FolderID != "" || !rec.PrivateAudience {
		return nil, head, 404, errDocumentAccess
	}
	if !rec.HasDocument || rec.HasProject || rec.HasApplication {
		return nil, head, 422, errors.New("only personal native Documents can be duplicated")
	}
	err = tx.QueryRowContext(ctx, `SELECT schema_version,head_rev,generation FROM documents WHERE workspace_id=? AND page_id=?`, WorkspaceIDFromContext(ctx), id).Scan(&head.Schema, &head.Rev, &head.Generation)
	if err != nil {
		return nil, head, 500, err
	}
	if head.Schema < 1 || head.Schema > 3 {
		return nil, head, 422, errors.New("unsupported Document schema; source preserved")
	}
	return rec, head, 200, nil
}

func duplicateDocumentFailure(w http.ResponseWriter, status int, err error) {
	message := "Document duplication unavailable; retry the original mutation_id"
	if status < 500 {
		message = err.Error()
	}
	replyError(w, status, message)
}

// A receipt binds the original request, not the current source head. Replay
// still rechecks current source and destination authority, but never recopies.
func (h *PageHandler) duplicateDocumentReplay(r *http.Request, tx *sql.Tx, packet duplicateDocumentRequest, digest string) (duplicateDocumentResponse, bool, int, error) {
	var result duplicateDocumentResponse
	var stored string
	err := tx.QueryRowContext(r.Context(), `SELECT request_digest,result_page_id,result_slug FROM document_copy_receipts WHERE workspace_id=? AND user_id=? AND mutation_id=?`, WorkspaceIDFromContext(r.Context()), UserFromContext(r.Context()).ID, packet.MutationID).Scan(&stored, &result.Page.ID, &result.Page.Slug)
	if errors.Is(err, sql.ErrNoRows) {
		return result, false, 0, nil
	}
	if err != nil {
		return result, true, 500, err
	}
	if stored != digest {
		return result, true, 409, errors.New("mutation_id was used with a different request")
	}
	if _, _, status, err := h.duplicateDocumentSource(r, tx, result.Page.ID); err != nil {
		return result, true, status, err
	}
	return result, true, 200, nil
}

// The existing draft parser deliberately forbids null. This closed two-field
// packet alone permits pages_version:null, while still rejecting duplicate keys.
func documentCopyExpectedObject(raw []byte) (map[string]json.RawMessage, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errors.New("source object required")
	}
	fields := map[string]json.RawMessage{}
	for d.More() {
		token, err = d.Token()
		if err != nil {
			return nil, err
		}
		key, ok := token.(string)
		if !ok || fields[key] != nil || (key != "id" && key != "pages_version") {
			return nil, errors.New("closed source fields required")
		}
		var value json.RawMessage
		if err = d.Decode(&value); err != nil {
			return nil, err
		}
		fields[key] = value
	}
	if _, err = d.Token(); err != nil {
		return nil, err
	}
	if d.Decode(new(any)) != io.EOF {
		return nil, errors.New("one source object required")
	}
	return fields, nil
}

func (h *PageHandler) DuplicatePersonalDocument(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	u := UserFromContext(r.Context())
	agent, _ := pageGrantCallerIsAgent(r)
	if u == nil || agent || projectAgentFrom(r.Context()) != nil || r.Context().Value(artifactRunKey{}) != nil {
		replyError(w, 401, "human authentication required")
		return
	}
	if validateDocumentDraftQuery(r, WorkspaceIDFromContext(r.Context())) != nil {
		replyError(w, 400, "only the authenticated workspace query is supported")
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	fields, shapeErr := documentDraftObject(raw)
	var packet duplicateDocumentRequest
	if err != nil || shapeErr != nil || len(fields) != 2 || fields["mutation_id"] == nil || fields["expected_source"] == nil || json.Unmarshal(raw, &packet) != nil || !documentCheckpointID.MatchString(packet.MutationID) || packet.ExpectedSource.ID != r.PathValue("id") || !documentCheckpointID.MatchString(packet.ExpectedSource.ID) {
		replyError(w, 400, "closed immutable source and mutation_id required")
		return
	}
	expected, err := documentCopyExpectedObject(fields["expected_source"])
	if err != nil || len(expected) != 2 || expected["id"] == nil || expected["pages_version"] == nil || len(r.Header.Values("Idempotency-Key")) != 1 || r.Header.Get("Idempotency-Key") != packet.MutationID {
		replyError(w, 400, "matching Idempotency-Key and closed expected_source required")
		return
	}
	if v := packet.ExpectedSource.PagesVersion; v != nil && (*v < 0 || *v >= 9007199254740991) {
		replyError(w, 400, "bounded Page membership version required")
		return
	}
	canonical, _ := json.Marshal(packet)
	sum := sha256.Sum256(canonical)
	digest := hex.EncodeToString(sum[:])
	begin := func() (*sql.Tx, error) {
		tx, err := h.db.BeginTx(r.Context(), nil)
		if err != nil {
			return nil, err
		}
		if _, err = tx.ExecContext(r.Context(), `UPDATE document_writer_fence SET seq=seq WHERE id=1`); err != nil {
			tx.Rollback()
			return nil, err
		}
		return tx, nil
	}
	preflight, err := begin()
	if err != nil {
		duplicateDocumentFailure(w, 500, err)
		return
	}
	defer preflight.Rollback()
	source, head, status, err := h.duplicateDocumentSource(r, preflight, packet.ExpectedSource.ID)
	if err != nil {
		duplicateDocumentFailure(w, status, err)
		return
	}
	if h.documentCall == nil {
		replyError(w, 503, "Documents are disabled")
		return
	}
	if result, found, status, err := h.duplicateDocumentReplay(r, preflight, packet, digest); found {
		if err != nil {
			duplicateDocumentFailure(w, status, err)
			return
		}
		if err = preflight.Commit(); err != nil {
			duplicateDocumentFailure(w, 500, err)
			return
		}
		writeJSON(w, 200, result)
		return
	}
	if v := packet.ExpectedSource.PagesVersion; v != nil && *v != source.PagesVersion {
		replyError(w, 409, "source Page membership changed")
		return
	}
	if err = preflight.Commit(); err != nil {
		duplicateDocumentFailure(w, 500, err)
		return
	}
	// Materialize through the canonical engine reader, including persisted tail
	// updates. Pure preparation creates fresh Yjs state, without source history.
	bearer := "Bearer " + extractToken(r)
	read, status, err := h.documentCall(r.Context(), WorkspaceIDFromContext(r.Context()), bearer, source.ID, "read", map[string]any{})
	if err != nil || status >= 400 {
		if err == nil && status < 500 {
			replyError(w, status, "source Document read refused")
		} else {
			duplicateDocumentFailure(w, 503, err)
		}
		return
	}
	var materialized struct {
		Schema     int             `json:"schema"`
		Rev        int64           `json:"rev"`
		Generation int64           `json:"generation"`
		Content    json.RawMessage `json:"content"`
	}
	if json.Unmarshal(read, &materialized) != nil || len(materialized.Content) == 0 {
		duplicateDocumentFailure(w, 502, nil)
		return
	}
	if len(materialized.Content) > 1<<20 {
		replyError(w, 413, "canonical Document copy exceeds 1 MiB; source preserved")
		return
	}
	if materialized.Schema != head.Schema || materialized.Rev != head.Rev || materialized.Generation != head.Generation {
		replyError(w, 409, "source Document changed")
		return
	}
	schema := head.Schema
	if schema == 1 {
		schema = 2
	}
	id := generateCUID()
	slug := "copy-" + id
	preparedRaw, status, err := h.documentCall(r.Context(), WorkspaceIDFromContext(r.Context()), bearer, id, "prepare-copy", map[string]any{"schema": schema, "content": materialized.Content})
	if err != nil || status >= 400 {
		if err == nil && (status == 400 || status == 413 || status == 422) {
			replyError(w, 422, "canonical Document copy refused; source preserved")
			return
		}
		duplicateDocumentFailure(w, 503, err)
		return
	}
	var prepared struct {
		Schema   int             `json:"schema"`
		Snapshot []byte          `json:"snapshot"`
		Blocks   json.RawMessage `json:"blocks"`
	}
	var blocks []struct {
		ID   string `json:"id"`
		Hash string `json:"hash"`
	}
	if json.Unmarshal(preparedRaw, &prepared) != nil || prepared.Schema != schema || len(prepared.Snapshot) == 0 || json.Unmarshal(prepared.Blocks, &blocks) != nil || blocks == nil || len(blocks) > 10000 {
		duplicateDocumentFailure(w, 502, nil)
		return
	}
	if len(prepared.Snapshot) > 1<<20 {
		replyError(w, 413, "prepared Document copy exceeds 1 MiB; source preserved")
		return
	}
	tx, err := begin()
	if err != nil {
		duplicateDocumentFailure(w, 500, err)
		return
	}
	defer tx.Rollback()
	current, currentHead, status, err := h.duplicateDocumentSource(r, tx, source.ID)
	if err != nil {
		duplicateDocumentFailure(w, status, err)
		return
	}
	if result, found, status, err := h.duplicateDocumentReplay(r, tx, packet, digest); found {
		if err != nil {
			duplicateDocumentFailure(w, status, err)
			return
		}
		if err = tx.Commit(); err != nil {
			duplicateDocumentFailure(w, 500, err)
			return
		}
		writeJSON(w, 200, result)
		return
	}
	if currentHead != head || current.PagesVersion != source.PagesVersion {
		replyError(w, 409, "source Document changed before copy commit")
		return
	}
	var count int
	if err = tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM pages WHERE workspace_id=?`, WorkspaceIDFromContext(r.Context())).Scan(&count); err != nil {
		duplicateDocumentFailure(w, 500, err)
		return
	}
	if count >= pages.MaxPagesPerWorkspace {
		replyError(w, 409, "workspace Page limit reached")
		return
	}
	stamp := tsformat.Format(time.Now())
	spec := `{"apiVersion":"crewship/v1","kind":"Page","metadata":{"slug":"` + slug + `","name":"Document copy"},"spec":{"panels":[]}}`
	if _, err = tx.ExecContext(r.Context(), `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,private_audience,spec_json,created_at,updated_at) VALUES(?,?,?,'Document copy',?,1,?,?,?)`, id, WorkspaceIDFromContext(r.Context()), slug, u.ID, spec, stamp, stamp); err != nil {
		duplicateDocumentFailure(w, 500, err)
		return
	}
	if err = insertPageVersion(r.Context(), tx, id, 1, spec, u.ID, stamp); err != nil {
		duplicateDocumentFailure(w, 500, err)
		return
	}
	if _, err = documents.New(h.db, h.documentStoreOptions).InitRequestTx(r.Context(), tx, documents.Key{WorkspaceID: WorkspaceIDFromContext(r.Context()), PageID: id}, packet.MutationID, digest, schema, prepared.Snapshot, prepared.Blocks, "user:"+u.ID); err != nil {
		documentAuthorityError(w, err)
		return
	}
	if _, err = tx.ExecContext(r.Context(), `INSERT INTO document_copy_receipts(workspace_id,user_id,mutation_id,request_digest,source_page_id,result_page_id,result_slug,created_at) VALUES(?,?,?,?,?,?,?,?)`, WorkspaceIDFromContext(r.Context()), u.ID, packet.MutationID, digest, source.ID, id, slug, stamp); err != nil {
		duplicateDocumentFailure(w, 500, err)
		return
	}
	if err = tx.Commit(); err != nil {
		duplicateDocumentFailure(w, 500, err)
		return
	}
	writeJSON(w, 201, duplicateDocumentResponse{Page: duplicateDocumentPage{ID: id, Slug: slug}})
}
