package backupplan

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/backup/offsite"
)

// ─── fakes ──────────────────────────────────────────────────────────────────

type alertCall struct {
	kind     string
	opened   bool
	resolved bool
	count    int
	message  string
}

type fakeAlerter struct {
	mu    sync.Mutex
	calls []alertCall
}

func (a *fakeAlerter) Raised(_ context.Context, inc *Incident, opened bool, _ string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls = append(a.calls, alertCall{kind: inc.Kind, opened: opened, count: inc.Count, message: inc.Message})
}

func (a *fakeAlerter) Resolved(_ context.Context, inc *Incident) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls = append(a.calls, alertCall{kind: inc.Kind, resolved: true, count: inc.Count})
}

func (a *fakeAlerter) all() []alertCall {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]alertCall(nil), a.calls...)
}

// memDest is an in-memory offsite.Destination that stores the SHA-256 it is
// told, the way S3 keeps it as object metadata.
type memDest struct {
	mu      sync.Mutex
	objects map[string][]byte
	sums    map[string]string
	putErr  error
	deleted []string
}

func newMemDest() *memDest { return &memDest{objects: map[string][]byte{}, sums: map[string]string{}} }

func (m *memDest) Kind() string { return "memory" }
func (m *memDest) Put(_ context.Context, key string, r io.Reader, size int64, sum string) (offsite.Object, error) {
	if m.putErr != nil {
		return offsite.Object{}, m.putErr
	}
	b, err := io.ReadAll(r)
	if err != nil {
		return offsite.Object{}, err
	}
	h := sha256.Sum256(b)
	if int64(len(b)) != size || hex.EncodeToString(h[:]) != sum {
		return offsite.Object{}, offsite.ErrContentMismatch
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[key], m.sums[key] = b, sum
	return offsite.Object{Key: key, Size: size, SHA256: sum}, nil
}
func (m *memDest) Get(_ context.Context, key string) (io.ReadCloser, offsite.Object, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.objects[key]
	if !ok {
		return nil, offsite.Object{}, offsite.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(b)), offsite.Object{Key: key, Size: int64(len(b)), SHA256: m.sums[key]}, nil
}
func (m *memDest) List(context.Context, string) ([]offsite.Object, error) { return nil, nil }
func (m *memDest) Head(_ context.Context, key string) (offsite.Object, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.objects[key]
	if !ok {
		return offsite.Object{}, offsite.ErrNotFound
	}
	return offsite.Object{Key: key, Size: int64(len(b)), SHA256: m.sums[key]}, nil
}
func (m *memDest) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objects, key)
	m.deleted = append(m.deleted, key)
	return nil
}
func (m *memDest) Test(context.Context) error { return nil }

// fileExec writes a real (fake-content) bundle file, so off-site uploads
// have bytes to send.
type fileExec struct {
	dir   string
	mu    sync.Mutex
	specs []RunSpec
}

func (e *fileExec) Run(_ context.Context, spec RunSpec, progress func(string)) (*RunResult, error) {
	e.mu.Lock()
	e.specs = append(e.specs, spec)
	e.mu.Unlock()
	for _, p := range []string{"copy", "pack", "encrypt"} {
		progress(p)
	}
	path := filepath.Join(e.dir, "crewship-workspace-alpha-"+spec.RunID+".tar.zst")
	if err := os.WriteFile(path, []byte("sealed bundle of "+spec.RunID), 0o600); err != nil {
		return nil, err
	}
	return &RunResult{Path: path, Size: int64(len("sealed bundle of ") + len(spec.RunID)), SHA256: "sha",
		Manifest: &backup.Manifest{Scope: backup.ScopeWorkspace, Encryption: backup.Encryption{Enabled: true}}}, nil
}

func incidentsOf(t *testing.T, h *harness) []Incident {
	t.Helper()
	list, err := ListIncidents(context.Background(), h.db, IncidentFilter{})
	if err != nil {
		t.Fatal(err)
	}
	return list
}

// ─── incidents ──────────────────────────────────────────────────────────────

