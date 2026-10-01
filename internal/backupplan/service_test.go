package backupplan

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/testutil"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time  { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) Set(t time.Time) { c.mu.Lock(); c.t = t; c.mu.Unlock() }
func (c *fakeClock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// fakeExec records every run and writes no file.
type fakeExec struct {
	mu    sync.Mutex
	specs []RunSpec
	err   error
	gate  chan struct{} // when set, Run blocks until it can receive
	busy  int           // runs in flight
	peak  int
}

func (e *fakeExec) Run(ctx context.Context, spec RunSpec, progress func(string)) (*RunResult, error) {
	e.mu.Lock()
	e.specs = append(e.specs, spec)
	e.busy++
	if e.busy > e.peak {
		e.peak = e.busy
	}
	gate, err := e.gate, e.err
	e.mu.Unlock()
	defer func() { e.mu.Lock(); e.busy--; e.mu.Unlock() }()
	if gate != nil {
		<-gate
	}
	if err != nil {
		return nil, err
	}
	for _, p := range []string{"copy", "pack", "encrypt", "write"} {
		progress(p)
	}
	return &RunResult{Path: "/backups/" + spec.RunID + ".tar.zst", Size: 42, SHA256: "sha",
		Manifest: &backup.Manifest{Scope: backup.ScopeWorkspace, Encryption: backup.Encryption{Enabled: true}}}, nil
}

func (e *fakeExec) calls() []RunSpec {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]RunSpec(nil), e.specs...)
}

type fakeBusy struct {
	mu   sync.Mutex
	busy bool
}

func (b *fakeBusy) set(v bool) { b.mu.Lock(); b.busy = v; b.mu.Unlock() }
func (b *fakeBusy) Busy(context.Context, []string) (bool, string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.busy {
		return true, "2 agent(s) working", nil
	}
	return false, "", nil
}

type harness struct {
	t     *testing.T
	db    *sql.DB
	clock *fakeClock
	exec  *fakeExec
	busy  *fakeBusy
	svc   *Service
	key   string
}

