package api

import (
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/crewship-ai/crewship/internal/chain"
)

// ChainHandler serves the one route that answers "what caused what" —
// GET /api/v1/chains/{anchor}.
//
// One route rather than a family of them because the question is one
// question. A client asking "why did this run happen" and a client asking
// "what did this issue set off" want the same connected component seen from
// different ends; splitting that into /issues/{id}/chain and
// /runs/{id}/chain would duplicate the traversal and let the two drift.
//
// The traversal itself lives in internal/chain, following the same split as
// paymaster and cartographer: the handler does auth -> parse -> dispatch ->
// shape, and owns no SQL.
type ChainHandler struct {
	db     *sql.DB
	logger *slog.Logger
}

func NewChainHandler(db *sql.DB, logger *slog.Logger) *ChainHandler {
	return &ChainHandler{db: db, logger: logger}
}

// Get serves GET /api/v1/chains/{anchor}?depth=<n>&limit=<n>
//
// anchor is an issue identifier (ENG-4), an issue id, a run id, a routine id
// or slug, an assignment id, or an inbox item id — resolved in that order.
//
// The response is a flat typed graph: nodes[], edges[], truncated, plus the
// gaps[] list naming the links the schema cannot carry. Flat rather than
// nested because the data is a graph, not a tree — a run reached from both
// its routine and its issue has two parents, and any tree encoding would have
// to pick one and drop the other edge.
func (h *ChainHandler) Get(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		replyError(w, http.StatusUnauthorized, "workspace required")
		return
	}
	anchor := r.PathValue("anchor")
	if anchor == "" {
		replyError(w, http.StatusBadRequest, "anchor required")
		return
	}

	opt := chain.Options{
		MaxDepth: atoiOrZero(r.URL.Query().Get("depth")),
		MaxNodes: atoiOrZero(r.URL.Query().Get("limit")),
	}

	g, err := chain.Walk(r.Context(), h.db, workspaceID, anchor, opt)
	if errors.Is(err, chain.ErrAnchorNotFound) {
		// 404 rather than 403 for an anchor that exists in another
		// workspace: the two must be indistinguishable, or this endpoint
		// becomes an oracle for identifiers in a tenant the caller cannot
		// read. chain.Walk already collapses both cases into this one error.
		replyError(w, http.StatusNotFound, "no issue, run, routine, assignment or inbox item matches that anchor in this workspace")
		return
	}
	if err != nil {
		h.logger.Error("chain walk", "error", err, "anchor", anchor)
		replyError(w, http.StatusInternalServerError, "load chain")
		return
	}

	writeJSON(w, http.StatusOK, g)
}

// atoiOrZero returns 0 for anything unparseable, which chain.Options treats as
// "use the default". A malformed depth is a client bug that should still get
// an answer — rejecting it with a 400 would make the endpoint fail on a stray
// query string appended by a proxy, and the caps are clamped server-side
// anyway so no value here can widen the walk past its ceiling.
func atoiOrZero(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}