func TestIncidents_OpenRepeatResolve(t *testing.T) {
	h := newHarness(t, "2026-09-30T02:00:00Z")
	ctx := context.Background()
	t0 := mustTime(t, "2026-09-30T03:00:00Z")
	steps := []struct {
		name       string
		act        func() (*Incident, bool, error)
		wantOpened bool
		wantCount  int
		wantLast   time.Time
		wantMsg    string
	}{
		{"first failure opens", func() (*Incident, bool, error) {
			return RaiseIncident(ctx, h.db, "bp_1", IncidentFailed, "first", "br_1", t0, true)
		}, true, 1, t0, "first"},
		{"a repeat counts and moves last_at", func() (*Incident, bool, error) {
			return RaiseIncident(ctx, h.db, "bp_1", IncidentFailed, "second", "br_2", t0.Add(time.Hour), true)
		}, false, 2, t0.Add(time.Hour), "second"},
		{"a persisting condition only refreshes the message", func() (*Incident, bool, error) {
			return RaiseIncident(ctx, h.db, "bp_1", IncidentFailed, "third", "", t0.Add(2*time.Hour), false)
		}, false, 2, t0.Add(time.Hour), "third"},
	}
	var id string
	for _, s := range steps {
		in, opened, err := s.act()
		if err != nil {
			t.Fatalf("%s: %v", s.name, err)
		}
		if id == "" {
			id = in.ID
		}
		if in.ID != id || opened != s.wantOpened || in.Count != s.wantCount || in.LastAt != ts(s.wantLast) || in.Message != s.wantMsg {
			t.Fatalf("%s: got id=%s opened=%v count=%d last=%s msg=%q", s.name, in.ID, opened, in.Count, in.LastAt, in.Message)
		}
	}
	// Another plan and another kind are separate incidents.
	if _, opened, _ := RaiseIncident(ctx, h.db, "bp_2", IncidentFailed, "x", "", t0, true); !opened {
		t.Fatal("another plan shares the incident")
	}
	if _, opened, _ := RaiseIncident(ctx, h.db, "bp_1", IncidentStale, "x", "", t0, true); !opened {
		t.Fatal("another kind shares the incident")
	}
	closed, err := ResolveIncidents(ctx, h.db, "bp_1", []string{IncidentFailed}, t0.Add(3*time.Hour))
	if err != nil || len(closed) != 1 || closed[0].ID != id || closed[0].State != "resolved" {
		t.Fatalf("resolve = %+v %v", closed, err)
	}
	// The next failure after a resolution opens a NEW incident.
	in, opened, _ := RaiseIncident(ctx, h.db, "bp_1", IncidentFailed, "again", "", t0.Add(4*time.Hour), true)
	if !opened || in.ID == id || in.Count != 1 {
		t.Fatalf("after resolve: opened=%v id=%s count=%d", opened, in.ID, in.Count)
	}
	open, _ := ListIncidents(ctx, h.db, IncidentFilter{State: "open"})
	if len(open) != 3 {
		t.Fatalf("open incidents = %d, want 3", len(open))
	}
}

func TestService_RunOutcomesRaiseAndResolveIncidents(t *testing.T) {
	h := newHarness(t, "2026-09-30T02:00:00Z")
	alerts := &fakeAlerter{}
	h.svc.Alerts = alerts
	p := h.plan(func(p *Plan) { p.Name = "Complete recovery" })

	failedOnly := func() []Incident {
		var out []Incident
		for _, in := range incidentsOf(t, h) {
			if in.Kind == IncidentFailed {
				out = append(out, in)
			}
		}
		return out
	}
	h.exec.err = errors.New("disk on fire")
	for i, at := range []string{"2026-09-30T03:00:20Z", "2026-10-01T03:00:20Z"} {
		h.clock.Set(mustTime(t, at))
		h.tick()
		inc := failedOnly()
		if len(inc) != 1 || inc[0].Kind != IncidentFailed || inc[0].State != "open" || inc[0].Count != i+1 {
			t.Fatalf("after failure %d: %+v", i+1, inc)
		}
		if !strings.HasPrefix(inc[0].Message, "Complete recovery backup failed. Last successful backup: never") {
			t.Fatalf("message = %q", inc[0].Message)
		}
		if inc[0].PlanName == nil || *inc[0].PlanName != "Complete recovery" || inc[0].RunID == nil {
			t.Fatalf("incident = %+v", inc[0])
		}
	}
	h.exec.err = nil
	h.clock.Set(mustTime(t, "2026-10-02T03:00:20Z"))
	h.tick()
	inc := failedOnly()
	if len(inc) != 1 || inc[0].State != "resolved" || inc[0].ResolvedAt == nil {
		t.Fatalf("after a good run: %+v", inc)
	}
	var calls []alertCall
	for _, c := range alerts.all() {
		if c.kind == IncidentFailed {
			calls = append(calls, c)
		}
	}
	want := []alertCall{{kind: IncidentFailed, opened: true, count: 1}, {kind: IncidentFailed, opened: false, count: 2}, {kind: IncidentFailed, resolved: true, count: 2}}
	if len(calls) != len(want) {
		t.Fatalf("alerts = %+v", calls)
	}
	for i := range want {
		if calls[i].kind != want[i].kind || calls[i].opened != want[i].opened || calls[i].resolved != want[i].resolved || calls[i].count != want[i].count {
			t.Fatalf("alert %d = %+v, want %+v", i, calls[i], want[i])
		}
	}
	_ = p
}

