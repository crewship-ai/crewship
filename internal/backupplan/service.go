package backupplan

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"filippo.io/age"

	"github.com/crewship-ai/crewship/internal/backup"
)

// Clock is the service's notion of now. Tests pass a fake.
type Clock interface{ Now() time.Time }

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

// BusyChecker says whether a run's targets have work in flight. ids empty
// means the whole instance.
type BusyChecker interface {
	Busy(ctx context.Context, workspaceIDs []string) (busy bool, detail string, err error)
}

// Pauser reports an instance-wide hold on scheduled work (after an instance
// restore every schedule is held until an admin resumes — Track B). While
// paused, due runs are recorded as skipped with the reason.
type Pauser interface {
	Paused(ctx context.Context) (paused bool, reason string)
}

// Quiescer is the quiet window (Track B): drain running work and hold every
// write to protected data for the consistent copy only, never longer than
// capDur. The release func is called as soon as the copy ends (or on error).
type Quiescer interface {
	Quiesce(ctx context.Context, spec RunSpec, capDur time.Duration) (release func(), err error)
}

// LeaderGate: only the leader schedules and dispatches (multi-replica).
type LeaderGate interface{ IsLeader() bool }

const (
	// DefaultConcurrency is how many runs execute at once until the backup
	// settings (Track C2) say otherwise. The rest queue.
	DefaultConcurrency = 1
	// CatchUpGrace: a due time the scheduler reaches later than this (the
	// server was down, or busy past it) counts as missed, and missed due
	// times make ONE catch-up run.
	CatchUpGrace = 10 * time.Minute
	// TickInterval is how often the loop looks at due plans and the queue.
	TickInterval = 30 * time.Second
	// SchedulerActorID is the actor a scheduled run's bundle records.
	SchedulerActorID = "system:backup-scheduler"
	// defaultBusyWait / defaultBusyRetry apply to a manual run with no plan.
	defaultBusyWait  = 120
	defaultBusyRetry = 15
)

// Service is the backup scheduler and run executor.
type Service struct {
	DB         *sql.DB
	Logger     *slog.Logger
	Clock      Clock
	Workspace  Executor
	Instance   Executor
	Busy       BusyChecker
	Pause      Pauser
	Quiesce    Quiescer
	Leader     LeaderGate
	Recipients RecipientResolver
	// Concurrency returns the number of runs that may execute at once.
	Concurrency func(ctx context.Context) int
	// Verify is the check phase (default: backup.Verify, the checksum).
	Verify func(ctx context.Context, path string) error
	// BackupsDir is where bundles land (default backup.DefaultBackupsDir),
	// used to sweep an interrupted run's partial file.
	BackupsDir func() (string, error)
	// TempDir holds CreateBackup's staging files (default os.TempDir()).
	TempDir string
	// Alerts delivers incidents (nil: incidents are recorded only).
	Alerts Alerter
	// Destinations opens a plan's off-site destinations (default: the
	// stored ones, secret unsealed with the vault key).
	Destinations DestinationOpener
	// Space reports free/total bytes for the space floor (default: the
	// filesystem of the backups directory).
	Space SpaceFunc
	// HeartbeatClient pings the heartbeat URL (default: httpsafe's
	// public-https client).
	HeartbeatClient *http.Client
	// allowPrivateHeartbeat lets package tests ping an httptest server.
	allowPrivateHeartbeat bool
	limiters              limiterSet
	lastStale             time.Time
	lastDrill             time.Time

	// passphrases holds a manual run's passphrase, by run id, only until
	// the run takes it. It is never written to the database: a run whose
	// server restarts before it starts fails with "the passphrase is gone".
	keysMu      sync.Mutex
	passphrases map[string]string

	mu      sync.Mutex
	active  int
	wg      sync.WaitGroup
	runCtx  context.Context
	kick    chan struct{}
	started bool
}

// New returns a service with the defaults. The caller sets the workspace and
// instance executors; a run whose scope has none fails and says so.
func New(db *sql.DB, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		DB: db, Logger: logger, Clock: systemClock{},
		Busy: DBBusy{DB: db}, Recipients: DBRecipients{DB: db},
		Concurrency: SettingsConcurrency(db),
	}
}

func (s *Service) now() time.Time { return s.Clock.Now().UTC() }

func (s *Service) concurrency(ctx context.Context) int {
	if s.Concurrency != nil {
		if n := s.Concurrency(ctx); n > 0 {
			return n
		}
	}
	return DefaultConcurrency
}

func (s *Service) isLeader() bool { return s.Leader == nil || s.Leader.IsLeader() }

// Start recovers runs a previous process left behind, then runs the loop
// until ctx ends: every TickInterval (and whenever a manual run is queued)
// it starts due plans and dispatches queued runs.
func (s *Service) Start(ctx context.Context) {
	if n, err := s.RecoverAtBoot(ctx); err != nil {
		s.Logger.Warn("backup scheduler: boot recovery failed", "error", err)
	} else if n > 0 {
		s.Logger.Info("backup scheduler: runs interrupted by the last shutdown marked and retried", "count", n)
	}
	s.mu.Lock()
	s.runCtx = ctx
	s.kick = make(chan struct{}, 1)
	s.started = true
	s.mu.Unlock()
	go s.loop(ctx)
}

