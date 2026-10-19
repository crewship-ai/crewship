package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/slides"
)

// SlidesReadResponse contains only the currently admitted native deck.
type SlidesReadResponse struct {
	PageID        string      `json:"page_id"`
	SchemaVersion int         `json:"schema_version"`
	Revision      int64       `json:"revision"`
	OrderVersion  int64       `json:"order_version"`
	ContentHash   string      `json:"content_hash"`
	Deck          slides.Deck `json:"deck"`
}

type slidesHumanRead struct {
	pages    *PageHandler
	store    *slides.Store
	registry *artifacts.Registry
}

var errSlidesHumanOnly = errors.New("human Slides reader required")
var errSlidesCredential = errors.New("current human credential required")

// WithSlidesRead installs native Slides reads only when trusted server config
// opts in. The reader still checks the original human authority on every read.
func WithSlidesRead(enabled bool) RouterOption {
	return func(r *Router) { r.slidesEnabled = enabled }
}

// Availability is supplied by trusted server configuration, never by a request.
// The caller must opt in explicitly; no request can activate this route.
func (r *Router) registerSlidesReadRoutes(enabled bool) {
	if !enabled {
		return
	}
	h := &slidesHumanRead{pages: r.pages}
	if r.db != nil {
		h.store = slides.New(r.db, slides.Options{})
	}
	h.registry = artifacts.NewRegistry(artifacts.EnabledTypes{"slides": enabled}, nil, artifacts.WithSlidesAdapter(artifacts.NewSlidesAdapter(h.store, enabled)))
	authed, wsCtx := r.authMw.RequireAuth, r.authMw.RequireWorkspace
	// openapi: responses 200,401,403,404,409,500,503
	r.mux.Handle("GET /api/v1/pages/{slug}/slides", slidesReadHeaders(authed(wsCtx(http.HandlerFunc(h.read)))))
	h.registerInitialize(r)
}
func slidesReadHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func (h *slidesHumanRead) page(r *http.Request, reference string) (*pageRecord, error) {
	return h.pageForScope(r, reference, "pages:read")
}

// A write-only token may initialize its custodian's Page without acquiring read
// scope. All human, live membership and content-type checks remain shared.
func (h *slidesHumanRead) pageForScope(r *http.Request, reference, scope string) (*pageRecord, error) {
	user := UserFromContext(r.Context())
	agent, _ := pageGrantCallerIsAgent(r)
	if user == nil || agent || projectAgentFrom(r.Context()) != nil || r.Context().Value(artifactRunKey{}) != nil {
		return nil, errSlidesHumanOnly
	}
	ctx := r.Context()
	reader := h.pages.pageAuthReader(ctx)
	if err := validateContentHumanCredential(r, reader, scope); err != nil {
		return nil, errors.Join(errSlidesCredential, err)
	}
	workspace := WorkspaceIDFromContext(ctx)
	role, err := h.pages.liveContentHumanRole(ctx, workspace, user.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, slides.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	ctx = context.WithValue(ctx, ctxRole, role)
	rec, err := h.pages.loadPage(ctx, workspace, reference)
	if err != nil {
		return nil, err
	}
	viewer, err := h.pages.loadViewer(ctx, workspace, user.ID)
	if err != nil {
		return nil, err
	}
	visible, err := h.pages.canSeePage(ctx, workspace, rec, nil, viewer)
	if err != nil {
		return nil, err
	}
	if !visible {
		return nil, slides.ErrNotFound
	}
	if rec.OwnerUserID == "" || rec.OwnerCrewID != "" || rec.HasDocument || rec.HasProject || rec.HasApplication {
		return nil, slides.ErrNotFound
	}
	var panels int
	if err = reader.QueryRowContext(ctx, "SELECT count(*) FROM page_panels WHERE page_id=?", rec.ID).Scan(&panels); err != nil {
		return nil, err
	}
	if panels != 0 {
		return nil, slides.ErrNotFound
	}
	// Discover only current known extension schemas; absent features are not
	// requirements, while incompatible installed schemas fail closed.
	if _, _, err = privatePageOwnershipSQL(ctx, reader, "p"); err != nil {
		return nil, err
	}
	for _, table := range []string{"page_boards", "page_artifacts"} {
		var installed, attached bool
		if err = reader.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sqlite_schema WHERE name=? COLLATE NOCASE)", table).Scan(&installed); err != nil {
			return nil, err
		}
		if installed {
			if err = reader.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM "+table+" WHERE page_id=?)", rec.ID).Scan(&attached); err != nil {
				return nil, err
			}
			if attached {
				return nil, slides.ErrNotFound
			}
		}
	}
	return rec, nil
}
func (h *slidesHumanRead) read(w http.ResponseWriter, r *http.Request) {
	if h.pages == nil || h.store == nil {
		replyError(w, 503, "Slides persistence unavailable")
		return
	}
	rec, err := h.page(r, r.PathValue("slug"))
	if err != nil {
		slidesHumanReadError(w, err)
		return
	}
	key := slides.Key{WorkspaceID: WorkspaceIDFromContext(r.Context()), PageID: rec.ID}
	// Request-local snapshot of the last successful transaction authority check.
	var admitted *pageRecord
	authorize := func(ctx context.Context, tx *sql.Tx) (string, error) {
		admitted = nil
		ctx = context.WithValue(ctx, pageAuthReaderKey{}, tx)
		current, err := h.page(r.WithContext(ctx), "@"+key.PageID)
		if err != nil {
			return "", err
		}
		if current.ID != key.PageID {
			return "", slides.ErrNotFound
		}
		snapshot := *current
		admitted = &snapshot
		return "user:" + UserFromContext(ctx).ID, nil
	}
	operations, installed := h.registry.Slides()
	if !installed {
		replyError(w, 503, "Slides persistence unavailable")
		return
	}
	state, err := operations.Read(r.Context(), key, authorize)
	// Store may populate a candidate state before its final authorization fails.
	// No candidate, metadata or bytes may be published on any error.
	if err != nil {
		slidesHumanReadError(w, err)
		return
	}
	if admitted == nil {
		slidesHumanReadError(w, slides.ErrNotFound)
		return
	}
	if state.SchemaVersion != 1 || state.Deck.SchemaVersion != 1 {
		slidesHumanReadError(w, slides.ErrSchema)
		return
	}
	// Audit the final admitted custodian before bytes, outside the Store writer
	// transaction so neither rollback nor a busy writer can lose/deadlock it.
	if !h.pages.auditPrivateOwnerAccess(w, r, admitted) {
		return
	}
	writeJSON(w, 200, SlidesReadResponse{PageID: key.PageID, SchemaVersion: state.SchemaVersion, Revision: state.Revision, OrderVersion: state.OrderVersion, ContentHash: state.ContentHash, Deck: state.Deck})
}
func slidesHumanReadError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errSlidesHumanOnly):
		replyError(w, 403, "human Slides credentials required")
	case errors.Is(err, errContentCredentialScope):
		replyError(w, 403, "current pages:read scope required")
	case errors.Is(err, errSlidesCredential):
		replyError(w, 401, "current human credential required")
	case errors.Is(err, sql.ErrNoRows), errors.Is(err, slides.ErrNotFound):
		replyError(w, 404, "Slides not found")
	case errors.Is(err, slides.ErrSchema), errors.Is(err, artifacts.ErrUnsupported):
		replyError(w, 409, "unsupported Slides schema")
	default:
		replyError(w, 503, "Slides read unavailable")
	}
}
