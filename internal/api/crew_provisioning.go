package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
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

// orphanGCClient is the minimal slice of the Docker API used by the orphan-GC
// sweepers and CacheList. Exists as an interface so tests can swap in a fake
// without standing up a real Docker daemon. Satisfied by *docker.Client.
type orphanGCClient interface {
	ContainerList(ctx context.Context, options container.ListOptions) ([]container.Summary, error)
	ContainerRemove(ctx context.Context, containerID string, options container.RemoveOptions) error
	ImageList(ctx context.Context, options image.ListOptions) ([]image.Summary, error)
	ImageRemove(ctx context.Context, imageID string, options image.RemoveOptions) ([]image.DeleteResponse, error)
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
	gcClient    orphanGCClient // narrowed view for sweepers + CacheList; usually == docker
	provisioner *devcontainer.Provisioner

	// In-memory provisioning job state, keyed by crewID. MVP only.
	mu   sync.RWMutex
	jobs map[string]*ProvisionJob

	// rateLimiter caps concurrent and recent provisions per workspace. Guards
	// against runaway triggers (e.g. a buggy loop) exhausting Docker resources.
	rateLimiter *provisionRateLimiter

	// imgListMu guards imgListCache. CacheList + sweepOrphanCacheImages both
	// page through all local images; memoizing that O(n) Docker call trims
	// the UI poll cost and the background sweep. Workspace-scoped
	// `referenced_by` data is still queried fresh per request (cheap index
	// lookup) so tenants never see cross-workspace state.
	imgListMu    sync.Mutex
	imgListCache cachedImageList
}

type cachedImageList struct {
	images    []image.Summary
	fetchedAt time.Time
}

// imageListCacheTTL is short on purpose: cache images mutate only via our
// own Provision (which we can't invalidate from here without coupling) and
// CacheDelete (which we DO invalidate). The TTL bounds the staleness window
// for the admin UI while still cutting the common-case poll cost.
const imageListCacheTTL = 10 * time.Second

// Rate limit constants. Per-workspace bucket.
const (
	maxConcurrentProvisionsPerWorkspace = 3
	maxProvisionStartsPerMinute         = 10
)

// provisionRateLimiter tracks in-flight provisions per workspace and caps the
// number of starts per sliding 1-minute window. In-memory only; single-instance
// only (MVP). Horizontal scale would move this to Redis.
type provisionRateLimiter struct {
	mu           sync.Mutex
	running      map[string]int         // workspace_id -> current concurrent count
	recentStarts map[string][]time.Time // workspace_id -> start timestamps in last minute
}

func newProvisionRateLimiter() *provisionRateLimiter {
	return &provisionRateLimiter{
		running:      make(map[string]int),
		recentStarts: make(map[string][]time.Time),
	}
}

// tryAcquire attempts to reserve a provisioning slot for the given workspace.
// Returns an error describing the limit hit when capacity is exhausted.
// Successful acquires must be paired with release().
func (r *provisionRateLimiter) tryAcquire(workspaceID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Prune stale timestamps (older than 1 minute).
	now := time.Now()
	cutoff := now.Add(-1 * time.Minute)
	starts := r.recentStarts[workspaceID]
	fresh := starts[:0]
	for _, t := range starts {
		if t.After(cutoff) {
			fresh = append(fresh, t)
		}
	}
	r.recentStarts[workspaceID] = fresh

	if r.running[workspaceID] >= maxConcurrentProvisionsPerWorkspace {
		return fmt.Errorf("rate limited: %d concurrent provisions already running (max %d)",
			r.running[workspaceID], maxConcurrentProvisionsPerWorkspace)
	}
	if len(fresh) >= maxProvisionStartsPerMinute {
		return fmt.Errorf("rate limited: %d provisions started in last minute (max %d)",
			len(fresh), maxProvisionStartsPerMinute)
	}

	r.running[workspaceID]++
	r.recentStarts[workspaceID] = append(fresh, now)
	return nil
}