func (s *Service) loop(ctx context.Context) {
	t := time.NewTicker(TickInterval)
	defer t.Stop()
	s.Tick(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.kick:
		}
		s.Tick(ctx)
	}
}

// Kick asks for a dispatch now (a manual run was queued). Without Start
// (a router in tests, or before the loop runs) it dispatches on its own.
func (s *Service) Kick() {
	s.mu.Lock()
	started, kick := s.started, s.kick
	s.mu.Unlock()
	if started {
		select {
		case kick <- struct{}{}:
		default:
		}
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.dispatch(context.Background())
	}()
}

// Wait blocks until every run this service started has finished (tests).
func (s *Service) Wait() { s.wg.Wait() }

// Tick is one pass: start due plans, then dispatch queued runs. Exported so
// tests drive the scheduler with a fake clock and no sleeps.
func (s *Service) Tick(ctx context.Context) {
	if !s.isLeader() {
		return
	}
	if err := s.schedulePlans(ctx); err != nil {
		s.Logger.Warn("backup scheduler: schedule pass failed", "error", err)
	}
	s.dispatch(ctx)
	s.checkStale(ctx)
	s.checkDrillReminder(ctx)
}

func initialPhases(env bool) []Phase {
	names := []string{PhaseCopy, PhasePack, PhaseEncrypt, PhaseCheck, PhaseOffsite}
	out := make([]Phase, 0, len(names)+1)
	for _, n := range names {
		out = append(out, Phase{Name: n, Status: "pending"})
	}
	if env {
		out = append(out, Phase{Name: PhaseEnvironments, Status: "pending"})
	}
	return out
}

// Targets lists the workspaces a plan's run covers: "" for an instance
// run, or each selected (or every live) workspace.
func (s *Service) Targets(ctx context.Context, p *Plan) ([]string, error) {
	if p.Scope == ScopeInstance {
		return []string{""}, nil
	}
	if len(p.WorkspaceIDs) > 0 {
		live, err := LiveWorkspaceIDs(ctx, s.DB)
		if err != nil {
			return nil, err
		}
		known := map[string]bool{}
		for _, id := range live {
			known[id] = true
		}
		var out []string
		for _, id := range p.WorkspaceIDs {
			if known[id] {
				out = append(out, id)
			}
		}
		return out, nil
	}
	return LiveWorkspaceIDs(ctx, s.DB)
}

