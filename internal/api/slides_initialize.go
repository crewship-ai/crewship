package api

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/slides"
)

// Registration is called only by the trusted, enabled Slides registrar.
func (h *slidesHumanRead) registerInitialize(r *Router) {
	authed, workspace := r.authMw.RequireAuth, r.authMw.RequireWorkspace
	// openapi: responses 200,400,401,403,404,409,429,503
	r.mux.Handle("POST /api/v1/pages/{slug}/slides/initialize", slidesReadHeaders(authed(workspace(http.HandlerFunc(h.initialize)))))
}

// Initialization belongs to the current human custodian of an existing Page.
// Workspace OWNER visibility alone does not grant this operation.
func (h *slidesHumanRead) initializePage(r *http.Request, reference string) (*pageRecord, error) {
	rec, err := h.pageForScope(r, reference, "pages:write")
	if err != nil {
		return nil, err
	}
	user := UserFromContext(r.Context())
	if user == nil || rec.OwnerUserID != user.ID {
		return nil, slides.ErrNotFound
	}
	return rec, nil
}

func (h *slidesHumanRead) initialize(w http.ResponseWriter, r *http.Request) {
	if h.pages == nil || h.store == nil || h.registry == nil {
		replyError(w, 503, "Slides persistence unavailable")
		return
	}
	rec, err := h.initializePage(r, r.PathValue("slug"))
	if err != nil {
		slidesInitializeError(w, err)
		return
	}
	key := slides.Key{WorkspaceID: WorkspaceIDFromContext(r.Context()), PageID: rec.ID}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, slides.MaxDeckBytes))
	if err != nil {
		replyError(w, 400, "bounded Slides initialize request required")
		return
	}
	request, err := slides.DecodeInitialize(raw)
	if err != nil {
		if errors.Is(err, slides.ErrSchema) || errors.Is(err, slides.ErrConflict) {
			slidesInitializeError(w, err)
		} else {
			replyError(w, 400, "invalid Slides initialize request")
		}
		return
	}
	if request.PageID != key.PageID {
		slidesInitializeError(w, slides.ErrConflict)
		return
	}
	operations, installed := h.registry.Slides()
	if !installed {
		replyError(w, 503, "Slides persistence unavailable")
		return
	}
	authorize := func(ctx context.Context, tx *sql.Tx) (string, error) {
		ctx = context.WithValue(ctx, pageAuthReaderKey{}, tx)
		current, err := h.initializePage(r.WithContext(ctx), "@"+key.PageID)
		if err != nil {
			return "", err
		}
		if current.ID != key.PageID {
			return "", slides.ErrNotFound
		}
		return "user:" + UserFromContext(ctx).ID, nil
	}
	// Native Create commits its receipt only after both live authority checks.
	// Never publish a candidate receipt returned together with an error.
	receipt, err := operations.Create(r.Context(), key, request, authorize)
	if err != nil {
		slidesInitializeError(w, err)
		return
	}
	writeJSON(w, 200, receipt)
}

func slidesInitializeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errContentCredentialScope):
		replyError(w, 403, "current pages:write scope required")
	case errors.Is(err, slides.ErrConflict), errors.Is(err, slides.ErrDuplicate):
		replyError(w, 409, "Slides initialize conflict")
	case errors.Is(err, slides.ErrQuota):
		replyError(w, 429, "Slides retained history quota exceeded")
	case errors.Is(err, slides.ErrInvalid):
		replyError(w, 400, "invalid Slides initialize request")
	case errors.Is(err, slides.ErrSchema), errors.Is(err, artifacts.ErrUnsupported):
		replyError(w, 409, "unsupported Slides schema")
	default:
		slidesHumanReadError(w, err)
	}
}
