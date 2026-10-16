package api

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/slides"
)

var errSlideAccess = errors.New("Slides access denied")

type slideHTTP struct {
	pages *PageHandler
	store *slides.Store
}

// authorize executes twice under the Store writer fence. Content never supplies
// the actor, ACL or run scope, including when replaying an acknowledged receipt.
type slidePermission int

const (
	slideRead slidePermission = iota
	slideWrite
	slideComment
)

func (h *slideHTTP) authorize(r *http.Request, key slides.Key, write, initialize bool) slides.Authorize {
	permission := slideRead
	if write {
		permission = slideWrite
	}
	return h.authorizePermission(r, key, permission, initialize)
}
func (h *slideHTTP) authorizePermission(r *http.Request, key slides.Key, permission slidePermission, initialize bool) slides.Authorize {
	write := permission != slideRead
	return func(ctx context.Context, tx *sql.Tx) (string, error) {
		if permission < slideRead || permission > slideComment {
			return "", errSlideAccess
		}
		ctx = context.WithValue(ctx, pageAuthReaderKey{}, tx)
		user := UserFromContext(ctx)
		if user == nil {
			return "", errSlideAccess
		}
		role, err := h.pages.liveContentHumanRole(ctx, key.WorkspaceID, user.ID)
		if err != nil {
			return "", errSlideAccess
		}
		ctx = context.WithValue(ctx, ctxRole, role)
		var slug string
		if tx.QueryRowContext(ctx, `SELECT slug FROM pages WHERE workspace_id=? AND id=?`, key.WorkspaceID, key.PageID).Scan(&slug) != nil {
			return "", errSlideAccess
		}
		rec, err := h.pages.loadPage(ctx, key.WorkspaceID, slug)
		if err != nil || rec.OwnerUserID == "" || rec.OwnerCrewID != "" || rec.HasDocument || rec.HasArtifact || rec.HasBoard || rec.HasProject || rec.HasApplication {
			return "", errSlideAccess
		}
		// Use this transaction, never acquire a second database connection.
		var occupied bool
		if tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM page_panels WHERE page_id=?) OR EXISTS(SELECT 1 FROM page_project_live WHERE page_id=?)`, key.PageID, key.PageID).Scan(&occupied) != nil || occupied {
			return "", errSlideAccess
		}
		if a, ok := ctx.Value(artifactRunKey{}).(*artifactRunAuthority); ok {
			if permission == slideComment || initialize || a.WorkspaceID != key.WorkspaceID || a.HumanUserID != user.ID || h.pages.checkDocumentRunPageScope(ctx, key.PageID, a) != nil || h.pages.checkArtifactRun(ctx, key.PageID, a) != nil {
				return "", errSlideAccess
			}
			// A Doc request is limited to its admitted Doc operations, even if corrupt
			// or stale storage happens to expose the same Page through another format.
			var docBound bool
			if tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM document_requests WHERE run_id=?) OR EXISTS(SELECT 1 FROM page_artifact_run_delegations WHERE run_id=? AND document_request_id IS NOT NULL)`, a.RunID, a.RunID).Scan(&docBound) != nil || docBound {
				return "", errSlideAccess
			}
			return "agent:" + a.AgentID + ":" + a.RunID, nil
		}
		if IsCLIToken(extractToken(r)) {
			if validateDocumentCLITokenTx(ctx, tx, extractToken(r), user.ID, write) != nil {
				return "", errSlideAccess
			}
		} else {
			var expiry string
			var revoked sql.NullString
			if user.SessionID == "" || tx.QueryRowContext(ctx, `SELECT expires_at,revoked_at FROM user_sessions WHERE id=? AND user_id=?`, user.SessionID, user.ID).Scan(&expiry, &revoked) != nil || revoked.Valid {
				return "", errSlideAccess
			}
			limit, err := time.Parse(time.RFC3339Nano, expiry)
			if err != nil || !time.Now().Before(limit) {
				return "", errSlideAccess
			}
		}
		if initialize && rec.OwnerUserID != user.ID {
			return "", errSlideAccess
		}
		if permission == slideComment {
			if !h.pages.mayComment(ctx, key.WorkspaceID, user.ID, role, rec) {
				return "", errSlideAccess
			}
		} else if write {
			if !h.pages.mayEditSpec(ctx, key.WorkspaceID, user.ID, role, rec) {
				return "", errSlideAccess
			}
		} else {
			viewer, err := h.pages.loadViewer(ctx, key.WorkspaceID, user.ID)
			if err != nil {
				return "", errSlideAccess
			}
			visible, err := h.pages.canSeePage(ctx, key.WorkspaceID, rec, nil, viewer)
			if err != nil || !visible {
				return "", errSlideAccess
			}
		}
		return "user:" + user.ID, nil
	}
}
func slideHTTPError(w http.ResponseWriter, err error) {
	status, message := 500, "Slides authority failed"
	switch {
	case errors.Is(err, errSlideAccess):
		status, message = 403, "Slides access denied"
	case errors.Is(err, slides.ErrNotFound):
		status, message = 404, "Slides not found"
	case errors.Is(err, slides.ErrConflict), errors.Is(err, slides.ErrDuplicate):
		status, message = 409, err.Error()
	case errors.Is(err, slides.ErrInvalid):
		status, message = 400, err.Error()
	case errors.Is(err, slides.ErrQuota):
		status, message = 429, "Slides retained history quota exceeded"
	}
	replyError(w, status, message)
}