// LiveWorkspaceIDs is every workspace that is not deleted, by name.
func LiveWorkspaceIDs(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT id FROM workspaces WHERE deleted_at IS NULL ORDER BY name COLLATE NOCASE, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// WorkspaceLister over the live workspaces table.
type DBWorkspaces struct{ DB *sql.DB }

func (w DBWorkspaces) WorkspaceIDs(ctx context.Context) ([]string, error) {
	return LiveWorkspaceIDs(ctx, w.DB)
}

// carriedCategories is what a plan's custom bundle carries (env excluded:
// environments are their own capture).
func carriedCategories(p *Plan) ([]string, string) {
	if p.Preset != backup.PresetCustom {
		return []string{}, backup.KindFull
	}
	res, err := p.Resolution()
	if err != nil {
		return []string{}, backup.KindCustom
	}
	var out []string
	for _, k := range res.Carried() {
		if k != backup.CategoryEnv {
			out = append(out, k)
		}
	}
	if out == nil {
		out = []string{}
	}
	return out, backup.KindCustom
}

func (s *Service) schedulePlans(ctx context.Context) error {
	now := s.now()
	plans, err := ListPlans(ctx, s.DB)
	if err != nil {
		return err
	}
	for _, p := range plans {
		if !p.Enabled {
			continue
		}
		if p.NextRunAt == nil {
			SetNextRun(p, now)
			if _, err := s.DB.ExecContext(ctx, `UPDATE backup_plans SET next_run_at = ? WHERE id = ? AND next_run_at IS NULL`,
				nullStrPtr(p.NextRunAt), p.ID); err != nil {
				return err
			}
			continue
		}
		next, err := parseTS(*p.NextRunAt)
		if err != nil || next.After(now) {
			continue
		}
		if err := s.claimDue(ctx, p, next, now); err != nil {
			s.Logger.Warn("backup scheduler: could not start a due plan", "plan", p.ID, "error", err)
		}
	}
	return nil
}

// claimDue starts the plan's due run(s). Every due time between next and now
// that the scheduler reaches late (beyond CatchUpGrace) or together with
// another is missed; missed times make ONE catch-up run, never one per
// missed night. The runs and the plan's advanced next_run_at are written in
// one transaction, and the claim index makes a second claim of the same due
// time a no-op.
func (s *Service) claimDue(ctx context.Context, p *Plan, next, now time.Time) error {
	missed := OccurrencesBetween(p, next, now, 10000)
	if len(missed) == 0 {
		missed = []Occurrence{{At: next}}
	}
	due := missed[len(missed)-1]
	trigger, env := TriggerSchedule, due.Environments
	if len(missed) > 1 || now.Sub(missed[0].At) > CatchUpGrace {
		trigger = TriggerCatchup
		for _, m := range missed {
			env = env || m.Environments
		}
	}
	targets, err := s.Targets(ctx, p)
	if err != nil {
		return err
	}
	cats, kind := carriedCategories(p)
	paused, reason := false, ""
	if s.Pause != nil {
		paused, reason = s.Pause.Paused(ctx)
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	dueAt := due.At
	for _, ws := range targets {
		r := &Run{
			PlanID: p.ID, Trigger: trigger, Scope: p.Scope, WorkspaceID: ws, Kind: kind, Categories: cats,
			Environments: env, Status: StatusRunning, Phase: PhaseQueued, Phases: initialPhases(env),
			DueAt: &dueAt, NextAttemptAt: &now, Recipients: p.RecipientIDs, StartedAt: now,
		}
		if paused {
			r.Status, r.Phase, r.NextAttemptAt, r.EndedAt = StatusSkipped, "", nil, &now
			r.Error = "scheduled backups are held: " + reason
		}
		if _, err := insertRun(ctx, tx, r); err != nil {
			return err
		}
	}
	SetNextRun(p, now)
	if _, err := tx.ExecContext(ctx, `UPDATE backup_plans SET next_run_at = ?, last_run_at = ? WHERE id = ?`,
		nullStrPtr(p.NextRunAt), ts(dueAt), p.ID); err != nil {
		return err
	}
	return tx.Commit()
}

// ─── Dispatch and execution ─────────────────────────────────────────────────

func (s *Service) planOf(ctx context.Context, id string) *Plan {
	if id == "" {
		return nil
	}
	p, err := GetPlan(ctx, s.DB, id)
	if err != nil {
		return nil
	}
	return p
}

func busyWindow(p *Plan) (time.Duration, time.Duration) {
	if p == nil {
		return defaultBusyWait * time.Minute, defaultBusyRetry * time.Minute
	}
	retry := p.BusyRetry
	if retry < 1 {
		retry = defaultBusyRetry
	}
	return time.Duration(p.BusyWait) * time.Minute, time.Duration(retry) * time.Minute
}

func (s *Service) dispatch(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	free := s.concurrency(ctx) - s.active
	if free <= 0 {
		return
	}
	now := s.now()
	rows, err := s.DB.QueryContext(ctx, `SELECT `+runColumns+` FROM backup_runs
		WHERE status = 'running' AND phase IN ('queued','busy_wait') AND (next_attempt_at IS NULL OR next_attempt_at <= ?)
		ORDER BY started_at, id`, ts(now))
	if err != nil {
		s.Logger.Warn("backup scheduler: list queued runs", "error", err)
		return
	}
	var queued []*Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			s.Logger.Warn("backup scheduler: read queued run", "error", err)
			continue
		}
		queued = append(queued, r)
	}
	_ = rows.Close()
	runCtx := s.runCtx
	if runCtx == nil {
		runCtx = context.Background()
	}
	for _, r := range queued {
		if free <= 0 {
			return
		}
		plan := s.planOf(ctx, r.PlanID)
		var ids []string
		if r.WorkspaceID != "" {
			ids = []string{r.WorkspaceID}
		}
		if s.Busy != nil {
			busy, detail, err := s.Busy.Busy(ctx, ids)
			if err != nil {
				s.Logger.Warn("backup scheduler: busy check failed; running anyway", "run", r.ID, "error", err)
			} else if busy {
				s.handleBusy(ctx, r, plan, detail, now)
				continue
			}
		}
		res, err := s.DB.ExecContext(ctx, `UPDATE backup_runs SET phase = ?, next_attempt_at = NULL
			WHERE id = ? AND status = 'running' AND phase IN ('queued','busy_wait')`, PhaseStarting, r.ID)
		if err != nil {
			s.Logger.Warn("backup scheduler: claim run", "run", r.ID, "error", err)
			continue
		}
		if n, _ := res.RowsAffected(); n != 1 {
			continue
		}
		r.Phase = PhaseStarting
		s.active++
		free--
		s.wg.Add(1)
		go s.execute(runCtx, r, plan)
	}
}

// handleBusy keeps a run waiting while its targets are busy, up to the
// plan's busy window, retrying every busy_retry_minutes; then it records the
// run as skipped (Track C2 raises the incident from that row).
func (s *Service) handleBusy(ctx context.Context, r *Run, plan *Plan, detail string, now time.Time) {
	wait, retry := busyWindow(plan)
	deadline := r.StartedAt.Add(wait)
	if now.Before(deadline) {
		nextAttempt := now.Add(retry)
		if nextAttempt.After(deadline) {
			nextAttempt = deadline
		}
		msg := fmt.Sprintf("%s; next look %s, gives up at %s", detail, nextAttempt.Format("15:04 MST"), deadline.Format("15:04 MST"))
		setWaitPhase(r, now, "running", msg)
		if _, err := s.DB.ExecContext(ctx, `UPDATE backup_runs SET phase = ?, phases = ?, next_attempt_at = ? WHERE id = ? AND status = 'running'`,
			PhaseBusyWait, mustJSON(r.Phases), ts(nextAttempt), r.ID); err != nil {
			s.Logger.Warn("backup scheduler: park busy run", "run", r.ID, "error", err)
		}
		return
	}
	msg := fmt.Sprintf("skipped: still busy after %d minutes (%s)", int(wait/time.Minute), detail)
	setWaitPhase(r, now, "failed", msg)
	markPending(r, "skipped")
	r.Status, r.Error, r.EndedAt = StatusSkipped, msg, &now
	if err := finishRun(ctx, s.DB, r); err != nil {
		s.Logger.Warn("backup scheduler: record skipped run", "run", r.ID, "error", err)
	}
	s.afterRun(ctx, r, plan)
}

