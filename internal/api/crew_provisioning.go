package api

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/docker/docker/client"
)

// ProvisionJob tracks the state of an in-progress provisioning operation.
// In-memory state is fine for single-instance deployment (MVP). If crewshipd
// is ever scaled horizontally this must be moved to a shared store.
type ProvisionJob struct {
	CrewID      string
	Status      string // "pending", "running", "completed", "failed"
	StartedAt   time.Time
	CompletedAt *time.Time
	Error       string
	CachedImage string
	ConfigHash  string
}

// ProvisioningHandler provides endpoints for the devcontainer feature catalog
// and crew provisioning (trigger, status, rebuild).
type ProvisioningHandler struct {
	db             *sql.DB
	logger         *slog.Logger
	catalogFetcher *devcontainer.CatalogFetcher
	runtimeFetcher *devcontainer.RuntimeFetcher

	// Provisioner pipeline. May be nil in tests or when no Docker client is
	// available -- in that case the trigger endpoint returns 503.
	docker      *client.Client
	provisioner *devcontainer.Provisioner

	// In-memory provisioning job state, keyed by crewID. MVP only.
	mu   sync.RWMutex
	jobs map[string]*ProvisionJob
}

// NewProvisioningHandler creates a ProvisioningHandler with the given database and logger.
// Fetchers may be nil; in that case the handler falls back to the embedded catalogs.
// If docker is nil, the provisioner is disabled and ProvisionTrigger returns 503.
func NewProvisioningHandler(
	db *sql.DB,
	logger *slog.Logger,
	catalogFetcher *devcontainer.CatalogFetcher,
	runtimeFetcher *devcontainer.RuntimeFetcher,
	docker *client.Client,
	featureCacheDir string,
) *ProvisioningHandler {
	var provisioner *devcontainer.Provisioner
	if docker != nil {
		featureDL := devcontainer.NewFeatureDownloader(featureCacheDir, logger)
		installer := devcontainer.NewInstaller(docker, logger)
		provisioner = devcontainer.NewProvisioner(docker, installer, featureDL, logger)
	}
	return &ProvisioningHandler{
		db:             db,
		logger:         logger,
		catalogFetcher: catalogFetcher,
		runtimeFetcher: runtimeFetcher,
		docker:         docker,
		provisioner:    provisioner,
		jobs:           make(map[string]*ProvisionJob),
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

// nullStringPtr converts a sql.NullString to a *string (nil when invalid).
func nullStringPtr(s sql.NullString) *string {
	if !s.Valid {
		return nil
	}
	return &s.String
}

// ProvisionStatus returns the provisioning status for a crew. If an active job
// exists in memory, its status (pending/running/completed/failed) is returned;
// otherwise the status is derived from the persisted cached_image column.
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

	// Determine status -- check in-memory jobs first, then fall back to DB.
	h.mu.RLock()
	job, hasJob := h.jobs[crewID]
	h.mu.RUnlock()

	resp := map[string]any{
		"devcontainer_config": nullStringPtr(devcontainerConfig),
		"cached_image":        nullStringPtr(cachedImage),
		"config_hash":         nullStringPtr(cfgHash),
	}

	status := "idle"
	if hasJob {
		status = job.Status
		if job.Error != "" {
			resp["error"] = job.Error
		}
		resp["started_at"] = job.StartedAt.Format(time.RFC3339)
		if job.CompletedAt != nil {
			resp["completed_at"] = job.CompletedAt.Format(time.RFC3339)
		}
	} else if cachedImage.Valid && cachedImage.String != "" {
		status = "completed"
	}
	resp["status"] = status

	writeJSON(w, http.StatusOK, resp)
}

// ProvisionTrigger starts an asynchronous provisioning job for the given crew.
// Returns 202 immediately; the caller polls ProvisionStatus for progress.
// Returns 503 if the Docker client is not configured, 409 if a job is already
// in progress for the same crew.
func (h *ProvisioningHandler) ProvisionTrigger(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())
	if !canRole(role, "create") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
		return
	}

	if h.provisioner == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "provisioning not available (Docker client not configured)",
		})
		return
	}

	crewID := r.PathValue("crewId")
	if crewID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "crew ID is required"})
		return
	}

	// Load the crew's devcontainer configuration from the DB.
	var devcontainerCfg, miseCfg, runtimeImage sql.NullString
	err := h.db.QueryRowContext(r.Context(),
		`SELECT devcontainer_config, mise_config, runtime_image
		 FROM crews WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`,
		crewID, workspaceID,
	).Scan(&devcontainerCfg, &miseCfg, &runtimeImage)

	if err == sql.ErrNoRows {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "crew not found"})
		return
	}
	if err != nil {
		h.logger.Error("query crew for provisioning", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	if !devcontainerCfg.Valid || devcontainerCfg.String == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "crew has no devcontainer_config to provision",
		})
		return
	}

	// Reject if a job is already running/pending for this crew.
	h.mu.Lock()
	if existing, ok := h.jobs[crewID]; ok && (existing.Status == "pending" || existing.Status == "running") {
		status := existing.Status
		h.mu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]string{
			"error":  "provisioning already in progress",
			"status": status,
		})
		return
	}
	job := &ProvisionJob{
		CrewID:    crewID,
		Status:    "pending",
		StartedAt: time.Now(),
	}
	h.jobs[crewID] = job
	h.mu.Unlock()

	// Kick off async provisioning. Never block the HTTP handler.
	go h.runProvisioning(crewID, workspaceID, devcontainerCfg.String, miseCfg.String, runtimeImage.String, job)

	h.logger.Info("provisioning triggered", "crew_id", crewID)

	writeJSON(w, http.StatusAccepted, map[string]string{
		"status":  "started",
		"message": "Provisioning started. Monitor with 'crewship crew provision status <slug>'.",
	})
}

