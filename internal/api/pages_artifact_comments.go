package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

const maxArtifactCommentBytes = 4 << 10
const maxArtifactThreads = 100
const maxArtifactThreadComments = 50
const maxArtifactCommentMutations = 10000
const maxArtifactCommentHourlyMutations = 60

type artifactCommentWire struct {
	ID             string                    `json:"id"`
	Body           string                    `json:"body"`
	AuthorUserID   string                    `json:"author_user_id"`
	AuthorName     string                    `json:"author_name"`
	AuthorAgentID  string                    `json:"author_agent_id,omitempty"`
	AuthorRunID    string                    `json:"author_run_id,omitempty"`
	Version        int64                     `json:"version"`
	EditedAt       string                    `json:"edited_at,omitempty"`
	DeletedAt      string                    `json:"deleted_at,omitempty"`
	CanEdit        bool                      `json:"can_edit"`
	CanDelete      bool                      `json:"can_delete"`
	CanReact       bool                      `json:"can_react"`
	CanViewHistory bool                      `json:"can_view_history"`
	Reactions      []artifactCommentReaction `json:"reactions"`
	CreatedAt      string                    `json:"created_at"`
}
type artifactThreadWire struct {
	ID               string                `json:"id"`
	Revision         int64                 `json:"revision"`
	Anchor           artifactCommentAnchor `json:"anchor"`
	Status           string                `json:"status"`
	Version          int64                 `json:"version"`
	CreatedByUserID  string                `json:"created_by_user_id"`
	CreatedByName    string                `json:"created_by_name"`
	ResolvedByUserID string                `json:"resolved_by_user_id,omitempty"`
	ResolvedAt       string                `json:"resolved_at,omitempty"`
	CreatedAt        string                `json:"created_at"`
	UpdatedAt        string                `json:"updated_at"`
	Comments         []artifactCommentWire `json:"comments"`
	AppliedVersion   *int64                `json:"applied_version,omitempty"`
}

func (h *PageHandler) artifactCommentsPage(w http.ResponseWriter, r *http.Request, write bool) (*pageRecord, bool) {
	if a, ok := r.Context().Value(artifactCommentAuthorityKey{}).(*artifactRunAuthority); ok {
		return h.artifactAgentCommentsPage(w, r, a)
	}
	if agent, _ := pageGrantCallerIsAgent(r); agent {
		replyError(w, 401, "human authentication required")
		return nil, false
	}
	rec, ok := h.artifactPage(w, r, false)
	if !ok {
		return nil, false
	}
	if write && !h.mayComment(r.Context(), WorkspaceIDFromContext(r.Context()), UserFromContext(r.Context()).ID, RoleFromContext(r.Context()), rec) {
		replyError(w, 403, "page comment permission required")
		return nil, false
	}
	if !rec.HasArtifact {
		replyError(w, 404, "artifact not found")
		return nil, false
	}
	return rec, true
}

func artifactCommentHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}

