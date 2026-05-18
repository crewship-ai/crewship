package consolidate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/episodic"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/memory"
)

// RunnerOptions configures the background runner's cadence and paths.
// A zero value yields sensible production defaults: consolidation every
// 6h, compaction daily at 03:00 UTC, 30-day retention, look-back window
// 6h. Tests can override ConsolidationTick and CompactionTick to run the
// workers immediately and measure a single pass.
type RunnerOptions struct {
	// ConsolidationInterval is how often the consolidation loop wakes up.
	// Defaults to 6h. The runner scans every crew in every workspace on
	// each tick; cost is proportional to (#crews * avg entries in the
	// look-back window), not to tick frequency.
	ConsolidationInterval time.Duration

	// ConsolidationSince is the look-back window passed to each
	// Consolidator.Run. Defaults to ConsolidationInterval — i.e. each
	// run consolidates the events since the last run.
	ConsolidationSince time.Duration

	// CompactionHourUTC is the hour of the day (0..23) at which the
	// compaction worker fires. Defaults to 3 (03:00 UTC).
	CompactionHourUTC int

	// CompactionOlderThan is the retention cutoff for compaction.
	// Defaults to 30 days. Entries younger than this are always left
	// alone.
	CompactionOlderThan time.Duration

	// MinEntries is threshold below which consolidation is skipped for
	// a crew. Defaults to 10.
	MinEntries int

	// LLMModel is an informational label stored in the memory.consolidated
	// payload. No runtime meaning.
	LLMModel string

	// CrewMemoryRoot is the parent directory under which each crew's
	// learned-*.md files are written. The effective output directory for
	// a crew is filepath.Join(CrewMemoryRoot, crewSlug, "topics"). If
	// empty the runner defaults to "/crew/shared/.memory".
	CrewMemoryRoot string

	// BlobRoot is propagated to every per-crew Config as the content-
	// addressed memory version blob directory. Empty disables
	// versioning (legacy behaviour pre-v90). Production wires
	// {DataDir.Root}/memory/versions in cmd_start.
	BlobRoot string

	// MemoryVersionsRetention is the age cutoff for the daily
	// retention sweep. Rows older than (now - retention) are
	// eligible for deletion (subject to MemoryVersionsKeepLatest).
	// Default: 30 days. Zero disables the age-based delete pass
	// entirely; the orphan-blob sweep still runs.
	MemoryVersionsRetention time.Duration

	// MemoryVersionsKeepLatest is the per-(workspace_id, path) floor
	// — the N most recent rows are never deleted, regardless of age.
	// Matches Anthropic Managed Agents' "always keep last N" rule.
	// Default: 3.
	MemoryVersionsKeepLatest int

	// Logger for runner events (tick fires, skips, errors). Default: slog.Default().
	Logger *slog.Logger
}

// StartBackground spawns the two worker goroutines and returns a cancel
// function that stops both. The function returns immediately; work
// happens in the background until the returned cancel is invoked or the
// provided ctx is cancelled.
//
// The runner is deliberately conservative about errors: a failure to
// consolidate one crew does not stop the loop. All per-crew errors are
// collected per-tick and logged; the goroutine sleeps until the next
// tick regardless.
func StartBackground(
	ctx context.Context,
	db *sql.DB,
	j journal.Emitter,
	summarizer SummarizerClient,
	opts RunnerOptions,
) context.CancelFunc {
	opts = applyDefaults(opts)
	logger := opts.Logger

	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup

	consolidator := &Consolidator{
		DB:         db,
		Journal:    j,
		Summarizer: summarizer,
		Logger:     logger,
	}
	compactor := &Compactor{
		DB:      db,
		Journal: j,
		Logger:  logger,
	}

	wg.Add(2)
	go func() {
		defer wg.Done()
		runConsolidationLoop(ctx, db, consolidator, opts)
	}()
	go func() {
		defer wg.Done()
		runCompactionLoop(ctx, db, compactor, opts)
	}()

	return func() {
		cancel()
		wg.Wait()
	}
}

func applyDefaults(opts RunnerOptions) RunnerOptions {
	if opts.ConsolidationInterval <= 0 {
		opts.ConsolidationInterval = 6 * time.Hour
	}
	if opts.ConsolidationSince <= 0 {
		opts.ConsolidationSince = opts.ConsolidationInterval
	}
	if opts.CompactionHourUTC < 0 || opts.CompactionHourUTC > 23 {
		opts.CompactionHourUTC = 3
	}
	if opts.CompactionOlderThan <= 0 {
		opts.CompactionOlderThan = 30 * 24 * time.Hour
	}
	if opts.MinEntries <= 0 {
		opts.MinEntries = 10
	}
	if opts.CrewMemoryRoot == "" {
		opts.CrewMemoryRoot = "/crew/shared/.memory"
	}
	if opts.MemoryVersionsRetention < 0 {
		opts.MemoryVersionsRetention = 0
	} else if opts.MemoryVersionsRetention == 0 {
		// 30-day default — matches Anthropic Managed Agents'
		// shipped retention. Operators wanting an explicit
		// "disable retention sweep" must pass a negative value
		// (which we then clamp to 0 above on next runner restart);
		// today the natural way to disable is BlobRoot="" which
		// short-circuits the prune entirely.
		opts.MemoryVersionsRetention = 30 * 24 * time.Hour
	}
	if opts.MemoryVersionsKeepLatest < 0 {
		opts.MemoryVersionsKeepLatest = 0
	} else if opts.MemoryVersionsKeepLatest == 0 {
		opts.MemoryVersionsKeepLatest = 3
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return opts
}

// runConsolidationLoop wakes every ConsolidationInterval, iterates every
// (workspace, crew), and invokes Consolidator.Run per crew. Per-crew
// errors are aggregated into a single errors.Join for the tick so the
// log carries one structured line per tick rather than one per crew.
func runConsolidationLoop(ctx context.Context, db *sql.DB, c *Consolidator, opts RunnerOptions) {
	ticker := time.NewTicker(opts.ConsolidationInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := consolidateAllCrews(ctx, db, c, opts); err != nil {
				opts.Logger.Warn("consolidation tick completed with errors", "err", err)
			}
		}
	}
}

