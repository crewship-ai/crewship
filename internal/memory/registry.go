package memory

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
)

// WorkspaceMemoryRegistry lazily instantiates one *WorkspaceMemory per
// workspace_id, caching the result so subsequent reads reuse the same
// FTS5 index without re-walking the disk. The registry is the
// production-side carrier for the workspace tier — `For(id)` returns
// the WorkspaceMemory the orchestrator's [WORKSPACE MEMORY] block
// queries via GetContext.
//
// Lazy init choice: registering every workspace up-front means walking
// the entire memory root at server start, which is fine for small
// installs but degrades with hundreds of workspaces. On-demand init
// pays the FTS5 build cost only when the workspace actually gets an
// agent run that consults memory — typically once per workspace per
// server lifetime.
//
// Concurrency: For() takes a read lock for the cache hit hot path and
// promotes to a write lock with double-check on miss. Two concurrent
// callers for the same new workspace collapse into a single init.
type WorkspaceMemoryRegistry struct {
	rootDir string
	logger  *slog.Logger

	mu    sync.RWMutex
	cache map[string]*WorkspaceMemory
}

// NewWorkspaceMemoryRegistry constructs an empty registry rooted at
// rootDir. Each workspace gets a subdirectory at rootDir/{workspaceID}.
// Logger is required so init failures surface; pass slog.Default() if
// the caller doesn't have a scoped logger handy.
func NewWorkspaceMemoryRegistry(rootDir string, logger *slog.Logger) *WorkspaceMemoryRegistry {
	if logger == nil {
		logger = slog.Default()
	}
	return &WorkspaceMemoryRegistry{
		rootDir: rootDir,
		logger:  logger,
		cache:   make(map[string]*WorkspaceMemory),
	}
}

// For returns the *WorkspaceMemory for the given workspace, lazily
// creating it on first call. Returns nil on init failure — the
// orchestrator's buildWorkspaceMemoryBlock treats nil as "no workspace
// tier" so a one-off init error never wedges agent runs.
func (r *WorkspaceMemoryRegistry) For(workspaceID string) *WorkspaceMemory {
	if r == nil || workspaceID == "" || r.rootDir == "" {
		return nil
	}

	r.mu.RLock()
	wm, ok := r.cache[workspaceID]
	r.mu.RUnlock()
	if ok {
		return wm
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	// Double-check after acquiring the write lock — a second caller
	// that beat us to the upgrade may have already populated.
	if wm, ok := r.cache[workspaceID]; ok {
		return wm
	}

	path := filepath.Join(r.rootDir, workspaceID)
	wm, err := NewWorkspaceMemory(path)
	if err != nil {
		r.logger.Warn("workspace memory init failed",
			"error", err, "workspace_id", workspaceID, "path", path)
		// Cache the nil so we don't retry the failing path on
		// every agent run; an explicit Reload() will be added if
		// operators report needing it.
		r.cache[workspaceID] = nil
		return nil
	}
	r.cache[workspaceID] = wm
	return wm
}

// Close shuts down every cached WorkspaceMemory engine. Idempotent;
// safe to call from a shutdown hook even when no workspaces have been
// initialised.
func (r *WorkspaceMemoryRegistry) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var firstErr error
	for id, wm := range r.cache {
		if wm == nil {
			continue
		}
		if err := wm.Close(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("close workspace memory %s: %w", id, err)
		}
	}
	r.cache = nil
	return firstErr
}
