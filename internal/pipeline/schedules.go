package pipeline

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/crewship-ai/crewship/internal/inbox"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/leader"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

// Schedule is one cron trigger for a saved pipeline. Bound to a
// pipeline by ID (not slug) so renames don't break schedules.
//
// TargetVersion=nil means "always run head_version"; an explicit
// version pins production schedules so an agent edit can't
// accidentally change what the 8 AM run does.
type Schedule struct {
	ID                    string
	WorkspaceID           string
	Name                  string
	TargetPipelineID      string
	TargetPipelineVersion *int // nil = latest
	CronExpr              string
	Timezone              string
	InputsJSON            string // raw JSON; parsed lazily at fire time
	Enabled               bool
	LastRunAt             *time.Time
	// LastStatus is the outcome of the last main-routine fire:
	//   COMPLETED — run finished successfully
	//   FAILED    — run errored (a MANAGER inbox alert was raised)
	//   SKIPPED   — target routine not active (proposed/disabled)
	//   WAITING   — run parked on a human approval gate (healthy,
	//               non-terminal; resumes when the approval lands)
	//   DEDUPED   — a re-fire of the same occurrence hit the idempotency
	//               chokepoint; the original run owns the result (healthy)
	LastStatus string
	LastRunID  string
	NextRunAt  *time.Time

	// Wake gate. When WakePipelineID is set, each cron tick first
	// runs that (agentless — enforced at API save time) probe
	// routine; the main routine fires only when the probe's final
	// output is truthy (same falsey rule as step `if:`). Telemetry
	// is kept apart from last_run_* so "checked 96×, woke 3×" is
	// visible without conflating probe ticks with real runs.
	WakePipelineID string
	WakeInputsJSON string // raw JSON; parsed lazily at fire time
	WakeCheckCount int
	WakeFireCount  int
	LastWakeAt     *time.Time
	LastWakeStatus string // WOKE | SKIPPED | ERROR | HELD
	// WakeFailClosed flips the probe-failure default (#1372). When false
	// (default), a probe that errors / returns nil / finishes
	// non-COMPLETED fails OPEN — the main routine fires anyway and the
	// tick records ERROR. When true, that same non-affirmative outcome
	// HOLDS the run (records HELD): a broken or tampered probe can no
	// longer be the thing that triggers an unattended autonomous run.
	WakeFailClosed bool

	// Circuit breaker (#1405). ConsecutiveFailures counts back-to-back
	// FAILED main-routine fires; a COMPLETED fire resets it to 0.
	// MaxConsecutiveFailures is the per-schedule trip threshold (default
	// defaultMaxConsecutiveFailures). Once ConsecutiveFailures reaches
	// the threshold the schedule is disabled with DisabledReason set to
	// scheduleDisabledReasonCircuitBreaker, a journal event fires, and a
	// single actionable alert lands in the MANAGER inbox. Re-enabling
	// (Save with Enabled transitioning false→true) resets both.
	ConsecutiveFailures    int
	MaxConsecutiveFailures int
	DisabledReason         string

	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt *time.Time
}

// defaultMaxConsecutiveFailures is the trip threshold used when a
// schedule doesn't specify its own MaxConsecutiveFailures (zero/negative
// on create, or a pre-migration row).
const defaultMaxConsecutiveFailures = 5

// scheduleDisabledReasonCircuitBreaker is the DisabledReason value set
// when the circuit breaker (not an operator, not a bad cron expr)
// disables a schedule. Surfaced verbatim by the CLI (`schedules list` /
// `routine doctor`) so an operator can tell at a glance why a schedule
// went dark.
const scheduleDisabledReasonCircuitBreaker = "circuit_breaker"

// Wake check outcomes recorded in pipeline_schedules.last_wake_status.
const (
	WakeStatusWoke    = "WOKE"    // probe truthy (or absent) — main routine fired
	WakeStatusSkipped = "SKIPPED" // probe falsey — tick skipped, no main run
	WakeStatusError   = "ERROR"   // probe failed, fail-OPEN gate — main routine fired anyway
	WakeStatusHeld    = "HELD"    // probe failed, fail-CLOSED gate — run held, no main run (#1372)
)

// SaveScheduleInput is the payload for ScheduleStore.Save.
type SaveScheduleInput struct {
	ID                    string // "" = create; non-empty = update
	WorkspaceID           string
	Name                  string
	TargetPipelineID      string
	TargetPipelineVersion *int
	CronExpr              string
	Timezone              string
	Inputs                map[string]any
	Enabled               bool
	// WakePipelineID enables the wake gate ("" = no gate; on update,
	// "" clears an existing gate — whole-row replace semantics, same
	// as every other field here). The API layer validates the target
	// exists, is agentless, and isn't the schedule's own routine; the
	// store persists what it's given.
	WakePipelineID string
	WakeInputs     map[string]any
	// WakeFailClosed opts the gate into fail-closed probe-failure
	// handling (#1372). Ignored when WakePipelineID is empty (no gate).
	WakeFailClosed bool
	// MaxConsecutiveFailures overrides the circuit breaker's trip
	// threshold (#1405). <= 0 means "use defaultMaxConsecutiveFailures" —
	// on create that's a fresh default; on update the existing stored
	// value is left untouched (see Save's CASE-guarded UPDATE).
	MaxConsecutiveFailures int
}

// ScheduleStore is the persistence + listing API for
// pipeline_schedules. Lives alongside the pipeline.Store rather
// than as a separate package because the scheduler needs both
// (look up schedule, look up the pinned pipeline version).
type ScheduleStore struct {
	db *sql.DB
}

// NewScheduleStore returns a store backed by the given DB handle.
// The handle must already be migrated to v80+.
func NewScheduleStore(db *sql.DB) *ScheduleStore {
	return &ScheduleStore{db: db}
}