func TestService_DisabledEventIsRecordedNotDelivered(t *testing.T) {
	h := newHarness(t, "2026-09-30T02:00:00Z")
	alerts := &fakeAlerter{}
	h.svc.Alerts = alerts
	set := DefaultSettings()
	set.Events.Failed = false
	if err := SaveSettings(context.Background(), h.db, set, "u1", h.clock.Now()); err != nil {
		t.Fatal(err)
	}
	h.plan(nil)
	h.exec.err = errors.New("nope")
	h.clock.Set(mustTime(t, "2026-09-30T03:00:20Z"))
	h.tick()
	if inc := incidentsOf(t, h); len(inc) != 1 || inc[0].State != "open" {
		t.Fatalf("incident not recorded: %+v", inc)
	}
	if calls := alerts.all(); len(calls) != 0 {
		t.Fatalf("delivered a switched-off kind: %+v", calls)
	}
}

func TestService_StaleDetectionWithAFakeClock(t *testing.T) {
	h := newHarness(t, "2026-09-30T02:00:00Z")
	alerts := &fakeAlerter{}
	h.svc.Alerts = alerts
	p := h.plan(nil)
	// A good run at 03:00.
	h.clock.Set(mustTime(t, "2026-09-30T03:00:20Z"))
	h.tick()
	// Keep the plan from running again, so time passes without a backup.
	if _, err := h.db.Exec(`UPDATE backup_plans SET next_run_at = '2099-01-01T00:00:00Z' WHERE id = ?`, p.ID); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		at        string
		wantState string // "" = no incident yet
	}{
		{"2026-10-01T14:00:00Z", ""},         // 35 h after: within 36 h
		{"2026-10-01T15:10:00Z", "open"},     // 36 h 10 m: stale
		{"2026-10-01T15:20:00Z", "open"},     // still stale: no second delivery
		{"2026-10-02T03:00:00Z", "resolved"}, // a new good run clears it
	}
	for _, c := range cases {
		h.clock.Set(mustTime(t, c.at))
		if c.wantState == "resolved" {
			if _, err := h.svc.StartManual(context.Background(), ManualRequest{PlanID: p.ID}, ""); err != nil {
				t.Fatal(err)
			}
			h.svc.Wait()
		}
		h.svc.lastStale = time.Time{} // the check is throttled to every 5 minutes
		h.tick()
		var stale []Incident
		for _, in := range incidentsOf(t, h) {
			if in.Kind == IncidentStale {
				stale = append(stale, in)
			}
		}
		switch {
		case c.wantState == "" && len(stale) != 0:
			t.Fatalf("%s: stale too early: %+v", c.at, stale)
		case c.wantState != "" && (len(stale) != 1 || stale[0].State != c.wantState):
			t.Fatalf("%s: stale = %+v, want %s", c.at, stale, c.wantState)
		}
		if c.wantState == "open" && !strings.Contains(stale[0].Message, "no backup for over 36 hours") {
			t.Fatalf("message = %q", stale[0].Message)
		}
	}
	var raised int
	for _, c := range alerts.all() {
		if c.kind == IncidentStale && !c.resolved {
			raised++
		}
	}
	if raised != 1 {
		t.Fatalf("stale delivered %d times, want once", raised)
	}
}

