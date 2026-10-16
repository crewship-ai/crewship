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
	"unicode/utf8"

	"github.com/crewship-ai/crewship/internal/documents"
	"github.com/crewship-ai/crewship/internal/pages"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

type documentCreateRequest = documents.CreationInput

type documentCreationResult struct {
	PageID      string `json:"page_id"`
	WorkspaceID string `json:"workspace_id"`
	Slug        string `json:"slug"`
	Title       string `json:"title"`
	Type        string `json:"type"`
	Schema      int    `json:"schema"`
	Revision    int64  `json:"revision"`
	URL         string `json:"url"`
}

type documentCreationConsent struct {
	Submission string
	Consumed   sql.NullString
	Page       sql.NullString
}

// Creation grants only one new document. It never creates a general Page grant
// or artifact run delegation; later edits require separate human authorization.
func (h *PageHandler) documentCreationAuthority(ctx context.Context, a *artifactRunAuthority) (documentCreationConsent, error) {
	var value documentCreationConsent
	if h.checkArtifactRunPrincipal(ctx, a) != nil || h.checkDocumentRunPageScope(ctx, "", a) != nil || !h.artifactAgentPolicyAllows(ctx, a.WorkspaceID, a.CrewID) || !h.artifactPrincipalMayCreate(ctx, a.WorkspaceID, a.HumanUserID) {
		return value, errors.New("document creation authority denied")
	}
	var expiry string
	var revoked sql.NullString
	err := h.pageAuthReader(ctx).QueryRowContext(ctx, `SELECT submission_id,expires_at,revoked_at,consumed_at,result_page_id FROM document_creation_consents WHERE run_id=? AND message_id=? AND workspace_id=? AND human_user_id=? AND chat_id=?`, a.RunID, a.MessageID, a.WorkspaceID, a.HumanUserID, a.ChatID).Scan(&value.Submission, &expiry, &revoked, &value.Consumed, &value.Page)
	deadline, parseErr := time.Parse(time.RFC3339Nano, expiry)
	if err != nil || parseErr != nil || revoked.Valid || !time.Now().Before(deadline) {
		return value, errors.New("explicit live Docs creation permission required")
	}
	return value, nil
}

func (h *PageHandler) documentCreationReplay(ctx context.Context, a *artifactRunAuthority, consent documentCreationConsent, mutation, digest string) (documentCreationResult, error) {
	var result documentCreationResult
	if !consent.Page.Valid {
		return result, errors.New("created document was deleted; permission remains consumed")
	}
	var storedMutation, storedDigest string
	err := h.pageAuthReader(ctx).QueryRowContext(ctx, `SELECT p.id,p.workspace_id,p.slug,p.name,c.revision,c.mutation_id,c.request_digest FROM document_creations c JOIN pages p ON p.id=c.page_id AND p.workspace_id=c.workspace_id JOIN documents d ON d.page_id=p.id AND d.workspace_id=p.workspace_id WHERE c.run_id=? AND p.id=? AND p.workspace_id=? AND p.owner_user_id=? AND p.owner_crew_id IS NULL`, a.RunID, consent.Page.String, a.WorkspaceID, a.HumanUserID).Scan(&result.PageID, &result.WorkspaceID, &result.Slug, &result.Title, &result.Revision, &storedMutation, &storedDigest)
	if err != nil || storedMutation != mutation || storedDigest != digest {
		return result, errors.New("document creation mutation conflicts with consumed permission")
	}
	result.Type, result.Schema = "document", 3
	result.URL = "/pages?workspace_id=" + result.WorkspaceID + "&ref=" + result.PageID
	return result, nil
}

