package pipeline

import (
	"context"
	"crypto/rand"
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
	LastStatus            string
	LastRunID             string
	NextRunAt             *time.Time

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
	LastWakeStatus string // WOKE | SKIPPED | ERROR

	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt *time.Time
}

// Wake check outcomes recorded in pipeline_schedules.last_wake_status.
const (
	WakeStatusWoke    = "WOKE"    // probe truthy (or absent) — main routine fired
	WakeStatusSkipped = "SKIPPED" // probe falsey — tick skipped, no main run
	WakeStatusError   = "ERROR"   // probe failed — failed OPEN, main routine fired
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

	if in.ID == "" {
		// Create
		id := generateScheduleID()
		_, err = s.db.ExecContext(ctx, `
INSERT INTO pipeline_schedules (
    id, workspace_id, name, target_pipeline_id, target_pipeline_version,
    cron_expr, timezone, inputs_json, enabled, next_run_at,
    wake_pipeline_id, wake_inputs_json,
    created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, in.WorkspaceID, in.Name, in.TargetPipelineID,
			nullInt(in.TargetPipelineVersion),
			in.CronExpr, in.Timezone, string(inputsJSON), boolToInt(in.Enabled),
			nextRun.UTC().Format(time.RFC3339Nano),
			nullStr(in.WakePipelineID), string(wakeInputsJSON),
			now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano),
		)
		if err != nil {
			return nil, fmt.Errorf("insert schedule: %w", err)
		}
		return s.GetByID(ctx, id)
	}

	// Update
	_, err = s.db.ExecContext(ctx, `
UPDATE pipeline_schedules
SET name = ?, target_pipeline_id = ?, target_pipeline_version = ?,
    cron_expr = ?, timezone = ?, inputs_json = ?, enabled = ?,
    next_run_at = ?, wake_pipeline_id = ?, wake_inputs_json = ?,
    updated_at = ?
WHERE id = ? AND deleted_at IS NULL`,
		in.Name, in.TargetPipelineID, nullInt(in.TargetPipelineVersion),
		in.CronExpr, in.Timezone, string(inputsJSON), boolToInt(in.Enabled),
		nextRun.UTC().Format(time.RFC3339Nano),
		nullStr(in.WakePipelineID), string(wakeInputsJSON),
		now.Format(time.RFC3339Nano),
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
	now := time.Now().UTC().Format(time.RFC3339Nano)
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
func (s *ScheduleStore) listDueSchedules(ctx context.Context) ([]*Schedule, error) {
	rows, err := s.db.QueryContext(ctx, scheduleSelect+`
WHERE enabled = 1 AND deleted_at IS NULL AND next_run_at <= ?
ORDER BY next_run_at ASC LIMIT 100`,
		time.Now().UTC().Format(time.RFC3339Nano))
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
func (s *ScheduleStore) recordRun(ctx context.Context, scheduleID, runID, status string, nextRun time.Time) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `
UPDATE pipeline_schedules
SET last_run_at = ?, last_status = ?, last_run_id = ?, next_run_at = ?, updated_at = ?
WHERE id = ?`,
		now, status, nullStr(runID), nextRun.UTC().Format(time.RFC3339Nano), now, scheduleID,
	)
	return err
}

// recordWakeCheck persists the outcome of one wake-gate evaluation.
// Counters + last_wake_* are wake-only telemetry — last_run_* stays
// strictly about main runs. advanceNext is true on a SKIPPED tick
// (no main run follows, so the wake record is what moves next_run_at
// forward); on WOKE/ERROR the subsequent recordRun advances it.
func (s *ScheduleStore) recordWakeCheck(ctx context.Context, scheduleID, status string, nextRun time.Time, advanceNext bool) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
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
			fired, now, status, nextRun.UTC().Format(time.RFC3339Nano), now, scheduleID,
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
// the supplied Executor, and updates next_run_at. Single-instance
// — running multiple replicas would double-fire schedules; we'll
// add leader election alongside crew-level state in a follow-up.
type PipelineScheduler struct {
	store     *ScheduleStore
	pipelines *Store // pipeline lookup for the run
	executor  *Executor
	logger    *slog.Logger

	stopCh    chan struct{}
	stopped   chan struct{}
	startOnce sync.Once
	stopOnce  sync.Once
}

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

func (s *PipelineScheduler) tick(ctx context.Context) {
	due, err := s.store.listDueSchedules(ctx)
	if err != nil {
		s.logger.Warn("pipeline scheduler: list due", "error", err)
		return
	}
	for _, sched := range due {
		s.fireOne(ctx, sched)
	}
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
		return
	}
	var inputs map[string]any
	if sched.InputsJSON != "" {
		_ = json.Unmarshal([]byte(sched.InputsJSON), &inputs)
	}

	in := RunInput{
		PipelineID:    pipeline.ID,
		WorkspaceID:   sched.WorkspaceID,
		Inputs:        inputs,
		Mode:          ModeRun,
		TriggeredVia:  TriggeredViaSchedule,
		TriggeredByID: sched.ID,
	}

	res, runErr := s.executor.Run(ctx, in)
	status := "FAILED"
	runID := ""
	if res != nil {
		runID = res.RunID
		if runErr == nil && res.Status == "COMPLETED" {
			status = "COMPLETED"
		}
	}
	if runErr != nil {
		s.logger.Warn("pipeline scheduler: run failed", "schedule", sched.ID, "pipeline", pipeline.Slug, "error", runErr)
	}

	if err := s.store.recordRun(ctx, sched.ID, runID, status, nextRun); err != nil {
		s.logger.Warn("pipeline scheduler: record run", "error", err)
	}
}

// runWakeCheck executes the schedule's probe routine and maps its
// outcome to (proceed, wakeStatus):
//
//   - probe COMPLETED + truthy final output → (true, WOKE)
//   - probe COMPLETED + falsey final output → (false, SKIPPED)
//   - probe failed / didn't load            → (true, ERROR)
//
// Errors fail OPEN: a monitoring schedule whose probe broke must wake
// the main routine rather than go silently blind — occasional token
// spend on a flapping probe is the cheaper failure mode. The ERROR
// wake status keeps the breakage visible to operators.
//
// Truthiness reuses evalIfCondition, the same falsey rule step `if:`
// conditions use (empty/false/0/null/nil/no/off → skip), so probe
// authors learn one rule for both features.
func (s *PipelineScheduler) runWakeCheck(ctx context.Context, sched *Schedule) (bool, string) {
	var wakeInputs map[string]any
	if sched.WakeInputsJSON != "" {
		_ = json.Unmarshal([]byte(sched.WakeInputsJSON), &wakeInputs)
	}
	res, err := s.executor.Run(ctx, RunInput{
		PipelineID:    sched.WakePipelineID,
		WorkspaceID:   sched.WorkspaceID,
		Inputs:        wakeInputs,
		Mode:          ModeRun,
		TriggeredVia:  TriggeredViaWakeCheck,
		TriggeredByID: sched.ID,
	})
	if err != nil || res == nil || res.Status != "COMPLETED" {
		errMsg := ""
		if err != nil {
			errMsg = err.Error()
		} else if res != nil {
			errMsg = res.ErrorMessage
		}
		s.logger.Warn("pipeline scheduler: wake check failed — failing open",
			"schedule", sched.ID, "wake_pipeline", sched.WakePipelineID, "error", errMsg)
		return true, WakeStatusError
	}
	if evalIfCondition(res.Output) {
		return true, WakeStatusWoke
	}
	return false, WakeStatusSkipped
}

const scheduleSelect = `
SELECT id, workspace_id, name, target_pipeline_id, target_pipeline_version,
       cron_expr, timezone, inputs_json, enabled,
       last_run_at, COALESCE(last_status, ''), COALESCE(last_run_id, ''),
       next_run_at,
       COALESCE(wake_pipeline_id, ''), COALESCE(wake_inputs_json, '{}'),
       wake_check_count, wake_fire_count,
       last_wake_at, COALESCE(last_wake_status, ''),
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
		&createdAt, &updatedAt, &deletedAt,
	)
	if err != nil {
		return nil, err
	}
	s.Enabled = enabled != 0
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