func TestService_SpaceFloorRefusesARun(t *testing.T) {
	cases := []struct {
		name        string
		free, total uint64
		env         string
		wantSkipped bool
	}{
		{"plenty of room", 900 << 30, 1000 << 30, "", false},
		{"would leave under 10 %", 95 << 30, 1000 << 30, "", true},
		{"the floor moved to 5 % by the operator", 95 << 30, 1000 << 30, "5", false},
		{"less than the staging need", 1 << 20, 1 << 40, "", true},
		{"less than the staging need even with no floor", 1 << 20, 1 << 40, "0", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv(MinFreePercentEnv, c.env)
			h := newHarness(t, "2026-09-30T02:00:00Z")
			h.svc.Space = func(string) (uint64, uint64, error) { return c.free, c.total, nil }
			// A recent 2 GiB bundle of ws_a: the next run needs ~4 GiB.
			if _, err := h.db.Exec(`INSERT INTO backup_catalog (id, file_path, scope, workspace_id, slug, created_at, created_by, size, sha256, encrypted, format_version)
				VALUES ('c1','/b/one.tar.zst','workspace','ws_a','alpha','2026-09-29T03:00:00Z','x',?, 'sha', 1, 3)`, int64(2<<30)); err != nil {
				t.Fatal(err)
			}
			p := h.plan(nil)
			h.clock.Set(mustTime(t, "2026-09-30T03:00:20Z"))
			h.tick()
			r := h.runs(p.ID)[0]
			if c.wantSkipped {
				if r.Status != StatusSkipped || !strings.Contains(r.Error, "not enough disk space") || !strings.Contains(r.Error, "free does not start") {
					t.Fatalf("run = %s %q", r.Status, r.Error)
				}
				if len(h.exec.calls()) != 0 {
					t.Fatal("the executor ran anyway")
				}
				if inc := incidentsOf(t, h); len(inc) != 1 || inc[0].Kind != IncidentFailed || !strings.Contains(inc[0].Message, "did not run") {
					t.Fatalf("incident = %+v", inc)
				}
				return
			}
			if r.Status != StatusDone {
				t.Fatalf("run = %s %q", r.Status, r.Error)
			}
		})
	}
}