func (h *PageHandler) CreateDocumentAgent(w http.ResponseWriter, r *http.Request) {
	artifactCommentHeaders(w)
	checked, ok := h.artifactAgentRequest(w, r)
	if !ok {
		return
	}
	r = checked
	a := r.Context().Value(artifactRunKey{}).(*artifactRunAuthority)
	if h.documentCall == nil {
		replyError(w, 503, "Docs are not enabled on this server")
		return
	}
	release, ok := acquireArtifactUpload(w, r)
	if !ok {
		return
	}
	defer release()
	raw, ok := readCapped(w, r, 1<<20, "document authoring")
	if !ok {
		return
	}
	var request documentCreateRequest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if !utf8.Valid(raw) || decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF || (documents.CreationIntent{SubmissionID: request.MutationID}).Validate() != nil {
		replyError(w, 400, "one bounded document authoring object and valid mutation_id required")
		return
	}
	if err := documents.ValidateAuthoring(request.Blocks); err != nil {
		replyError(w, 422, err.Error())
		return
	}
	pageSpec := pages.Document{APIVersion: pages.DocumentAPIVersion, Kind: pages.DocumentKind, Metadata: pages.Metadata{Slug: request.Slug, Name: request.Name}}
	if err := pageSpec.ValidateEmpty(); err != nil {
		writeSpecError(w, err)
		return
	}
	preflight, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		replyInternalError(w, h.logger, "begin document creation preflight", err)
		return
	}
	defer preflight.Rollback()
	if _, err = preflight.ExecContext(r.Context(), `UPDATE document_writer_fence SET seq=seq WHERE id=1`); err != nil {
		replyInternalError(w, h.logger, "fence document creation preflight", err)
		return
	}
	preflightCtx := context.WithValue(r.Context(), pageAuthReaderKey{}, preflight)
	consent, err := h.documentCreationAuthority(preflightCtx, a)
	if err != nil {
		replyError(w, 403, err.Error())
		return
	}
	source, _ := json.Marshal(request)
	sum := sha256.Sum256(append([]byte("crewship-doc-create-v1\n"+consent.Submission+"\n"), source...))
	digest := hex.EncodeToString(sum[:])
	if consent.Consumed.Valid {
		result, err := h.documentCreationReplay(preflightCtx, a, consent, request.MutationID, digest)
		if err != nil {
			replyError(w, 409, err.Error())
			return
		}
		if err = preflight.Commit(); err != nil {
			replyInternalError(w, h.logger, "commit creation preflight replay", err)
			return
		}
		writeJSON(w, 200, result)
		return
	}
	if err = preflight.Commit(); err != nil {
		replyInternalError(w, h.logger, "commit creation preflight", err)
		return
	}
	// Trusted pure computation happens before acquiring the SQL writer. Generated
	// IDs/snapshot never participate in the original authoring fingerprint.
	pageID := generateCUID()
	preparedRaw, status, err := h.documentCall(r.Context(), a.WorkspaceID, "Bearer "+extractToken(r), pageID, "prepare-create", map[string]any{"blocks": request.Blocks})
	if err != nil {
		replyError(w, 503, "document creation preparation unavailable; retry the same mutation_id")
		return
	}
	if status >= 400 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(preparedRaw)
		return
	}
	var prepared struct {
		Schema   int             `json:"schema"`
		Snapshot []byte          `json:"snapshot"`
		Blocks   json.RawMessage `json:"blocks"`
	}
	if json.Unmarshal(preparedRaw, &prepared) != nil || prepared.Schema != 3 || len(prepared.Snapshot) == 0 || len(prepared.Snapshot) > 1<<20 {
		replyError(w, 502, "invalid document creation preparation")
		return
	}
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		replyInternalError(w, h.logger, "begin document creation", err)
		return
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(r.Context(), `UPDATE document_writer_fence SET seq=seq WHERE id=1`); err != nil {
		replyInternalError(w, h.logger, "fence document creation", err)
		return
	}
	ctx := context.WithValue(r.Context(), pageAuthReaderKey{}, tx)
	consent, err = h.documentCreationAuthority(ctx, a)
	if err != nil {
		replyError(w, 403, err.Error())
		return
	}
	if consent.Consumed.Valid {
		result, err := h.documentCreationReplay(ctx, a, consent, request.MutationID, digest)
		if err != nil {
			replyError(w, 409, err.Error())
			return
		}
		if err = tx.Commit(); err != nil {
			replyInternalError(w, h.logger, "commit creation replay", err)
			return
		}
		writeJSON(w, 200, result)
		return
	}
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pages WHERE workspace_id=?`, a.WorkspaceID).Scan(&count); err != nil {
		replyInternalError(w, h.logger, "count document Pages", err)
		return
	}
	if count >= pages.MaxPagesPerWorkspace {
		replyError(w, 409, "workspace page limit reached")
		return
	}
	spec, err := json.Marshal(pageSpec)
	if err != nil {
		replyInternalError(w, h.logger, "encode document Page", err)
		return
	}
	stamp := tsformat.Format(time.Now())
	if _, err = tx.ExecContext(ctx, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)`, pageID, a.WorkspaceID, request.Slug, request.Name, a.HumanUserID, string(spec), stamp, stamp); err != nil {
		if isUniqueViolation(err) {
			replyError(w, 409, "a Page with this slug already exists")
		} else {
			replyInternalError(w, h.logger, "create document Page", err)
		}
		return
	}
	if err = insertPageVersion(ctx, tx, pageID, 1, string(spec), a.HumanUserID, stamp); err != nil {
		replyInternalError(w, h.logger, "create document Page version", err)
		return
	}
	store := documents.New(h.db, h.documentStoreOptions)
	if _, err = store.InitRequestTx(ctx, tx, documents.Key{WorkspaceID: a.WorkspaceID, PageID: pageID}, request.MutationID, digest, 3, prepared.Snapshot, prepared.Blocks, "agent:"+a.AgentID+":"+a.RunID); err != nil {
		h.logger.Error("created document initialization failed", "error", err)
		documentAuthorityError(w, err)
		return
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO document_creations(page_id,workspace_id,run_id,agent_id,human_user_id,source_chat_id,submission_id,mutation_id,request_digest,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, pageID, a.WorkspaceID, a.RunID, a.AgentID, a.HumanUserID, a.ChatID, consent.Submission, request.MutationID, digest, stamp); err != nil {
		replyInternalError(w, h.logger, "record document creation result", err)
		return
	}
	result, err := tx.ExecContext(ctx, `UPDATE document_creation_consents SET consumed_at=?,result_page_id=? WHERE run_id=? AND consumed_at IS NULL AND revoked_at IS NULL`, stamp, pageID, a.RunID)
	if err != nil {
		replyInternalError(w, h.logger, "consume document creation permission", err)
		return
	}
	if n, _ := result.RowsAffected(); n != 1 {
		replyError(w, 409, "document creation permission already consumed")
		return
	}
	if _, err = h.documentCreationAuthority(ctx, a); err != nil {
		replyError(w, 403, err.Error())
		return
	}
	if err = tx.Commit(); err != nil {
		replyInternalError(w, h.logger, "commit document creation", err)
		return
	}
	broadcastWorkspaceEvent(h.hub, a.WorkspaceID, "page.updated", map[string]any{})
	writeJSON(w, 201, documentCreationResult{PageID: pageID, WorkspaceID: a.WorkspaceID, Slug: request.Slug, Title: request.Name, Type: "document", Schema: 3, Revision: 0, URL: "/pages?workspace_id=" + a.WorkspaceID + "&ref=" + pageID})
}