func phaseIndex(r *Run, name string) int {
	for i, p := range r.Phases {
		if p.Name == name {
			return i
		}
	}
	return -1
}

func setWaitPhase(r *Run, now time.Time, status, detail string) {
	i := phaseIndex(r, PhaseWait)
	if i < 0 {
		started := ts(now)
		r.Phases = append([]Phase{{Name: PhaseWait, StartedAt: &started}}, r.Phases...)
		i = 0
	}
	r.Phases[i].Status = status
	r.Phases[i].Detail = &detail
	if status != "running" {
		ended := ts(now)
		r.Phases[i].EndedAt = &ended
	}
}

func markPending(r *Run, status string) {
	for i := range r.Phases {
		if r.Phases[i].Status == "pending" || r.Phases[i].Status == "running" {
			r.Phases[i].Status = status
		}
	}
}

func (s *Service) startPhase(r *Run, name string) {
	i := phaseIndex(r, name)
	if i < 0 {
		r.Phases = append(r.Phases, Phase{Name: name})
		i = len(r.Phases) - 1
	}
	now := ts(s.now())
	r.Phases[i].StartedAt, r.Phases[i].Status, r.Phases[i].EndedAt = &now, "running", nil
	r.Phase = name
}

func (s *Service) endPhase(r *Run, name, status, detail string) {
	i := phaseIndex(r, name)
	if i < 0 {
		return
	}
	now := ts(s.now())
	if r.Phases[i].StartedAt == nil {
		r.Phases[i].StartedAt = &now
	}
	r.Phases[i].EndedAt, r.Phases[i].Status = &now, status
	if detail != "" {
		r.Phases[i].Detail = &detail
	}
}

func (s *Service) runningPhase(r *Run) string {
	for _, p := range r.Phases {
		if p.Status == "running" && p.Name != PhaseWait {
			return p.Name
		}
	}
	return ""
}

func (s *Service) actorFor(ctx context.Context, r *Run) backup.Actor {
	if r.ActorUserID == "" {
		return backup.Actor{UserID: SchedulerActorID, Email: "backup-scheduler", Role: "OWNER"}
	}
	var email string
	_ = s.DB.QueryRowContext(ctx, `SELECT email FROM users WHERE id = ?`, r.ActorUserID).Scan(&email)
	// An instance admin need not belong to the workspace; the backup runs
	// with the instance's authority, recorded under their id.
	return backup.Actor{UserID: r.ActorUserID, Email: email, Role: "OWNER"}
}