func TestStagingNeed_TwiceTheLargestRecentOfTheScope(t *testing.T) {
	h := newHarness(t, "2026-09-30T02:00:00Z")
	for _, q := range []string{
		`INSERT INTO backup_catalog (id, file_path, scope, workspace_id, slug, created_at, created_by, size, sha256, encrypted, format_version) VALUES
			('a','/b/a','workspace','ws_a','alpha','2026-09-29T03:00:00Z','x',100,'s',1,3),
			('b','/b/b','workspace','ws_b','beta','2026-09-29T03:00:00Z','x',300,'s',1,3),
			('c','/b/c','workspace','ws_a','alpha','2026-01-01T03:00:00Z','x',5000,'s',1,3),
			('d','/b/d','instance','','instance','2026-09-29T03:00:00Z','x',700,'s',1,3)`,
	} {
		if _, err := h.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := backup.ListCatalog(context.Background(), h.db, "")
	if err != nil {
		t.Fatal(err)
	}
	now := mustTime(t, "2026-09-30T03:00:00Z")
	for _, c := range []struct {
		scope, ws string
		want      int64
	}{
		{ScopeWorkspaces, "ws_a", 200}, // the old 5000 is not recent
		{ScopeWorkspaces, "ws_b", 600},
		{ScopeWorkspaces, "", 600},
		{ScopeInstance, "", 1400},
		{ScopeWorkspaces, "ws_c", 0},
	} {
		if got := StagingNeed(entries, c.scope, c.ws, now); got != c.want {
			t.Errorf("StagingNeed(%s, %s) = %d, want %d", c.scope, c.ws, got, c.want)
		}
	}
}

// ─── limits ─────────────────────────────────────────────────────────────────

func TestService_LimitsReachTheExecutorAndTheQueue(t *testing.T) {
	h := newHarness(t, "2026-09-30T02:00:00Z")
	set := DefaultSettings()
	set.Limits = Limits{Concurrency: 2, CPUCores: 3, DiskMBps: 40, UploadMBps: 10}
	if err := SaveSettings(context.Background(), h.db, set, "u1", h.clock.Now()); err != nil {
		t.Fatal(err)
	}
	p := h.plan(func(p *Plan) { p.WorkspaceIDs = []string{} }) // three runs
	h.exec.gate = make(chan struct{})
	h.clock.Set(mustTime(t, "2026-09-30T03:00:10Z"))
	h.svc.Tick(context.Background())
	// Both slots fill before anything is released.
	deadline := time.Now().Add(10 * time.Second)
	for {
		h.exec.mu.Lock()
		busy := h.exec.busy
		h.exec.mu.Unlock()
		if busy == 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	for i := 0; i < 3; i++ {
		h.exec.gate <- struct{}{}
	}
	h.svc.Wait()
	if h.exec.peak != 2 {
		t.Fatalf("peak concurrency %d, want the settings' 2", h.exec.peak)
	}
	calls := h.exec.calls()
	if len(calls) != 3 {
		t.Fatalf("%d runs", len(calls))
	}
	first := calls[0].DiskThrottle
	lim, ok := first.(*offsite.Limiter)
	if !ok || lim == nil || lim.Burst() != 40<<20 {
		t.Fatalf("disk throttle = %#v, want a 40 MB/s limiter", first)
	}
	for _, c := range calls {
		if c.EncoderConcurrency != 3 {
			t.Fatalf("encoder concurrency %d, want 3", c.EncoderConcurrency)
		}
		if c.DiskThrottle != first {
			t.Fatal("each run got its own disk limiter; the cap must be shared")
		}
	}
	_ = p
	// No cap: no throttle at all.
	set.Limits.DiskMBps = 0
	if err := SaveSettings(context.Background(), h.db, set, "u1", h.clock.Now()); err != nil {
		t.Fatal(err)
	}
	h.exec.gate = nil
	if _, err := h.svc.StartManual(context.Background(), ManualRequest{PlanID: p.ID}, ""); err != nil {
		t.Fatal(err)
	}
	h.svc.Wait()
	calls = h.exec.calls()
	if calls[len(calls)-1].DiskThrottle != nil {
		t.Fatalf("disk throttle with no cap = %#v", calls[len(calls)-1].DiskThrottle)
	}
}

func TestSettings_NormalizeAndRoundTrip(t *testing.T) {
	h := newHarness(t, "2026-09-30T02:00:00Z")
	ctx := context.Background()
	got, err := LoadSettings(ctx, h.db)
	if err != nil {
		t.Fatal(err)
	}
	if got.Limits.Concurrency != 1 || got.StaleAlertHours != 36 || got.DrillReminder != DrillMonthly || !got.Events.Failed || got.HeartbeatURL != nil {
		t.Fatalf("defaults = %+v", got)
	}
	bad := []struct {
		name string
		mut  func(*Settings)
		want string
	}{
		{"zero concurrency", func(s *Settings) { s.Limits.Concurrency = 0 }, "concurrency"},
		{"negative upload", func(s *Settings) { s.Limits.UploadMBps = -1 }, "negative"},
		{"http heartbeat", func(s *Settings) { u := "http://hc.example.com/ping/x"; s.HeartbeatURL = &u }, "https"},
		{"private heartbeat", func(s *Settings) { u := "https://127.0.0.1/ping"; s.HeartbeatURL = &u }, "heartbeat_url"},
		{"drill reminder", func(s *Settings) { s.DrillReminder = "daily" }, "drill_reminder"},
		{"stale hours", func(s *Settings) { s.StaleAlertHours = 0 }, "stale_alert_hours"},
	}
	for _, b := range bad {
		s := DefaultSettings()
		b.mut(&s)
		if err := s.Normalize(); err == nil || !strings.Contains(err.Error(), b.want) {
			t.Errorf("%s: err = %v, want %q", b.name, err, b.want)
		}
	}
	s := DefaultSettings()
	u := " https://hc.example.com/ping/abc "
	s.HeartbeatURL, s.Channels = &u, []string{"slack", " slack ", "email"}
	s.Limits = Limits{Concurrency: 2, CPUCores: 4, DiskMBps: 80, UploadMBps: 20}
	if err := s.Normalize(); err != nil {
		t.Fatal(err)
	}
	if err := SaveSettings(ctx, h.db, s, "u1", h.clock.Now()); err != nil {
		t.Fatal(err)
	}
	got, _ = LoadSettings(ctx, h.db)
	if *got.HeartbeatURL != "https://hc.example.com/ping/abc" || strings.Join(got.Channels, ",") != "slack,email" || got.Limits != s.Limits {
		t.Fatalf("round trip = %+v", got)
	}
}

// ─── heartbeat ──────────────────────────────────────────────────────────────

func TestService_HeartbeatPingsAfterAGoodRunOnly(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/ping/abc" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		hits.Add(1)
	}))
	defer srv.Close()
	h := newHarness(t, "2026-09-30T02:00:00Z")
	h.svc.HeartbeatClient, h.svc.allowPrivateHeartbeat = srv.Client(), true
	set := DefaultSettings()
	u := srv.URL + "/ping/abc"
	set.HeartbeatURL = &u
	if err := SaveSettings(context.Background(), h.db, set, "u1", h.clock.Now()); err != nil {
		t.Fatal(err)
	}
	h.plan(nil)
	h.exec.err = errors.New("boom")
	h.clock.Set(mustTime(t, "2026-09-30T03:00:20Z"))
	h.tick()
	if hits.Load() != 0 {
		t.Fatal("a failed run pinged the heartbeat")
	}
	h.exec.err = nil
	h.clock.Set(mustTime(t, "2026-10-01T03:00:20Z"))
	h.tick()
	if hits.Load() != 1 {
		t.Fatalf("heartbeat hits = %d, want 1", hits.Load())
	}
	got, _ := LoadSettings(context.Background(), h.db)
	if got.HeartbeatLastOK == nil || !*got.HeartbeatLastOK || got.HeartbeatLastAt == nil {
		t.Fatalf("last ping not recorded: %+v", got)
	}
}

