package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

type boardThreadWire struct {
	ID               string                `json:"id"`
	Anchor           boardCommentAnchor    `json:"anchor"`
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

func (h *PageHandler) boardCommentsPage(w http.ResponseWriter, r *http.Request, write bool) (*pageRecord, bool) {
	if agent, _ := pageGrantCallerIsAgent(r); agent || UserFromContext(r.Context()) == nil || artifactAgentID(r) != "" {
		replyError(w, 401, "human authentication required")
		return nil, false
	}
	rec, ok := h.boardPage(w, r, false)
	if !ok {
		return nil, false
	}
	if write && !h.mayComment(r.Context(), WorkspaceIDFromContext(r.Context()), UserFromContext(r.Context()).ID, RoleFromContext(r.Context()), rec) {
		replyError(w, 403, "page comment permission required")
		return nil, false
	}
	if !rec.HasBoard {
		replyError(w, 404, "board not found")
		return nil, false
	}
	return rec, true
}

func (h *PageHandler) loadBoardThread(ctx context.Context, pageID, threadID string) (*boardThreadWire, error) {
	q := h.pageAuthReader(ctx)
	v := &boardThreadWire{Comments: make([]artifactCommentWire, 0)}
	var anchor string
	err := q.QueryRowContext(ctx, `SELECT t.id,t.anchor_json,t.status,t.version,COALESCE(t.created_by_user_id,''),COALESCE(u.full_name,''),COALESCE(t.resolved_by_user_id,''),COALESCE(t.resolved_at,''),t.created_at,t.updated_at FROM page_board_threads t LEFT JOIN users u ON u.id=t.created_by_user_id WHERE t.page_id=? AND t.id=?`, pageID, threadID).Scan(&v.ID, &anchor, &v.Status, &v.Version, &v.CreatedByUserID, &v.CreatedByName, &v.ResolvedByUserID, &v.ResolvedAt, &v.CreatedAt, &v.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(anchor), &v.Anchor); err != nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, `SELECT c.id,c.body,COALESCE(c.author_user_id,''),COALESCE(u.full_name,''),c.created_at FROM page_board_comments c LEFT JOIN users u ON u.id=c.author_user_id WHERE c.thread_id=? ORDER BY c.thread_version LIMIT 50`, threadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var c artifactCommentWire
		if err := rows.Scan(&c.ID, &c.Body, &c.AuthorUserID, &c.AuthorName, &c.CreatedAt); err != nil {
			return nil, err
		}
		v.Comments = append(v.Comments, c)
	}
	return v, rows.Err()
}

func (h *PageHandler) ListBoardThreads(w http.ResponseWriter, r *http.Request) {
	artifactCommentHeaders(w)
	tx, err := h.db.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		replyInternalError(w, h.logger, "begin board discussion read", err)
		return
	}
	defer tx.Rollback()
	r = r.WithContext(context.WithValue(r.Context(), pageAuthReaderKey{}, tx))
	if user := UserFromContext(r.Context()); user != nil {
		var role string
		if tx.QueryRowContext(r.Context(), `SELECT role FROM workspace_members WHERE workspace_id=? AND user_id=?`, WorkspaceIDFromContext(r.Context()), user.ID).Scan(&role) == nil {
			r = r.WithContext(context.WithValue(r.Context(), ctxRole, role))
		}
	}

	rec, ok := h.boardCommentsPage(w, r, false)
	if !ok {
		return
	}
	rows, err := tx.QueryContext(r.Context(), `SELECT id FROM page_board_threads WHERE page_id=? ORDER BY created_at DESC,id DESC LIMIT 100`, rec.ID)
	if err != nil {
		replyInternalError(w, h.logger, "list board threads", err)
		return
	}
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			replyInternalError(w, h.logger, "list board threads", err)
			return
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		replyInternalError(w, h.logger, "list board threads", err)
		return
	}
	threads := make([]boardThreadWire, 0, len(ids))
	for _, id := range ids {
		v, err := h.loadBoardThread(r.Context(), rec.ID, id)
		if err != nil {
			replyInternalError(w, h.logger, "read board thread", err)
			return
		}
		threads = append(threads, *v)
	}
	canComment := h.mayComment(r.Context(), WorkspaceIDFromContext(r.Context()), UserFromContext(r.Context()).ID, RoleFromContext(r.Context()), rec)
	writeJSON(w, 200, map[string]any{"threads": threads, "can_comment": canComment, "can_resolve": h.mayEditSpec(r.Context(), WorkspaceIDFromContext(r.Context()), UserFromContext(r.Context()).ID, RoleFromContext(r.Context()), rec)})
}