func (s *Service) execute(ctx context.Context, r *Run, plan *Plan) {
	defer func() {
		s.mu.Lock()
		s.active--
		s.mu.Unlock()
		// Start whatever queued behind this one before this run counts as
		// finished, so Wait covers the whole queue.
		s.dispatch(ctx)
		s.wg.Done()
	}()
	persist := func() {
		if err := saveProgress(ctx, s.DB, r); err != nil {
			s.Logger.Warn("backup run: save progress", "run", r.ID, "error", err)
		}
	}
	fail := func(err error) {
		now := s.now()
		phase := s.runningPhase(r)
		if phase == "" {
			phase = PhaseCopy
		}
		s.endPhase(r, phase, "failed", err.Error())
		markPending(r, "skipped")
		r.Status, r.Error, r.EndedAt = StatusFailed, err.Error(), &now
		if ferr := finishRun(ctx, s.DB, r); ferr != nil {
			s.Logger.Warn("backup run: record failure", "run", r.ID, "error", ferr)
		}
		s.Logger.Warn("backup run failed", "run", r.ID, "plan", r.PlanID, "workspace", r.WorkspaceID, "error", err)
		s.afterRun(ctx, r, plan)
	}
	if i := phaseIndex(r, PhaseWait); i >= 0 && r.Phases[i].Status == "running" {
		setWaitPhase(r, s.now(), "done", "")
	}
	passphrase, hasPass := s.takePassphrase(r.ID)
	var recipients []age.Recipient
	if !hasPass {
		if len(r.Recipients) == 0 {
			fail(errors.New("cannot encrypt: this run was started with a passphrase, which is held in memory only and was lost when the server restarted; start it again"))
			return
		}
		var err error
		recipients, _, err = s.Recipients.Resolve(ctx, r.Recipients)
		if err != nil {
			fail(fmt.Errorf("cannot encrypt: %w", err))
			return
		}
	}
	preset := backup.PresetWorkspace
	if plan != nil {
		preset = plan.Preset
	}
	if r.Kind == backup.KindCustom {
		preset = backup.PresetCustom
	}
	set := s.settings(ctx)
	diskLimiter, uploadLimiter := s.limiters.get(set.Limits)
	spec := RunSpec{
		RunID: r.ID, PlanID: r.PlanID, Preset: preset, Scope: r.Scope, WorkspaceID: r.WorkspaceID,
		Environments: r.Environments, Recipients: recipients, Passphrase: passphrase, Actor: s.actorFor(ctx, r),
		HoldCap: 20 * time.Minute, EncoderConcurrency: set.Limits.CPUCores,
	}
	if diskLimiter != nil {
		spec.DiskThrottle = diskLimiter
	}
	if plan != nil && plan.HoldCap > 0 {
		spec.HoldCap = time.Duration(plan.HoldCap) * time.Minute
	}
	if r.Kind == backup.KindCustom {
		spec.Categories = r.Categories
	}
	exec := s.Workspace
	if r.Scope == ScopeInstance {
		exec = s.Instance
	}
	if exec == nil {
		fail(errors.New("no backup executor is configured for this scope"))
		return
	}

	// The space floor: a run that would leave less than 10 % of the disk
	// free does not start, and says why.
	if reason := s.spaceRefusal(ctx, r); reason != "" {
		now := s.now()
		markPending(r, "skipped")
		r.Status, r.Error, r.EndedAt = StatusSkipped, reason, &now
		if err := finishRun(ctx, s.DB, r); err != nil {
			s.Logger.Warn("backup run: record refusal", "run", r.ID, "error", err)
		}
		s.Logger.Warn("backup run refused: not enough disk space", "run", r.ID, "reason", reason)
		s.afterRun(ctx, r, plan)
		return
	}

	s.startPhase(r, PhaseCopy)
	persist()
	var release func()
	if s.Quiesce != nil {
		var err error
		release, err = s.Quiesce.Quiesce(ctx, spec, spec.HoldCap)
		if err != nil {
			if isBusy(err) {
				s.parkPassphrase(r.ID, passphrase, hasPass)
				s.requeueBusy(ctx, r, plan, err.Error())
				return
			}
			fail(fmt.Errorf("quiet window: %w", err))
			return
		}
	}
	// The hold starts once the window is open, not while it drains.
	holdStart := s.now()
	releaseHold := func() {
		if release != nil {
			release()
			release = nil
			ms := s.now().Sub(holdStart).Milliseconds()
			r.HoldMs = &ms
		}
	}
	defer releaseHold()
	progress := func(phase string) {
		switch phase {
		case "copy":
			// already in copy
		case "pack":
			releaseHold()
			s.endPhase(r, PhaseCopy, "done", "")
			s.startPhase(r, PhasePack)
		case "encrypt":
			s.endPhase(r, PhasePack, "done", "")
			s.startPhase(r, PhaseEncrypt)
		default:
			return
		}
		persist()
	}
	res, err := exec.Run(ctx, spec, progress)
	releaseHold()
	if err != nil {
		if isBusy(err) {
			// Work started between the busy check and the copy: back to
			// the busy window rather than a failure.
			s.parkPassphrase(r.ID, passphrase, hasPass)
			s.requeueBusy(ctx, r, plan, err.Error())
			return
		}
		fail(err)
		return
	}
	for _, name := range []string{PhaseCopy, PhasePack, PhaseEncrypt} {
		if i := phaseIndex(r, name); i >= 0 && r.Phases[i].Status != "done" {
			s.endPhase(r, name, "done", "")
		}
	}
	s.startPhase(r, PhaseCheck)
	persist()
	verify := s.Verify
	if verify == nil {
		verify = verifyChecksum
	}
	checkErr := verify(ctx, res.Path)
	if checkErr != nil {
		s.endPhase(r, PhaseCheck, "failed", checkErr.Error())
	} else {
		s.endPhase(r, PhaseCheck, "done", "checksum matches")
	}
	s.startPhase(r, PhaseOffsite)
	if checkErr != nil {
		s.endPhase(r, PhaseOffsite, "skipped", "the bundle failed its check; nothing was copied")
	} else {
		persist()
		status, detail := s.copyOffsite(ctx, r, plan, res.Path, res.Manifest, uploadLimiter)
		s.endPhase(r, PhaseOffsite, status, detail)
	}
	if r.Environments {
		// The environments are captured with the crews' files (same pause)
		// and saved into the environment store; the manifest says what
		// came of it.
		s.startPhase(r, PhaseEnvironments)
		status, detail := environmentsPhase(res.Manifest)
		s.endPhase(r, PhaseEnvironments, status, detail)
	}

	if res.HoldMS != nil {
		r.HoldMs = res.HoldMS
	}
	entry := backup.CatalogEntryFromResult(&backup.CreateResult{Path: res.Path, Size: res.Size, SHA256: res.SHA256, Manifest: res.Manifest}, res.Manifest)
	entry.PlanID, entry.RunID = r.PlanID, r.ID
	if r.Scope == ScopeInstance && entry.Kind != backup.KindCustom {
		entry.Kind, entry.Slug = backup.KindInstance, "instance"
	}
	if entry.CreatedBy == "" {
		entry.CreatedBy = spec.Actor.Email
	}
	if err := backup.UpsertCatalogEntry(ctx, s.DB, entry); err != nil {
		s.Logger.Warn("backup run: catalog upsert failed", "run", r.ID, "path", res.Path, "error", err)
	} else if e, err := backup.GetCatalogEntry(ctx, s.DB, res.Path); err == nil {
		r.CatalogID = e.ID
	}
	now := s.now()
	size := res.Size
	r.BundlePath, r.Size, r.EndedAt = res.Path, &size, &now
	r.Incomplete = []backup.IncompleteItem{}
	if res.Manifest != nil && len(res.Manifest.Contents.Incomplete) > 0 {
		r.Incomplete = res.Manifest.Contents.Incomplete
	}
	switch {
	case checkErr != nil:
		r.Status, r.Error = StatusFailed, "the written bundle failed its checksum check: "+checkErr.Error()
	case len(r.Incomplete) > 0:
		r.Status = StatusIncomplete
	default:
		r.Status = StatusDone
	}
	if err := finishRun(ctx, s.DB, r); err != nil {
		s.Logger.Warn("backup run: record result", "run", r.ID, "error", err)
	}
	s.afterRun(ctx, r, plan)
	if r.Status == StatusFailed {
		return
	}
	if r.RetryOf != "" {
		if _, err := s.DB.ExecContext(ctx, `UPDATE backup_runs SET retried_at = ? WHERE id = ?`, ts(now), r.RetryOf); err != nil {
			s.Logger.Warn("backup run: mark retried", "run", r.RetryOf, "error", err)
		}
	}
	if plan != nil && (r.WorkspaceID != "" || r.Scope == ScopeInstance) {
		// The plan's keep rules, after every good run: workspace bundles per
		// workspace, instance bundles as one group. Deleted bundles give
		// back their environment layers and lose their off-site copies.
		var dropped []string
		var err error
		if r.Scope == ScopeInstance {
			dropped, err = backup.RotateInstancePlanWithPolicy(ctx, s.DB, filepath.Dir(res.Path), plan.ID, plan.Policy(), false)
		} else {
			dropped, err = backup.RotatePlanWithPolicy(ctx, s.DB, filepath.Dir(res.Path), r.WorkspaceID, plan.ID, plan.Policy(), false)
		}
		if err != nil {
			s.Logger.Warn("backup run: retention failed", "run", r.ID, "plan", plan.ID, "error", err)
		} else if len(dropped) > 0 {
			s.Logger.Info("backup run: retention removed old copies", "run", r.ID, "plan", plan.ID, "removed", len(dropped))
			s.dropRemoteCopies(ctx, dropped)
		}
	}
}