// release decrements the concurrent-provision counter. Safe to call multiple
// times per workspace; will not go below zero.
func (r *provisionRateLimiter) release(workspaceID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.running[workspaceID] > 0 {
		r.running[workspaceID]--
	}
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
	h := &ProvisioningHandler{
		db:             db,
		logger:         logger,
		catalogFetcher: catalogFetcher,
		runtimeFetcher: runtimeFetcher,
		docker:         docker,
		provisioner:    provisioner,
		jobs:           make(map[string]*ProvisionJob),
		rateLimiter:    newProvisionRateLimiter(),
	}
	if docker != nil {
		h.gcClient = docker // *client.Client implements orphanGCClient
	}
	// Launch background cleanup so completed/failed jobs don't accumulate
	// forever. Handler lives for the process lifetime; Background ctx is OK.
	go h.startJobCleanupRoutine(context.Background())

	// Orphan GC — only runs when Docker is wired in. Does one-shot sweep at
	// startup (best-effort, non-fatal) then periodic sweeps. Both temp
	// containers (leaked if crewshipd SIGKILL'd during provision) and orphan
	// cache-images (leaked if crash between ContainerCommit and DB UPDATE)
	// are handled here.
	if docker != nil {
		go h.runStartupAndPeriodicGC(context.Background())
	}

	return h
}

// Orphan GC tunables. A provisioning run should finish well under tempContainerMaxAge;
// anything older is almost certainly a crash remnant.
const (
	tempContainerMaxAge = 1 * time.Hour
	orphanGCInterval    = 30 * time.Minute
	orphanGCSweepCap    = 200 // defensive — don't stall on pathological state

	// cacheImageMinAge is a safety window: a crewship-cache:* image younger
	// than this is skipped by the orphan sweeper even if no crew row points
	// at it. Rationale — Provision() writes the DB row AFTER `docker commit`.
	// Between those two steps (seconds at most) the image legitimately looks
	// "unreferenced". A 5-minute floor is many orders of magnitude larger
	// than the actual race window, at zero operational cost.
	cacheImageMinAge = 5 * time.Minute

	// cacheGCAutoDeleteEnv gates destructive removal of unreferenced
	// crewship-cache:* images. Default (unset/false) is log-only — an operator
	// has to opt in to deletion because dropping an image someone just built
	// locally is surprising.
	cacheGCAutoDeleteEnv = "CREWSHIP_CACHE_GC_AUTODELETE"
)

// runStartupAndPeriodicGC performs one sweep at process startup and then
// schedules recurring sweeps every orphanGCInterval. Exits on ctx.Done.
func (h *ProvisioningHandler) runStartupAndPeriodicGC(ctx context.Context) {
	// Startup sweep — tolerate failures (Docker may not yet be reachable).
	h.sweepOrphanTempContainers(ctx)
	h.sweepOrphanCacheImages(ctx)

	ticker := time.NewTicker(orphanGCInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.sweepOrphanTempContainers(ctx)
			h.sweepOrphanCacheImages(ctx)
		}
	}
}

// sweepOrphanTempContainers removes temp containers created by the provisioner
// that have outlived a full provisioning run (tempContainerMaxAge). A normal
// run cleans up via defer; this sweeper only catches the crash/SIGKILL path.
// Filtered by the Provisioner's label so we never touch unrelated containers.
func (h *ProvisioningHandler) sweepOrphanTempContainers(ctx context.Context) {
	if h.gcClient == nil {
		return
	}
	start := time.Now()
	labelFilter := devcontainer.TempContainerLabelKey + "=" + devcontainer.TempContainerLabelValue
	containers, err := h.gcClient.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: filters.NewArgs(filters.Arg("label", labelFilter)),
	})
	if err != nil {
		h.logger.Warn("orphan temp-container GC: list failed", "error", err)
		return
	}
	cutoff := time.Now().Add(-tempContainerMaxAge).Unix()
	removed := 0
	for i, c := range containers {
		if i >= orphanGCSweepCap {
			h.logger.Warn("orphan temp-container GC: sweep cap hit; remaining containers skipped",
				"cap", orphanGCSweepCap, "total", len(containers))
			break
		}
		if c.Created >= cutoff {
			continue
		}
		if err := h.gcClient.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true}); err != nil {
			h.logger.Warn("orphan temp-container GC: remove failed", "container", c.ID, "error", err)
			continue
		}
		removed++
	}
	if removed > 0 {
		h.logger.Info("orphan temp-container GC: removed stale temp containers",
			"removed", removed, "scanned", len(containers), "duration", time.Since(start))
	} else {
		h.logger.Debug("orphan temp-container GC: nothing to remove",
			"scanned", len(containers), "duration", time.Since(start))
	}
}

