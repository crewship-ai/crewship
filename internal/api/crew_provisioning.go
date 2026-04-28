package api

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"sync"

	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/crewship-ai/crewship/internal/ws"
	"github.com/docker/docker/client"
)

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

	// wsHub is the WebSocket broadcaster used to push live `provision.progress`,
	// `provision.completed` and `provision.failed` events on the
	// `workspace:{id}` channel. May be nil — broadcasts are no-ops in that
	// case, which is the path tests take.
	wsHub *ws.Hub

	// imgListMu guards imgListCache. CacheList + sweepOrphanCacheImages both
	// page through all local images; memoizing that O(n) Docker call trims
	// the UI poll cost and the background sweep. Workspace-scoped
	// `referenced_by` data is still queried fresh per request (cheap index
	// lookup) so tenants never see cross-workspace state.
	imgListMu    sync.Mutex
	imgListCache cachedImageList
}

func NewProvisioningHandler(
	db *sql.DB,
	logger *slog.Logger,
	catalogFetcher *devcontainer.CatalogFetcher,
	runtimeFetcher *devcontainer.RuntimeFetcher,
	docker *client.Client,
	featureCacheDir string,
	wsHub *ws.Hub,
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
		wsHub:          wsHub,
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
