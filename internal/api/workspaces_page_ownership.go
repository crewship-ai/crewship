package api

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/inbox"
)

// Custody metadata never includes private source, scene, spec or history.
type pageOwnershipSuccessor struct {
	UserID string `json:"user_id"`
	Name   string `json:"name"`
	Email  string `json:"email"`
}
type pageOwnershipSummary struct {
	ID                 string                   `json:"id"`
	Slug               string                   `json:"slug"`
	Name               string                   `json:"name"`
	Type               string                   `json:"type"`
	OwnerUserID        string                   `json:"owner_user_id"`
	OwnerName          string                   `json:"owner_name"`
	EligibleSuccessors []pageOwnershipSuccessor `json:"eligible_successors"`
}

// Named wire DTOs keep the metadata-only contract explicit for callers and the
// audited OpenAPI catalog. Empty next_cursor is omitted at the end of a listing.
type WorkspacePageOwnershipSummary = pageOwnershipSummary
type WorkspacePageOwnershipSuccessor = pageOwnershipSuccessor
type WorkspacePageOwnershipListResponse struct {
	NextCursor string                          `json:"next_cursor,omitempty"`
	Pages      []WorkspacePageOwnershipSummary `json:"pages"`
}
type WorkspacePageOwnershipTransferRequest struct {
	ExpectedOwnerUserID string `json:"expected_owner_user_id"`
	SuccessorUserID     string `json:"successor_user_id"`
}
type WorkspacePageOwnershipError struct {
	Code  string `json:"code,omitempty"`
	Error string `json:"error"`
}

func pageOwnershipRows(ctx context.Context, q pageAuthQueryer, ws, owner, page string) ([]pageOwnershipSummary, error) {
	return pageOwnershipRowsAfter(ctx, q, ws, owner, page, "", -1)
}