func (h *PageHandler) loadArtifactThread(ctx context.Context, pageID, threadID string) (*artifactThreadWire, error) {
	q := h.pageAuthReader(ctx)
	v := &artifactThreadWire{Comments: make([]artifactCommentWire, 0)}
	var anchor string
	err := q.QueryRowContext(ctx, `SELECT t.id,t.revision,t.anchor_json,t.status,t.version,COALESCE(t.created_by_user_id,''),COALESCE(u.full_name,''),COALESCE(t.resolved_by_user_id,''),COALESCE(t.resolved_at,''),t.created_at,t.updated_at FROM page_artifact_threads t LEFT JOIN users u ON u.id=t.created_by_user_id WHERE t.page_id=? AND t.id=?`, pageID, threadID).Scan(&v.ID, &v.Revision, &anchor, &v.Status, &v.Version, &v.CreatedByUserID, &v.CreatedByName, &v.ResolvedByUserID, &v.ResolvedAt, &v.CreatedAt, &v.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(anchor), &v.Anchor); err != nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, `SELECT c.id,c.body,COALESCE(c.author_user_id,''),COALESCE(u.full_name,''),c.created_at,COALESCE(c.author_agent_id,''),COALESCE(c.author_run_id,''),c.version,COALESCE(c.edited_at,''),COALESCE(c.deleted_at,'') FROM page_artifact_comments c LEFT JOIN users u ON u.id=c.author_user_id WHERE c.thread_id=? ORDER BY c.thread_version LIMIT 50`, threadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var c artifactCommentWire
		if err := rows.Scan(&c.ID, &c.Body, &c.AuthorUserID, &c.AuthorName, &c.CreatedAt, &c.AuthorAgentID, &c.AuthorRunID, &c.Version, &c.EditedAt, &c.DeletedAt); err != nil {
			return nil, err
		}
		v.Comments = append(v.Comments, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if err = h.artifactCommentDetails(ctx, pageID, v); err != nil {
		return nil, err
	}
	return v, nil
}

func (h *PageHandler) ListArtifactThreads(w http.ResponseWriter, r *http.Request) {
	artifactCommentHeaders(w)
	if !humanArtifactCommentCaller(w, r) {
		return
	}
	tx, err := h.db.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		h.boardError(w, err)
		return
	}
	defer tx.Rollback()
	ctx := context.WithValue(r.Context(), pageAuthReaderKey{}, tx)
	var role string
	if err := tx.QueryRowContext(ctx, `SELECT m.role FROM workspace_members m JOIN users u ON u.id=m.user_id AND u.suspended_at IS NULL WHERE m.workspace_id=? AND m.user_id=?`, WorkspaceIDFromContext(ctx), UserFromContext(ctx).ID).Scan(&role); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			replyError(w, 404, "page not found")
		} else {
			h.boardError(w, err)
		}
		return
	}
	ctx = context.WithValue(ctx, ctxRole, role)
	h.listArtifactThreadsSnapshot(w, r.WithContext(ctx))
}
func (h *PageHandler) listArtifactThreadsSnapshot(w http.ResponseWriter, r *http.Request) {
	artifactCommentHeaders(w)
	rec, ok := h.artifactCommentsPage(w, r, false)
	if !ok {
		return
	}
	var revision int64
	if raw := r.URL.Query().Get("revision"); raw != "" {
		var err error
		revision, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || revision < 1 {
			replyError(w, 400, "revision must be positive")
			return
		}
	}
	if revision > 0 {
		var present int
		if err := h.pageAuthReader(r.Context()).QueryRowContext(r.Context(), `SELECT COUNT(*) FROM page_artifact_revisions WHERE page_id=? AND revision=?`, rec.ID, revision).Scan(&present); err != nil {
			replyInternalError(w, h.logger, "read discussion revision", err)
			return
		} else if present == 0 {
			replyError(w, 404, "artifact revision not found")
			return
		}
	}
	rows, err := h.pageAuthReader(r.Context()).QueryContext(r.Context(), `SELECT id FROM page_artifact_threads WHERE page_id=? AND (?=0 OR revision=?) ORDER BY created_at DESC,id DESC LIMIT 100`, rec.ID, revision, revision)
	if err != nil {
		replyInternalError(w, h.logger, "list artifact threads", err)
		return
	}
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			replyInternalError(w, h.logger, "list artifact threads", err)
			return
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		replyInternalError(w, h.logger, "list artifact threads", err)
		return
	}
	threads := make([]artifactThreadWire, 0, len(ids))
	for _, id := range ids {
		v, err := h.loadArtifactThread(r.Context(), rec.ID, id)
		if err != nil {
			replyInternalError(w, h.logger, "read artifact thread", err)
			return
		}
		threads = append(threads, *v)
	}
	canComment := h.mayComment(r.Context(), WorkspaceIDFromContext(r.Context()), UserFromContext(r.Context()).ID, RoleFromContext(r.Context()), rec)
	writeJSON(w, 200, map[string]any{"threads": threads, "can_comment": canComment, "can_resolve": artifactAgentID(r) == "" && h.mayEditSpec(r.Context(), WorkspaceIDFromContext(r.Context()), UserFromContext(r.Context()).ID, RoleFromContext(r.Context()), rec)})
}

