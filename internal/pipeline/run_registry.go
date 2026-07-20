package pipeline

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrConcurrencyLimitReached is returned by RunRegistry.Acquire when
// the workspace already has the maximum number of in-flight runs for
// the given concurrency key. Callers should map to HTTP 429 with a
// Retry-After hint (or queue, depending on the surface).
var ErrConcurrencyLimitReached = errors.New("pipeline: concurrency limit reached")

// ErrDuplicateRunID is returned by RunRegistry.Acquire when the
// requested RunID is already live in the registry. Silently
// overwriting the existing entry would orphan the live run's cancel
// func (Cancel would pre-empt the wrong context) and let two
// executions share one run id — the exact double-resume hazard the
// boot-scan lifetime fence guards against. Callers treat this as
// "the run is already executing on this process".
var ErrDuplicateRunID = errors.New("pipeline: run id already registered in run registry")

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
// Thread-safety: a single mutex guards both maps. Acquire / Release /
// Cancel are O(1); the lock is held just long enough to mutate, never
// across user code.
type RunRegistry struct {
	mu   sync.Mutex
	runs map[string]*runEntry
	// keyCounts is the admission counter: live-run count per
	// workspace+concurrency key, so Acquire's gate is O(1) instead of
	// a scan of every live run. Mutated only under mu, always in the
	// same critical section as runs, and deleted at zero so a long-
	// lived process can't accumulate an entry per distinct key ever
	// seen. Runs with an empty concurrency key are not tracked here —
	// they don't compete for a slot.
	keyCounts map[string]int
}

// NewRunRegistry builds an empty registry. One per process.
func NewRunRegistry() *RunRegistry {
	return &RunRegistry{
		runs:      make(map[string]*runEntry),
		keyCounts: make(map[string]int),
	}
}

// concurrencyCountKey composes the keyCounts map key. NUL separates
// the two halves so a workspace id ending in the separator can't
// collide with a different (workspace, key) pair.
func concurrencyCountKey(workspaceID, concurrencyKey string) string {
	return workspaceID + "\x00" + concurrencyKey
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

	// Duplicate run id = a second execution trying to share a live
	// run's identity (e.g. a boot-resume racing a scheduler-started
	// run). Overwriting would orphan the live entry's cancel func and
	// let the loser's release() delete the winner's slot. Refuse.
	if _, exists := r.runs[opts.RunID]; exists {
		r.mu.Unlock()
		return nil, func() {}, ErrDuplicateRunID
	}

	countKey := ""
	if opts.ConcurrencyKey != "" {
		max := opts.MaxConcurrent
		if max <= 0 {
			max = 1
		}
		countKey = concurrencyCountKey(opts.WorkspaceID, opts.ConcurrencyKey)
		if r.keyCounts[countKey] >= max {
			r.mu.Unlock()
			return nil, func() {}, ErrConcurrencyLimitReached
		}
	}

	ctx, cancel := context.WithCancel(parent)
	entry := &runEntry{
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
	r.runs[opts.RunID] = entry
	if countKey != "" {
		r.keyCounts[countKey]++
	}
	r.mu.Unlock()

	// The identity check makes release both idempotent and safe
	// against a stale closure: once this entry is gone, a second call
	// (or a call after the same run id was re-acquired) must not evict
	// someone else's run — that would decrement a counter it never
	// incremented and leave the key permanently over- or under-booked.
	release := func() {
		r.mu.Lock()
		if cur, ok := r.runs[opts.RunID]; ok && cur == entry {
			cur.cancel() // idempotent
			delete(r.runs, opts.RunID)
			if countKey != "" {
				if n := r.keyCounts[countKey] - 1; n > 0 {
					r.keyCounts[countKey] = n
				} else {
					delete(r.keyCounts, countKey)
				}
			}
		}
		r.mu.Unlock()
	}
	return ctx, release, nil
}

// PrecheckConcurrency reports whether a ModeRun dispatch of the given
// DSL with the given inputs would be rejected by the concurrency gate
// RIGHT NOW. It mirrors Executor.Run's Acquire gate exactly — the same
// inputs-defaults merge, the same concurrency_key template render, and
// the same count-vs-max comparison against this registry's live
// entries — but does NOT reserve a slot. It exists for handlers that
// dispatch runs asynchronously (FireWebhook's 202-then-run) and must
// reject over-limit work synchronously (429 + Retry-After) before
// handing the sender an accepted response.
//
// Because no slot is reserved, there is a TOCTOU window between this
// check and the background Acquire: callers must still handle
// ErrConcurrencyLimitReached from the eventual Run. The executor's
// gate stays authoritative for every dispatch path.
//
// Returns nil when the DSL declares no concurrency_key (no gate) or a
// slot is free; ErrConcurrencyLimitReached when the key is at
// capacity; ErrConcurrencyKeyEmpty (wrapped) when the author asked for
// a gate but the key renders empty — the same config error the
// executor fails the run with.
func (r *RunRegistry) PrecheckConcurrency(ctx context.Context, dsl *DSL, workspaceID string, inputs map[string]any) error {
	if r == nil || dsl == nil || dsl.ConcurrencyKey == "" {
		return nil
	}
	key, _, keyErr := renderConcurrencyKey(ctx, dsl.ConcurrencyKey, mergeInputs(inputs, dsl))
	if keyErr != nil {
		return fmt.Errorf("%w: template %q", keyErr, dsl.ConcurrencyKey)
	}
	max := dsl.MaxConcurrent
	if max <= 0 {
		max = 1
	}
	if r.Count(workspaceID, key) >= max {
		return ErrConcurrencyLimitReached
	}
	return nil
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

// IsLive reports whether a run with the given id is currently
// tracked by this registry, i.e. executing on this process. The boot
// resume scan uses it as a lifetime fence: a pipeline_runs row whose
// id is live here was started by THIS lifetime (scheduler/HTTP) and
// must not be "resumed" a second time.
func (r *RunRegistry) IsLive(runID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.runs[runID]
	return ok
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
// key. An empty key means "every live run in the workspace", which the
// admission counters don't track, so that case still walks the map.
// Used by tests and admin observability; the production concurrency
// gate is inside Acquire (atomic with the insert).
func (r *RunRegistry) Count(workspaceID, concurrencyKey string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if concurrencyKey != "" {
		return r.keyCounts[concurrencyCountKey(workspaceID, concurrencyKey)]
	}
	n := 0
	for _, entry := range r.runs {
		if entry.info.WorkspaceID != workspaceID {
			continue
		}
		n++
	}
	return n
}
