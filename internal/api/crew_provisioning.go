package api

import (
	"database/sql"
	"log/slog"
	"net/http"

	"github.com/crewship-ai/crewship/internal/devcontainer"
)

// ProvisioningHandler provides endpoints for the devcontainer feature catalog
// and crew provisioning status.
type ProvisioningHandler struct {
	db             *sql.DB
	logger         *slog.Logger
	catalogFetcher *devcontainer.CatalogFetcher
	runtimeFetcher *devcontainer.RuntimeFetcher
}

// NewProvisioningHandler creates a ProvisioningHandler with the given database and logger.
// Fetchers may be nil; in that case the handler falls back to the embedded catalogs.
func NewProvisioningHandler(
	db *sql.DB,
	logger *slog.Logger,
	catalogFetcher *devcontainer.CatalogFetcher,
	runtimeFetcher *devcontainer.RuntimeFetcher,
) *ProvisioningHandler {
	return &ProvisioningHandler{
		db:             db,
		logger:         logger,
		catalogFetcher: catalogFetcher,
		runtimeFetcher: runtimeFetcher,
	}
}

// CatalogList returns the devcontainer feature catalog, optionally filtered
// by a search query parameter. Data comes from the dynamic fetcher when
// available; otherwise from the embedded fallback.
func (h *ProvisioningHandler) CatalogList(w http.ResponseWriter, r *http.Request) {
	search := r.URL.Query().Get("search")
	var all []devcontainer.CatalogEntry
	if h.catalogFetcher != nil {
		all = h.catalogFetcher.GetCatalog(r.Context())
	} else {
		all = append(all, devcontainer.FallbackCatalog...)
	}
	entries := devcontainer.FilterCatalog(all, search)

	writeJSON(w, http.StatusOK, map[string]any{
		"features": entries,
	})
}

// RuntimeCatalogList returns the mise runtime/tool catalog, optionally
// filtered by a search query parameter.
func (h *ProvisioningHandler) RuntimeCatalogList(w http.ResponseWriter, r *http.Request) {
	search := r.URL.Query().Get("search")
	var all []devcontainer.RuntimeCatalogEntry
	if h.runtimeFetcher != nil {
		all = h.runtimeFetcher.GetRuntimes(r.Context())
	} else {
		all = append(all, devcontainer.FallbackRuntimeCatalog...)
	}
	entries := devcontainer.FilterRuntimes(all, search)

	writeJSON(w, http.StatusOK, map[string]any{
		"runtimes": entries,
	})
}

// provisioningStatusResponse is the JSON shape returned by ProvisionStatus.
type provisioningStatusResponse struct {
	Status             string  `json:"status"`
	CachedImage        *string `json:"cached_image,omitempty"`
	ConfigHash         *string `json:"config_hash,omitempty"`
	DevcontainerConfig *string `json:"devcontainer_config,omitempty"`
}

// ProvisionStatus returns the provisioning status for a crew.
func (h *ProvisioningHandler) ProvisionStatus(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	crewID := r.PathValue("crewId")
	if crewID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "crew ID is required"})
		return
	}

	var devcontainerConfig, cachedImage, cfgHash sql.NullString
	err := h.db.QueryRowContext(r.Context(),
		`SELECT devcontainer_config, cached_image, config_hash
		 FROM crews WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`,
		crewID, workspaceID,
	).Scan(&devcontainerConfig, &cachedImage, &cfgHash)

	if err == sql.ErrNoRows {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "crew not found"})
		return
	}
	if err != nil {
		h.logger.Error("query crew provisioning status", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	resp := provisioningStatusResponse{Status: "idle"}

	if devcontainerConfig.Valid {
		resp.DevcontainerConfig = &devcontainerConfig.String
	}
	if cachedImage.Valid {
		resp.CachedImage = &cachedImage.String
		resp.Status = "completed"
	}
	if cfgHash.Valid {
		resp.ConfigHash = &cfgHash.String
	}

	writeJSON(w, http.StatusOK, resp)
}

// ProvisionTrigger validates the crew and marks provisioning intent. The actual
// provisioning (Docker build) is deferred to the orchestrator layer (Phase 7).
func (h *ProvisioningHandler) ProvisionTrigger(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())
	if !canRole(role, "create") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
		return
	}

	crewID := r.PathValue("crewId")
	if crewID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "crew ID is required"})
		return
	}

	var devcontainerConfig sql.NullString
	err := h.db.QueryRowContext(r.Context(),
		`SELECT devcontainer_config FROM crews WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`,
		crewID, workspaceID,
	).Scan(&devcontainerConfig)

	if err == sql.ErrNoRows {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "crew not found"})
		return
	}
	if err != nil {
		h.logger.Error("query crew for provisioning", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	if !devcontainerConfig.Valid || devcontainerConfig.String == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no devcontainer configuration"})
		return
	}

	// Clear cached_image + config_hash so the next container start will re-provision.
	_, err = h.db.ExecContext(r.Context(),
		`UPDATE crews SET cached_image = NULL, config_hash = NULL, updated_at = datetime('now')
		 WHERE id = ? AND workspace_id = ?`,
		crewID, workspaceID,
	)
	if err != nil {
		h.logger.Error("clear cached image for provisioning", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	h.logger.Info("provisioning triggered", "crew_id", crewID)

	writeJSON(w, http.StatusAccepted, map[string]string{
		"status":  "cache_invalidated",
		"message": "Provisioning cache cleared. Container will be re-provisioned on next agent start.",
	})
}

// ProvisionRebuild invalidates the cached image and triggers re-provisioning.
func (h *ProvisioningHandler) ProvisionRebuild(w http.ResponseWriter, r *http.Request) {
	// Rebuild is the same as trigger — it clears the cache and signals re-provision.
	h.ProvisionTrigger(w, r)
}