type artifactCommentMutation struct {
	Revision    int64                  `json:"revision,omitempty"`
	Anchor      *artifactCommentAnchor `json:"anchor,omitempty"`
	Body        string                 `json:"body,omitempty"`
	BaseVersion *int64                 `json:"base_version,omitempty"`
	Resolved    *bool                  `json:"resolved,omitempty"`
	MutationID  string                 `json:"mutation_id"`
}

func artifactCommentError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": message, "code": code})
}
func (h *PageHandler) CreateArtifactThread(w http.ResponseWriter, r *http.Request) {
	h.mutateArtifactThread(w, r, "create")
}
func (h *PageHandler) ReplyArtifactThread(w http.ResponseWriter, r *http.Request) {
	h.mutateArtifactThread(w, r, "reply")
}
func (h *PageHandler) ResolveArtifactThread(w http.ResponseWriter, r *http.Request) {
	h.mutateArtifactThread(w, r, "resolve")
}

func (h *PageHandler) mutateArtifactThread(w http.ResponseWriter, r *http.Request, operation string) {
	if _, agent := r.Context().Value(artifactCommentAuthorityKey{}).(*artifactRunAuthority); agent && operation != "reply" {
		replyError(w, 403, "agent discussion authority permits replies only")
		return
	}
	artifactCommentHeaders(w)
	rec, ok := h.artifactCommentsPage(w, r, true)
	if !ok {
		return
	}
	body, ok := readCapped(w, r, 6*maxArtifactCommentBytes+2048, "artifact comment")
	if !ok {
		return
	}
	if !utf8.Valid(body) {
		replyError(w, 400, "comment request must be UTF-8 JSON")
		return
	}
	var req artifactCommentMutation
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		if errors.Is(err, errInvalidArtifactAnchor) {
			replyError(w, 422, "invalid comment anchor")
			return
		}
		replyError(w, 400, "invalid comment request")
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		replyError(w, 400, "comment request must contain one JSON object")
		return
	}
	if len(req.MutationID) < 1 || len(req.MutationID) > 128 || strings.TrimSpace(req.MutationID) != req.MutationID {
		replyError(w, 400, "mutation_id must contain 1 to 128 bytes")
		return
	}
	if operation == "create" {
		if req.Revision < 1 || validateArtifactCommentAnchor(req.Anchor) != nil || req.BaseVersion != nil || req.Resolved != nil {
			replyError(w, 422, "a revision and valid schema_version 1 document or viewport point anchor are required")
			return
		}
	} else {
		if req.BaseVersion == nil || *req.BaseVersion < 1 || req.Revision != 0 || req.Anchor != nil {
			replyError(w, 400, "base_version is required; revision and anchor are immutable")
			return
		}
		if operation == "resolve" {
			if req.Resolved == nil || req.Body != "" {
				replyError(w, 400, "resolved is required; resolution cannot change comment text")
				return
			}
		} else if req.Resolved != nil {
			replyError(w, 400, "a reply cannot change resolution")
			return
		}
	}
	if operation != "resolve" {
		if strings.TrimSpace(req.Body) == "" {
			replyError(w, 422, "comment text is required")
			return
		}
		if len(req.Body) > maxArtifactCommentBytes {
			replyError(w, 413, "comment text exceeds 4 KiB")
			return
		}
	}
	if operation == "resolve" && !h.mayEditSpec(r.Context(), WorkspaceIDFromContext(r.Context()), UserFromContext(r.Context()).ID, RoleFromContext(r.Context()), rec) {
		replyError(w, 403, "page write permission required to resolve threads")
		return
	}
	threadID := r.PathValue("threadId")
	encoded, _ := json.Marshal(struct {
		Operation string
		ThreadID  string
		Request   artifactCommentMutation
	}{operation, threadID, req})
	digest := attachmentDigest(encoded)
	ws := WorkspaceIDFromContext(r.Context())
	user := UserFromContext(r.Context())
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		replyInternalError(w, h.logger, "begin artifact comment", err)
		return
	}
	defer tx.Rollback()
	fenced, err := tx.ExecContext(r.Context(), `UPDATE pages SET updated_at=updated_at WHERE id=? AND workspace_id=?`, rec.ID, ws)
	if err != nil {
		replyInternalError(w, h.logger, "fence artifact comment", err)
		return
	}
	if n, _ := fenced.RowsAffected(); n != 1 {
		replyError(w, 404, "page not found")
		return
	}
	var role string
	if err := tx.QueryRowContext(r.Context(), `SELECT role FROM workspace_members WHERE workspace_id=? AND user_id=?`, ws, user.ID).Scan(&role); err != nil {
		replyError(w, 403, "workspace membership required")
		return
	}
	ctx := context.WithValue(r.Context(), pageAuthReaderKey{}, tx)
	ctx = context.WithValue(ctx, ctxRole, role)
	rec, ok = h.artifactCommentsPage(w, r.WithContext(ctx), true)
	if !ok {
		return
	}
	if operation == "resolve" && !h.mayEditSpec(ctx, ws, user.ID, role, rec) {
		replyError(w, 403, "page write permission required to resolve threads")
		return
	}
	var receiptThread, receiptDigest, receiptActor, receiptAgent, receiptRun string
	var applied int64
	err = tx.QueryRowContext(ctx, `SELECT thread_id,request_digest,COALESCE(actor_user_id,''),result_version,COALESCE(actor_agent_id,''),COALESCE(actor_run_id,'') FROM page_artifact_comment_mutations WHERE page_id=? AND mutation_id=?`, rec.ID, req.MutationID).Scan(&receiptThread, &receiptDigest, &receiptActor, &applied, &receiptAgent, &receiptRun)
	if err == nil {
		if receiptDigest != digest || receiptActor != user.ID || receiptAgent != artifactAgentID(r) || receiptRun != artifactRunID(r) {
			artifactCommentError(w, 409, "mutation_conflict", "mutation_id was already used for another request")
			return
		}
		v, err := h.loadArtifactThread(ctx, rec.ID, receiptThread)
		if err != nil {
			replyInternalError(w, h.logger, "read comment retry", err)
			return
		}
		v.AppliedVersion = &applied
		writeJSON(w, 200, v)
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		replyInternalError(w, h.logger, "read comment receipt", err)
		return
	}
	now := h.clock.Now().UTC()
	stamp := tsformat.Format(now)
	if err := compactDiscussionReceipts(r.Context(), tx, rec.ID, false, now); err != nil {
		h.boardError(w, err)
		return
	}
	var total, recent int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(CASE WHEN created_at>? THEN 1 ELSE 0 END),0) FROM page_artifact_comment_mutations WHERE page_id=?`, tsformat.Format(now.Add(-time.Hour)), rec.ID).Scan(&total, &recent); err != nil {
		replyInternalError(w, h.logger, "check comment quota", err)
		return
	}
	if total >= maxArtifactCommentMutations {
		artifactCommentError(w, 409, "comment_mutation_limit", "artifact discussion mutation limit reached")
		return
	}
	if recent >= maxArtifactCommentHourlyMutations {
		w.Header().Set("Retry-After", "3600")
		artifactCommentError(w, 429, "rate_limited", "artifact discussion mutation rate limit reached")
		return
	}
	commentID := ""
	if operation == "create" {
		var present, threads int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM page_artifact_revisions WHERE page_id=? AND revision=?`, rec.ID, req.Revision).Scan(&present); err != nil {
			replyInternalError(w, h.logger, "read comment source revision", err)
			return
		}
		if present != 1 {
			replyError(w, 404, "artifact revision not found")
			return
		}
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM page_artifact_threads WHERE page_id=?`, rec.ID).Scan(&threads); err != nil {
			replyInternalError(w, h.logger, "count artifact threads", err)
			return
		}
		if threads >= maxArtifactThreads {
			artifactCommentError(w, 409, "thread_limit", "artifact thread limit reached")
			return
		}
		threadID = generateCUID()
		anchor, _ := json.Marshal(req.Anchor)
		if _, err := tx.ExecContext(ctx, `INSERT INTO page_artifact_threads(id,page_id,revision,anchor_json,created_by_user_id,created_at,updated_at) VALUES (?,?,?,?,?,?,?)`, threadID, rec.ID, req.Revision, string(anchor), user.ID, stamp, stamp); err != nil {
			replyInternalError(w, h.logger, "create artifact thread", err)
			return
		}
		applied = 1
	} else {
		var current int64
		if err := tx.QueryRowContext(ctx, `SELECT version FROM page_artifact_threads WHERE page_id=? AND id=?`, rec.ID, threadID).Scan(&current); errors.Is(err, sql.ErrNoRows) {
			replyError(w, 404, "thread not found")
			return
		} else if err != nil {
			replyInternalError(w, h.logger, "read thread version", err)
			return
		}
		if current != *req.BaseVersion {
			artifactCommentError(w, 409, "stale_thread", "thread changed; reload before replying or resolving")
			return
		}
		applied = current + 1
		if operation == "reply" {
			var count int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM page_artifact_comments WHERE thread_id=?`, threadID).Scan(&count); err != nil {
				replyInternalError(w, h.logger, "count thread comments", err)
				return
			}
			if count >= maxArtifactThreadComments {
				artifactCommentError(w, 409, "comment_limit", "thread comment limit reached")
				return
			}
			if _, err := tx.ExecContext(ctx, `UPDATE page_artifact_threads SET version=?,updated_at=? WHERE id=? AND page_id=? AND version=?`, applied, stamp, threadID, rec.ID, current); err != nil {
				replyInternalError(w, h.logger, "advance thread version", err)
				return
			}
		} else {
			status, resolver, resolvedAt := "open", "", ""
			operation = "reopen"
			if *req.Resolved {
				status, resolver, resolvedAt = "resolved", user.ID, stamp
				operation = "resolve"
			}
			if _, err := tx.ExecContext(ctx, `UPDATE page_artifact_threads SET version=?,status=?,resolved_by_user_id=NULLIF(?,''),resolved_at=NULLIF(?,''),updated_at=? WHERE id=? AND page_id=? AND version=?`, applied, status, resolver, resolvedAt, stamp, threadID, rec.ID, current); err != nil {
				replyInternalError(w, h.logger, "resolve artifact thread", err)
				return
			}
		}
	}
	if operation == "create" || operation == "reply" {
		commentID = generateCUID()
		if _, err := tx.ExecContext(ctx, `INSERT INTO page_artifact_comments(id,thread_id,thread_version,body,author_user_id,created_at,author_agent_id,author_run_id) VALUES (?,?,?,?,?,?,NULLIF(?,''),NULLIF(?,''))`, commentID, threadID, applied, req.Body, user.ID, stamp, artifactAgentID(r), artifactRunID(r)); err != nil {
			replyInternalError(w, h.logger, "save artifact comment", err)
			return
		}
	}
	if commentID != "" {
		if err := h.recordArtifactCommentInitial(ctx, tx, rec.ID, threadID, commentID, req.Body, user.ID, artifactAgentID(r), artifactRunID(r), stamp); err != nil {
			replyInternalError(w, h.logger, "audit initial comment", err)
			return
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO page_artifact_comment_mutations(id,page_id,thread_id,comment_id,mutation_id,request_digest,actor_user_id,operation,result_version,created_at,actor_agent_id,actor_run_id) VALUES (?,?,?,NULLIF(?,''),?,?,?,?,?,?,NULLIF(?,''),NULLIF(?,''))`, generateCUID(), rec.ID, threadID, commentID, req.MutationID, digest, user.ID, operation, applied, stamp, artifactAgentID(r), artifactRunID(r)); err != nil {
		replyInternalError(w, h.logger, "save comment receipt", err)
		return
	}
	v, err := h.loadArtifactThread(ctx, rec.ID, threadID)
	if err != nil {
		replyInternalError(w, h.logger, "read saved thread", err)
		return
	}
	v.AppliedVersion = &applied
	if err := tx.Commit(); err != nil {
		replyInternalError(w, h.logger, "commit artifact comment", err)
		return
	}
	broadcastWorkspaceEvent(h.hub, ws, "page.updated", map[string]any{"page_id": rec.ID})
	status := 200
	if operation == "create" || operation == "reply" {
		status = 201
	}
	writeJSON(w, status, v)
}