// consolidateAllCrews enumerates every non-deleted crew and runs the
// consolidator against it. Errors are collected with errors.Join so each
// crew fails independently; a single crew's LLM timeout does not shadow
// successful work on its siblings.
func consolidateAllCrews(ctx context.Context, db *sql.DB, c *Consolidator, opts RunnerOptions) error {
	crews, err := listCrews(ctx, db)
	if err != nil {
		return fmt.Errorf("list crews: %w", err)
	}
	var errs []error
	for _, cr := range crews {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		cfg := Config{
			WorkspaceID:  cr.WorkspaceID,
			CrewID:       cr.ID,
			Since:        opts.ConsolidationSince,
			MinEntries:   opts.MinEntries,
			LLMModel:     opts.LLMModel,
			OutputDir:    filepath.Join(opts.CrewMemoryRoot, cr.Slug, "topics"),
			ProposalMode: hitlEnabled(),
			BlobRoot:     opts.BlobRoot,
		}
		res, rerr := c.Run(ctx, cfg)
		switch {
		case rerr != nil:
			errs = append(errs, fmt.Errorf("crew %s: %w", cr.ID, rerr))
		case res.Skipped:
			// Deliberately not logging — skips are the common case and
			// spamming an info line per crew per tick drowns out real
			// events. The journal has the authoritative record.
		default:
			opts.Logger.Info("consolidation ran",
				"workspace_id", cr.WorkspaceID,
				"crew_id", cr.ID,
				"entries_scanned", res.EntriesScanned,
				"rules_appended", res.RulesAppended,
				"output", res.OutputPath,
			)
		}
	}
	return errors.Join(errs...)
}

// runCompactionLoop fires once per day at CompactionHourUTC. We compute
// the next fire time from the current time rather than using a ticker
// so the loop is robust to machine sleep/resume — after a suspend, it
// simply targets the next 03:00 ahead of `now` instead of drifting.
func runCompactionLoop(ctx context.Context, db *sql.DB, comp *Compactor, opts RunnerOptions) {
	for {
		next := nextDailyAt(time.Now().UTC(), opts.CompactionHourUTC)
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Until(next)):
		}
		if err := compactAllWorkspaces(ctx, db, comp, opts); err != nil {
			opts.Logger.Warn("compaction tick completed with errors", "err", err)
		}
		// Fold the memory decay pass into the same daily tick. Both
		// operate on the same storage and benefit from running at
		// off-peak hours; adding a third goroutine would complicate
		// shutdown for no real benefit. Errors are logged but do not
		// affect compaction — these are independent best-efforts.
		if n, err := episodic.DecayAndReinforce(ctx, db, time.Now()); err != nil {
			opts.Logger.Warn("memory decay tick failed", "err", err)
		} else {
			opts.Logger.Info("memory decay tick completed", "rows_updated", n)
		}
		// Persist one workspace-level HealthSnapshot per workspace
		// so the dashboard has a daily time-series to plot.
		if err := snapshotAllWorkspaces(ctx, db, opts); err != nil {
			opts.Logger.Warn("health snapshot tick failed", "err", err)
		}
		// Memory-versions retention has two coordinated passes, both
		// driven from this single daily tick so we never have two
		// background loops racing on memory_versions:
		//
		//   1. Global pass — memory.PruneOldVersions. Enforces a
		//      cluster-wide floor (keep latest N per
		//      (workspace_id, path) regardless of age) and sweeps
		//      orphan blobs. The keep-N floor is the load-bearing
		//      bit: a workspace with retention_days=7 that wrote
		//      one critical pin 14 days ago and never updated it
		//      still keeps the pin because the global pass refuses
		//      to delete the LAST row at a path.
		//
		//   2. Per-workspace pass — memory.SweepAllWorkspaces. Reads
		//      workspaces.memory_config.versions_retention_days and
		//      tightens for tenants with windows shorter than the
		//      global retention. A dev sandbox with retention_days=7
		//      drops older versions even though the global window
		//      is 30 days. Runs AFTER the global pass so the keep-N
		//      floor is already satisfied; the per-workspace pass
		//      only trims by age and respects the same single-
		//      statement transactional guarantees.
		//
		// Disabled silently when BlobRoot or retention window is
		// unset (test harness, dev mode). The per-workspace pass
		// runs regardless of BlobRoot — it doesn't touch blobs,
		// only rows.
		if opts.BlobRoot != "" && opts.MemoryVersionsRetention > 0 {
			res, err := memory.PruneOldVersions(ctx, db, opts.BlobRoot, opts.MemoryVersionsRetention, opts.MemoryVersionsKeepLatest)
			if err != nil {
				opts.Logger.Warn("memory_versions retention sweep failed", "err", err)
			} else {
				opts.Logger.Info("memory_versions retention sweep",
					"rows_deleted", res.RowsDeleted,
					"blobs_deleted", res.BlobsDeleted,
					"errors", len(res.Errors))
				for _, e := range res.Errors {
					opts.Logger.Warn("retention sweep partial failure", "err", e)
				}
			}
		}
		// Per-workspace tightening pass. comp.Journal is the same
		// emitter the rest of the runner uses, so the
		// memory.versions_swept events land alongside the
		// compaction.completed events for the same tick. A nil
		// emitter (test harness) is fine — SweepAllWorkspaces
		// skips the emit when emitter==nil.
		if err := memory.SweepAllWorkspaces(ctx, db, comp.Journal); err != nil {
			opts.Logger.Warn("per-workspace memory retention sweep failed", "err", err)
		}
	}
}