// isBusy: running work kept the copy from starting (a workspace guard, the
// instance drain, or another quiet window already open). The run goes back
// to its busy window instead of failing.
func isBusy(err error) bool {
	return errors.Is(err, backup.ErrAgentRunning) || errors.Is(err, backup.ErrInstanceBusy)
}

func (s *Service) stashPassphrase(runID, p string) {
	s.keysMu.Lock()
	defer s.keysMu.Unlock()
	if s.passphrases == nil {
		s.passphrases = map[string]string{}
	}
	s.passphrases[runID] = p
}

func (s *Service) takePassphrase(runID string) (string, bool) {
	s.keysMu.Lock()
	defer s.keysMu.Unlock()
	p, ok := s.passphrases[runID]
	delete(s.passphrases, runID)
	return p, ok
}

// parkPassphrase puts a taken passphrase back for a run that returns to the
// busy window, so its next attempt can still encrypt.
func (s *Service) parkPassphrase(runID, p string, had bool) {
	if had {
		s.stashPassphrase(runID, p)
	}
}

func verifyChecksum(ctx context.Context, path string) error {
	v, err := backup.Verify(ctx, path)
	if err != nil {
		return err
	}
	if !v.Valid {
		if v.Err != nil {
			return v.Err
		}
		return errors.New("checksum mismatch")
	}
	return nil
}

func (s *Service) requeueBusy(ctx context.Context, r *Run, plan *Plan, detail string) {
	for i := range r.Phases {
		if r.Phases[i].Name != PhaseWait {
			r.Phases[i].Status, r.Phases[i].StartedAt, r.Phases[i].EndedAt, r.Phases[i].Detail = "pending", nil, nil, nil
		}
	}
	s.handleBusy(ctx, r, plan, detail, s.now())
}

// ─── Boot recovery ──────────────────────────────────────────────────────────

