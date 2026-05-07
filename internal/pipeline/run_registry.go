package pipeline

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ErrConcurrencyLimitReached is returned by RunRegistry.Acquire when
// the workspace already has the maximum number of in-flight runs for
// the given concurrency key. Callers should map to HTTP 429 with a
// Retry-After hint (or queue, depending on the surface).
var ErrConcurrencyLimitReached = errors.New("pipeline: concurrency limit reached")

// ErrRunNotFound is returned by RunRegistry.Cancel when no run with
// the given id is currently in flight on this process. Distinct from
// "run finished" — the registry only tracks live runs, so a
// completed run drops out of the map and is no longer cancellable.
var ErrRunNotFound = errors.New("pipeline: run not found in registry")

// RunInfo is the read-only snapshot returned by RunRegistry.Active.
// Used by the /runs/active API and the inbox UI to show what's
// currently executing across the workspace.
type RunInfo struct {
	RunID           string
	WorkspaceID     string
	PipelineID      string
	PipelineSlug    string
	ConcurrencyKey  string
	StartedAt       time.Time
	CancelRequested bool
}

// runEntry is the registry's internal record. Holds the cancel func
// for the run's context plus the metadata exposed via Active().
type runEntry struct {
	info   RunInfo
	cancel context.CancelFunc
}

// RunRegistry tracks in-flight pipeline runs for cancel + concurrency
// control. Single-instance only (no leader election); a multi-replica
// deployment would need a shared registry to avoid double-firing on
// concurrency-limited keys, but for single-binary that's not a concern.
//
// Thread-safety: a single mutex guards the map. Acquire / Release /
// Cancel are O(map_size) + O(1) operations; the lock is held just
// long enough to mutate, never across user code.
type RunRegistry struct {
	mu   sync.Mutex
	runs map[string]*runEntry
}

// NewRunRegistry builds an empty registry. One per process.
func NewRunRegistry() *RunRegistry {
	return &RunRegistry{runs: make(map[string]*runEntry)}
}

// AcquireOpts configures one Acquire call. Bundled in a struct so the
// signature stays small as new gates land (queue position, priority,
// rate limit) without touching every call site.
type AcquireOpts struct {
	RunID          string
	WorkspaceID    string
	PipelineID     string
	PipelineSlug   string
	ConcurrencyKey string // empty = no concurrency gate
	MaxConcurrent  int    // 0 with non-empty key = treat as 1 (serial)
}

// Acquire reserves a slot for a new run. Returns:
//   - cancellable child context (the registry stores its cancel
//     func so Cancel(runID) can pre-empt the run);
//   - release func that the caller MUST defer to free the slot;
//   - error: ErrConcurrencyLimitReached when the key is at capacity.
//
// When ConcurrencyKey is empty the registry skips the count check —
// the run is still tracked for cancel + Active() but doesn't compete
// for a key slot.
func (r *RunRegistry) Acquire(parent context.Context, opts AcquireOpts) (context.Context, func(), error) {
	r.mu.Lock()

	if opts.ConcurrencyKey != "" {
		max := opts.MaxConcurrent
		if max <= 0 {
			max = 1
		}
		count := 0
		for _, entry := range r.runs {
			if entry.info.WorkspaceID == opts.WorkspaceID && entry.info.ConcurrencyKey == opts.ConcurrencyKey {
				count++
			}
		}
		if count >= max {
			r.mu.Unlock()
			return nil, func() {}, ErrConcurrencyLimitReached
		}
	}

	ctx, cancel := context.WithCancel(parent)
	r.runs[opts.RunID] = &runEntry{
		info: RunInfo{
			RunID:          opts.RunID,
			WorkspaceID:    opts.WorkspaceID,
			PipelineID:     opts.PipelineID,
			PipelineSlug:   opts.PipelineSlug,
			ConcurrencyKey: opts.ConcurrencyKey,
			StartedAt:      time.Now(),
		},
		cancel: cancel,
	}
	r.mu.Unlock()

	release := func() {
		r.mu.Lock()
		if entry, ok := r.runs[opts.RunID]; ok {
			entry.cancel() // idempotent
			delete(r.runs, opts.RunID)
		}
		r.mu.Unlock()
	}
	return ctx, release, nil
}

// Cancel pre-empts an in-flight run by triggering its context. The
// run loop checks ctx.Err() between steps and propagates the
// cancellation into the AgentRunner, which kills the underlying CLI
// process. Returns ErrRunNotFound if the run already completed (or
// was never registered on this replica).
//
// Cancellation is best-effort: an agent_run that's mid-token-stream
// may emit a few more chunks before the CLI exits. The run's final
// status will be CANCELLED regardless.
func (r *RunRegistry) Cancel(runID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.runs[runID]
	if !ok {
		return ErrRunNotFound
	}
	entry.info.CancelRequested = true
	entry.cancel()
	return nil
}

// IsCancelRequested reports whether Cancel has been called for the
// given runID. Used by the executor when classifying a context-
// cancelled exit (USER cancel vs deadline expiry vs parent ctx
// teardown) so the run records CANCELLED instead of FAILED.
func (r *RunRegistry) IsCancelRequested(runID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.runs[runID]
	if !ok {
		return false
	}
	return entry.info.CancelRequested
}

// Active returns a snapshot of currently-running runs for a
// workspace. Empty workspaceID returns all runs (admin view).
//
// The returned slice is a copy — callers can iterate without holding
// the registry lock.
func (r *RunRegistry) Active(workspaceID string) []RunInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]RunInfo, 0, len(r.runs))
	for _, entry := range r.runs {
		if workspaceID != "" && entry.info.WorkspaceID != workspaceID {
			continue
		}
		out = append(out, entry.info)
	}
	return out
}

// Count returns how many runs match the given workspace + concurrency
// key. Used by tests and admin observability; the production
// concurrency gate is inside Acquire (atomic with the insert).
func (r *RunRegistry) Count(workspaceID, concurrencyKey string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, entry := range r.runs {
		if entry.info.WorkspaceID != workspaceID {
			continue
		}
		if concurrencyKey != "" && entry.info.ConcurrencyKey != concurrencyKey {
			continue
		}
		n++
	}
	return n
}
