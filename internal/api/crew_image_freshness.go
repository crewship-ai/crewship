package api

// #1845 — the button. The daily sweep tells an operator that a crew is running
// an older build of its image; these two routes are what they can do about it
// without shell access to the host.
//
// Read and write are split across two roles on purpose. Seeing that a crew is
// behind is a READ — a VIEWER watching a dashboard is exactly who should be
// able to notice — while refreshing pulls from a registry and force-removes a
// container that agents may be mid-run in, which is a mutation.

import (
	"database/sql"
	"log/slog"
	"net/http"

	"github.com/crewship-ai/crewship/internal/provider"
)

// CrewImageHandler serves crew image freshness. It holds the optional
// provider capability rather than a raw Docker client (which is what the
// neighbouring ProvisioningHandler carries) because the digest comparison
// belongs to whoever creates the containers: re-deriving "which image does
// this crew run" at the API layer would be a second reconstruction of
// EnsureCrewRuntime's CachedImage > Image > default chain, free to disagree
// with the one that actually starts.
type CrewImageHandler struct {
	db     *sql.DB
	logger *slog.Logger
	// freshness is nil when the process has no container provider
	// (--no-docker) or the provider cannot report image digests (the
	// apple-container path). Both answer 503 rather than a fabricated
	// "current" — "you are up to date" from something that did not look is
	// the worst possible answer this endpoint could give.
	freshness provider.CrewImageFreshness
}

// NewCrewImageHandler wires the handler. container may be nil, and may be a
// provider that does not implement the capability; both degrade to 503.
func NewCrewImageHandler(db *sql.DB, logger *slog.Logger, container provider.ContainerProvider) *CrewImageHandler {
	h := &CrewImageHandler{db: db, logger: logger}
	if fresh, ok := container.(provider.CrewImageFreshness); ok {
		h.freshness = fresh
	}
	return h
}

// crewImageConfig loads the crew's identity and image configuration, scoped to
// the caller's workspace. Returns ok=false having already written the response.
func (h *CrewImageHandler) crewImageConfig(w http.ResponseWriter, r *http.Request) (provider.CrewConfig, bool) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	crewID := r.PathValue("crewId")
	if crewID == "" {
		replyError(w, http.StatusBadRequest, "crew ID is required")
		return provider.CrewConfig{}, false
	}

	var cfg provider.CrewConfig
	err := h.db.QueryRowContext(r.Context(), `
		SELECT id, slug, COALESCE(runtime_image, ''), COALESCE(cached_image, '')
		  FROM crews
		 WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`,
		crewID, workspaceID,
	).Scan(&cfg.ID, &cfg.Slug, &cfg.Image, &cfg.CachedImage)
	if err == sql.ErrNoRows {
		// 404, not 403: the answer names an image reference and a container
		// id, so a crew in another workspace must be indistinguishable from
		// one that does not exist.
		replyError(w, http.StatusNotFound, "crew not found")
		return provider.CrewConfig{}, false
	}
	if err != nil {
		replyInternalError(w, h.logger, "query crew for image status", err)
		return provider.CrewConfig{}, false
	}
	return cfg, true
}

// unavailable reports that nothing on this instance can answer the question.
func (h *CrewImageHandler) unavailable(w http.ResponseWriter) bool {
	if h.freshness != nil {
		return false
	}
	writeJSON(w, http.StatusServiceUnavailable, map[string]string{
		"error": "image freshness is not available (no container provider that can report image digests)",
	})
	return true
}

// Status answers GET /api/v1/crews/{crewId}/image-status.
//
// Read-only in the strict sense: it never pulls and never removes. `reason`
// is part of the contract rather than a debug field — a client that renders
// `behind:false` without it cannot tell "confirmed current" from "the registry
// was unreachable", and would show a green tick for a check that never ran.
func (h *CrewImageHandler) Status(w http.ResponseWriter, r *http.Request) {
	cfg, ok := h.crewImageConfig(w, r)
	if !ok {
		return
	}
	if h.unavailable(w) {
		return
	}

	st, err := h.freshness.CrewImageState(r.Context(), cfg)
	if err != nil {
		replyInternalError(w, h.logger, "read crew image state", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"crew_id":         cfg.ID,
		"image":           st.Image,
		"container_id":    st.ContainerID,
		"running":         st.Running,
		"running_digest":  st.RunningDigest,
		"resolved_digest": st.ResolvedDigest,
		"behind":          st.Behind,
		"reason":          st.Reason,
	})
}

// Refresh answers POST /api/v1/crews/{crewId}/refresh-image: pull the crew's
// image, then drop its container so the next agent exec starts from the fresh
// copy.
//
// "update" (MANAGER+), matching the neighbouring provision/rebuild/
// restart-agents routes. It is deliberately not "manage": recycling a crew
// onto the current image is routine operations, and putting it behind
// OWNER/ADMIN would leave the people who run the crews unable to act on a
// notification addressed to them.
func (h *CrewImageHandler) Refresh(w http.ResponseWriter, r *http.Request) {
	if !canRole(RoleFromContext(r.Context()), "update") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}
	cfg, ok := h.crewImageConfig(w, r)
	if !ok {
		return
	}
	if h.unavailable(w) {
		return
	}

	res, err := h.freshness.RefreshCrewImage(r.Context(), cfg)
	if err != nil {
		// A failed pull must never read as success. The most common cause is a
		// throttling registry, and an operator who was told "refreshed" would
		// stop looking while still on the old image.
		replyInternalError(w, h.logger, "refresh crew image", err)
		return
	}
	h.logger.Info("crew image refreshed",
		"crew_id", cfg.ID, "image", res.Image,
		"previous_digest", res.PreviousDigest, "new_digest", res.NewDigest,
		"container_removed", res.ContainerRemoved)
	writeJSON(w, http.StatusOK, map[string]any{
		"crew_id":           cfg.ID,
		"image":             res.Image,
		"previous_digest":   res.PreviousDigest,
		"new_digest":        res.NewDigest,
		"container_removed": res.ContainerRemoved,
	})
}