func TestService_HeartbeatRetriesOnceAndRecordsTheFailure(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	h := newHarness(t, "2026-09-30T02:00:00Z")
	h.svc.HeartbeatClient, h.svc.allowPrivateHeartbeat = srv.Client(), true
	if err := h.svc.ping(context.Background(), srv.URL); err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("ping err = %v", err)
	}
	if hits.Load() != 2 {
		t.Fatalf("attempts = %d, want 2 (one retry)", hits.Load())
	}
	// Without the test hook, a loopback URL never leaves the server.
	h.svc.allowPrivateHeartbeat = false
	if err := h.svc.ping(context.Background(), srv.URL); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("private ping err = %v", err)
	}
	if hits.Load() != 2 {
		t.Fatal("the SSRF guard let a loopback ping through")
	}
}

// ─── off-site ───────────────────────────────────────────────────────────────

func TestService_OffsiteCopyIsRecordedOnlyAfterVerify(t *testing.T) {
	h := newHarness(t, "2026-09-30T02:00:00Z")
	alerts := &fakeAlerter{}
	h.svc.Alerts = alerts
	dir := t.TempDir()
	fe := &fileExec{dir: dir}
	h.svc.Workspace = fe
	good, bad := newMemDest(), newMemDest()
	bad.putErr = errors.New("403 AccessDenied")
	dests := map[string]*memDest{"bdst_good": good, "bdst_bad": bad}
	h.svc.Destinations = func(_ context.Context, id string) (offsite.Destination, *Destination, error) {
		d, ok := dests[id]
		if !ok {
			return nil, nil, ErrNotFound
		}
		return d, &Destination{ID: id, Name: strings.TrimPrefix(id, "bdst_")}, nil
	}
	for _, id := range []string{"bdst_good", "bdst_bad"} {
		d := &Destination{Name: id, Endpoint: "https://s3.example.com", Bucket: "b", AccessKeyID: "AK"}
		if err := InsertDestination(context.Background(), h.db, d, "enc", "u1", h.clock.Now()); err != nil {
			t.Fatal(err)
		}
		if _, err := h.db.Exec(`UPDATE backup_offsite_destinations SET id = ? WHERE id = ?`, id, d.ID); err != nil {
			t.Fatal(err)
		}
	}

	// Only the good destination: the copy is recorded and verified.
	p := h.plan(func(p *Plan) { p.Destinations = []string{"local", "bdst_good"} })
	h.clock.Set(mustTime(t, "2026-09-30T03:00:20Z"))
	h.tick()
	r := h.runs(p.ID)[0]
	ph := r.Phases[phaseIndex(r, PhaseOffsite)]
	if r.Status != StatusDone || ph.Status != "done" || ph.Detail == nil || !strings.Contains(*ph.Detail, "copied and verified to good") {
		t.Fatalf("run %s, off-site phase %+v", r.Status, ph)
	}
	copies, _ := CopiesOf(context.Background(), h.db, r.BundlePath)
	if len(copies) != 1 || copies[0].DestinationID != "bdst_good" || copies[0].Key != "workspaces/ws_a/"+filepath.Base(r.BundlePath) {
		t.Fatalf("copies = %+v", copies)
	}
	if len(good.objects) != 1 {
		t.Fatalf("objects at the destination = %d", len(good.objects))
	}

	// Add the failing one: the good copy still lands, the phase fails, the
	// run keeps its local bundle (done), and an off-site incident opens.
	p.Destinations = []string{"local", "bdst_good", "bdst_bad"}
	if err := UpdatePlan(context.Background(), h.db, p, "u1", h.clock.Now()); err != nil {
		t.Fatal(err)
	}
	ids, err := h.svc.StartManual(context.Background(), ManualRequest{PlanID: p.ID}, "")
	if err != nil || len(ids) != 1 {
		t.Fatalf("StartManual = %v, %v", ids, err)
	}
	h.svc.Wait()
	// Both runs share the fake clock's started_at, so pick the manual one by
	// id rather than by list order.
	for _, run := range h.runs(p.ID) {
		if run.ID == ids[0] {
			r = run
		}
	}
	ph = r.Phases[phaseIndex(r, PhaseOffsite)]
	if r.ID != ids[0] || r.Status != StatusDone || ph.Status != "failed" || !strings.Contains(*ph.Detail, "bad: 403 AccessDenied") {
		t.Fatalf("run %s, off-site phase %+v", r.Status, ph)
	}
	if copies, _ := CopiesOf(context.Background(), h.db, r.BundlePath); len(copies) != 1 {
		t.Fatalf("a failed upload was recorded as a copy: %+v", copies)
	}
	var offsiteInc *Incident
	for _, in := range incidentsOf(t, h) {
		if in.Kind == IncidentOffsite {
			in := in
			offsiteInc = &in
		}
	}
	if offsiteInc == nil || offsiteInc.State != "open" || !strings.Contains(offsiteInc.Message, "upload to bad failed") {
		t.Fatalf("off-site incident = %+v", offsiteInc)
	}

	// Retention of the bundle deletes its remote copies and their records.
	h.svc.dropRemoteCopies(context.Background(), []string{r.BundlePath})
	if copies, _ := CopiesOf(context.Background(), h.db, r.BundlePath); len(copies) != 0 {
		t.Fatalf("copy records left after retention: %+v", copies)
	}
	if len(good.deleted) != 1 {
		t.Fatalf("remote deletes = %v", good.deleted)
	}
}