// lookup only resolves an immutable Page key; no content is returned until the
// transaction's live authority succeeds. Absent and invisible Pages share 403.
func (h *slideHTTP) key(w http.ResponseWriter, r *http.Request) (slides.Key, bool) {
	key := slides.Key{WorkspaceID: WorkspaceIDFromContext(r.Context())}
	if h.pages.db.QueryRowContext(r.Context(), `SELECT id FROM pages WHERE workspace_id=? AND slug=?`, key.WorkspaceID, r.PathValue("slug")).Scan(&key.PageID) != nil {
		slideHTTPError(w, errSlideAccess)
		return key, false
	}
	return key, true
}
func (h *slideHTTP) Read(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	key, ok := h.key(w, r)
	if !ok {
		return
	}
	var revision int64
	if raw := r.PathValue("revision"); raw != "" {
		var err error
		revision, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || revision < 1 {
			replyError(w, 400, "positive revision required")
			return
		}
	}
	state, err := h.store.Read(r.Context(), key, revision, h.authorize(r, key, false, false))
	if err != nil {
		slideHTTPError(w, err)
		return
	}
	writeJSON(w, 200, state)
}
func (h *slideHTTP) History(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	key, ok := h.key(w, r)
	if !ok {
		return
	}
	history, err := h.store.History(r.Context(), key, h.authorize(r, key, false, false))
	if err != nil {
		slideHTTPError(w, err)
		return
	}
	writeJSON(w, 200, history)
}

type SlideOutlineElement struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Hash string `json:"hash"`
}
type SlideOutlineEntry struct {
	ID       string                `json:"id"`
	Hash     string                `json:"hash"`
	Elements []SlideOutlineElement `json:"elements"`
}
type SlideOutline struct {
	PageID       string              `json:"page_id"`
	Revision     int64               `json:"revision"`
	OrderVersion int64               `json:"order_version"`
	ContentHash  string              `json:"content_hash"`
	Slides       []SlideOutlineEntry `json:"slides"`
}

func (h *slideHTTP) Outline(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	key, ok := h.key(w, r)
	if !ok {
		return
	}
	state, err := h.store.Read(r.Context(), key, 0, h.authorize(r, key, false, false))
	if err != nil {
		slideHTTPError(w, err)
		return
	}
	outline := SlideOutline{PageID: key.PageID, Revision: state.Revision, OrderVersion: state.OrderVersion, ContentHash: state.ContentHash, Slides: []SlideOutlineEntry{}}
	for _, id := range state.Deck.SlideOrder {
		for _, slide := range state.Deck.Slides {
			if slide.ID != id {
				continue
			}
			hash, _ := slides.SlideHash(slide)
			entry := SlideOutlineEntry{ID: id, Hash: hash, Elements: []SlideOutlineElement{}}
			for _, element := range slide.Elements {
				hash, _ := slides.ElementHash(element)
				entry.Elements = append(entry.Elements, SlideOutlineElement{ID: element.ID, Type: element.Type, Hash: hash})
			}
			outline.Slides = append(outline.Slides, entry)
		}
	}
	writeJSON(w, 200, outline)
}
func (h *slideHTTP) Mutate(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	key, ok := h.key(w, r)
	if !ok {
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, slides.MaxDeckBytes))
	if err != nil {
		replyError(w, 400, "bounded Slides request required")
		return
	}
	operation := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	auth := h.authorize(r, key, true, operation == "initialize")
	var receipt slides.Receipt
	switch operation {
	case "initialize":
		var request slides.InitializeRequest
		request, err = slides.DecodeInitialize(raw)
		if err == nil {
			receipt, err = h.store.Initialize(r.Context(), key, request, auth)
		} else {
			replyError(w, 400, err.Error())
			return
		}
	case "patch":
		var request slides.PatchRequest
		request, err = slides.DecodePatch(raw)
		if err == nil {
			receipt, err = h.store.Patch(r.Context(), key, request, auth)
		} else {
			replyError(w, 400, err.Error())
			return
		}
	case "reorder":
		var request slides.ReorderRequest
		request, err = slides.DecodeReorder(raw)
		if err == nil {
			receipt, err = h.store.Reorder(r.Context(), key, request, auth)
		} else {
			replyError(w, 400, err.Error())
			return
		}
	case "restore":
		var request slides.RestoreRequest
		request, err = slides.DecodeRestore(raw)
		if err == nil {
			receipt, err = h.store.Restore(r.Context(), key, request, auth)
		} else {
			replyError(w, 400, err.Error())
			return
		}
	default:
		replyError(w, 404, "unknown Slides operation")
		return
	}
	if err != nil {
		slideHTTPError(w, err)
		return
	}
	if !receipt.Duplicate {
		broadcastWorkspaceEvent(h.pages.hub, key.WorkspaceID, "page.updated", map[string]any{"page_id": key.PageID})
	}
	writeJSON(w, 200, receipt)
}