// snapshotAllWorkspaces computes + persists a workspace-wide health
// score per workspace. Crew-level snapshots stay on-demand via the
// API because workspaces with 100+ crews would push compute past
// what "nightly" is supposed to mean.
func snapshotAllWorkspaces(ctx context.Context, db *sql.DB, opts RunnerOptions) error {
	ws, err := listWorkspaces(ctx, db)
	if err != nil {
		return fmt.Errorf("list workspaces: %w", err)
	}
	for _, id := range ws {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		snap, err := ComputeHealth(ctx, db, id, "")
		if err != nil {
			opts.Logger.Warn("health compute failed", "err", err, "workspace_id", id)
			continue
		}
		if err := PersistSnapshot(ctx, db, snap); err != nil {
			opts.Logger.Warn("health persist failed", "err", err, "workspace_id", id)
		}
	}
	return nil
}

// compactAllWorkspaces runs compaction for each workspace. Each workspace
// is independent: a failure in one does not cancel siblings. Logging is
// one line per workspace so operator dashboards can attribute throughput.
func compactAllWorkspaces(ctx context.Context, db *sql.DB, comp *Compactor, opts RunnerOptions) error {
	ws, err := listWorkspaces(ctx, db)
	if err != nil {
		return fmt.Errorf("list workspaces: %w", err)
	}
	var errs []error
	for _, id := range ws {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		res, rerr := comp.Run(ctx, id, opts.CompactionOlderThan)
		if rerr != nil {
			errs = append(errs, fmt.Errorf("workspace %s: %w", id, rerr))
			continue
		}
		if res.BucketsCreated == 0 {
			continue
		}
		opts.Logger.Info("compaction ran",
			"workspace_id", id,
			"entries_deleted", res.EntriesDeleted,
			"buckets_created", res.BucketsCreated,
			"bytes_freed", res.BytesFreed,
		)
	}
	return errors.Join(errs...)
}

// hitlEnabled reads CREWSHIP_CONSOLIDATE_HITL on every tick so an
// operator can flip the kill switch without restarting the server.
// Accepts the conventional "1", "true", "yes" forms (case-insensitive);
// anything else is treated as off. Defaults to off so the existing
// direct-write contract survives the upgrade unchanged.
func hitlEnabled() bool {
	v := strings.TrimSpace(os.Getenv("CREWSHIP_CONSOLIDATE_HITL"))
	switch strings.ToLower(v) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// nextDailyAt returns the next UTC instant whose hour equals hour and
// minute/second are zero. If `now` is already past today's target hour,
// the result is tomorrow's; otherwise it's today's.
func nextDailyAt(now time.Time, hour int) time.Time {
	now = now.UTC()
	target := time.Date(now.Year(), now.Month(), now.Day(), hour, 0, 0, 0, time.UTC)
	if !target.After(now) {
		target = target.Add(24 * time.Hour)
	}
	return target
}

type crewRow struct {
	ID          string
	WorkspaceID string
	Slug        string
}

// listCrews returns every live crew across every workspace. The query is
// local to this file so the consolidate package does not pick up a
// dependency on the crews package; the two columns we care about are
// a stable public surface of the schema.
func listCrews(ctx context.Context, db *sql.DB) ([]crewRow, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, workspace_id, slug FROM crews WHERE deleted_at IS NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]crewRow, 0, 8)
	for rows.Next() {
		var r crewRow
		if err := rows.Scan(&r.ID, &r.WorkspaceID, &r.Slug); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// listWorkspaces returns every workspace ID. Workspaces do not currently
// have a deleted_at column so we return them all.
func listWorkspaces(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT id FROM workspaces`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]string, 0, 4)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