func newHarness(t *testing.T, start string) *harness {
	t.Helper()
	db := testutil.MigratedSQLDB(t)
	for _, q := range []string{
		`INSERT INTO workspaces(id,name,slug,created_at) VALUES('ws_a','Alpha','alpha','2026-01-01 00:00:00'),('ws_b','Beta','beta','2026-01-02 00:00:00'),('ws_c','Gamma','gamma','2026-01-03 00:00:00')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, db: db, clock: &fakeClock{t: mustTime(t, start)}, exec: &fakeExec{}, busy: &fakeBusy{}, key: id.Recipient().String()}
	h.svc = New(db, slog.New(slog.NewTextHandler(io.Discard, nil)))
	h.svc.Clock, h.svc.Workspace, h.svc.Busy = h.clock, h.exec, h.busy
	h.svc.Verify = func(context.Context, string) error { return nil }
	h.svc.BackupsDir = func() (string, error) { return t.TempDir(), nil }
	// A roomy disk, whatever the machine running the tests has left: the
	// space floor has its own tests.
	h.svc.Space = func(string) (uint64, uint64, error) { return 900 << 30, 1000 << 30, nil }
	return h
}

// plan inserts a normalized plan with next_run_at computed from now.
func (h *harness) plan(mut func(*Plan)) *Plan {
	h.t.Helper()
	p := Defaults(backup.PresetWorkspace)
	p.WorkspaceIDs = []string{"ws_a"}
	p.RecipientIDs = []string{h.key}
	if mut != nil {
		mut(&p)
	}
	ctx := context.Background()
	if err := Normalize(ctx, &p, DBWorkspaces{DB: h.db}, h.svc.Recipients); err != nil {
		h.t.Fatalf("normalize: %v", err)
	}
	SetNextRun(&p, h.clock.Now())
	if err := InsertPlan(ctx, h.db, &p, "u1", h.clock.Now()); err != nil {
		h.t.Fatal(err)
	}
	return &p
}

func (h *harness) tick() {
	h.svc.Tick(context.Background())
	h.svc.Wait()
}

func (h *harness) runs(planID string) []*Run {
	h.t.Helper()
	rs, err := ListRuns(context.Background(), h.db, RunFilter{PlanID: planID})
	if err != nil {
		h.t.Fatal(err)
	}
	return rs
}

func TestScheduler_RunsADuePlanOnceAndAdvances(t *testing.T) {
	h := newHarness(t, "2026-09-30T02:00:00Z")
	p := h.plan(nil)
	if p.NextRunAt == nil || *p.NextRunAt != "2026-09-30T03:00:00Z" {
		t.Fatalf("next_run_at = %v, want 03:00 today", p.NextRunAt)
	}
	h.tick()
	if n := len(h.runs(p.ID)); n != 0 {
		t.Fatalf("%d runs before the due time", n)
	}
	h.clock.Set(mustTime(t, "2026-09-30T03:00:20Z"))
	h.tick()
	h.tick() // a second pass at the same instant must not start it again
	rs := h.runs(p.ID)
	if len(rs) != 1 || len(h.exec.calls()) != 1 {
		t.Fatalf("runs=%d executor calls=%d, want 1/1", len(rs), len(h.exec.calls()))
	}
	r := rs[0]
	if r.Trigger != TriggerSchedule || r.Status != StatusDone || r.WorkspaceID != "ws_a" || r.BundlePath == "" {
		t.Fatalf("run = %+v", r)
	}
	var names []string
	for _, ph := range r.Phases {
		names = append(names, ph.Name+":"+ph.Status)
	}
	want := []string{"copy:done", "pack:done", "encrypt:done", "check:done", "off-site:skipped"}
	if len(names) != len(want) {
		t.Fatalf("phases %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("phases %v, want %v", names, want)
		}
	}
	got, _ := GetPlan(context.Background(), h.db, p.ID)
	if got.NextRunAt == nil || *got.NextRunAt != "2026-10-01T03:00:00Z" || got.LastRunAt == nil {
		t.Fatalf("plan after the run: next=%v last=%v", got.NextRunAt, got.LastRunAt)
	}
	// The bundle is catalogued with its plan and run.
	e, err := backup.GetCatalogEntry(context.Background(), h.db, r.BundlePath)
	if err != nil || e.PlanID != p.ID || e.RunID != r.ID || r.CatalogID != e.ID {
		t.Fatalf("catalog entry %+v (%v), run catalog_id %q", e, err, r.CatalogID)
	}
}

// A second process (or a restart mid-claim) cannot claim the same night.
func TestScheduler_ClaimIsIdempotentAcrossServices(t *testing.T) {
	h := newHarness(t, "2026-09-30T02:00:00Z")
	p := h.plan(nil)
	h.clock.Set(mustTime(t, "2026-09-30T03:00:05Z"))
	other := New(h.db, h.svc.Logger)
	other.Clock, other.Workspace, other.Busy, other.Verify = h.clock, h.exec, h.busy, h.svc.Verify
	plan, _ := GetPlan(context.Background(), h.db, p.ID)
	// Both claim the same due time from the same stale plan row.
	if err := h.svc.claimDue(context.Background(), plan, mustTime(t, "2026-09-30T03:00:00Z"), h.clock.Now()); err != nil {
		t.Fatal(err)
	}
	if err := other.claimDue(context.Background(), plan, mustTime(t, "2026-09-30T03:00:00Z"), h.clock.Now()); err != nil {
		t.Fatal(err)
	}
	h.tick()
	other.Tick(context.Background())
	other.Wait()
	if n := len(h.runs(p.ID)); n != 1 {
		t.Fatalf("%d runs for one due time, want 1", n)
	}
	if n := len(h.exec.calls()); n != 1 {
		t.Fatalf("executor ran %d times, want 1", n)
	}
}

// Down over three nights: one catch-up run when the server is back, not three.
func TestScheduler_OneCatchUpAfterDowntime(t *testing.T) {
	h := newHarness(t, "2026-09-26T12:00:00Z")
	p := h.plan(func(p *Plan) { p.EnvMode = backup.EnvModeComplete; p.EnvCadence = EnvWeekly })
	h.clock.Set(mustTime(t, "2026-09-30T09:15:00Z")) // missed 27, 28, 29, 30 at 03:00
	h.tick()
	rs := h.runs(p.ID)
	if len(rs) != 1 {
		t.Fatalf("%d runs after downtime, want exactly one catch-up", len(rs))
	}
	r := rs[0]
	if r.Trigger != TriggerCatchup || r.DueAt == nil || !r.DueAt.Equal(mustTime(t, "2026-09-30T03:00:00Z")) {
		t.Fatalf("run trigger=%s due=%v", r.Trigger, r.DueAt)
	}
	// Sunday 27 Sep was an environments night; the catch-up carries it.
	if !r.Environments {
		t.Fatal("catch-up lost the missed environments night")
	}
	got, _ := GetPlan(context.Background(), h.db, p.ID)
	if *got.NextRunAt != "2026-10-01T03:00:00Z" {
		t.Fatalf("next_run_at = %s", *got.NextRunAt)
	}
	// Reaching a single due time late (beyond the grace) is a catch-up too.
	h.clock.Set(mustTime(t, "2026-10-01T04:00:00Z"))
	h.tick()
	if rs := h.runs(p.ID); len(rs) != 2 || rs[0].Trigger != TriggerCatchup {
		t.Fatalf("late single run: %d runs, newest trigger %s", len(rs), rs[0].Trigger)
	}
}

func TestScheduler_BusyWaitsThenSkips(t *testing.T) {
	h := newHarness(t, "2026-09-30T02:00:00Z")
	p := h.plan(func(p *Plan) { p.BusyWait = 60; p.BusyRetry = 15 })
	h.busy.set(true)
	h.clock.Set(mustTime(t, "2026-09-30T03:00:10Z"))
	h.tick()
	r := h.runs(p.ID)[0]
	if r.Status != StatusRunning || r.Phase != PhaseBusyWait || r.NextAttemptAt == nil ||
		!r.NextAttemptAt.Equal(mustTime(t, "2026-09-30T03:15:10Z")) || r.Phases[0].Name != PhaseWait {
		t.Fatalf("busy run = status %s phase %s next %v phases %+v", r.Status, r.Phase, r.NextAttemptAt, r.Phases)
	}
	h.clock.Add(5 * time.Minute) // not yet time to look again
	h.tick()
	if len(h.exec.calls()) != 0 {
		t.Fatal("ran while busy")
	}
	h.clock.Set(mustTime(t, "2026-09-30T04:00:30Z")) // past the 60-minute window
	h.tick()
	r = h.runs(p.ID)[0]
	if r.Status != StatusSkipped || r.Error == "" || len(h.exec.calls()) != 0 {
		t.Fatalf("after the window: status %s error %q calls %d", r.Status, r.Error, len(h.exec.calls()))
	}
}

func TestScheduler_BusyThenFreeRuns(t *testing.T) {
	h := newHarness(t, "2026-09-30T02:00:00Z")
	p := h.plan(nil)
	h.busy.set(true)
	h.clock.Set(mustTime(t, "2026-09-30T03:00:10Z"))
	h.tick()
	h.busy.set(false)
	h.clock.Add(15 * time.Minute)
	h.tick()
	r := h.runs(p.ID)[0]
	if r.Status != StatusDone || len(h.exec.calls()) != 1 || r.Phases[0].Name != PhaseWait || r.Phases[0].Status != "done" {
		t.Fatalf("run = status %s calls %d phases %+v", r.Status, len(h.exec.calls()), r.Phases)
	}
}

// The process died mid-run: the run is interrupted at boot and retried once.
func TestScheduler_InterruptedRunIsRetriedOnce(t *testing.T) {
	h := newHarness(t, "2026-09-30T03:30:00Z")
	p := h.plan(nil)
	started := mustTime(t, "2026-09-30T03:00:00Z")
	dead := &Run{PlanID: p.ID, Trigger: TriggerSchedule, Scope: ScopeWorkspaces, WorkspaceID: "ws_a", Status: StatusRunning,
		Phase: PhasePack, Phases: initialPhases(false), DueAt: &started, Recipients: []string{h.key}, StartedAt: started}
	if _, err := insertRun(context.Background(), h.db, dead); err != nil {
		t.Fatal(err)
	}
	n, err := h.svc.RecoverAtBoot(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("recover = %d, %v", n, err)
	}
	h.tick()
	rs := h.runs(p.ID)
	if len(rs) != 2 {
		t.Fatalf("%d runs, want the interrupted one and its retry", len(rs))
	}
	var orig, retry *Run
	for _, r := range rs {
		if r.ID == dead.ID {
			orig = r
		} else {
			retry = r
		}
	}
	if orig.Status != StatusInterrupted || orig.RetriedAt == nil || orig.Error == "" {
		t.Fatalf("original = status %s retried %v error %q", orig.Status, orig.RetriedAt, orig.Error)
	}
	if retry.RetryOf != dead.ID || retry.Status != StatusDone {
		t.Fatalf("retry = %+v", retry)
	}
	// A retry that is itself interrupted is not retried again.
	if _, err := h.db.Exec(`UPDATE backup_runs SET status='running', phase='copy' WHERE id = ?`, retry.ID); err != nil {
		t.Fatal(err)
	}
	if n, _ := h.svc.RecoverAtBoot(context.Background()); n != 1 {
		t.Fatalf("second recover = %d", n)
	}
	if rs := h.runs(p.ID); len(rs) != 2 {
		t.Fatalf("an interrupted retry was retried again (%d runs)", len(rs))
	}
}

// A held instance (after a restore) does not retry an interrupted run on its
// own: found live — a recovered server retried the run that had taken its
// bundle on the source and wrote a backup nobody asked for.
func TestScheduler_InterruptedRunIsNotRetriedWhileHeld(t *testing.T) {
	h := newHarness(t, "2026-09-30T03:30:00Z")
	h.svc.Pause = fakePause{reason: "an instance restore left schedules held"}
	p := h.plan(nil)
	started := mustTime(t, "2026-09-30T03:00:00Z")
	dead := &Run{PlanID: p.ID, Trigger: TriggerManual, Scope: ScopeWorkspaces, WorkspaceID: "ws_a", Status: StatusRunning,
		Phase: PhaseCopy, Phases: initialPhases(false), Recipients: []string{h.key}, StartedAt: started}
	if _, err := insertRun(context.Background(), h.db, dead); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.RecoverAtBoot(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.tick()
	rs := h.runs(p.ID)
	if len(rs) != 1 || rs[0].Status != StatusInterrupted {
		t.Fatalf("runs = %d (first status %s); want only the interrupted one, no retry", len(rs), rs[0].Status)
	}
}

func TestScheduler_ConcurrencyQueuesTheRest(t *testing.T) {
	h := newHarness(t, "2026-09-30T02:00:00Z")
	p := h.plan(func(p *Plan) { p.WorkspaceIDs = []string{} }) // every workspace: three runs
	h.exec.gate = make(chan struct{})
	h.clock.Set(mustTime(t, "2026-09-30T03:00:10Z"))
	h.svc.Tick(context.Background())
	if n := len(h.runs(p.ID)); n != 3 {
		t.Fatalf("%d runs, want one per workspace", n)
	}
	for i := 0; i < 3; i++ {
		h.exec.gate <- struct{}{}
	}
	h.svc.Wait()
	if h.exec.peak != 1 {
		t.Fatalf("peak concurrency %d, want 1", h.exec.peak)
	}
	for _, r := range h.runs(p.ID) {
		if r.Status != StatusDone {
			t.Fatalf("run %s status %s", r.WorkspaceID, r.Status)
		}
	}
}

func TestScheduler_InstanceScopeWithoutAnExecutorFailsHonestly(t *testing.T) {
	h := newHarness(t, "2026-09-30T02:00:00Z")
	p := h.plan(func(p *Plan) { *p = Defaults(backup.PresetComplete); p.RecipientIDs = []string{h.key} })
	h.clock.Set(mustTime(t, "2026-09-30T03:00:10Z"))
	h.tick()
	r := h.runs(p.ID)[0]
	if r.Status != StatusFailed || r.Scope != ScopeInstance || !strings.Contains(r.Error, "no backup executor") {
		t.Fatalf("instance run = status %s error %q", r.Status, r.Error)
	}
}

// fakeInstanceExec answers like the instance bundle creator: it measures
// its own hold and writes an instance manifest.
type fakeInstanceExec struct {
	fakeExec
	hold int64
}

func (e *fakeInstanceExec) Run(ctx context.Context, spec RunSpec, progress func(string)) (*RunResult, error) {
	res, err := e.fakeExec.Run(ctx, spec, progress)
	if err != nil {
		return nil, err
	}
	res.Manifest = &backup.Manifest{Scope: backup.ScopeInstance, Encryption: backup.Encryption{Enabled: true}}
	res.HoldMS = &e.hold
	return res, nil
}

func TestScheduler_InstanceScopeRunsTheInstanceExecutor(t *testing.T) {
	h := newHarness(t, "2026-09-30T02:00:00Z")
	inst := &fakeInstanceExec{hold: 1234}
	h.svc.Instance = inst
	p := h.plan(func(p *Plan) { *p = Defaults(backup.PresetComplete); p.RecipientIDs = []string{h.key}; p.HoldCap = 7 })
	h.clock.Set(mustTime(t, "2026-09-30T03:00:10Z"))
	h.tick()
	r := h.runs(p.ID)[0]
	if r.Status != StatusDone || r.HoldMs == nil || *r.HoldMs != 1234 {
		t.Fatalf("instance run = %+v", r)
	}
	if len(h.exec.calls()) != 0 {
		t.Fatal("the workspace executor ran an instance run")
	}
	specs := inst.calls()
	if len(specs) != 1 || specs[0].HoldCap != 7*time.Minute || specs[0].WorkspaceID != "" || len(specs[0].Recipients) != 1 {
		t.Fatalf("instance spec = %+v", specs)
	}
	e, err := backup.GetCatalogEntry(context.Background(), h.db, r.BundlePath)
	if err != nil || e.Kind != backup.KindInstance || e.RunID != r.ID {
		t.Fatalf("catalog = %+v, %v", e, err)
	}
}

// A manual instance run has no plan; it must still run as Complete recovery
// (the full crew scope, /var/lib included) — found live on dev3.
func TestManualInstanceRunIsCompleteRecovery(t *testing.T) {
	h := newHarness(t, "2026-09-30T02:00:00Z")
	inst := &fakeInstanceExec{}
	h.svc.Instance = inst
	if _, err := h.svc.StartManual(context.Background(), ManualRequest{Scope: ScopeInstance, RecipientIDs: []string{h.key}}, "u1"); err != nil {
		t.Fatal(err)
	}
	h.svc.Wait()
	specs := inst.calls()
	if len(specs) != 1 || specs[0].Preset != backup.PresetComplete || specs[0].ScopeLevel() != backup.ScopeLevelFull {
		t.Fatalf("manual instance spec = %+v", specs)
	}
}

type fakePause struct{ reason string }

func (f fakePause) Paused(context.Context) (bool, string) { return f.reason != "", f.reason }

func TestScheduler_HeldInstanceSkipsScheduledRunsButNotManualOnes(t *testing.T) {
	h := newHarness(t, "2026-09-30T02:00:00Z")
	h.svc.Pause = fakePause{reason: "an instance restore left schedules held"}
	p := h.plan(nil)
	h.clock.Set(mustTime(t, "2026-09-30T03:00:10Z"))
	h.tick()
	r := h.runs(p.ID)[0]
	if r.Status != StatusSkipped || !strings.Contains(r.Error, "held") {
		t.Fatalf("scheduled run while held = %+v", r)
	}
	ids, err := h.svc.StartManual(context.Background(), ManualRequest{PlanID: p.ID}, "u1")
	if err != nil {
		t.Fatal(err)
	}
	h.svc.Wait()
	m, _ := GetRun(context.Background(), h.db, ids[0])
	if m.Status != StatusDone {
		t.Fatalf("manual run while held = %+v", m)
	}
}

type fakeQuiesce struct {
	mu    sync.Mutex
	err   error
	calls []RunSpec
	rel   int
}

func (q *fakeQuiesce) Quiesce(_ context.Context, spec RunSpec, capDur time.Duration) (func(), error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	spec.HoldCap = capDur
	q.calls = append(q.calls, spec)
	if q.err != nil {
		return nil, q.err
	}
	return func() { q.mu.Lock(); q.rel++; q.mu.Unlock() }, nil
}

func TestManualRun_PassphraseIsUsedOnceAndSurvivesABusyRequeue(t *testing.T) {
	h := newHarness(t, "2026-09-30T12:00:00Z")
	q := &fakeQuiesce{err: fmt.Errorf("%w: 1 agent run(s) in this process", backup.ErrInstanceBusy)}
	h.svc.Quiesce = q
	ctx := context.Background()
	if _, err := h.svc.StartManual(ctx, ManualRequest{Scope: ScopeWorkspaces, WorkspaceIDs: []string{"ws_a"}, Passphrase: "pw", Recipients: []string{h.key}}, "u1"); !IsValidation(err) {
		t.Fatalf("recipients and a passphrase together: %v", err)
	}
	if _, err := h.svc.StartManual(ctx, ManualRequest{Scope: "galaxy", Passphrase: "pw"}, "u1"); !IsValidation(err) {
		t.Fatalf("bad scope: %v", err)
	}
	ids, err := h.svc.StartManual(ctx, ManualRequest{Scope: ScopeWorkspaces, WorkspaceIDs: []string{"ws_a"}, Passphrase: "correct horse"}, "u1")
	if err != nil || len(ids) != 1 {
		t.Fatalf("start = %v, %v", ids, err)
	}
	h.svc.Wait()
	r, _ := GetRun(ctx, h.db, ids[0])
	if r.Status != StatusRunning || r.Phase != PhaseBusyWait {
		t.Fatalf("busy quiet window: run = status %s phase %s error %q", r.Status, r.Phase, r.Error)
	}
	if len(r.Recipients) != 0 {
		t.Fatalf("a passphrase run stored recipients %v", r.Recipients)
	}
	// The window opens on the next attempt; the passphrase is still there.
	q.mu.Lock()
	q.err = nil
	q.mu.Unlock()
	h.clock.Add(16 * time.Minute)
	h.tick()
	r, _ = GetRun(ctx, h.db, ids[0])
	if r.Status != StatusDone {
		t.Fatalf("after the retry: %+v", r)
	}
	specs := h.exec.calls()
	if len(specs) != 1 || specs[0].Passphrase != "correct horse" || len(specs[0].Recipients) != 0 {
		t.Fatalf("executor spec = %+v", specs)
	}
	if q.rel != 1 {
		t.Fatalf("quiet window released %d times", q.rel)
	}
	if _, ok := h.svc.takePassphrase(ids[0]); ok {
		t.Fatal("the passphrase outlived its run")
	}
}

func TestManualRun_PassphraseLostOnRestartFailsClearly(t *testing.T) {
	h := newHarness(t, "2026-09-30T12:00:00Z")
	h.busy.set(true)
	ctx := context.Background()
	ids, err := h.svc.StartManual(ctx, ManualRequest{Scope: ScopeWorkspaces, WorkspaceIDs: []string{"ws_a"}, Passphrase: "pw"}, "u1")
	if err != nil {
		t.Fatal(err)
	}
	h.svc.Wait()
	_, _ = h.svc.takePassphrase(ids[0]) // what a restart does
	h.busy.set(false)
	h.clock.Add(16 * time.Minute)
	h.tick()
	r, _ := GetRun(ctx, h.db, ids[0])
	if r.Status != StatusFailed || !strings.Contains(r.Error, "passphrase") {
		t.Fatalf("run = status %s error %q", r.Status, r.Error)
	}
}

func TestManualRun_RecipientsOverrideAPlansForThisRun(t *testing.T) {
	h := newHarness(t, "2026-09-30T12:00:00Z")
	p := h.plan(nil)
	other, _ := age.GenerateX25519Identity()
	ids, err := h.svc.StartManual(context.Background(), ManualRequest{PlanID: p.ID, Recipients: []string{other.Recipient().String()}}, "u1")
	if err != nil {
		t.Fatal(err)
	}
	h.svc.Wait()
	r, _ := GetRun(context.Background(), h.db, ids[0])
	if r.PlanID != p.ID || len(r.Recipients) != 1 || r.Recipients[0] != other.Recipient().String() {
		t.Fatalf("run = %+v", r)
	}
}

func TestKitDetail(t *testing.T) {
	for _, tc := range []struct {
		missing []string
		sealed  int
		want    string
	}{
		{[]string{"v1"}, 3, "Key version v1 is not in it; restoring elsewhere means re-entering 3 sealed value(s)"},
		{[]string{"v1", "v2"}, 41, "Key versions v1 and v2 are not in it"},
		{[]string{"v1", "v2", "v3"}, 1, "Key versions v1, v2 and v3 are"},
	} {
		if got := kitDetail(tc.missing, tc.sealed); !strings.Contains(got, tc.want) {
			t.Errorf("kitDetail(%v) = %q, want %q", tc.missing, got, tc.want)
		}
	}
}

func TestScheduler_FailedRunRecordsThePhase(t *testing.T) {
	h := newHarness(t, "2026-09-30T02:00:00Z")
	p := h.plan(nil)
	h.exec.err = errors.New("disk full")
	h.clock.Set(mustTime(t, "2026-09-30T03:00:10Z"))
	h.tick()
	r := h.runs(p.ID)[0]
	if r.Status != StatusFailed || r.Error != "disk full" || r.EndedAt == nil {
		t.Fatalf("run = %+v", r)
	}
}

func TestManualRun_QueuesAndRunsWithoutAPlan(t *testing.T) {
	h := newHarness(t, "2026-09-30T12:00:00Z")
	h.plan(nil) // recipients come from here
	ids, err := h.svc.StartManual(context.Background(), ManualRequest{
		Scope: ScopeWorkspaces, WorkspaceIDs: []string{"ws_b"}, Preset: backup.PresetCustom,
		Contents: []string{"memory"}, Note: "before upgrade",
	}, "u1")
	if err != nil || len(ids) != 1 {
		t.Fatalf("start manual = %v, %v", ids, err)
	}
	h.svc.Wait()
	r, err := GetRun(context.Background(), h.db, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if r.Trigger != TriggerManual || r.Status != StatusDone || r.Kind != backup.KindCustom || r.Note != "before upgrade" {
		t.Fatalf("manual run = %+v", r)
	}
	specs := h.exec.calls()
	if len(specs) != 1 || len(specs[0].Categories) != 2 || specs[0].Categories[0] != "agents" || specs[0].Categories[1] != "memory" {
		t.Fatalf("executor spec = %+v", specs)
	}
	if specs[0].Actor.UserID != "u1" {
		t.Fatalf("actor = %+v", specs[0].Actor)
	}
}
