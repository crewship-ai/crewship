package api

import (
	"context"
	"database/sql"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/crewship-ai/crewship/internal/slides"
)

type SlidesCommentThreadsResponse struct {
	Items      []slides.CommentThread `json:"items"`
	NextCursor string                 `json:"next_cursor"`
	Watermark  int64                  `json:"watermark"`
	HasMore    bool                   `json:"has_more"`
	CanComment bool                   `json:"can_comment"`
	CanResolve bool                   `json:"can_resolve"`
}
type SlidesCommentThreadResponse struct {
	Thread     slides.CommentThread `json:"thread"`
	CanComment bool                 `json:"can_comment"`
	CanResolve bool                 `json:"can_resolve"`
}
type SlidesCommentHistoryResponse struct {
	Items      []slides.CommentEvent `json:"items"`
	NextCursor string                `json:"next_cursor"`
	Watermark  int64                 `json:"watermark"`
	HasMore    bool                  `json:"has_more"`
	CanComment bool                  `json:"can_comment"`
	CanResolve bool                  `json:"can_resolve"`
}

// Comments are human-only, including reads and resolution. Keeping this guard
// outside the generic content authority prevents future run transports from
// inheriting a human comment capability through read/write fallback.
func (h *slideHTTP) commentAuthority(r *http.Request, key slides.Key, permission slidePermission) slides.Authorize {
	base := h.authorizePermission(r, key, permission, false)
	return func(ctx context.Context, tx *sql.Tx) (string, error) {
		if _, ok := ctx.Value(artifactRunKey{}).(*artifactRunAuthority); ok {
			return "", errSlideAccess
		}
		return base(ctx, tx)
	}
}

// Flags are advisory snapshots captured under the same read authority fence.
// Every write/replay still checks its own exact credential and live permission.
func (h *slideHTTP) commentReadAuthority(r *http.Request, key slides.Key, canComment, canResolve *bool) slides.Authorize {
	base := h.commentAuthority(r, key, slideRead)
	return func(ctx context.Context, tx *sql.Tx) (string, error) {
		actor, err := base(ctx, tx)
		if err != nil {
			return "", err
		}
		ctx = context.WithValue(ctx, pageAuthReaderKey{}, tx)
		user := UserFromContext(ctx)
		role, err := h.pages.liveContentHumanRole(ctx, key.WorkspaceID, user.ID)
		if err != nil {
			return "", err
		}
		ctx = context.WithValue(ctx, ctxRole, role)
		var slug string
		if tx.QueryRowContext(ctx, `SELECT slug FROM pages WHERE workspace_id=? AND id=?`, key.WorkspaceID, key.PageID).Scan(&slug) != nil {
			return "", errSlideAccess
		}
		rec, err := h.pages.loadPage(ctx, key.WorkspaceID, slug)
		if err != nil {
			return "", err
		}
		writableScope := true
		if IsCLIToken(extractToken(r)) {
			writableScope = validateDocumentCLITokenTx(ctx, tx, extractToken(r), user.ID, true) == nil
		}
		*canComment = writableScope && h.pages.mayComment(ctx, key.WorkspaceID, user.ID, role, rec)
		*canResolve = writableScope && h.pages.mayEditSpec(ctx, key.WorkspaceID, user.ID, role, rec)
		return actor, nil
	}
}
func slideCommentPage(w http.ResponseWriter, r *http.Request) (slides.CommentPageRequest, bool) {
	page := slides.CommentPageRequest{Cursor: r.URL.Query().Get("cursor")}
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > slides.MaxCommentPageSize {
			replyError(w, 400, "limit must be between 1 and 100")
			return page, false
		}
		page.Limit = limit
	}
	return page, true
}
func (h *slideHTTP) ReadComments(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	key, ok := h.key(w, r)
	if !ok {
		return
	}
	page, ok := slideCommentPage(w, r)
	if !ok {
		return
	}
	var canComment, canResolve bool
	auth := h.commentReadAuthority(r, key, &canComment, &canResolve)
	threadID := r.PathValue("threadId")
	if threadID == "" {
		result, err := h.store.CommentThreads(r.Context(), key, page, auth)
		if err != nil {
			slideHTTPError(w, err)
			return
		}
		writeJSON(w, 200, SlidesCommentThreadsResponse{Items: result.Items, NextCursor: result.NextCursor, Watermark: result.Watermark, HasMore: result.HasMore, CanComment: canComment, CanResolve: canResolve})
		return
	}
	if strings.HasSuffix(r.URL.Path, "/history") {
		result, err := h.store.CommentHistory(r.Context(), key, threadID, page, auth)
		if err != nil {
			slideHTTPError(w, err)
			return
		}
		writeJSON(w, 200, SlidesCommentHistoryResponse{Items: result.Items, NextCursor: result.NextCursor, Watermark: result.Watermark, HasMore: result.HasMore, CanComment: canComment, CanResolve: canResolve})
		return
	}
	result, err := h.store.CommentThread(r.Context(), key, threadID, auth)
	if err != nil {
		slideHTTPError(w, err)
		return
	}
	writeJSON(w, 200, SlidesCommentThreadResponse{Thread: result, CanComment: canComment, CanResolve: canResolve})
}
func (h *slideHTTP) MutateComment(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	key, ok := h.key(w, r)
	if !ok {
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, slides.MaxCommentRequestBytes))
	if err != nil {
		replyError(w, 400, "bounded comment JSON required")
		return
	}
	operation := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	permission := slideComment
	if operation == "resolve" {
		permission = slideWrite
	}
	auth := h.commentAuthority(r, key, permission)
	var receipt slides.CommentReceipt
	switch operation {
	case "comments":
		var request slides.CreateCommentRequest
		request, err = slides.DecodeCreateComment(raw)
		if err != nil {
			replyError(w, 400, err.Error())
			return
		}
		receipt, err = h.store.CreateComment(r.Context(), key, request, auth)
	case "replies":
		var request slides.ReplyCommentRequest
		request, err = slides.DecodeReplyComment(raw)
		if err != nil {
			replyError(w, 400, err.Error())
			return
		}
		receipt, err = h.store.ReplyComment(r.Context(), key, r.PathValue("threadId"), request, auth)
	case "resolve":
		var request slides.ResolveCommentRequest
		request, err = slides.DecodeResolveComment(raw)
		if err != nil {
			replyError(w, 400, err.Error())
			return
		}
		receipt, err = h.store.ResolveComment(r.Context(), key, r.PathValue("threadId"), request, auth)
	default:
		replyError(w, 404, "unknown Slides comment operation")
		return
	}
	if err != nil {
		slideHTTPError(w, err)
		return
	}
	if !receipt.Duplicate {
		broadcastWorkspaceEvent(h.pages.hub, key.WorkspaceID, "slides.comments.changed", map[string]any{})
	}
	writeJSON(w, 200, receipt)
}
