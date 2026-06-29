package api

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/crewship-ai/crewship/internal/journal"
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

	// provisionPollInterval is how often EnsureProvisioned polls job state
	// while blocking a dispatch on a build. 0 means the 2s default; tests set
	// a small value to exercise the wait loop without real wall-clock waits.
	provisionPollInterval time.Duration

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
	//
	// RWMutex (not Mutex): cache hits are the steady-state case and only
	// need RLock, so concurrent admin-UI polls don't serialize on each
	// other. Misses take the write Lock and re-check the cache to dedupe
	// concurrent refreshes down to a single Docker.ImageList call.
	imgListMu    sync.RWMutex
	imgListCache cachedImageList

	// bgCtx scopes the lifetime of the cleanup + GC goroutines. Stop()
	// cancels it so test helpers don't leak workers across the suite —
	// each NewProvisioningHandler call previously spawned an immortal
	// goroutine bound to context.Background().
	bgCtx    context.Context
	bgCancel context.CancelFunc

	// journal mirrors provisioning lifecycle (queued → building →
	// complete/failed) into the unified Crew Journal. nil maps to
	// noopEmitter so tests + early bring-up keep working without
	// requiring journal wiring. Set via SetJournal at router setup.
	journal journal.Emitter
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
	bgCtx, bgCancel := context.WithCancel(context.Background())
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
		bgCtx:          bgCtx,
		bgCancel:       bgCancel,
		journal:        noopEmitter{},
	}
	if docker != nil {
		h.gcClient = docker // *client.Client implements orphanGCClient
	}
	// Launch background cleanup so completed/failed jobs don't accumulate
	// forever. Tied to bgCtx so Stop() can shut both routines down — tests
	// previously leaked one goroutine per NewProvisioningHandler call into
	// the rest of the suite.
	go h.startJobCleanupRoutine(bgCtx)

	// Orphan GC — only runs when Docker is wired in. Does one-shot sweep at
	// startup (best-effort, non-fatal) then periodic sweeps. Both temp
	// containers (leaked if crewshipd SIGKILL'd during provision) and orphan
	// cache-images (leaked if crash between ContainerCommit and DB UPDATE)
	// are handled here.
	if docker != nil {
		go h.runStartupAndPeriodicGC(bgCtx)
	}

	return h
}

// Stop cancels the handler's background cleanup + GC goroutines. Tests must
// register `t.Cleanup(h.Stop)` after constructing a handler to avoid leaking
// workers across the rest of the suite. In production it's a no-op since the
// handler outlives the process.
func (h *ProvisioningHandler) Stop() {
	if h.bgCancel != nil {
		h.bgCancel()
	}
}

// SetJournal wires a journal emitter so provisioning lifecycle events
// (queued, building, complete, failed) surface in the unified Crew
// Journal alongside operational events. Pass nil to keep the existing
// WS-broadcast-only path.
func (h *ProvisioningHandler) SetJournal(j journal.Emitter) {
	if j == nil {
		h.journal = noopEmitter{}
		return
	}
	h.journal = j
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