// sweepOrphanCacheImages finds crewship-cache:* images that have no referencing
// crew row across ALL workspaces. These are leaks from a crash window between
// ContainerCommit and the crews.cached_image UPDATE. Removal is opt-in via
// CREWSHIP_CACHE_GC_AUTODELETE=true — default is log-only for visibility.
func (h *ProvisioningHandler) sweepOrphanCacheImages(ctx context.Context) {
	if h.gcClient == nil {
		return
	}
	// 1. Collect every cached_image still referenced by any crew across all
	//    workspaces (no workspace filter — an image referenced by another
	//    tenant's crew must never be deleted).
	rows, err := h.db.QueryContext(ctx,
		`SELECT DISTINCT cached_image FROM crews
		 WHERE cached_image IS NOT NULL AND cached_image != ''
		       AND deleted_at IS NULL`)
	if err != nil {
		h.logger.Warn("orphan cache-image GC: query failed", "error", err)
		return
	}
	defer rows.Close()
	referenced := make(map[string]struct{})
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			h.logger.Warn("orphan cache-image GC: scan failed", "error", err)
			return
		}
		referenced[tag] = struct{}{}
	}
	// Critical: if iteration died mid-stream, `referenced` is incomplete and
	// using it to decide orphans could delete a still-live cache image.
	if err := rows.Err(); err != nil {
		h.logger.Warn("orphan cache-image GC: rows iteration failed", "error", err)
		return
	}

	// 2. Compare against the local image set. Any crewship-cache:* tag with
	//    no referencing row AND older than cacheImageMinAge is an orphan.
	//    The age floor closes the race between ContainerCommit and the DB
	//    UPDATE inside Provision() — a freshly-committed image legitimately
	//    looks "unreferenced" until the caller persists the link.
	imgs, err := h.listLocalImagesCached(ctx)
	if err != nil {
		h.logger.Warn("orphan cache-image GC: image list failed", "error", err)
		return
	}
	autoDelete := strings.EqualFold(os.Getenv(cacheGCAutoDeleteEnv), "true") ||
		os.Getenv(cacheGCAutoDeleteEnv) == "1"

	safeCutoff := time.Now().Add(-cacheImageMinAge).Unix()
	orphans := make([]string, 0)
	tooYoung := 0
	for _, img := range imgs {
		for _, tag := range img.RepoTags {
			if !strings.HasPrefix(tag, cacheImagePrefix) {
				continue
			}
			if _, ok := referenced[tag]; ok {
				continue
			}
			if img.Created > safeCutoff {
				tooYoung++
				continue
			}
			orphans = append(orphans, tag)
		}
	}
	if len(orphans) == 0 {
		h.logger.Debug("orphan cache-image GC: nothing to report", "skipped_too_young", tooYoung)
		return
	}
	if !autoDelete {
		h.logger.Info("orphan cache-image GC: unreferenced cache images detected (log-only, set CREWSHIP_CACHE_GC_AUTODELETE=true to remove)",
			"orphans", orphans, "count", len(orphans), "skipped_too_young", tooYoung)
		return
	}
	removed := 0
	for _, tag := range orphans {
		if _, err := h.gcClient.ImageRemove(ctx, tag, image.RemoveOptions{Force: false, PruneChildren: true}); err != nil {
			h.logger.Warn("orphan cache-image GC: remove failed", "tag", tag, "error", err)
			continue
		}
		removed++
	}
	h.logger.Info("orphan cache-image GC: removed unreferenced cache images",
		"removed", removed, "total_orphans", len(orphans), "skipped_too_young", tooYoung)
}

// cleanupOldJobs removes completed/failed jobs older than 1h from the jobs map.
// Called periodically from the provisioning handler lifetime.
func (h *ProvisioningHandler) cleanupOldJobs() {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now()
	const ttl = 1 * time.Hour
	for crewID, job := range h.jobs {
		if job.Status != "completed" && job.Status != "failed" {
			continue
		}
		if job.CompletedAt == nil {
			continue
		}
		if now.Sub(*job.CompletedAt) > ttl {
			delete(h.jobs, crewID)
		}
	}
}

