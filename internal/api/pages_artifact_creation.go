package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/pages"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

func (h *PageHandler) artifactPrincipalMayCreate(ctx context.Context, ws, user string) bool {
	var role string
	var caps sql.NullString
	if err := h.pageAuthReader(ctx).QueryRowContext(ctx, `SELECT role,capabilities FROM workspace_members WHERE workspace_id=? AND user_id=?`, ws, user).Scan(&role, &caps); err != nil {
		return false
	}
	return canRole(role, "create") || HasCapability(resolveCapabilitiesFromRow(caps, role), CapabilityPageCreate)
}

// A page created by this run continues to require its explicit creation opt-in
// and the human's live create authority. Other manually delegated pages keep
// the existing publication contract; creation never expands their grants.
func (h *PageHandler) checkArtifactCreationPublication(ctx context.Context, pageID string, a *artifactRunAuthority) error {
	q := h.pageAuthReader(ctx)
	var present int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM page_artifact_creations WHERE page_id=? AND run_id=?`, pageID, a.RunID).Scan(&present); err != nil {
		return err
	}
	if present == 0 {
		return nil
	}
	if !h.artifactPrincipalMayCreate(ctx, a.WorkspaceID, a.HumanUserID) {
		return errors.New("human page creation authority revoked")
	}
	var expires string
	var revoked sql.NullString
	if err := q.QueryRowContext(ctx, `SELECT expires_at,revoked_at FROM artifact_creation_authorizations WHERE run_id=? AND authorized_by_user_id=?`, a.RunID, a.HumanUserID).Scan(&expires, &revoked); err != nil {
		return errors.New("creation authorization required")
	}
	deadline, err := time.Parse(time.RFC3339Nano, expires)
	if err != nil || !time.Now().Before(deadline) || revoked.Valid {
		return errors.New("creation authorization expired or revoked")
	}
	return h.checkBoardJobCreation(ctx, a, true)
}

func (h *PageHandler) humanCreationAuthorizationTx(w http.ResponseWriter, r *http.Request) (*sql.Tx, *http.Request, *artifactRunAuthority, string, bool) {
	user := UserFromContext(r.Context())
	agent, _ := pageGrantCallerIsAgent(r)
	if user == nil || agent {
		replyError(w, 401, "human authentication required")
		return nil, r, nil, "", false
	}
	ws := WorkspaceIDFromContext(r.Context())
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		replyInternalError(w, h.logger, "begin creation authorization", err)
		return nil, r, nil, "", false
	}
	fail := func() (*sql.Tx, *http.Request, *artifactRunAuthority, string, bool) {
		tx.Rollback()
		return nil, r, nil, "", false
	}
	if _, err := tx.ExecContext(r.Context(), `UPDATE workspace_members SET role=role WHERE workspace_id=? AND user_id=?`, ws, user.ID); err != nil {
		replyInternalError(w, h.logger, "fence creation authorization", err)
		return fail()
	}
	ctx := context.WithValue(r.Context(), pageAuthReaderKey{}, tx)
	r = r.WithContext(ctx)
	a := &artifactRunAuthority{RunID: r.PathValue("runId"), WorkspaceID: ws, HumanUserID: user.ID}
	var expires string
	if err := tx.QueryRowContext(ctx, `SELECT agent_id,crew_id,chat_id,message_id,expires_at FROM artifact_run_principals WHERE run_id=? AND workspace_id=? AND human_user_id=?`, a.RunID, ws, user.ID).Scan(&a.AgentID, &a.CrewID, &a.ChatID, &a.MessageID, &expires); err != nil {
		artifactCommentError(w, 403, "no_human_principal", "verified live chat run for this human required")
		return fail()
	}
	return tx, r, a, expires, true
}

func (h *PageHandler) AuthorizeArtifactCreation(w http.ResponseWriter, r *http.Request) {
	artifactCommentHeaders(w)
	b, ok := readCapped(w, r, 4096, "creation authorization")
	if !ok {
		return
	}
	var empty struct{}
	decoder := json.NewDecoder(strings.NewReader(string(b)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&empty) != nil || decoder.Decode(new(any)) != io.EOF {
		replyError(w, 400, "creation authorization accepts only an empty JSON object")
		return
	}
	tx, r, a, expires, ok := h.humanCreationAuthorizationTx(w, r)
	if !ok {
		return
	}
	defer tx.Rollback()
	if err := h.checkArtifactRunPrincipal(r.Context(), a); err != nil {
		replyError(w, 403, err.Error())
		return
	}
	if !h.artifactPrincipalMayCreate(r.Context(), a.WorkspaceID, a.HumanUserID) {
		replyError(w, 403, "page.create authority required")
		return
	}
	result, err := tx.ExecContext(r.Context(), `INSERT INTO artifact_creation_authorizations(run_id,authorized_by_user_id,expires_at,granted_at) VALUES (?,?,?,?) ON CONFLICT(run_id) DO NOTHING`, a.RunID, a.HumanUserID, expires, tsformat.Format(time.Now()))
	if err != nil {
		replyInternalError(w, h.logger, "authorize artifact creation", err)
		return
	}
	var revoked, consumed sql.NullString
	if err := tx.QueryRowContext(r.Context(), `SELECT expires_at,revoked_at,consumed_at FROM artifact_creation_authorizations WHERE run_id=?`, a.RunID).Scan(&expires, &revoked, &consumed); err != nil {
		replyInternalError(w, h.logger, "read creation authorization", err)
		return
	}
	if err := tx.Commit(); err != nil {
		replyInternalError(w, h.logger, "commit creation authorization", err)
		return
	}
	status := 200
	if n, _ := result.RowsAffected(); n == 1 {
		status = 201
	}
	remaining := 1
	if consumed.Valid || revoked.Valid {
		remaining = 0
	}
	writeJSON(w, status, map[string]any{"run_id": a.RunID, "max_artifacts": 1, "remaining": remaining, "expires_at": expires, "revoked": revoked.Valid})
}

func (h *PageHandler) RevokeArtifactCreation(w http.ResponseWriter, r *http.Request) {
	artifactCommentHeaders(w)
	tx, r, a, _, ok := h.humanCreationAuthorizationTx(w, r)
	if !ok {
		return
	}
	defer tx.Rollback()
	// Revocation remains possible after the run ends or create capability is
	// withdrawn; only the original human can remove this narrow admission.
	stamp := tsformat.Format(time.Now())
	if _, err := tx.ExecContext(r.Context(), `UPDATE artifact_creation_authorizations SET revoked_at=?,revoked_by_user_id=? WHERE run_id=? AND authorized_by_user_id=?`, stamp, a.HumanUserID, a.RunID, a.HumanUserID); err != nil {
		replyInternalError(w, h.logger, "revoke artifact creation", err)
		return
	}
	if _, err := tx.ExecContext(r.Context(), `UPDATE page_artifact_run_delegations SET revoked_at=?,revoked_by_user_id=? WHERE run_id=? AND page_id IN (SELECT page_id FROM page_artifact_creations WHERE run_id=?)`, stamp, a.HumanUserID, a.RunID, a.RunID); err != nil {
		replyInternalError(w, h.logger, "revoke created artifact publication", err)
		return
	}
	if err := tx.Commit(); err != nil {
		replyInternalError(w, h.logger, "commit creation revocation", err)
		return
	}
	w.WriteHeader(204)
}

func (h *PageHandler) CreateArtifactAgent(w http.ResponseWriter, r *http.Request) {
	artifactCommentHeaders(w)
	checked, ok := h.artifactAgentRequest(w, r)
	if !ok {
		return
	}
	r = checked
	a := r.Context().Value(artifactRunKey{}).(*artifactRunAuthority)
	if h.artifactStoragePath == "" {
		replyError(w, 503, "artifact storage is not configured")
		return
	}
	release, ok := acquireArtifactUpload(w, r)
	if !ok {
		return
	}
	defer release()
	var req struct {
		Slug       string `json:"slug"`
		Name       string `json:"name"`
		HTML       string `json:"html"`
		MutationID string `json:"mutation_id"`
	}
	if !decodeArtifactJSON(w, r, &req) {
		return
	}
	if len(req.MutationID) < 1 || len(req.MutationID) > 128 || strings.TrimSpace(req.MutationID) != req.MutationID {
		replyError(w, 400, "mutation_id must contain 1 to 128 bytes")
		return
	}
	if len(req.HTML) == 0 {
		replyError(w, 422, "HTML must be nonempty UTF-8")
		return
	}
	if len(req.HTML) > maxArtifactHTMLBytes {
		replyError(w, 413, "HTML exceeds 10 MiB")
		return
	}
	doc := pages.Document{APIVersion: pages.DocumentAPIVersion, Kind: pages.DocumentKind, Metadata: pages.Metadata{Slug: req.Slug, Name: req.Name}}
	if err := doc.ValidateEmpty(); err != nil {
		writeSpecError(w, err)
		return
	}
	spec, err := json.Marshal(doc)
	if err != nil {
		replyInternalError(w, h.logger, "encode created artifact", err)
		return
	}
	sha := attachmentDigest([]byte(req.HTML))
	fingerprint := attachmentDigest([]byte(req.Slug + "\x00" + req.Name + "\x00" + sha))
	// Refuse default-off or already consumed admission before writing bytes.
	if err := h.checkArtifactRunPrincipal(r.Context(), a); err != nil {
		replyError(w, 403, err.Error())
		return
	}
	if !h.artifactPrincipalMayCreate(r.Context(), a.WorkspaceID, a.HumanUserID) {
		replyError(w, 403, "page.create authority required")
		return
	}
	var earlyExpiry string
	var earlyRevoked, earlyConsumed sql.NullString
	if err := h.db.QueryRowContext(r.Context(), `SELECT expires_at,revoked_at,consumed_at FROM artifact_creation_authorizations WHERE run_id=? AND authorized_by_user_id=?`, a.RunID, a.HumanUserID).Scan(&earlyExpiry, &earlyRevoked, &earlyConsumed); err != nil {
		artifactCommentError(w, 403, "creation_authorization_required", "a human must authorize this run to create one artifact")
		return
	}
	earlyDeadline, err := time.Parse(time.RFC3339Nano, earlyExpiry)
	if err != nil || earlyRevoked.Valid || !time.Now().Before(earlyDeadline) {
		artifactCommentError(w, 403, "creation_authorization_required", "artifact creation authorization expired or revoked")
		return
	}
	if earlyConsumed.Valid {
		var old string
		if err := h.db.QueryRowContext(r.Context(), `SELECT request_digest FROM page_artifact_creations WHERE run_id=? AND mutation_id=?`, a.RunID, req.MutationID).Scan(&old); err != nil {
			artifactCommentError(w, 409, "creation_limit", "this run has already created its one authorized artifact")
			return
		} else if old != fingerprint {
			artifactCommentError(w, 409, "mutation_conflict", "mutation_id was already used for another creation")
			return
		}
	}
	unlock := lockAttachmentBlob(a.WorkspaceID, sha)
	defer unlock()
	// Failed/denied creation can leave an unreferenced blob; normal GC owns that
	// cleanup. The lock spans the file write and atomic page/revision commit.
	if _, _, err := storeAttachmentBlob(h.artifactStoragePath, a.WorkspaceID, []byte(req.HTML)); err != nil {
		replyInternalError(w, h.logger, "store created artifact", err)
		return
	}
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		replyInternalError(w, h.logger, "begin agent artifact creation", err)
		return
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(r.Context(), `UPDATE artifact_run_principals SET expires_at=expires_at WHERE run_id=?`, a.RunID); err != nil {
		replyInternalError(w, h.logger, "fence agent artifact creation", err)
		return
	}
	ctx := context.WithValue(r.Context(), pageAuthReaderKey{}, tx)
	if err := h.checkArtifactRunPrincipal(ctx, a); err != nil {
		replyError(w, 403, err.Error())
		return
	}
	if !h.artifactPrincipalMayCreate(ctx, a.WorkspaceID, a.HumanUserID) {
		replyError(w, 403, "page.create authority required")
		return
	}
	var expires string
	var revoked, consumed sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT expires_at,revoked_at,consumed_at FROM artifact_creation_authorizations WHERE run_id=? AND authorized_by_user_id=?`, a.RunID, a.HumanUserID).Scan(&expires, &revoked, &consumed); err != nil {
		artifactCommentError(w, 403, "creation_authorization_required", "a human must authorize this run to create one artifact")
		return
	}
	limit, err := time.Parse(time.RFC3339Nano, expires) // tsformat:allow: parsing a stored expiry for time comparison; no timestamp is written
	if err != nil || !time.Now().Before(limit) || revoked.Valid {
		artifactCommentError(w, 403, "creation_authorization_required", "artifact creation authorization expired or revoked")
		return
	}
	if err := h.checkBoardJobCreation(ctx, a, true); err != nil {
		replyError(w, 403, err.Error())
		return
	}
	var pageID, oldDigest, oldHuman, oldAgent, slug string
	var head int64
	err = tx.QueryRowContext(ctx, `SELECT c.page_id,c.request_digest,COALESCE(c.human_user_id,''),COALESCE(c.agent_id,''),p.slug,s.head_rev FROM page_artifact_creations c JOIN pages p ON p.id=c.page_id JOIN page_artifacts s ON s.page_id=p.id WHERE c.run_id=? AND c.mutation_id=? AND p.workspace_id=?`, a.RunID, req.MutationID, a.WorkspaceID).Scan(&pageID, &oldDigest, &oldHuman, &oldAgent, &slug, &head)
	if err == nil {
		if oldDigest != fingerprint || oldHuman != a.HumanUserID || oldAgent != a.AgentID {
			artifactCommentError(w, 409, "mutation_conflict", "mutation_id was already used for another creation")
			return
		}
		if err := h.checkArtifactRun(ctx, pageID, a); err != nil {
			replyError(w, 403, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"page_id": pageID, "slug": slug, "revision": 1, "head_rev": head, "url": "/pages/" + slug})
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		replyInternalError(w, h.logger, "read artifact creation retry", err)
		return
	}
	if consumed.Valid {
		artifactCommentError(w, 409, "creation_limit", "this run has already created its one authorized artifact")
		return
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pages WHERE workspace_id=?`, a.WorkspaceID).Scan(&count); err != nil {
		replyInternalError(w, h.logger, "count artifact pages", err)
		return
	}
	if count >= pages.MaxPagesPerWorkspace {
		artifactCommentError(w, 409, "page_limit", "workspace page limit reached")
		return
	}
	if !h.enforceArtifactWorkspaceRate(w, r, tx, a.WorkspaceID, h.clock.Now().UTC()) {
		return
	}
	pageID = generateCUID()
	stamp := tsformat.Format(h.clock.Now().UTC())
	if _, err := tx.ExecContext(ctx, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?)`, pageID, a.WorkspaceID, req.Slug, req.Name, a.HumanUserID, string(spec), stamp, stamp); err != nil {
		if isUniqueViolation(err) {
			artifactCommentError(w, 409, "slug_conflict", "a page with this slug already exists")
		} else {
			replyInternalError(w, h.logger, "create artifact page", err)
		}
		return
	}
	if err := insertPageVersion(ctx, tx, pageID, 1, string(spec), a.HumanUserID, stamp); err != nil {
		replyInternalError(w, h.logger, "record created page version", err)
		return
	}
	// The human's explicit one-run instruction authorizes only this new private
	// Page. No workspace/crew grant or owner supplied by the agent is accepted.
	if _, err := tx.ExecContext(ctx, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id,granted_at) VALUES (?,'agent',?,'write',?,?)`, pageID, a.AgentID, a.HumanUserID, stamp); err != nil {
		replyInternalError(w, h.logger, "grant created artifact agent", err)
		return
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO page_artifact_run_delegations(page_id,run_id,granted_by_user_id,expires_at,granted_at) VALUES (?,?,?,?,?)`, pageID, a.RunID, a.HumanUserID, expires, stamp); err != nil {
		replyInternalError(w, h.logger, "delegate created artifact run", err)
		return
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO page_artifact_revisions(id,page_id,revision,sha256,author_user_id,mutation_id,request_digest,created_at,author_agent_id,author_run_id,source_chat_id) VALUES (?,?,1,?,?,?,?,?,?,?,?)`, generateCUID(), pageID, sha, a.HumanUserID, req.MutationID, attachmentDigest([]byte(fmt.Sprintf("%d/%t/%d/%s", 0, false, 0, sha))), stamp, a.AgentID, a.RunID, a.ChatID); err != nil {
		replyInternalError(w, h.logger, "save created artifact revision", err)
		return
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO page_artifact_mutation_receipts(page_id,mutation_id,revision,request_digest,author_user_id,author_agent_id,author_run_id,created_at) VALUES(?,?,1,?,?,?,?,?)`, pageID, req.MutationID, attachmentDigest([]byte(fmt.Sprintf("%d/%t/%d/%s", 0, false, 0, sha))), a.HumanUserID, a.AgentID, a.RunID, stamp); err != nil {
		replyInternalError(w, h.logger, "save creation retry receipt", err)
		return
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO page_artifacts(page_id,head_rev) VALUES (?,1)`, pageID); err != nil {
		replyInternalError(w, h.logger, "save created artifact head", err)
		return
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO page_artifact_creations(page_id,run_id,agent_id,human_user_id,mutation_id,request_digest,created_at) VALUES (?,?,?,?,?,?,?)`, pageID, a.RunID, a.AgentID, a.HumanUserID, req.MutationID, fingerprint, stamp); err != nil {
		replyInternalError(w, h.logger, "record artifact creation", err)
		return
	}
	if _, err := tx.ExecContext(ctx, `UPDATE artifact_creation_authorizations SET consumed_at=?,consumed_page_id=? WHERE run_id=? AND consumed_at IS NULL`, stamp, pageID, a.RunID); err != nil {
		replyInternalError(w, h.logger, "consume artifact creation authorization", err)
		return
	}
	if err := h.completeBoardJobCreation(r.WithContext(ctx), tx, a, pageID); err != nil {
		replyInternalError(w, h.logger, "complete board artifact job", err)
		return
	}
	if err := tx.Commit(); err != nil {
		replyInternalError(w, h.logger, "commit agent artifact creation", err)
		return
	}
	broadcastWorkspaceEvent(h.hub, a.WorkspaceID, "page.updated", map[string]any{"page_id": pageID})
	var updatedBoard string
	if h.db.QueryRowContext(r.Context(), `SELECT board_page_id FROM page_board_requests WHERE run_id=? AND result_page_id=? AND pin_status='pinned'`, a.RunID, pageID).Scan(&updatedBoard) == nil {
		broadcastWorkspaceEvent(h.hub, a.WorkspaceID, "page.updated", map[string]any{"page_id": updatedBoard})
	}
	writeJSON(w, 201, map[string]any{"page_id": pageID, "slug": req.Slug, "revision": 1, "head_rev": 1, "url": "/pages/" + req.Slug})
}