// RecoverAtBoot marks every run a previous process left mid-execution as
// interrupted, sweeps its partial bundle and stale staging files, and queues
// ONE retry for it (a retry that is itself interrupted is not retried
// again). Runs that were only queued or waiting out the busy window had not
// started and simply continue. Returns how many runs were interrupted.
func (s *Service) RecoverAtBoot(ctx context.Context) (int, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+runColumns+` FROM backup_runs
		WHERE status = 'running' AND phase NOT IN ('queued','busy_wait')`)
	if err != nil {
		return 0, err
	}
	var stuck []*Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			_ = rows.Close()
			return 0, err
		}
		stuck = append(stuck, r)
	}
	_ = rows.Close()
	dir := s.backupsDir()
	// Staging is wiped on the next start after a crash, whether or not a
	// run row was left mid-flight: CreateBackup's sealed temp files, and
	// an instance run's staging directory and partial bundle.
	defer s.sweepStaging()
	if dir != "" {
		if n := backup.SweepInstanceStaging(dir, 0); n > 0 {
			s.Logger.Info("backup scheduler: removed an interrupted instance backup's staging", "entries", n)
		}
	}
	if len(stuck) == 0 {
		return 0, nil
	}
	now := s.now()
	for _, r := range stuck {
		phase := r.Phase
		if p := s.runningPhase(r); p != "" {
			phase = p
		}
		msg := fmt.Sprintf("interrupted: the server stopped during %s", phase)
		s.endPhase(r, phase, "failed", "interrupted")
		markPending(r, "skipped")
		r.Status, r.Error, r.EndedAt = StatusInterrupted, msg, &now
		if err := finishRun(ctx, s.DB, r); err != nil {
			return 0, err
		}
		if dir != "" && r.WorkspaceID != "" {
			var slug string
			if err := s.DB.QueryRowContext(ctx, `SELECT slug FROM workspaces WHERE id = ?`, r.WorkspaceID).Scan(&slug); err == nil && slug != "" {
				backup.SweepStalePartials(ctx, dir, slug, 0)
			}
		}
		if r.RetryOf != "" {
			// A retry interrupted in turn is not retried again: that
			// is a failure someone has to hear about.
			s.afterRun(ctx, r, s.planOf(ctx, r.PlanID))
			continue
		}
		retry := &Run{
			PlanID: r.PlanID, Trigger: r.Trigger, Note: "retry after interruption", Scope: r.Scope,
			WorkspaceID: r.WorkspaceID, Kind: r.Kind, Categories: r.Categories, Environments: r.Environments,
			Status: StatusRunning, Phase: PhaseQueued, Phases: initialPhases(r.Environments), DueAt: r.DueAt,
			NextAttemptAt: &now, RetryOf: r.ID, Recipients: r.Recipients, ActorUserID: r.ActorUserID, StartedAt: now,
		}
		if r.Note != "" {
			retry.Note = r.Note + " · retry after interruption"
		}
		if _, err := insertRun(ctx, s.DB, retry); err != nil {
			return 0, err
		}
	}
	return len(stuck), nil
}

// sweepStaging removes CreateBackup staging files nobody has written to for
// ten minutes: a live create streams into them continuously, so an idle one
// belonged to a process that died.
func (s *Service) sweepStaging() {
	dir := s.TempDir
	if dir == "" {
		dir = os.TempDir()
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-10 * time.Minute)
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !(strings.HasPrefix(name, "crewship-backup-payload-") || strings.HasPrefix(name, "crewship-backup-sealed-")) {
			continue
		}
		if info, err := e.Info(); err == nil && info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(dir, name))
		}
	}
}

// ─── Manual runs ────────────────────────────────────────────────────────────

// ManualRequest is POST /api/v1/admin/instance/backups/run.
type ManualRequest struct {
	PlanID       string   `json:"plan_id,omitempty"`
	Scope        string   `json:"scope,omitempty"`
	WorkspaceIDs []string `json:"workspace_ids,omitempty"`
	Preset       string   `json:"preset,omitempty"`
	Contents     []string `json:"contents,omitempty"`
	EnvMode      string   `json:"env_mode,omitempty"`
	RecipientIDs []string `json:"recipient_ids,omitempty"`
	// Recipients is accepted as another name for recipient_ids (age1…
	// public keys or recipient ids).
	Recipients []string `json:"recipients,omitempty"`
	// Passphrase encrypts this run's bundles instead of recipients. It is
	// held in memory until the run starts and never stored.
	Passphrase string `json:"passphrase,omitempty"`
	Note       string `json:"note,omitempty"`
}