// startJobCleanupRoutine runs cleanupOldJobs every 10 minutes.
// Shuts down when ctx is cancelled.
func (h *ProvisioningHandler) startJobCleanupRoutine(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.cleanupOldJobs()
		}
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

	// Rate limit per workspace. Acquired here (after the per-crew dedupe
	// check) so the limit reflects actual provisioning starts, not rejected
	// duplicates. runProvisioning must release the slot on every exit path.
	if err := h.rateLimiter.tryAcquire(workspaceID); err != nil {
		h.mu.Lock()
		delete(h.jobs, crewID)
		h.mu.Unlock()
		writeJSON(w, http.StatusTooManyRequests, map[string]string{
			"error": err.Error(),
		})
		return
	}

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
	// Release the rate-limit slot regardless of success/failure.
	defer h.rateLimiter.release(workspaceID)

	// Panic recovery — mark job as failed and log, don't crash the server.
	// Registered AFTER rate-limit release so LIFO order runs this first:
	// job state is updated, then the slot is freed.
	defer func() {
		if r := recover(); r != nil {
			h.mu.Lock()
			if j, ok := h.jobs[crewID]; ok {
				j.Status = "failed"
				j.Error = fmt.Sprintf("internal error: %v", r)
				now := time.Now()
				j.CompletedAt = &now
			}
			h.mu.Unlock()
			h.logger.Error("provisioning panicked",
				"crew_id", crewID,
				"workspace_id", workspaceID,
				"panic", r,
				"stack", string(debug.Stack()),
			)
		}
	}()

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

	// Serialize aggregated feature requirements (privileged, capAdd, mounts,
	// containerEnv) so the runtime can apply them when starting the crew
	// container. Without this, features like DinD (privileged:true +
	// docker.sock mount) would silently not work at runtime.
	var reqJSON sql.NullString
	if reqBytes, marshalErr := json.Marshal(result.Requirements); marshalErr != nil {
		h.logger.Warn("marshal cached_requirements failed, storing NULL",
			"crew_id", crewID, "error", marshalErr)
	} else if !isEmptyRequirements(result.Requirements) {
		reqJSON = sql.NullString{String: string(reqBytes), Valid: true}
	}

	// Persist the cached image reference on the crew row. Use a fresh context
	// (not the 30-min provisioning ctx, which may be near its deadline).
	updateCtx, updateCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer updateCancel()
	_, err = h.db.ExecContext(updateCtx,
		`UPDATE crews SET cached_image = ?, config_hash = ?, cached_requirements = ?, updated_at = datetime('now')
		 WHERE id = ? AND workspace_id = ?`,
		result.CachedImage, result.ConfigHash, reqJSON, crewID, workspaceID,
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
		`UPDATE crews SET cached_image = NULL, config_hash = NULL, cached_requirements = NULL, updated_at = datetime('now')
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

// cacheImagePrefix is the Docker repository name used for all provisioned
// devcontainer caches. CacheList and CacheDelete refuse to touch anything
// outside this namespace.
const cacheImagePrefix = "crewship-cache:"

// CacheImageInfo describes a cached devcontainer image for the CLI/UI.
type CacheImageInfo struct {
	Tag          string   `json:"tag"`
	Size         int64    `json:"size"`
	CreatedAt    int64    `json:"created_at"` // Unix seconds (Docker image.Summary.Created is int64).
	ReferencedBy []string `json:"referenced_by"`
}

// referencedCacheImages returns the set of cached_image tags currently
// referenced by live (non-deleted) crews, with the list of crew slugs that
// reference each tag. Used by both the list and prune paths to prevent
// deleting an image a crew still depends on.
func (h *ProvisioningHandler) referencedCacheImages(ctx context.Context) (map[string][]string, error) {
	rows, err := h.db.QueryContext(ctx,
		`SELECT cached_image, slug FROM crews
		 WHERE cached_image IS NOT NULL AND cached_image != '' AND deleted_at IS NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	refs := make(map[string][]string)
	for rows.Next() {
		var tag, slug string
		if err := rows.Scan(&tag, &slug); err != nil {
			return nil, err
		}
		refs[tag] = append(refs[tag], slug)
	}
	return refs, rows.Err()
}

