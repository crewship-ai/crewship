package api

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/crewship-ai/crewship/internal/slides"
)

var errSlidesHumanAccess = errors.New("Slides access denied")

// RegisterSlidesHuman explicitly installs schema-1 human read/initialization.
// Call once after NewRouter and before serving. Disabled installs no routes.
// It enables no other operations, image transport, editor or agent authority.
func (r *Router) RegisterSlidesHuman(enabled bool) {
	if !enabled {
		return
	}
	h := &slidesHumanHTTP{pages: r.pages, store: slides.New(r.db, slides.Options{})}
	authed, wsCtx := r.authMw.RequireAuth, r.authMw.RequireWorkspace
	// openapi: responses 200,400,401,403,404,409,500
	r.mux.Handle("GET /api/v1/pages/{slug}/slides", authed(wsCtx(http.HandlerFunc(h.read))))
	// openapi: responses 200,400,401,403,404,409,500
	r.mux.Handle("GET /api/v1/pages/{slug}/slides/revisions/{revision}", authed(wsCtx(http.HandlerFunc(h.read))))
	// openapi: responses 200,400,401,403,404,409,429,500
	r.authedMut("POST", "/api/v1/pages/{slug}/slides/initialize", roleInline, h.initialize)
}

type slidesHumanHTTP struct {
	pages *PageHandler
	store *slides.Store
}