// StartManual queues a manual run (one per workspace for scope=workspaces)
// and returns the run ids at once; the runs execute in the background, so
// no client timeout can cancel them. Without plan_id the request names the
// scope, preset and contents; recipients default to those of the enabled
// plan that covers the same scope.
func (s *Service) StartManual(ctx context.Context, req ManualRequest, actorUserID string) ([]string, error) {
	switch req.Scope {
	case "", ScopeInstance, ScopeWorkspaces:
	default:
		return nil, invalid("scope must be instance or workspaces")
	}
	keys := append(append([]string{}, req.RecipientIDs...), req.Recipients...)
	req.RecipientIDs, req.Recipients = nil, nil
	for _, k := range keys {
		if k = strings.TrimSpace(k); k != "" {
			req.RecipientIDs = append(req.RecipientIDs, k)
		}
	}
	if req.Passphrase != "" && len(req.RecipientIDs) > 0 {
		return nil, invalid("every backup is encrypted: give recipients or a passphrase, not both")
	}
	if len(req.RecipientIDs) > 0 {
		if _, _, err := s.Recipients.Resolve(ctx, req.RecipientIDs); err != nil {
			return nil, invalid("%v", err)
		}
	}
	var p Plan
	if req.PlanID != "" {
		got, err := GetPlan(ctx, s.DB, req.PlanID)
		if err != nil {
			return nil, err
		}
		p = *got
		// The plan decides what is backed up; keys given with the request
		// replace the plan's for this run only.
		if len(req.RecipientIDs) > 0 {
			p.RecipientIDs = req.RecipientIDs
		}
	} else {
		preset := req.Preset
		if preset == "" {
			preset = backup.PresetWorkspace
			if req.Scope == ScopeInstance {
				preset = backup.PresetComplete
			}
		}
		p = Defaults(preset)
		if req.Scope != "" {
			p.Scope = req.Scope
		}
		p.WorkspaceIDs, p.Contents = req.WorkspaceIDs, req.Contents
		if req.EnvMode != "" {
			p.EnvMode = req.EnvMode
		}
		p.RecipientIDs = req.RecipientIDs
		if len(p.RecipientIDs) == 0 && req.Passphrase == "" {
			rids, err := s.defaultRecipients(ctx, &p)
			if err != nil {
				return nil, err
			}
			p.RecipientIDs = rids
		}
		if req.Passphrase != "" {
			// Normalize insists on recipients (a plan must name who can
			// open it); a passphrase run satisfies that with the
			// passphrase, so validate the rest with a stand-in.
			p.RecipientIDs = []string{passphraseStandIn}
			if err := Normalize(ctx, &p, DBWorkspaces{DB: s.DB}, nil); err != nil {
				return nil, err
			}
			p.RecipientIDs = nil
		} else if err := Normalize(ctx, &p, DBWorkspaces{DB: s.DB}, s.Recipients); err != nil {
			return nil, err
		}
	}
	if req.Passphrase != "" {
		p.RecipientIDs = []string{}
	}
	targets, err := s.Targets(ctx, &p)
	if err != nil {
		return nil, err
	}
	if len(targets) == 0 {
		return nil, invalid("there is no workspace to back up")
	}
	cats, kind := carriedCategories(&p)
	now := s.now()
	env := p.EnvMode == backup.EnvModeComplete
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var ids []string
	for _, ws := range targets {
		r := &Run{
			PlanID: req.PlanID, Trigger: TriggerManual, Note: strings.TrimSpace(req.Note), Scope: p.Scope,
			WorkspaceID: ws, Kind: kind, Categories: cats, Environments: env, Status: StatusRunning,
			Phase: PhaseQueued, Phases: initialPhases(env), NextAttemptAt: &now, Recipients: p.RecipientIDs,
			ActorUserID: actorUserID, StartedAt: now,
		}
		if _, err := insertRun(ctx, tx, r); err != nil {
			return nil, err
		}
		ids = append(ids, r.ID)
	}
	// Stash before the commit: the scheduler loop may pick a committed run
	// up at once, and it must find the passphrase there.
	if req.Passphrase != "" {
		for _, id := range ids {
			s.stashPassphrase(id, req.Passphrase)
		}
	}
	if err := tx.Commit(); err != nil {
		for _, id := range ids {
			_, _ = s.takePassphrase(id)
		}
		return nil, err
	}
	s.Kick()
	return ids, nil
}

// passphraseStandIn fills recipient_ids while a passphrase run's request is
// validated; it is never stored or resolved.
const passphraseStandIn = "passphrase"

// defaultRecipients: the recipients of the first enabled full plan covering
// the scope, else of any enabled plan.
func (s *Service) defaultRecipients(ctx context.Context, p *Plan) ([]string, error) {
	plans, err := ListPlans(ctx, s.DB)
	if err != nil {
		return nil, err
	}
	var fallback []string
	for _, q := range plans {
		if !q.Enabled || len(q.RecipientIDs) == 0 {
			continue
		}
		if fallback == nil {
			fallback = q.RecipientIDs
		}
		if q.Preset != backup.PresetCustom && (q.Scope == p.Scope || q.Scope == ScopeWorkspaces && len(q.WorkspaceIDs) == 0) {
			return q.RecipientIDs, nil
		}
	}
	if fallback != nil {
		return fallback, nil
	}
	return nil, invalid("recipient_ids is required: no plan exists to take recipients from, and every backup is encrypted")
}

// ─── Busy ───────────────────────────────────────────────────────────────────

// DBBusy counts agents that are running (status running or busy, the same
// test CreateBackup's idle guard applies) and routine runs in progress.
type DBBusy struct{ DB *sql.DB }

func (b DBBusy) Busy(ctx context.Context, ids []string) (bool, string, error) {
	filter, args := "", []any{}
	if len(ids) > 0 {
		filter = " AND c.workspace_id IN (?" + strings.Repeat(",?", len(ids)-1) + ")"
		for _, id := range ids {
			args = append(args, id)
		}
	}
	var agents int
	if err := b.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM agents a JOIN crews c ON c.id = a.crew_id
		WHERE a.status IN ('running','busy')`+filter, args...).Scan(&agents); err != nil {
		return false, "", err
	}
	runFilter := strings.ReplaceAll(filter, "c.workspace_id", "workspace_id")
	var runs int
	if err := b.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM pipeline_runs WHERE status = 'running'`+runFilter, args...).Scan(&runs); err != nil {
		runs = 0 // tolerate a schema without routine runs
	}
	if agents == 0 && runs == 0 {
		return false, "", nil
	}
	var parts []string
	if agents > 0 {
		parts = append(parts, fmt.Sprintf("%d agent(s) working", agents))
	}
	if runs > 0 {
		parts = append(parts, fmt.Sprintf("%d routine run(s) in progress", runs))
	}
	return true, strings.Join(parts, ", "), nil
}