// Save creates or updates a schedule. Cron parse is delegated to
// the caller (CLI / API handler) so a 400 fires before we hit the
// DB; we still re-parse here to compute next_run_at.
func (s *ScheduleStore) Save(ctx context.Context, in SaveScheduleInput) (*Schedule, error) {
	if in.WorkspaceID == "" || in.TargetPipelineID == "" || in.CronExpr == "" {
		return nil, errors.New("pipeline_schedules: workspace_id + target_pipeline_id + cron_expr required")
	}
	if in.Timezone == "" {
		in.Timezone = "UTC"
	}
	loc, err := time.LoadLocation(in.Timezone)
	if err != nil {
		return nil, fmt.Errorf("invalid timezone %q: %w", in.Timezone, err)
	}
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	sched, err := parser.Parse(in.CronExpr)
	if err != nil {
		return nil, fmt.Errorf("invalid cron expression %q: %w", in.CronExpr, err)
	}
	nextRun := sched.Next(time.Now().In(loc))
	inputsJSON, err := json.Marshal(in.Inputs)
	if err != nil {
		return nil, fmt.Errorf("marshal inputs: %w", err)
	}
	if string(inputsJSON) == "null" {
		inputsJSON = []byte("{}")
	}
	wakeInputsJSON, err := json.Marshal(in.WakeInputs)
	if err != nil {
		return nil, fmt.Errorf("marshal wake inputs: %w", err)
	}
	if string(wakeInputsJSON) == "null" {
		wakeInputsJSON = []byte("{}")
	}
	now := time.Now().UTC()
	maxFailures := in.MaxConsecutiveFailures
	if maxFailures <= 0 {
		maxFailures = defaultMaxConsecutiveFailures
	}

	if in.ID == "" {
		// Create
		id := generateScheduleID()
		_, err = s.db.ExecContext(ctx, `
INSERT INTO pipeline_schedules (
    id, workspace_id, name, target_pipeline_id, target_pipeline_version,
    cron_expr, timezone, inputs_json, enabled, next_run_at,
    wake_pipeline_id, wake_inputs_json, wake_fail_closed,
    max_consecutive_failures,
    created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, in.WorkspaceID, in.Name, in.TargetPipelineID,
			nullInt(in.TargetPipelineVersion),
			in.CronExpr, in.Timezone, string(inputsJSON), boolToInt(in.Enabled),
			tsformat.Format(nextRun),
			nullStr(in.WakePipelineID), string(wakeInputsJSON), boolToInt(in.WakeFailClosed),
			maxFailures,
			tsformat.Format(now), tsformat.Format(now),
		)
		if err != nil {
			return nil, fmt.Errorf("insert schedule: %w", err)
		}
		return s.GetByID(ctx, id)
	}

	// Update. consecutive_failures / disabled_reason reset to a clean
	// slate exactly when this Save re-enables a previously-disabled
	// schedule (enabled false→true) — the CASE reads the PRE-update
	// `enabled` column value, so it correctly distinguishes "was already
	// enabled, stays enabled" (leave the breaker state alone) from
	// "was disabled, now enabled" (re-enable = clean slate). This is the
	// single code path both the dedicated `schedules enable` CLI command
	// and any generic `update --enabled` call go through.
	// max_consecutive_failures only changes when the caller explicitly
	// passed a positive override; otherwise the stored value survives an
	// unrelated field edit (cron tweak, rename, ...).
	newMaxFailures := any(nil)
	if in.MaxConsecutiveFailures > 0 {
		newMaxFailures = in.MaxConsecutiveFailures
	}

	// Preserve a DUE-but-unfired occurrence across an edit that doesn't
	// change WHEN the schedule fires (#1430, 3.5). Recomputing
	// next_run_at = Next(now) on every Save silently pushes a row whose
	// next_run_at has already passed (due, waiting for the next tick) into
	// the future — the pending run is swallowed by an unrelated edit
	// (rename, input tweak, re-enable). When the cron expr AND timezone are
	// unchanged and the stored next_run_at is already due, keep it so the
	// pending occurrence still fires. Any change to cron/timezone
	// legitimately recomputes (the old bar no longer means anything).
	nextRunToStore := nextRun
	if existing, gerr := s.GetByID(ctx, in.ID); gerr == nil {
		if existing.CronExpr == in.CronExpr && existing.Timezone == in.Timezone &&
			existing.NextRunAt != nil && !existing.NextRunAt.After(time.Now()) {
			nextRunToStore = *existing.NextRunAt
		}
	}
	_, err = s.db.ExecContext(ctx, `
UPDATE pipeline_schedules
SET name = ?, target_pipeline_id = ?, target_pipeline_version = ?,
    cron_expr = ?, timezone = ?, inputs_json = ?, enabled = ?,
    max_consecutive_failures = COALESCE(?, max_consecutive_failures),
    consecutive_failures = CASE WHEN ? = 1 AND enabled = 0 THEN 0 ELSE consecutive_failures END,
    disabled_reason = CASE WHEN ? = 1 AND enabled = 0 THEN NULL ELSE disabled_reason END,
    next_run_at = ?, wake_pipeline_id = ?, wake_inputs_json = ?,
    wake_fail_closed = ?, updated_at = ?
WHERE id = ? AND deleted_at IS NULL`,
		in.Name, in.TargetPipelineID, nullInt(in.TargetPipelineVersion),
		in.CronExpr, in.Timezone, string(inputsJSON), boolToInt(in.Enabled),
		newMaxFailures,
		boolToInt(in.Enabled), boolToInt(in.Enabled),
		tsformat.Format(nextRunToStore),
		nullStr(in.WakePipelineID), string(wakeInputsJSON), boolToInt(in.WakeFailClosed),
		tsformat.Format(now),
		in.ID,
	)
	if err != nil {
		return nil, fmt.Errorf("update schedule: %w", err)
	}
	return s.GetByID(ctx, in.ID)
}

// GetByID returns a schedule by id, or ErrNotFound.
func (s *ScheduleStore) GetByID(ctx context.Context, id string) (*Schedule, error) {
	rows, err := s.db.QueryContext(ctx, scheduleSelect+` WHERE id = ? AND deleted_at IS NULL`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, ErrNotFound
	}
	return scanSchedule(rows)
}

// List returns workspace schedules ordered by next_run_at ascending
// so "what fires next" is visually obvious in the UI.
func (s *ScheduleStore) List(ctx context.Context, workspaceID string) ([]*Schedule, error) {
	rows, err := s.db.QueryContext(ctx,
		scheduleSelect+` WHERE workspace_id = ? AND deleted_at IS NULL ORDER BY next_run_at ASC`,
		workspaceID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Schedule
	for rows.Next() {
		s, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// SoftDelete marks a schedule deleted; the scheduler skips
// deleted_at IS NOT NULL rows.
func (s *ScheduleStore) SoftDelete(ctx context.Context, id string) error {
	now := tsformat.Format(time.Now())
	res, err := s.db.ExecContext(ctx,
		`UPDATE pipeline_schedules SET deleted_at = ?, updated_at = ?, enabled = 0 WHERE id = ? AND deleted_at IS NULL`,
		now, now, id,
	)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// listDueSchedules returns enabled schedules whose next_run_at has
// passed. Called by the scheduler tick.
//
// next_run_at is compared as a string, so both sides use the
// fixed-width tsformat (#990). Legacy rows written pre-fix with
// RFC3339Nano (cron-derived whole seconds render with NO fraction)
// compare greater than a fractional bound for their boundary second —
// a one-poll-tick firing delay that self-heals as soon as the bound
// crosses into the next second, and disappears once the row's next
// UpdateAfterRun/Save rewrites it fixed-width.
func (s *ScheduleStore) listDueSchedules(ctx context.Context) ([]*Schedule, error) {
	rows, err := s.db.QueryContext(ctx, scheduleSelect+`
WHERE enabled = 1 AND deleted_at IS NULL AND next_run_at <= ?
ORDER BY next_run_at ASC LIMIT 100`,
		tsformat.Format(time.Now()))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Schedule
	for rows.Next() {
		s, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// recordRun persists the outcome of a fire — last_run_at, last_status,
// last_run_id — and computes next_run_at from the cron expr.
//
// Circuit breaker bookkeeping (#1405) rides along here since it's the
// same terminal-status transition: a COMPLETED fire resets
// consecutive_failures to 0 (the routine is healthy again); a FAILED
// fire increments it. SKIPPED / WAITING / DEDUPED are non-terminal or
// healthy-but-not-a-success outcomes and leave the counter untouched.
func (s *ScheduleStore) recordRun(ctx context.Context, scheduleID, runID, status string, nextRun time.Time) error {
	now := tsformat.Format(time.Now())
	var counterSQL string
	switch status {
	case "COMPLETED":
		counterSQL = `, consecutive_failures = 0`
	case "FAILED":
		counterSQL = `, consecutive_failures = consecutive_failures + 1`
	}
	_, err := s.db.ExecContext(ctx, `
UPDATE pipeline_schedules
SET last_run_at = ?, last_status = ?, last_run_id = ?, next_run_at = ?, updated_at = ?`+counterSQL+`
WHERE id = ?`,
		now, status, nullStr(runID), tsformat.Format(nextRun), now, scheduleID,
	)
	return err
}

// disableForCircuitBreaker disables a schedule and stamps
// disabled_reason so the CLI can distinguish an operator-initiated
// disable (disabled_reason stays NULL) from a circuit-breaker trip.
func (s *ScheduleStore) disableForCircuitBreaker(ctx context.Context, scheduleID string) error {
	now := tsformat.Format(time.Now())
	_, err := s.db.ExecContext(ctx, `
UPDATE pipeline_schedules
SET enabled = 0, disabled_reason = ?, updated_at = ?
WHERE id = ?`,
		scheduleDisabledReasonCircuitBreaker, now, scheduleID,
	)
	return err
}

// recordWakeCheck persists the outcome of one wake-gate evaluation.
// Counters + last_wake_* are wake-only telemetry — last_run_* stays
// strictly about main runs. advanceNext is true when no main run
// follows (SKIPPED, or a fail-closed HELD tick), so the wake record is
// what moves next_run_at forward; on WOKE/ERROR the subsequent recordRun
// advances it. wake_fire_count counts ticks that fired the main run, so
// HELD (held, no main run) does NOT increment it — only WOKE and the
// fail-OPEN ERROR do.
func (s *ScheduleStore) recordWakeCheck(ctx context.Context, scheduleID, status string, nextRun time.Time, advanceNext bool) error {
	now := tsformat.Format(time.Now())
	fired := 0
	if status == WakeStatusWoke || status == WakeStatusError {
		fired = 1
	}
	if advanceNext {
		_, err := s.db.ExecContext(ctx, `
UPDATE pipeline_schedules
SET wake_check_count = wake_check_count + 1,
    wake_fire_count = wake_fire_count + ?,
    last_wake_at = ?, last_wake_status = ?, next_run_at = ?, updated_at = ?
WHERE id = ?`,
			fired, now, status, tsformat.Format(nextRun), now, scheduleID,
		)
		return err
	}
	_, err := s.db.ExecContext(ctx, `
UPDATE pipeline_schedules
SET wake_check_count = wake_check_count + 1,
    wake_fire_count = wake_fire_count + ?,
    last_wake_at = ?, last_wake_status = ?, updated_at = ?
WHERE id = ?`,
		fired, now, status, now, scheduleID,
	)
	return err
}

// PipelineScheduler ticks every 30s, fires due schedules through
// the supplied Executor, and updates next_run_at.
//
// Multi-replica safe: when a leader Gate is attached (SetLeaderGate),
// the tick fires only on the replica holding the scheduler lease, so
// two replicas can't double-fire. A nil gate means "always leader" —
// the unchanged single-instance behaviour (#1376).
type PipelineScheduler struct {
	store     *ScheduleStore
	pipelines *Store // pipeline lookup for the run
	executor  *Executor
	logger    *slog.Logger

	// leaderGate, when non-nil, gates each tick on holding the scheduler
	// lease. Nil = single-instance (always fire).
	leaderGate leader.Gate

	// emitter records journal events for scheduler-level occurrences that
	// aren't per-run (circuit breaker trips, missed-occurrence catch-up).
	// Nil-safe via ensureEmitter — defaults to a no-op so schedulers built
	// without SetEmitter (most existing tests) keep working unchanged.
	emitter Emitter

	// maxConcurrency bounds how many due schedules fire at once per tick
	// (#1406). Before this, tick() called fireOne serially — one slow
	// COMPLETED/FAILED routine stalled every other due schedule for the
	// rest of that 30s tick. Mirrors PendingRunDispatcher's worker-pool
	// pattern (pending_dispatcher.go). Zero (the NewPipelineScheduler
	// default) falls back to defaultScheduleDispatchConcurrency at the
	// first tick; tests may set it directly before calling tick/Start.
	maxConcurrency int
	// sem is the bounded worker pool, sized lazily (via poolOnce) on the
	// first tick so a maxConcurrency set directly after construction (as
	// tests do) still takes effect.
	sem      chan struct{}
	poolOnce sync.Once
	// dispatchSaturatedCount counts ticks where at least one due
	// schedule had to wait for a free pool slot — the observability
	// breadcrumb #1406 asks for so an under-provisioned scheduler (more
	// due schedules per tick than maxConcurrency) is visible rather than
	// silently queueing.
	dispatchSaturatedCount atomic.Int64

	stopCh    chan struct{}
	stopped   chan struct{}
	startOnce sync.Once
	stopOnce  sync.Once
}

// defaultScheduleDispatchConcurrency bounds how many due schedules fire
// concurrently in one tick when the caller hasn't overridden
// maxConcurrency. Same band as PendingRunDispatcher's
// defaultDispatchConcurrency (#834) — high enough that co-due schedules
// start together, low enough not to stampede the executor/provider.
const defaultScheduleDispatchConcurrency = 12

// SetLeaderGate attaches a leader-election gate so this scheduler only fires
// while its replica holds the scheduler lease. Call before Start. Passing nil
// (the default) keeps single-instance behaviour.
func (s *PipelineScheduler) SetLeaderGate(g leader.Gate) { s.leaderGate = g }

// SetEmitter wires the journal emitter used for scheduler-level events
// (circuit breaker trips #1405, missed-occurrence catch-up #1409). Nil
// (the default) keeps events unrecorded rather than panicking.
func (s *PipelineScheduler) SetEmitter(e Emitter) { s.emitter = ensureEmitter(e) }

// NewPipelineScheduler wires a scheduler ready to start. Caller
// invokes Start to spawn the tick goroutine.
func NewPipelineScheduler(store *ScheduleStore, pipelines *Store, executor *Executor, logger *slog.Logger) *PipelineScheduler {
	if logger == nil {
		logger = slog.Default()
	}
	return &PipelineScheduler{
		store:     store,
		pipelines: pipelines,
		executor:  executor,
		logger:    logger,
		emitter:   nopEmitter{},
		stopCh:    make(chan struct{}),
		stopped:   make(chan struct{}),
	}
}

// Start spawns the tick goroutine. Idempotent — safe to call twice.
func (s *PipelineScheduler) Start(ctx context.Context) {
	s.startOnce.Do(func() {
		go s.run(ctx)
	})
}

// Stop signals the tick goroutine to exit and blocks until it does.
// Idempotent.
func (s *PipelineScheduler) Stop() {
	s.stopOnce.Do(func() {
		close(s.stopCh)
		<-s.stopped
	})
}

func (s *PipelineScheduler) run(ctx context.Context) {
	defer close(s.stopped)
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	// Fire one tick on startup so newly-due schedules don't wait 30s
	s.tick(ctx)
	for {
		select {
		case <-s.stopCh:
			return
		case <-ctx.Done():
			return
		case <-t.C:
			s.tick(ctx)
		}
	}
}

// tick fires every due schedule for this poll. Schedules dispatch onto a
// bounded worker pool (mirrors PendingRunDispatcher, pending_dispatcher.go)
// instead of running serially (#1406) — one slow COMPLETED/FAILED routine
// used to stall every other due schedule for the rest of the 30s tick.
// tick blocks until every schedule dispatched THIS call has finished (a
// per-tick sync.WaitGroup, not the scheduler's lifetime), so callers that
// invoke tick() directly and immediately assert on the outcome (existing
// tests, force-fire paths) keep working unchanged — what changed is that
// due schedules within one tick now run CONCURRENTLY with each other
// rather than one-at-a-time.
func (s *PipelineScheduler) tick(ctx context.Context) {
	// Leader gate: on a multi-replica deploy only the lease holder fires, so
	// two replicas don't both dispatch the same due schedule. Nil gate (the
	// single-instance default) always passes.
	if s.leaderGate != nil && !s.leaderGate.IsLeader() {
		return
	}
	due, err := s.store.listDueSchedules(ctx)
	if err != nil {
		s.logger.Warn("pipeline scheduler: list due", "error", err)
		return
	}
	if len(due) == 0 {
		return
	}
	s.poolOnce.Do(func() {
		max := s.maxConcurrency
		if max < 1 {
			max = defaultScheduleDispatchConcurrency
		}
		s.maxConcurrency = max
		s.sem = make(chan struct{}, max)
	})

	var wg sync.WaitGroup
	saturatedThisTick := false
	for _, sched := range due {
		select {
		case s.sem <- struct{}{}:
		default:
			// Pool saturated: every worker slot is already claimed by an
			// earlier due schedule in THIS tick. Record the breadcrumb once
			// per tick (not once per waiting schedule — that would just be
			// a duplicate signal) so an under-provisioned scheduler (more
			// co-due schedules than maxConcurrency) is observable instead
			// of silently queueing, then fall through to a blocking
			// acquire — the schedule still fires, just after a slot frees.
			if !saturatedThisTick {
				saturatedThisTick = true
				s.dispatchSaturatedCount.Add(1)
				s.logger.Warn("pipeline scheduler: dispatch pool saturated",
					"due_count", len(due), "max_concurrency", s.maxConcurrency)
			}
			select {
			case s.sem <- struct{}{}:
			case <-ctx.Done():
				wg.Wait()
				return
			}
		}
		wg.Add(1)
		go func(sc *Schedule) {
			defer wg.Done()
			defer func() { <-s.sem }()
			s.fireOne(ctx, sc)
		}(sched)
	}
	wg.Wait()
}

// ScheduledFireIdempotencyKey derives a deterministic idempotency key for one
// OCCURRENCE of a trigger, so a re-fire of the same occurrence — a duplicate
// tick within the interval, or a process restart before the trigger's next_run
// is advanced — dedupes at the LookupOrReserve chokepoint, while the next
// occurrence (a distinct bucket) gets a fresh key and fires normally.
// Grounds the cron/deferred paths in exactly-once semantics (idempotency keys,
// per Stripe/AWS). The executor SILENTLY IGNORES an empty key — which is exactly
// why these two trigger sites double-fired before this key was populated.
//
// Exported so all firing paths share one dedup discipline: the pipeline cron
// scheduler and deferred dispatcher here, plus the agent scheduler
// (internal/scheduler, #816) and recurring-issue dispatcher (#813), which fire
// outside the pipeline executor and must reserve against the shared
// pipeline_run_idempotency table directly.
func ScheduledFireIdempotencyKey(kind, id, bucket string) string {
	sum := sha256.Sum256([]byte(kind + "\x00" + id + "\x00" + bucket))
	return kind + "-" + hex.EncodeToString(sum[:16])
}

func (s *PipelineScheduler) fireOne(ctx context.Context, sched *Schedule) {
	// Compute the next run BEFORE invoking — if the run takes longer
	// than the cron interval (e.g. minutely cron + 90s pipeline),
	// the next_run_at is already in the past when we exit and the
	// next tick fires it again. That's the expected behaviour for
	// short-interval crons; long-interval crons (daily, hourly)
	// have plenty of headroom.
	loc, _ := time.LoadLocation(sched.Timezone)
	if loc == nil {
		loc = time.UTC
	}
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	cronSched, err := parser.Parse(sched.CronExpr)
	if err != nil {
		s.logger.Error("pipeline scheduler: parse cron", "error", err, "schedule", sched.ID)
		// Disable the schedule to prevent infinite tick loops on a bad expr
		_, _ = s.store.db.ExecContext(ctx, `UPDATE pipeline_schedules SET enabled = 0 WHERE id = ?`, sched.ID)
		return
	}
	nextRun := cronSched.Next(time.Now().In(loc))

	// Missed-occurrence visibility (#1409): if this row's due bar
	// (NextRunAt) lagged behind now by more than one cron interval —
	// downtime, a stuck process, a long leader-election gap — a live
	// process would have fired more than once in that window. We still
	// only fire ONCE here (no backfill), but the silent loss is worth a
	// single breadcrumb so an incident review can see it.
	if sched.NextRunAt != nil {
		now := time.Now().In(loc)
		if missed := countMissedOccurrences(cronSched, *sched.NextRunAt, now); missed > 0 {
			s.emitMissedOccurrences(ctx, sched, missed, *sched.NextRunAt, now)
		}
	}

	// Wake gate — when the schedule carries a probe routine, run it
	// first and only fall through to the main routine when the probe
	// says wake. A SKIPPED tick advances next_run_at via the wake
	// record (no recordRun follows) and leaves last_run_* untouched,
	// so the run telemetry stays strictly about main runs.
	if sched.WakePipelineID != "" {
		proceed, wakeStatus := s.runWakeCheck(ctx, sched)
		if err := s.store.recordWakeCheck(ctx, sched.ID, wakeStatus, nextRun, !proceed); err != nil {
			s.logger.Warn("pipeline scheduler: record wake check", "error", err, "schedule", sched.ID)
		}
		if !proceed {
			return
		}
	}

	// Resolve pipeline + parse inputs
	pipeline, err := s.pipelines.GetByID(ctx, sched.TargetPipelineID)
	if err != nil {
		s.logger.Error("pipeline scheduler: load pipeline", "error", err, "schedule", sched.ID)
		_ = s.store.recordRun(ctx, sched.ID, "", "FAILED", nextRun)
		// This FAILED path (deleted/broken target routine) must alert too —
		// it returns before the post-execution alert below, so without this
		// a broken cron target fails silently forever.
		s.alertFailedScheduledRun(ctx, sched, "", "", "", "the schedule's target routine could not be loaded (deleted or broken)")
		// A permanently-broken target (deleted routine) fails every tick.
		// recordRun just bumped the consecutive-failure count; consult the
		// breaker here too so this path auto-disables like a runtime
		// failure does — otherwise the schedule fires and fails forever.
		// The pipeline never loaded, so there's no slug to label it with;
		// maybeTripCircuitBreaker falls back to "target routine".
		s.maybeTripCircuitBreaker(ctx, sched, "")
		return
	}
	var inputs map[string]any
	if sched.InputsJSON != "" {
		_ = json.Unmarshal([]byte(sched.InputsJSON), &inputs)
	}

	// Bucket = the occurrence identity. next_run_at is the timestamp that made
	// this row due; it stays fixed across a mid-run restart (recordRun advances
	// it only after the run returns) and is distinct for the next occurrence.
	// Defensive fallback (should never be nil for a due row): a minute bucket so
	// distinct minutes still fire and we never dedupe forever.
	occBucket := time.Now().UTC().Truncate(time.Minute).Format(time.RFC3339)
	if sched.NextRunAt != nil {
		occBucket = sched.NextRunAt.UTC().Format(time.RFC3339)
	}

	in := RunInput{
		PipelineID: pipeline.ID,
		// Honour the pin: a schedule with target_pipeline_version set
		// executes that immutable version, not head — that is the whole
		// point of pinning a production schedule (see Schedule doc). A
		// missing pinned version surfaces as ErrPinnedVersionNotFound
		// from the executor and lands in the FAILED + alert path below —
		// never a silent head fallback.
		PinnedVersion: sched.TargetPipelineVersion,
		WorkspaceID:   sched.WorkspaceID,
		Inputs:        inputs,
		Mode:          ModeRun,
		TriggeredVia:  TriggeredViaSchedule,
		TriggeredByID: sched.ID,
		// Exactly-once on the cron path: dedupe a re-fire of the same
		// occurrence (duplicate tick / restart before next_run_at advanced).
		IdempotencyKey: ScheduledFireIdempotencyKey("sched", sched.ID, occBucket),
	}

	res, runErr := s.executor.Run(ctx, in)

	// Governance airbag: a routine that is 'proposed' (unapproved) or
	// 'disabled' (admin-killed) is refused by the executor. On the cron path
	// that is a SKIP, not a failure — advance next_run_at and stay quiet
	// rather than firing a MANAGER alert on every tick (which would spam the
	// inbox for the whole time a routine sits disabled or awaiting approval).
	if errors.Is(runErr, ErrRoutineNotActive) {
		s.logger.Info("pipeline scheduler: skipping non-active routine",
			"schedule", sched.ID, "pipeline", pipeline.Slug, "reason", runErr)
		if err := s.store.recordRun(ctx, sched.ID, "", "SKIPPED", nextRun); err != nil {
			s.logger.Warn("pipeline scheduler: record skipped run", "error", err)
		}
		return
	}

	status := "FAILED"
	runID := ""
	if res != nil {
		runID = res.RunID
		if runErr == nil {
			switch res.Status {
			case "COMPLETED":
				status = "COMPLETED"
			case "WAITING":
				// The run parked on a human approval (wait step) — a
				// healthy, NON-terminal outcome, not a failure. Record it
				// as WAITING so the schedules list shows the truth, and
				// skip the failed-run alert below (the waitpoint itself
				// already raised its own approval inbox card).
				status = "WAITING"
			case "DEDUPED":
				// A re-fire of the same occurrence (duplicate tick / restart
				// before next_run_at advanced) hit the idempotency chokepoint —
				// the original run owns the result. That's a SUCCESSFUL dedup,
				// not a failure: record it as DEDUPED and skip the MANAGER alert
				// below (without this it falls through to FAILED and raises a
				// false alarm for a healthy idempotency hit).
				status = "DEDUPED"
			}
		}
	}
	if runErr != nil {
		s.logger.Warn("pipeline scheduler: run failed", "schedule", sched.ID, "pipeline", pipeline.Slug, "error", runErr)
	}

	if err := s.store.recordRun(ctx, sched.ID, runID, status, nextRun); err != nil {
		s.logger.Warn("pipeline scheduler: record run", "error", err)
	}

	if status == "FAILED" {
		errLine := "the run did not complete"
		if runErr != nil {
			errLine = truncateForPreview(runErr.Error())
		}
		s.alertFailedScheduledRun(ctx, sched, pipeline.ID, pipeline.Slug, runID, errLine)
		s.maybeTripCircuitBreaker(ctx, sched, pipeline.Slug)
	}
}

// maybeTripCircuitBreaker checks whether this FAILED fire pushed the
// schedule's consecutive-failure streak to (or past) its trip
// threshold and, if so, disables it (#1405). sched is the row as it
// was BEFORE this fire's recordRun — its ConsecutiveFailures is the
// pre-fire count, so + 1 is exactly the count recordRun just
// persisted. Guarded on sched.DisabledReason so a caller that fires an
// already-tripped schedule directly (bypassing listDueSchedules'
// enabled=1 filter) can never re-trip or double-alert.
// maxMissedOccurrenceScan bounds countMissedOccurrences' walk so a
// pathological case (a sub-minute cron left dark for months) can't spin
// the loop indefinitely. Past this many occurrences we stop counting
// and report the cap — the exact figure doesn't matter once it's this
// large; "the schedule was down for a very long time" is the message.
const maxMissedOccurrenceScan = 10000

// countMissedOccurrences walks cronSched forward from `from` (a
// schedule's stale next_run_at) counting occurrences that fall at or
// before `now` — i.e. fires that a continuously-running process would
// have made but this one didn't. Capped at maxMissedOccurrenceScan.
func countMissedOccurrences(cronSched cron.Schedule, from, now time.Time) int {
	missed := 0
	cursor := from
	for missed < maxMissedOccurrenceScan {
		next := cronSched.Next(cursor)
		if next.After(now) {
			break
		}
		missed++
		cursor = next
	}
	return missed
}

// emitMissedOccurrences records the #1409 observability breadcrumb: a
// schedule recovering from a gap wide enough to have silently absorbed
// one or more cron occurrences.
func (s *PipelineScheduler) emitMissedOccurrences(ctx context.Context, sched *Schedule, missed int, windowStart, windowEnd time.Time) {
	s.logger.Warn("pipeline scheduler: schedule recovering from downtime — occurrences skipped",
		"schedule", sched.ID, "missed", missed, "window_start", windowStart, "window_end", windowEnd)
	_, _ = s.emitter.Emit(ctx, journal.Entry{
		WorkspaceID: sched.WorkspaceID,
		Type:        journal.EntryPipelineScheduleMissedOccurrences,
		Severity:    journal.SeverityWarn,
		ActorType:   journal.ActorOrchestrator,
		ActorID:     sched.ID,
		Summary: fmt.Sprintf("Schedule %s skipped %d occurrence(s) between %s and %s",
			sched.Name, missed, windowStart.UTC().Format(time.RFC3339), windowEnd.UTC().Format(time.RFC3339)),
		Payload: map[string]any{
			"schedule_id":  sched.ID,
			"missed_count": missed,
			"window_start": windowStart.UTC().Format(time.RFC3339),
			"window_end":   windowEnd.UTC().Format(time.RFC3339),
		},
	})
}

func (s *PipelineScheduler) maybeTripCircuitBreaker(ctx context.Context, sched *Schedule, pipelineSlug string) {
	if sched.DisabledReason == scheduleDisabledReasonCircuitBreaker {
		return
	}
	maxFailures := sched.MaxConsecutiveFailures
	if maxFailures <= 0 {
		maxFailures = defaultMaxConsecutiveFailures
	}
	newCount := sched.ConsecutiveFailures + 1
	if newCount < maxFailures {
		return
	}
	if err := s.store.disableForCircuitBreaker(ctx, sched.ID); err != nil {
		s.logger.Warn("pipeline scheduler: circuit breaker disable", "error", err, "schedule", sched.ID)
		return
	}
	label := pipelineSlug
	if label == "" {
		label = "target routine"
	}
	s.logger.Warn("pipeline scheduler: circuit breaker tripped — schedule disabled",
		"schedule", sched.ID, "pipeline", label, "consecutive_failures", newCount, "max", maxFailures)
	_, _ = s.emitter.Emit(ctx, journal.Entry{
		WorkspaceID: sched.WorkspaceID,
		Type:        journal.EntryPipelineScheduleCircuitBreaker,
		Severity:    journal.SeverityError,
		ActorType:   journal.ActorOrchestrator,
		ActorID:     sched.ID,
		Summary:     fmt.Sprintf("Schedule %s disabled after %d straight failures", sched.Name, newCount),
		Payload: map[string]any{
			"schedule_id":              sched.ID,
			"pipeline_slug":            label,
			"consecutive_failures":     newCount,
			"max_consecutive_failures": maxFailures,
		},
	})
	if err := inbox.Insert(ctx, s.store.db, s.logger, inbox.Item{
		WorkspaceID: sched.WorkspaceID,
		Kind:        "schedule_circuit_breaker_tripped",
		SourceID:    sched.ID,
		TargetRole:  "MANAGER",
		Title:       fmt.Sprintf("Routine %s paused after %d straight failures", label, newCount),
		BodyMD: fmt.Sprintf(
			"Schedule **%s** (routine `%s`) failed %d times in a row and has been auto-disabled to stop the spam / cost bleed. "+
				"Inspect the recent failed runs, fix the cause, then `crewship routine schedules enable %s`.",
			sched.Name, label, newCount, sched.ID),
		SenderType: "pipeline",
		SenderName: sched.Name,
		Priority:   "high",
		Payload: map[string]interface{}{
			"schedule_id":          sched.ID,
			"consecutive_failures": newCount,
		},
	}); err != nil {
		s.logger.Warn("pipeline scheduler: circuit breaker inbox alert", "error", err, "schedule", sched.ID)
	}
}

// alertFailedScheduledRun surfaces a failed cron run as a MANAGER inbox item
// so a broken cron doesn't fail silently — nobody is watching a scheduled run
// live. Invoked for EVERY recordRun(..., "FAILED", ...) path, including the
// early target-load failure where pipelineID/pipelineSlug are unknown.
//
// Dedup key (SourceID) is the run id, falling back to the schedule id when the
// run never started, so INSERT OR IGNORE yields one item per failed run rather
// than a flood. Ad-hoc runs never call this — the operator who triggered them
// is already looking at the result.
func (s *PipelineScheduler) alertFailedScheduledRun(ctx context.Context, sched *Schedule, pipelineID, pipelineSlug, runID, errLine string) {
	sourceID := runID
	if sourceID == "" {
		sourceID = sched.ID
	}
	label := pipelineSlug
	if label == "" {
		label = "target routine"
	}
	if err := inbox.Insert(ctx, s.store.db, s.logger, inbox.Item{
		WorkspaceID: sched.WorkspaceID,
		Kind:        "failed_run",
		SourceID:    sourceID,
		TargetRole:  "MANAGER",
		Title:       fmt.Sprintf("Scheduled routine failed: %s", label),
		BodyMD:      fmt.Sprintf("Schedule **%s** fired `%s` and it failed — %s.", sched.Name, label, errLine),
		SenderType:  "pipeline",
		SenderName:  sched.Name,
		Priority:    "high",
		Payload: map[string]interface{}{
			"schedule_id": sched.ID,
			"pipeline_id": pipelineID,
			"run_id":      runID,
		},
	}); err != nil {
		s.logger.Warn("pipeline scheduler: inbox alert on failed run", "error", err, "schedule", sched.ID)
	}
}

// evalWakeProbe maps a wake-probe invocation's outcome to
// (proceed, wakeStatus). This is the single security-critical wake-gate
// decision (#1372): the ONLY affirmative outcome is a COMPLETED run whose
// final output is truthy. Every other shape — an executor error, a nil
// result, a timeout (surfaced as an error), or any non-COMPLETED status —
// is "non-affirmative" and its handling is governed by failClosed:
//
//   - COMPLETED + truthy output → (true,  WOKE)     — probe said wake
//   - COMPLETED + falsey output → (false, SKIPPED)  — probe said skip
//   - non-affirmative, fail-OPEN  (default) → (true,  ERROR) — fire anyway
//   - non-affirmative, fail-CLOSED          → (false, HELD)  — hold the run
//
// Fail-OPEN stays the default so a plain monitoring schedule whose probe
// flaps does not go silently blind; the ERROR status keeps the breakage
// visible. Fail-CLOSED is the safe default for an UNATTENDED gate: a
// broken or tampered probe must not be the thing that green-lights an
// autonomous run, so any outcome that cannot AFFIRM "wake" holds instead.
//
// Truthiness reuses evalIfCondition, the same falsey rule step `if:`
// conditions use (empty/false/0/null/nil/no/off → skip), so probe
// authors learn one rule for both features. Note an empty/ambiguous
// output on a COMPLETED run is already non-affirmative → SKIPPED, so it
// never fires the main run regardless of policy.
func evalWakeProbe(res *RunResult, runErr error, failClosed bool) (bool, string) {
	// A DEDUPED probe means a duplicate tick (or a restart before next_run_at
	// advanced) hit the probe's own idempotency key — the ORIGINAL tick's
	// probe already owns the wake decision for this occurrence (#1430, 3.6).
	// Treating DEDUPED as a generic non-affirmative outcome would fail OPEN and
	// fire the main routine off the dedupe, even when the in-flight probe would
	// return falsey. It is neither a truthy probe nor a failure: do NOT fire,
	// and don't touch the fail-open/closed policy (there is nothing broken to
	// protect against — a probe IS deciding). Record it as a skipped tick.
	if runErr == nil && res != nil && res.Status == "DEDUPED" {
		return false, WakeStatusSkipped
	}
	if runErr != nil || res == nil || res.Status != "COMPLETED" {
		if failClosed {
			return false, WakeStatusHeld
		}
		return true, WakeStatusError
	}
	if evalIfCondition(res.Output) {
		return true, WakeStatusWoke
	}
	return false, WakeStatusSkipped
}

// runWakeCheck executes the schedule's probe routine and delegates the
// outcome mapping to evalWakeProbe (which owns the fail-open/fail-closed
// policy). A non-affirmative probe is logged so a broken gate stays
// visible whether it failed open (fired anyway) or closed (held).
func (s *PipelineScheduler) runWakeCheck(ctx context.Context, sched *Schedule) (bool, string) {
	var wakeInputs map[string]any
	if sched.WakeInputsJSON != "" {
		_ = json.Unmarshal([]byte(sched.WakeInputsJSON), &wakeInputs)
	}
	// The wake probe is an arbitrary user routine that CAN have side effects, so
	// a re-fire of the same occurrence must dedupe too (not just the main run).
	// Key it on the occurrence with a "wake" discriminator so it never collides
	// with the main run's key for the same tick.
	wakeBucket := time.Now().UTC().Truncate(time.Minute).Format(time.RFC3339)
	if sched.NextRunAt != nil {
		wakeBucket = sched.NextRunAt.UTC().Format(time.RFC3339)
	}
	res, err := s.executor.Run(ctx, RunInput{
		PipelineID:     sched.WakePipelineID,
		WorkspaceID:    sched.WorkspaceID,
		Inputs:         wakeInputs,
		Mode:           ModeRun,
		TriggeredVia:   TriggeredViaWakeCheck,
		TriggeredByID:  sched.ID,
		IdempotencyKey: ScheduledFireIdempotencyKey("sched-wake", sched.ID, wakeBucket),
	})
	proceed, status := evalWakeProbe(res, err, sched.WakeFailClosed)
	if status == WakeStatusError || status == WakeStatusHeld {
		// Scrub before logging: a wake-probe error can echo rendered step
		// content, which may include a templated secret ({{ secrets.* }}).
		errMsg := ""
		if err != nil {
			errMsg = scriptAuditScrubber.Scrub(err.Error())
		} else if res != nil {
			errMsg = scriptAuditScrubber.Scrub(res.ErrorMessage)
		}
		disposition := "failing open — firing main run"
		if status == WakeStatusHeld {
			disposition = "failing closed — holding run"
		}
		s.logger.Warn("pipeline scheduler: wake check failed — "+disposition,
			"schedule", sched.ID, "wake_pipeline", sched.WakePipelineID,
			"fail_closed", sched.WakeFailClosed, "error", errMsg)
	}
	return proceed, status
}

const scheduleSelect = `
SELECT id, workspace_id, name, target_pipeline_id, target_pipeline_version,
       cron_expr, timezone, inputs_json, enabled,
       last_run_at, COALESCE(last_status, ''), COALESCE(last_run_id, ''),
       next_run_at,
       COALESCE(wake_pipeline_id, ''), COALESCE(wake_inputs_json, '{}'),
       wake_check_count, wake_fire_count,
       last_wake_at, COALESCE(last_wake_status, ''),
       COALESCE(wake_fail_closed, 0),
       COALESCE(consecutive_failures, 0), COALESCE(max_consecutive_failures, 5),
       COALESCE(disabled_reason, ''),
       created_at, updated_at, deleted_at
FROM pipeline_schedules`

func scanSchedule(rs rowScanner) (*Schedule, error) {
	var (
		s             Schedule
		targetVersion sql.NullInt64
		lastRunAt     sql.NullString
		nextRunAt     sql.NullString
		lastWakeAt    sql.NullString
		deletedAt     sql.NullString
		enabled       int
		wakeFailClsd  int
		createdAt     string
		updatedAt     string
	)
	err := rs.Scan(
		&s.ID, &s.WorkspaceID, &s.Name, &s.TargetPipelineID, &targetVersion,
		&s.CronExpr, &s.Timezone, &s.InputsJSON, &enabled,
		&lastRunAt, &s.LastStatus, &s.LastRunID,
		&nextRunAt,
		&s.WakePipelineID, &s.WakeInputsJSON,
		&s.WakeCheckCount, &s.WakeFireCount,
		&lastWakeAt, &s.LastWakeStatus,
		&wakeFailClsd,
		&s.ConsecutiveFailures, &s.MaxConsecutiveFailures,
		&s.DisabledReason,
		&createdAt, &updatedAt, &deletedAt,
	)
	if err != nil {
		return nil, err
	}
	s.Enabled = enabled != 0
	s.WakeFailClosed = wakeFailClsd != 0
	if targetVersion.Valid {
		v := int(targetVersion.Int64)
		s.TargetPipelineVersion = &v
	}
	s.LastRunAt = parseTimePtr(lastRunAt.String)
	s.NextRunAt = parseTimePtr(nextRunAt.String)
	s.LastWakeAt = parseTimePtr(lastWakeAt.String)
	s.CreatedAt = parseTimeOrZero(createdAt)
	s.UpdatedAt = parseTimeOrZero(updatedAt)
	if deletedAt.Valid {
		t := parseTimeOrZero(deletedAt.String)
		s.DeletedAt = &t
	}
	return &s, nil
}

func generateScheduleID() string {
	ts := time.Now().UnixMilli()
	c := scheduleIDCounter.Add(1)
	tail := c % 65536
	const hexdigits = "0123456789abcdef"
	rb := make([]byte, 4)
	if _, err := rand.Read(rb); err != nil {
		for i := range rb {
			rb[i] = byte(c >> (i * 8))
		}
	}
	return "psched_c" + strconv.FormatInt(ts, 36) +
		string([]byte{
			hexdigits[(tail>>12)&0xf], hexdigits[(tail>>8)&0xf],
			hexdigits[(tail>>4)&0xf], hexdigits[tail&0xf],
		}) + hex.EncodeToString(rb)
}

var scheduleIDCounter atomic.Uint64

func nullInt(p *int) any {
	if p == nil {
		return sql.NullInt64{}
	}
	return *p
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