// CacheList returns metadata for every crewship-cache:* image on the host,
// annotated with the list of crew slugs referencing it.
//
// Workspace scoping: the image store is host-global (Docker has no concept
// of workspaces), so this endpoint returns all cache images visible to the
// daemon. The referenced_by field is filtered to crews in the requester's
// workspace, matching how other provisioning endpoints behave.
func (h *ProvisioningHandler) CacheList(w http.ResponseWriter, r *http.Request) {
	role := RoleFromContext(r.Context())
	if !canRole(role, "read") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
		return
	}
	if h.docker == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "cache management not available (Docker client not configured)",
		})
		return
	}

	workspaceID := WorkspaceIDFromContext(r.Context())

	// Build referenced_by map scoped to this workspace.
	refRows, err := h.db.QueryContext(r.Context(),
		`SELECT cached_image, slug FROM crews
		 WHERE cached_image IS NOT NULL AND cached_image != ''
		       AND deleted_at IS NULL AND workspace_id = ?`,
		workspaceID)
	if err != nil {
		h.logger.Error("query referenced cache images", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	defer refRows.Close()
	refs := make(map[string][]string)
	for refRows.Next() {
		var tag, slug string
		if err := refRows.Scan(&tag, &slug); err != nil {
			h.logger.Error("scan referenced cache image", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
			return
		}
		refs[tag] = append(refs[tag], slug)
	}

	imgs, err := h.listLocalImagesCached(r.Context())
	if err != nil {
		h.logger.Error("docker image list", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	out := make([]CacheImageInfo, 0)
	for _, img := range imgs {
		for _, tag := range img.RepoTags {
			if !strings.HasPrefix(tag, cacheImagePrefix) {
				continue
			}
			out = append(out, CacheImageInfo{
				Tag:          tag,
				Size:         img.Size,
				CreatedAt:    img.Created,
				ReferencedBy: refs[tag],
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"images": out})
}

// listLocalImagesCached returns the local Docker image set with a short-lived
// memoization. Callers must treat the slice as read-only.
func (h *ProvisioningHandler) listLocalImagesCached(ctx context.Context) ([]image.Summary, error) {
	h.imgListMu.Lock()
	defer h.imgListMu.Unlock()

	if h.imgListCache.images != nil && time.Since(h.imgListCache.fetchedAt) < imageListCacheTTL {
		return h.imgListCache.images, nil
	}
	imgs, err := h.gcClient.ImageList(ctx, image.ListOptions{})
	if err != nil {
		return nil, err
	}
	h.imgListCache = cachedImageList{images: imgs, fetchedAt: time.Now()}
	return imgs, nil
}

// invalidateImageListCache forces the next listLocalImagesCached call to hit
// Docker. Called after CacheDelete; Provision can't reach us from the
// devcontainer package without coupling, so we rely on the TTL for that path.
func (h *ProvisioningHandler) invalidateImageListCache() {
	h.imgListMu.Lock()
	h.imgListCache = cachedImageList{}
	h.imgListMu.Unlock()
}

// CacheDelete removes a single crewship-cache:* image. Refuses if the image
// is referenced by any crew (across all workspaces, not just the caller's —
// we never want to delete a live crew's cache from another workspace).
// Query param ?force=true bypasses the referenced check.
func (h *ProvisioningHandler) CacheDelete(w http.ResponseWriter, r *http.Request) {
	role := RoleFromContext(r.Context())
	if !canRole(role, "delete") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
		return
	}
	if h.docker == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "cache management not available (Docker client not configured)",
		})
		return
	}

	tag := r.PathValue("tag")
	if tag == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "tag is required"})
		return
	}
	// Hard-enforce the crewship-cache namespace — we never delete an
	// arbitrary Docker image on behalf of a caller.
	if !strings.HasPrefix(tag, cacheImagePrefix) {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "only crewship-cache:* tags may be deleted",
		})
		return
	}

	force := r.URL.Query().Get("force") == "true"

	if !force {
		refs, err := h.referencedCacheImages(r.Context())
		if err != nil {
			h.logger.Error("query referenced cache images", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
			return
		}
		if crews, ok := refs[tag]; ok && len(crews) > 0 {
			writeJSON(w, http.StatusConflict, map[string]any{
				"error":         "image is referenced by live crews; pass ?force=true to delete anyway",
				"referenced_by": crews,
			})
			return
		}
	}

	// Use the narrow gcClient interface — same underlying *client.Client, but
	// keeps the destructive surface aligned with the orphan sweeper for both
	// readability and test parity.
	_, err := h.gcClient.ImageRemove(r.Context(), tag, image.RemoveOptions{Force: force, PruneChildren: true})
	if err != nil {
		h.logger.Error("docker image remove", "tag", tag, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	// Cached image list no longer reflects local state.
	h.invalidateImageListCache()
	writeJSON(w, http.StatusOK, map[string]string{"tag": tag, "status": "deleted"})
}

// isEmptyRequirements reports whether an AggregatedRequirements value has no
// runtime customizations. Used to store NULL instead of "{}" in the DB so the
// absence of requirements is trivially distinguishable at query time.
func isEmptyRequirements(r devcontainer.AggregatedRequirements) bool {
	return !r.Privileged && !r.Init &&
		len(r.ContainerEnv) == 0 &&
		len(r.Mounts) == 0 &&
		len(r.CapAdd) == 0 &&
		len(r.SecurityOpt) == 0 &&
		len(r.PostStartCommands) == 0
}