// runProvisioning executes the full provisioning pipeline asynchronously.
// It updates the in-memory job state and persists the result to the DB.
func (h *ProvisioningHandler) runProvisioning(crewID, workspaceID, cfgJSON, miseJSON, runtimeImg string, job *ProvisionJob) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	h.mu.Lock()
	job.Status = "running"
	h.mu.Unlock()

	cfg, err := devcontainer.ParseBytes([]byte(cfgJSON))
	if err != nil {
		h.markJobFailed(job, fmt.Errorf("parse devcontainer_config: %w", err))
		return
	}

	// Resolve base image: runtime_image takes precedence (user override) over cfg.Image.
	baseImage := cfg.Image
	if runtimeImg != "" {
		baseImage = runtimeImg
	}
	if baseImage == "" {
		h.markJobFailed(job, fmt.Errorf("no base image in devcontainer config or runtime_image"))
		return
	}
	// Ensure the config hash reflects the resolved base image.
	cfg.Image = baseImage

	h.logger.Info("starting provisioning",
		"crew_id", crewID,
		"base_image", baseImage,
		"features", len(cfg.Features),
	)

	result, err := h.provisioner.Provision(ctx, baseImage, cfg, miseJSON)
	if err != nil {
		h.markJobFailed(job, fmt.Errorf("provision: %w", err))
		return
	}

	// Persist the cached image reference on the crew row. Use a fresh context
	// (not the 30-min provisioning ctx, which may be near its deadline).
	updateCtx, updateCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer updateCancel()
	_, err = h.db.ExecContext(updateCtx,
		`UPDATE crews SET cached_image = ?, config_hash = ?, updated_at = datetime('now')
		 WHERE id = ? AND workspace_id = ?`,
		result.CachedImage, result.ConfigHash, crewID, workspaceID,
	)
	if err != nil {
		h.markJobFailed(job, fmt.Errorf("update db: %w", err))
		return
	}

	now := time.Now()
	h.mu.Lock()
	job.Status = "completed"
	job.CompletedAt = &now
	job.CachedImage = result.CachedImage
	job.ConfigHash = result.ConfigHash
	h.mu.Unlock()

	h.logger.Info("provisioning completed",
		"crew_id", crewID,
		"cached_image", result.CachedImage,
		"config_hash", result.ConfigHash,
	)
}

// markJobFailed records a failure on the job and logs it.
func (h *ProvisioningHandler) markJobFailed(job *ProvisionJob, err error) {
	h.logger.Error("provisioning failed", "crew_id", job.CrewID, "error", err)
	now := time.Now()
	h.mu.Lock()
	job.Status = "failed"
	job.CompletedAt = &now
	job.Error = err.Error()
	h.mu.Unlock()
}

// ProvisionRebuild invalidates the cached image and triggers re-provisioning.
// Implemented as: clear DB cache columns, then delegate to ProvisionTrigger.
func (h *ProvisioningHandler) ProvisionRebuild(w http.ResponseWriter, r *http.Request) {
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
	// Clear cache so Provisioner won't short-circuit on the existing tag.
	_, err := h.db.ExecContext(r.Context(),
		`UPDATE crews SET cached_image = NULL, config_hash = NULL, updated_at = datetime('now')
		 WHERE id = ? AND workspace_id = ?`,
		crewID, workspaceID,
	)
	if err != nil {
		h.logger.Error("clear cached image for rebuild", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	h.ProvisionTrigger(w, r)
}
