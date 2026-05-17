package consolidate

import (
	"context"
	"log/slog"
	"path/filepath"
	"sync"
	"time"
)

// PostRunTrigger fires the consolidator opportunistically when an
// agent run completes, debouncing per (workspace, crew) so a chatty
// crew producing many short runs doesn't queue up a consolidation
// dogpile. The literature signal: Letta's sleep-time agents +
// OpenClaw Dreaming both fire on idle, not just on cron — running
// the heavy LLM extraction pass while the agent is *between* tasks
// (rather than waiting for the next 6h cron tick) tightens the loop
// from "your rule shows up tomorrow" to "next conversation has it".
//
// Debounce semantics:
//
//   - Per (workspace_id, crew_id) pair, track last-fire timestamp
//   - On OnRunCompleted, only fire if (now - last) >= Debounce
//   - Default Debounce = 30 min; tunable per Options
//   - Fires asynchronously — caller (run-completion handler) never
//     waits on consolidation
//
// Failure stance: a missed fire is fine (the 6h cron is the safety
// net). A double fire is fine too (Consolidator.Run is idempotent
// against the same journal window). The debounce exists for cost,
// not correctness.
type PostRunTrigger struct {
	consolidator   *Consolidator
	debounce       time.Duration
	crewMemoryRoot string
	blobRoot       string
	since          time.Duration
	minEntries     int
	logger         *slog.Logger
	now            func() time.Time

	mu       sync.Mutex
	lastFire map[string]time.Time
}

// PostRunTriggerOptions parameterises the trigger. Zero values yield
// production defaults (30 min debounce, 6h look-back, MinEntries 10).
type PostRunTriggerOptions struct {
	Debounce       time.Duration
	CrewMemoryRoot string
	BlobRoot       string
	Since          time.Duration
	MinEntries     int
	Logger         *slog.Logger
	// Now overrides time.Now for testability. Production leaves
	// nil and the trigger uses the real clock.
	Now func() time.Time
}

// NewPostRunTrigger constructs a trigger bound to the supplied
// Consolidator. Returns nil + does nothing useful if the
// consolidator argument is nil — the caller can wire a no-op
// instance during early-boot without a guard.
func NewPostRunTrigger(c *Consolidator, opts PostRunTriggerOptions) *PostRunTrigger {
	if opts.Debounce <= 0 {
		opts.Debounce = 30 * time.Minute
	}
	if opts.CrewMemoryRoot == "" {
		opts.CrewMemoryRoot = "/crew/shared/.memory"
	}
	if opts.Since <= 0 {
		opts.Since = 6 * time.Hour
	}
	if opts.MinEntries <= 0 {
		opts.MinEntries = 10
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &PostRunTrigger{
		consolidator:   c,
		debounce:       opts.Debounce,
		crewMemoryRoot: opts.CrewMemoryRoot,
		blobRoot:       opts.BlobRoot,
		since:          opts.Since,
		minEntries:     opts.MinEntries,
		logger:         opts.Logger,
		now:            opts.Now,
		lastFire:       make(map[string]time.Time),
	}
}

// OnRunCompleted is the trigger entry point. Caller invokes it
// after emitting the run.completed journal entry. Returns true if
// a consolidation pass was kicked off (in a goroutine — the call
// itself doesn't block), false if debounced. crewSlug is needed
// to compute OutputDir matching the cron runner's convention.
//
// The async goroutine respects the supplied context for cancellation
// so a shutdown signal halts in-flight passes. We deliberately
// don't store inflight handles — losing track of one mid-flight is
// strictly better than blocking the caller's run-completion path.
func (t *PostRunTrigger) OnRunCompleted(ctx context.Context, workspaceID, crewID, crewSlug string) bool {
	if t == nil || t.consolidator == nil {
		return false
	}
	if workspaceID == "" || crewID == "" {
		return false
	}
	key := workspaceID + ":" + crewID
	now := t.now()

	t.mu.Lock()
	last, seen := t.lastFire[key]
	if seen && now.Sub(last) < t.debounce {
		t.mu.Unlock()
		t.logger.Debug("post-run consolidator debounced",
			"workspace_id", workspaceID, "crew_id", crewID,
			"since_last", now.Sub(last), "debounce", t.debounce)
		return false
	}
	t.lastFire[key] = now
	t.mu.Unlock()

	cfg := Config{
		WorkspaceID:  workspaceID,
		CrewID:       crewID,
		Since:        t.since,
		MinEntries:   t.minEntries,
		OutputDir:    filepath.Join(t.crewMemoryRoot, crewSlug, "topics"),
		BlobRoot:     t.blobRoot,
		ProposalMode: hitlEnabled(),
	}
	go func() {
		res, err := t.consolidator.Run(ctx, cfg)
		switch {
		case err != nil:
			t.logger.Warn("post-run consolidation failed",
				"err", err, "workspace_id", workspaceID, "crew_id", crewID)
		case res.Skipped:
			t.logger.Debug("post-run consolidation skipped (below threshold)",
				"workspace_id", workspaceID, "crew_id", crewID,
				"entries_scanned", res.EntriesScanned)
		default:
			t.logger.Info("post-run consolidation ran",
				"workspace_id", workspaceID, "crew_id", crewID,
				"rules_appended", res.RulesAppended,
				"entries_scanned", res.EntriesScanned)
		}
	}()
	return true
}