func TestOverview_OffsiteVerifiedWhenEveryRecentBundleHasACopy(t *testing.T) {
	h := newHarness(t, "2026-09-30T02:00:00Z")
	ctx := context.Background()
	d := &Destination{Name: "r2-backups", Endpoint: "https://s3.example.com", Bucket: "b", AccessKeyID: "AK"}
	if err := InsertDestination(ctx, h.db, d, "enc", "u1", h.clock.Now()); err != nil {
		t.Fatal(err)
	}
	p := Defaults("complete")
	p.RecipientIDs, p.Destinations = []string{h.key}, []string{"local", d.ID}
	if err := Normalize(ctx, &p, DBWorkspaces{DB: h.db}, h.svc.Recipients); err != nil {
		t.Fatal(err)
	}
	if err := InsertPlan(ctx, h.db, &p, "u1", h.clock.Now()); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO backup_catalog (id, file_path, scope, workspace_id, slug, kind, created_at, created_by, size, sha256, encrypted, format_version) VALUES
			('i1','/b/i1','instance','','instance','instance','2026-09-28T03:00:00Z','x',10,'s',1,3),
			('i2','/b/i2','instance','','instance','instance','2026-09-29T03:00:00Z','x',10,'s',1,3)`,
	} {
		if _, err := h.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	now := mustTime(t, "2026-09-30T03:00:00Z")
	ov, err := BuildOverview(ctx, h.db, OverviewInput{Scope: ScopeInstance}, now)
	if err != nil {
		t.Fatal(err)
	}
	if ov.Status.OffsiteVerified || ov.Status.Where.DetailTone == nil || !strings.Contains(*ov.Status.Where.Detail, "0 of 2") {
		t.Fatalf("no copies: where = %+v verified=%v", ov.Status.Where, ov.Status.OffsiteVerified)
	}
	_ = RecordCopy(ctx, h.db, Copy{BundlePath: "/b/i1", DestinationID: d.ID, Key: "k1", Size: 10, SHA256: "s", VerifiedAt: ts(now)})
	ov, _ = BuildOverview(ctx, h.db, OverviewInput{Scope: ScopeInstance}, now)
	if ov.Status.OffsiteVerified {
		t.Fatal("verified with one recent bundle uncopied")
	}
	_ = RecordCopy(ctx, h.db, Copy{BundlePath: "/b/i2", DestinationID: d.ID, Key: "k2", Size: 10, SHA256: "s", VerifiedAt: ts(now)})
	ov, _ = BuildOverview(ctx, h.db, OverviewInput{Scope: ScopeInstance}, now)
	if !ov.Status.OffsiteVerified || ov.Status.Where.Value != "This server and r2-backups" || ov.Status.Where.DetailTone != nil {
		t.Fatalf("all copied: where = %+v verified=%v", ov.Status.Where, ov.Status.OffsiteVerified)
	}
}

// ─── recipients ─────────────────────────────────────────────────────────────

func TestRecipients_ValidateStoreAndRefuseDeletingOneInUse(t *testing.T) {
	h := newHarness(t, "2026-09-30T02:00:00Z")
	ctx := context.Background()
	bad := []struct {
		name string
		r    Recipient
		want string
	}{
		{"no name", Recipient{PublicKey: h.key}, "name"},
		{"private key", Recipient{Name: "x", PublicKey: "AGE-SECRET-KEY-1QQQ"}, "PRIVATE"},
		{"garbage", Recipient{Name: "x", PublicKey: "age1nope"}, "not a valid age public key"},
	}
	for _, b := range bad {
		r := b.r
		if err := NormalizeRecipient(&r); err == nil || !strings.Contains(err.Error(), b.want) {
			t.Errorf("%s: err = %v", b.name, err)
		}
	}
	r := Recipient{Name: " ops-2026 ", PublicKey: " " + h.key + " ", Holder: "platform lead"}
	if err := NormalizeRecipient(&r); err != nil {
		t.Fatal(err)
	}
	if err := InsertRecipient(ctx, h.db, &r, "u1", h.clock.Now()); err != nil {
		t.Fatal(err)
	}
	dup := Recipient{Name: "again", PublicKey: h.key}
	if err := InsertRecipient(ctx, h.db, &dup, "u1", h.clock.Now()); !errors.Is(err, ErrDuplicateRecipient) {
		t.Fatalf("duplicate err = %v", err)
	}
	// Plans resolve the id through the table.
	recs, names, err := DBRecipients{DB: h.db}.Resolve(ctx, []string{r.ID})
	if err != nil || len(recs) != 1 || names[0] != "ops-2026" {
		t.Fatalf("resolve = %v %v %v", recs, names, err)
	}
	p := h.plan(func(p *Plan) { p.Name = "Nightly"; p.RecipientIDs = []string{r.ID} })
	list, _ := ListRecipients(ctx, h.db)
	if len(list) != 1 || strings.Join(list[0].UsedBy, ",") != "Nightly" || !strings.HasPrefix(list[0].Fingerprint, "SHA256:") {
		t.Fatalf("list = %+v", list)
	}
	used, err := DeleteRecipient(ctx, h.db, h.db, r.ID)
	if !errors.Is(err, ErrRecipientInUse) || strings.Join(used, ",") != "Nightly" {
		t.Fatalf("delete in use = %v %v", used, err)
	}
	// Encrypting to the key directly (age1…) is using it too.
	p.RecipientIDs = []string{h.key}
	if err := UpdatePlan(ctx, h.db, p, "u1", h.clock.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := DeleteRecipient(ctx, h.db, h.db, r.ID); !errors.Is(err, ErrRecipientInUse) {
		t.Fatalf("delete while a plan names the raw key: %v", err)
	}
	other, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	p.RecipientIDs = []string{other.Recipient().String()}
	if err := UpdatePlan(ctx, h.db, p, "u1", h.clock.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := DeleteRecipient(ctx, h.db, h.db, r.ID); err != nil {
		t.Fatalf("delete after the plan changed: %v", err)
	}
	if _, err := GetRecipient(ctx, h.db, r.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("still there: %v", err)
	}
}

func TestAgo(t *testing.T) {
	now := mustTime(t, "2026-09-30T12:00:00Z")
	at := func(d time.Duration) *time.Time { x := now.Add(-d); return &x }
	cases := []struct {
		t    *time.Time
		want string
	}{
		{nil, "never"},
		{at(20 * time.Second), "just now"},
		{at(5 * time.Minute), "5 minutes ago"},
		{at(32 * time.Hour), "32 hours ago"},
		{at(5 * 24 * time.Hour), "5 days ago"},
	}
	for _, c := range cases {
		if got := Ago(c.t, now); got != c.want {
			t.Errorf("Ago = %q, want %q", got, c.want)
		}
	}
}