// Current human credentials and private audience are checked transactionally.
// The Store invokes this twice under its writer fence, including exact retries.
func (h *slidesHumanHTTP) authorize(r *http.Request, key slides.Key, initialize bool, capture func(*pageRecord)) slides.Authorize {
	return func(ctx context.Context, tx *sql.Tx) (string, error) {
		if capture != nil {
			capture(nil)
		}
		ctx = context.WithValue(ctx, pageAuthReaderKey{}, tx)
		r = r.WithContext(ctx)
		user := UserFromContext(ctx)
		agent, _ := pageGrantCallerIsAgent(r)
		if user == nil || agent || projectAgentFrom(ctx) != nil || ctx.Value(artifactRunKey{}) != nil {
			return "", errSlidesHumanAccess
		}
		scope := "pages:read"
		if initialize {
			scope = "pages:write"
		}
		if validateContentHumanCredential(r, tx, scope) != nil {
			return "", errSlidesHumanAccess
		}
		var role string
		if initialize {
			var err error
			role, err = h.pages.liveContentHumanRole(ctx, key.WorkspaceID, user.ID)
			if err != nil {
				return "", errSlidesHumanAccess
			}
		} else {
			viewer, err := h.pages.livePrivatePageReader(ctx, key.WorkspaceID, user.ID)
			if err != nil || viewer == nil {
				return "", errSlidesHumanAccess
			}
			role = viewer.Role
		}
		ctx = context.WithValue(ctx, ctxRole, role)
		rec, err := h.pages.loadPageForRead(ctx, key.WorkspaceID, "@"+key.PageID)
		if err != nil || rec.OwnerUserID == "" || rec.OwnerCrewID != "" || !rec.PrivateAudience || rec.HasDocument || rec.HasProject || rec.HasApplication {
			return "", errSlidesHumanAccess
		}
		if _, _, err = privatePageOwnershipSQL(ctx, tx, "p"); err != nil {
			return "", errSlidesHumanAccess
		}
		var occupied bool
		if tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM page_panels WHERE page_id=?) OR EXISTS(SELECT 1 FROM page_project_live WHERE page_id=?)`, key.PageID, key.PageID).Scan(&occupied) != nil || occupied {
			return "", errSlidesHumanAccess
		}
		for _, table := range []string{"page_artifacts", "page_boards", "gridcraft_heads"} {
			var installed, attached bool
			if tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_schema WHERE name=? COLLATE NOCASE)`, table).Scan(&installed) != nil {
				return "", errSlidesHumanAccess
			}
			if installed && (tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM "+table+" WHERE page_id=?)", key.PageID).Scan(&attached) != nil || attached) {
				return "", errSlidesHumanAccess
			}
		}
		if initialize {
			if rec.OwnerUserID != user.ID || !h.pages.mayEditSpec(ctx, key.WorkspaceID, user.ID, role, rec) {
				return "", errSlidesHumanAccess
			}
		} else {
			viewer, err := h.pages.livePrivatePageReader(ctx, key.WorkspaceID, user.ID)
			if err != nil || !privatePageViewer(rec, viewer) {
				return "", errSlidesHumanAccess
			}
		}
		if capture != nil {
			capture(rec)
		}
		return "user:" + user.ID, nil
	}
}
func slidesHumanError(w http.ResponseWriter, err error) {
	code, message := 500, "Slides authority failed"
	switch {
	case errors.Is(err, errSlidesHumanAccess):
		code, message = 404, "Slides not found"
	case errors.Is(err, slides.ErrNotFound):
		code, message = 404, "Slides not found"
	case errors.Is(err, slides.ErrConflict), errors.Is(err, slides.ErrDuplicate):
		code, message = 409, "Slides identity or mutation conflict"
	case errors.Is(err, slides.ErrSchema):
		code, message = 409, "Slides schema unavailable"
	case errors.Is(err, slides.ErrInvalid):
		code, message = 400, "Invalid Slides request"
	case errors.Is(err, slides.ErrQuota):
		code, message = 429, "Slides retained history quota exceeded"
	}
	replyError(w, code, message)
}
func (h *slidesHumanHTTP) key(r *http.Request, initialize bool) (slides.Key, error) {
	key := slides.Key{WorkspaceID: WorkspaceIDFromContext(r.Context())}
	// Resolving a slug here does not authorize content. Initialization binds the
	// request's immutable Page ID again inside the fenced transaction.
	column, ref := "slug", r.PathValue("slug")
	if len(ref) > 0 && ref[0] == '@' {
		column, ref = "id", ref[1:]
	}
	err := h.pages.db.QueryRowContext(r.Context(), "SELECT id FROM pages WHERE workspace_id=? AND "+column+"=?", key.WorkspaceID, ref).Scan(&key.PageID)
	if err != nil {
		return key, errSlidesHumanAccess
	}
	// Hide private Page existence before reporting request-shape errors.
	// This read-only preflight ends before reading the request body; the Store
	// still rechecks live authority twice inside its fenced transaction.
	tx, err := h.pages.db.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return key, errSlidesHumanAccess
	}
	defer tx.Rollback()
	if _, err := h.authorize(r, key, initialize, nil)(r.Context(), tx); err != nil {
		return key, errSlidesHumanAccess
	}
	return key, nil
}
func (h *slidesHumanHTTP) read(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	key, err := h.key(r, false)
	if err != nil {
		slidesHumanError(w, err)
		return
	}
	var revision int64
	if raw := r.PathValue("revision"); raw != "" {
		revision, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || revision < 1 {
			replyError(w, 400, "positive revision required")
			return
		}
	}
	var record *pageRecord
	state, err := h.store.Read(r.Context(), key, revision, h.authorize(r, key, false, func(rec *pageRecord) { record = rec }))
	if err != nil {
		slidesHumanError(w, err)
		return
	}
	// Entire unsupported states remain private, including metadata/hashes.
	if state.SchemaVersion != slides.SchemaVersion {
		slidesHumanError(w, slides.ErrSchema)
		return
	}
	if record == nil {
		slidesHumanError(w, errSlidesHumanAccess)
		return
	}
	if !h.pages.auditPrivateOwnerAccess(w, r, record) {
		return
	}
	writeJSON(w, 200, state)
}
func (h *slidesHumanHTTP) initialize(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	key, err := h.key(r, true)
	if err != nil {
		slidesHumanError(w, err)
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, slides.MaxDeckBytes))
	if err != nil {
		replyError(w, 400, "bounded Slides request required")
		return
	}
	request, err := slides.DecodeInitialize(raw)
	if err != nil {
		replyError(w, 400, "invalid Slides initialization")
		return
	}
	if request.SchemaVersion != slides.SchemaVersion || request.Deck.SchemaVersion != slides.SchemaVersion {
		slidesHumanError(w, slides.ErrSchema)
		return
	}
	result, err := h.store.Initialize(r.Context(), key, request, h.authorize(r, key, true, nil))
	if err != nil {
		slidesHumanError(w, err)
		return
	}
	writeJSON(w, 200, result)
}