// Only departure conflict metadata uses an unrestricted row count; administrative
// eligibility listing always supplies a validated page size and reads one extra.
func pageOwnershipRowsAfter(ctx context.Context, q pageAuthQueryer, ws, owner, page, after string, limit int) ([]pageOwnershipSummary, error) {
	rows, err := q.QueryContext(ctx, `SELECT p.id,p.slug,p.name,CASE WHEN EXISTS(SELECT 1 FROM page_boards WHERE page_id=p.id) THEN 'board' ELSE 'artifact' END,COALESCE(p.owner_user_id,''),COALESCE(u.full_name,'') FROM pages p LEFT JOIN users u ON u.id=p.owner_user_id WHERE p.workspace_id=? AND (?='' OR p.owner_user_id=?) AND (?='' OR p.id=?) AND (EXISTS(SELECT 1 FROM page_artifacts WHERE page_id=p.id) OR EXISTS(SELECT 1 FROM page_boards WHERE page_id=p.id)) AND (?='' OR p.id>?) ORDER BY p.id LIMIT ?`, ws, owner, owner, page, page, after, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []pageOwnershipSummary{}
	for rows.Next() {
		var p pageOwnershipSummary
		if err := rows.Scan(&p.ID, &p.Slug, &p.Name, &p.Type, &p.OwnerUserID, &p.OwnerName); err != nil {
			return nil, err
		}
		p.EligibleSuccessors = []pageOwnershipSuccessor{}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (h *WorkspaceHandler) pageOwnershipActor(w http.ResponseWriter, r *http.Request, ph *PageHandler) (*AuthUser, bool) {
	user := UserFromContext(r.Context())
	agent, _ := pageGrantCallerIsAgent(r)
	if user == nil || agent || projectAgentFrom(r.Context()) != nil {
		replyError(w, 401, "human authentication required")
		return nil, false
	}
	if _, delegated := r.Context().Value(artifactRunKey{}).(*artifactRunAuthority); delegated {
		replyError(w, 403, "human workspace administrator required")
		return nil, false
	}
	role, err := ph.liveContentHumanRole(r.Context(), WorkspaceIDFromContext(r.Context()), user.ID)
	if err != nil || !canRole(role, "manage") {
		replyError(w, 403, "active trusted workspace administrator required")
		return nil, false
	}
	return user, true
}

// pageOwnershipCandidates deliberately uses canonical Page visibility, including
// folder and use-time grant issuer rights. Membership alone never adds access.
func pageOwnershipCandidates(ctx context.Context, ph *PageHandler, ws string, p pageOwnershipSummary) ([]pageOwnershipSuccessor, error) {
	rows, err := ph.pageAuthReader(ctx).QueryContext(ctx, `SELECT u.id,COALESCE(u.full_name,''),u.email FROM workspace_members m JOIN users u ON u.id=m.user_id WHERE m.workspace_id=? AND u.id!=? AND u.suspended_at IS NULL AND NOT EXISTS(SELECT 1 FROM workspace_members restricted WHERE restricted.user_id=u.id AND restricted.access_mode='restricted') ORDER BY u.id`, ws, p.OwnerUserID)
	if err != nil {
		return nil, err
	}
	candidates := []pageOwnershipSuccessor{}
	for rows.Next() {
		var c pageOwnershipSuccessor
		if err := rows.Scan(&c.UserID, &c.Name, &c.Email); err != nil {
			rows.Close()
			return nil, err
		}
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	rec, err := ph.loadPage(ctx, ws, p.Slug)
	if err != nil {
		return nil, err
	}
	out := []pageOwnershipSuccessor{}
	for _, c := range candidates {
		role, err := ph.liveContentHumanRole(ctx, ws, c.UserID)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		viewerCtx := context.WithValue(ctx, ctxRole, role)
		viewer, err := ph.loadViewer(viewerCtx, ws, c.UserID)
		if err != nil {
			return nil, err
		}
		visible, err := ph.canSeePage(viewerCtx, ws, rec, nil, viewer)
		if err != nil {
			return nil, err
		}
		if visible {
			out = append(out, c)
		}
	}
	return out, nil
}

type pageOwnershipCursor struct {
	Workspace string `json:"workspace"`
	Owner     string `json:"owner"`
	PageID    string `json:"page_id"`
}

func pageOwnershipPaging(r *http.Request, ws, owner string) (int, string, error) {
	limit := 25
	if raw := r.URL.Query().Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 100 {
			return 0, "", errors.New("limit must be between 1 and 100")
		}
		limit = value
	}
	raw := r.URL.Query().Get("cursor")
	if raw == "" {
		return limit, "", nil
	}
	if len(raw) > 1024 {
		return 0, "", errors.New("invalid ownership cursor")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return 0, "", errors.New("invalid ownership cursor")
	}
	var cursor pageOwnershipCursor
	if err = json.Unmarshal(decoded, &cursor); err != nil || cursor.Workspace != ws || cursor.Owner != owner || cursor.PageID == "" || len(cursor.PageID) > 128 {
		return 0, "", errors.New("invalid ownership cursor for this workspace and owner filter")
	}
	return limit, cursor.PageID, nil
}

// ListPageOwnership returns metadata to human workspace administrators. It is
// separate from opening the private document and never returns document bytes.
func (h *WorkspaceHandler) ListPageOwnership(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	tx, err := h.db.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		replyInternalError(w, h.logger, "begin custody listing", err)
		return
	}
	defer tx.Rollback()
	ctx := context.WithValue(r.Context(), pageAuthReaderKey{}, tx)
	r = r.WithContext(ctx)
	ph := &PageHandler{db: h.db, logger: h.logger}
	if _, ok := h.pageOwnershipActor(w, r, ph); !ok {
		return
	}
	ws := WorkspaceIDFromContext(ctx)
	owner := strings.TrimSpace(r.URL.Query().Get("owner_user_id"))
	limit, after, err := pageOwnershipPaging(r, ws, owner)
	if err != nil {
		replyError(w, 400, err.Error())
		return
	}
	pages, err := pageOwnershipRowsAfter(ctx, tx, ws, owner, "", after, limit+1)
	nextCursor := ""
	if len(pages) > limit {
		pages = pages[:limit]
		raw, _ := json.Marshal(pageOwnershipCursor{Workspace: ws, Owner: owner, PageID: pages[len(pages)-1].ID})
		nextCursor = base64.RawURLEncoding.EncodeToString(raw)
	}
	if err == nil {
		for i := range pages {
			pages[i].EligibleSuccessors, err = pageOwnershipCandidates(ctx, ph, ws, pages[i])
			if err != nil {
				break
			}
		}
	}
	if err != nil {
		replyInternalError(w, h.logger, "list output custody", err)
		return
	}
	writeJSON(w, 200, WorkspacePageOwnershipListResponse{Pages: pages, NextCursor: nextCursor})
}

// TransferPageOwnership immediately transfers one output after explicit human
// selection. A writer fence orders all live authority checks with revocation.
func (h *WorkspaceHandler) TransferPageOwnership(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var body WorkspacePageOwnershipTransferRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil || strings.TrimSpace(body.ExpectedOwnerUserID) == "" || strings.TrimSpace(body.SuccessorUserID) == "" {
		replyError(w, 400, "expected_owner_user_id and successor_user_id are required")
		return
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		replyError(w, 400, "one JSON object required")
		return
	}

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		replyInternalError(w, h.logger, "begin custody transfer", err)
		return
	}
	defer tx.Rollback()
	ws := WorkspaceIDFromContext(r.Context())
	id := r.PathValue("pageID")
	if _, err = tx.ExecContext(r.Context(), `UPDATE pages SET id=id WHERE workspace_id=? AND id=?`, ws, id); err != nil {
		replyInternalError(w, h.logger, "custody writer fence", err)
		return
	}
	ctx := context.WithValue(r.Context(), pageAuthReaderKey{}, tx)
	r = r.WithContext(ctx)
	ph := &PageHandler{db: h.db, logger: h.logger}
	actor, ok := h.pageOwnershipActor(w, r, ph)
	if !ok {
		return
	}
	pages, err := pageOwnershipRows(ctx, tx, ws, "", id)
	if err != nil {
		replyInternalError(w, h.logger, "load output custody", err)
		return
	}
	if len(pages) != 1 {
		replyError(w, 404, "output page not found")
		return
	}
	p := pages[0]
	if p.OwnerUserID != body.ExpectedOwnerUserID {
		writeJSON(w, 409, WorkspacePageOwnershipError{Error: "page ownership changed; refresh before transferring", Code: "owner_changed"})
		return
	}
	p.EligibleSuccessors, err = pageOwnershipCandidates(ctx, ph, ws, p)
	if err != nil {
		replyInternalError(w, h.logger, "resolve custody successor", err)
		return
	}
	eligible := false
	for _, c := range p.EligibleSuccessors {
		if c.UserID == body.SuccessorUserID {
			eligible = true
			break
		}
	}
	if !eligible {
		replyError(w, 403, "successor must be another active trusted workspace member with current page read access")
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	result, err := tx.ExecContext(ctx, `UPDATE pages SET owner_user_id=?,owner_crew_id=NULL,updated_at=? WHERE id=? AND workspace_id=? AND owner_user_id=?`, body.SuccessorUserID, now, id, ws, body.ExpectedOwnerUserID)
	if err != nil {
		replyInternalError(w, h.logger, "transfer output custody", err)
		return
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		writeJSON(w, 409, WorkspacePageOwnershipError{Error: "page ownership changed; refresh before transferring", Code: "owner_changed"})
		return
	}
	// The Page identifies a subject with new custody news, not a once-only
	// event. Reopen its administrator card atomically; the journal below keeps
	// the history of every committed transfer.
	if err = inbox.UpsertTx(ctx, tx, h.logger, inbox.Item{
		WorkspaceID: ws,
		Kind:        pagesTransferInboxKind,
		SourceID:    id,
		TargetRole:  "ADMIN",
		Title:       "Output ownership explicitly transferred",
		BodyMD:      "A workspace administrator selected a human successor who already had read access. Private content and stored grant rows were preserved; effective access follows current Page permissions.",
		SenderType:  "system",
		SenderName:  "Pages",
		Payload:     map[string]any{"page_id": id, "page_slug": p.Slug, "from_user_id": p.OwnerUserID, "to_user_id": body.SuccessorUserID, "actor_user_id": actor.ID, "reason": "explicit_human_transfer"},
	}); err != nil {
		replyInternalError(w, h.logger, "record output custody notice", err)
		return
	}
	refreshed, err := pageOwnershipRows(ctx, tx, ws, "", id)
	if err != nil {
		replyInternalError(w, h.logger, "load transferred output", err)
		return
	}
	refreshed[0].EligibleSuccessors, err = pageOwnershipCandidates(ctx, ph, ws, refreshed[0])
	if err != nil {
		replyInternalError(w, h.logger, "resolve transferred output successors", err)
		return
	}
	if err = tx.Commit(); err != nil {
		replyInternalError(w, h.logger, "commit output custody", err)
		return
	}
	emitPageOwnerTransferJournals(ctx, h.journal, ws, actor.ID, p.OwnerUserID, []pageOwnerTransferResult{{PageID: id, PageSlug: p.Slug, PageName: p.Name, ToUserID: body.SuccessorUserID, Reason: "explicit_human_transfer"}})
	response := WorkspacePageOwnershipSummary(refreshed[0])
	writeJSON(w, 200, response)
}

const pageOwnershipHelp = "Ask a workspace administrator to resolve ownership. For Artifacts and Boards, select a human successor in Settings > General > Artifact and Board ownership, then retry."

func replyPageOwnershipConflict(w http.ResponseWriter, prefix string, e *ErrPagesNeedManualTransfer) {
	writeJSON(w, http.StatusConflict, map[string]any{"error": prefix + strings.TrimPrefix(e.Error(), "cannot erase this user: "), "code": "page_ownership_transfer_required", "pages": e.OutputPages})
}

// Instance administration does not grant workspace Page visibility. Even an
// instance administrator who belongs to the workspace gets the same minimal
// contract; names and contacts belong in the workspace-authorized custody API.
func replyInstancePageOwnershipConflict(w http.ResponseWriter, e *ErrPagesNeedManualTransfer) {
	pages := make([]map[string]string, 0, len(e.PageIDs))
	for _, id := range e.PageIDs {
		pages = append(pages, map[string]string{"id": id})
	}
	writeJSON(w, http.StatusConflict, map[string]any{
		"error": "cannot remove this member: page ownership must be resolved first. " + pageOwnershipHelp,
		"code":  "page_ownership_transfer_required", "pages": pages,
	})
}