type boardCommentMutation struct {
	Anchor      *boardCommentAnchor `json:"anchor,omitempty"`
	Body        string              `json:"body,omitempty"`
	BaseVersion *int64              `json:"base_version,omitempty"`
	Resolved    *bool               `json:"resolved,omitempty"`
	MutationID  string              `json:"mutation_id"`
}

func (h *PageHandler) CreateBoardThread(w http.ResponseWriter, r *http.Request) {
	h.mutateBoardThread(w, r, "create")
}
func (h *PageHandler) ReplyBoardThread(w http.ResponseWriter, r *http.Request) {
	h.mutateBoardThread(w, r, "reply")
}
func (h *PageHandler) ResolveBoardThread(w http.ResponseWriter, r *http.Request) {
	h.mutateBoardThread(w, r, "resolve")
}

func (h *PageHandler) mutateBoardThread(w http.ResponseWriter, r *http.Request, operation string) {
	artifactCommentHeaders(w)
	rec, ok := h.boardCommentsPage(w, r, true)
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
	if err := checkBoardCommentRequestJSON(body); err != nil {
		replyError(w, 400, "invalid comment request")
		return
	}
	var req boardCommentMutation
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		if errors.Is(err, errInvalidBoardAnchor) {
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
		if validateBoardCommentAnchor(req.Anchor) != nil || req.BaseVersion != nil || req.Resolved != nil {
			replyError(w, 422, "a valid schema_version 1 board or board-element anchor is required")
			return
		}
	} else {
		if req.BaseVersion == nil || *req.BaseVersion < 1 || req.Anchor != nil {
			replyError(w, 400, "base_version is required; anchor is immutable")
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
		Request   boardCommentMutation
	}{operation, threadID, req})
	digest := attachmentDigest(encoded)
	ws := WorkspaceIDFromContext(r.Context())
	user := UserFromContext(r.Context())
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		replyInternalError(w, h.logger, "begin board comment", err)
		return
	}
	defer tx.Rollback()
	fenced, err := tx.ExecContext(r.Context(), `UPDATE pages SET updated_at=updated_at WHERE id=? AND workspace_id=?`, rec.ID, ws)
	if err != nil {
		replyInternalError(w, h.logger, "fence board comment", err)
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
	rec, ok = h.boardCommentsPage(w, r.WithContext(ctx), true)
	if !ok {
		return
	}
	if operation == "resolve" && !h.mayEditSpec(ctx, ws, user.ID, role, rec) {
		replyError(w, 403, "page write permission required to resolve threads")
		return
	}
	var receiptThread, receiptDigest, receiptActor string
	var applied int64
	err = tx.QueryRowContext(ctx, `SELECT thread_id,request_digest,COALESCE(actor_user_id,''),result_version FROM page_board_comment_mutations WHERE page_id=? AND mutation_id=? AND actor_user_id=?`, rec.ID, req.MutationID, user.ID).Scan(&receiptThread, &receiptDigest, &receiptActor, &applied)
	if err == nil {
		if receiptDigest != digest || receiptActor != user.ID {
			artifactCommentError(w, 409, "mutation_conflict", "mutation_id was already used for another request")
			return
		}
		v, err := h.loadBoardThread(ctx, rec.ID, receiptThread)
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
	var total, recent int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(CASE WHEN created_at>? THEN 1 ELSE 0 END),0) FROM page_board_comment_mutations WHERE page_id=?`, tsformat.Format(now.Add(-time.Hour)), rec.ID).Scan(&total, &recent); err != nil {
		replyInternalError(w, h.logger, "check comment quota", err)
		return
	}
	if total >= maxArtifactCommentMutations {
		artifactCommentError(w, 409, "comment_mutation_limit", "board discussion mutation limit reached")
		return
	}
	if recent >= maxArtifactCommentHourlyMutations {
		w.Header().Set("Retry-After", "3600")
		artifactCommentError(w, 429, "rate_limited", "board discussion mutation rate limit reached")
		return
	}
	commentID := ""
	if operation == "create" {
		if !h.captureBoardCommentAnchor(w, r.WithContext(ctx), tx, rec.ID, req.Anchor) {
			return
		}
		var threads int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM page_board_threads WHERE page_id=?`, rec.ID).Scan(&threads); err != nil {
			replyInternalError(w, h.logger, "count artifact threads", err)
			return
		}
		if threads >= maxArtifactThreads {
			artifactCommentError(w, 409, "thread_limit", "board thread limit reached")
			return
		}
		threadID = generateCUID()
		anchor, _ := json.Marshal(req.Anchor)
		if _, err := tx.ExecContext(ctx, `INSERT INTO page_board_threads(id,page_id,anchor_json,created_by_user_id,created_at,updated_at) VALUES (?,?,?,?,?,?)`, threadID, rec.ID, string(anchor), user.ID, stamp, stamp); err != nil {
			replyInternalError(w, h.logger, "create board thread", err)
			return
		}
		applied = 1
	} else {
		var current int64
		if err := tx.QueryRowContext(ctx, `SELECT version FROM page_board_threads WHERE page_id=? AND id=?`, rec.ID, threadID).Scan(&current); errors.Is(err, sql.ErrNoRows) {
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
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM page_board_comments WHERE thread_id=?`, threadID).Scan(&count); err != nil {
				replyInternalError(w, h.logger, "count thread comments", err)
				return
			}
			if count >= maxArtifactThreadComments {
				artifactCommentError(w, 409, "comment_limit", "thread comment limit reached")
				return
			}
			if _, err := tx.ExecContext(ctx, `UPDATE page_board_threads SET version=?,updated_at=? WHERE id=? AND page_id=? AND version=?`, applied, stamp, threadID, rec.ID, current); err != nil {
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
			if _, err := tx.ExecContext(ctx, `UPDATE page_board_threads SET version=?,status=?,resolved_by_user_id=NULLIF(?,''),resolved_at=NULLIF(?,''),updated_at=? WHERE id=? AND page_id=? AND version=?`, applied, status, resolver, resolvedAt, stamp, threadID, rec.ID, current); err != nil {
				replyInternalError(w, h.logger, "resolve board thread", err)
				return
			}
		}
	}
	if operation == "create" || operation == "reply" {
		commentID = generateCUID()
		if _, err := tx.ExecContext(ctx, `INSERT INTO page_board_comments(id,thread_id,thread_version,body,author_user_id,created_at) VALUES (?,?,?,?,?,?)`, commentID, threadID, applied, req.Body, user.ID, stamp); err != nil {
			replyInternalError(w, h.logger, "save board comment", err)
			return
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO page_board_comment_mutations(id,page_id,thread_id,comment_id,mutation_id,request_digest,actor_user_id,operation,result_version,created_at) VALUES (?,?,?,NULLIF(?,''),?,?,?,?,?,?)`, generateCUID(), rec.ID, threadID, commentID, req.MutationID, digest, user.ID, operation, applied, stamp); err != nil {
		replyInternalError(w, h.logger, "save comment receipt", err)
		return
	}
	v, err := h.loadBoardThread(ctx, rec.ID, threadID)
	if err != nil {
		replyInternalError(w, h.logger, "read saved thread", err)
		return
	}
	v.AppliedVersion = &applied
	if err := tx.Commit(); err != nil {
		replyInternalError(w, h.logger, "commit board comment", err)
		return
	}
	broadcastWorkspaceEvent(h.hub, ws, "page.updated", map[string]any{"page_id": rec.ID})
	status := 200
	if operation == "create" || operation == "reply" {
		status = 201
	}
	writeJSON(w, status, v)
}
