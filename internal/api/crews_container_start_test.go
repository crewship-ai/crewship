package api

// There was no way to start a crew container.
//
// `crewship crew provision` builds an image and stops; the container is
// created lazily on the crew's first agent run. So the only way to get a
// crew running was to run an agent at it, which spends tokens, and the
// 409 from `crew files save` into a crew-owned tree told operators to
// "start the crew" with no command that would.
//
// The endpoint must be the SAME three steps the run path takes —
// EnsureProvisioned, buildCrewRuntimeConfig, crewstart.Start — because
// internal/crewstart exists precisely to stop call sites from having
// their own idea of what starting a crew means (a crew started without
// its declared sidecars, or from the bare base image instead of its
// devcontainer, is the failure that package was written for).

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

// startFakeProvider records the config it was asked to start so the
// tests can assert the crew was started the way a run would start it.
type startFakeProvider struct {
	gotConfig   provider.CrewConfig
	calls       int
	ensureErr   error
	statusState string // "" = provider cannot tell
	statusErr   error  // when set, ContainerStatus fails with it
}

func (p *startFakeProvider) EnsureCrewRuntime(_ context.Context, cfg provider.CrewConfig) (string, error) {
	p.calls++
	p.gotConfig = cfg
	if p.ensureErr != nil {
		return "", p.ensureErr
	}
	return "container-started", nil
}
func (p *startFakeProvider) StopCrewRuntime(_ context.Context, _ string) error   { return nil }
func (p *startFakeProvider) RemoveCrewRuntime(_ context.Context, _ string) error { return nil }

// ContainerStatus backs the post-start verification. Default (empty
// state) means "cannot tell", which the handler treats as running — the
// existing tests keep their behaviour; the new one sets it explicitly.
func (p *startFakeProvider) ContainerStatus(_ context.Context, _ string) (*provider.ContainerStatus, error) {
	if p.statusErr != nil {
		return nil, p.statusErr
	}
	if p.statusState == "" {
		return nil, nil
	}
	return &provider.ContainerStatus{State: p.statusState}, nil
}
func (p *startFakeProvider) ContainerStats(_ context.Context, _ string) (*provider.ContainerMetrics, error) {
	return nil, nil
}
func (p *startFakeProvider) Exec(_ context.Context, _ provider.ExecConfig) (*provider.ExecResult, error) {
	return nil, nil
}
func (p *startFakeProvider) ExecInspect(_ context.Context, _ string) (bool, int, error) {
	return false, 0, nil
}
func (p *startFakeProvider) CrewContainerName(_ string, slug string) string { return "crew-" + slug }
func (p *startFakeProvider) CopyToContainer(_ context.Context, _, _ string, _ io.Reader) error {
	return nil
}

var _ provider.ContainerProvider = (*startFakeProvider)(nil)

// startFakeProvisioner stands in for *ProvisioningHandler. The gate has
// to be CALLED — a cold crew started without it comes up on the bare
// runtime image, which has no agent CLI.
type startFakeProvisioner struct {
	ensureCalls int
	ensureErr   error
}

func (p *startFakeProvisioner) EnqueueForCrew(_ context.Context, _, _ string) (EnqueueResult, error) {
	return EnqueueResult{}, nil
}
func (p *startFakeProvisioner) EnsureProvisioned(_ context.Context, _, _ string, _ time.Duration) error {
	p.ensureCalls++
	return p.ensureErr
}

// startFakeActivity records what the idle-TTL reaper was told.
type startFakeActivity struct {
	crewID      string
	containerID string
	ttlHours    int
	calls       int
	forgotten   int
}

func (a *startFakeActivity) NoteCrewActivity(crewID, containerID string, ttlHours int) {
	a.calls++
	a.crewID, a.containerID, a.ttlHours = crewID, containerID, ttlHours
}

func (a *startFakeActivity) ForgetCrewActivity(crewID string) {
	a.forgotten++
	a.crewID = crewID
}

// Orchestrator.NoteCrewActivity: "Every path that calls EnsureCrewRuntime
// must call this — before #1662 only RunAgent did, so a container woken
// by a script step or a prewarm was tracked by nothing and ran until the
// daemon restarted."
//
// ContainerStart is a prewarm by that definition: it is the only start
// path with no agent run behind it to report the activity incidentally.
// Skip the call and `crewship crew start` leaks a container past its
// container_ttl_hours — worst exactly where the feature is aimed, an
// operator starting several crews to land a restore.
func TestContainerStart_ReportsActivityToTheIdleReaper(t *testing.T) {
	cp := &startFakeProvider{}
	h, wsID, crewID := startTestHandler(t, cp)
	act := &startFakeActivity{}
	h.SetActivityNoter(act)

	rec := httptest.NewRecorder()
	h.ContainerStart(rec, startRequest(crewID, wsID, "OWNER"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	if act.calls != 1 {
		t.Fatalf("NoteCrewActivity called %d times, want 1 — an unreported container "+
			"runs until crewshipd restarts", act.calls)
	}
	if act.crewID != crewID {
		t.Errorf("noted crew %q, want %q", act.crewID, crewID)
	}
	if act.containerID != "container-started" {
		t.Errorf("noted container %q, want the one just started", act.containerID)
	}
}

// A failed start has no container, so there is nothing to report and
// nothing to reap. Reporting anyway would put a phantom in the reaper's
// map keyed to an empty container id.
func TestContainerStart_ReportsNoActivityWhenTheStartFailed(t *testing.T) {
	cp := &startFakeProvider{ensureErr: errors.New("no such image")}
	h, wsID, crewID := startTestHandler(t, cp)
	act := &startFakeActivity{}
	h.SetActivityNoter(act)

	rec := httptest.NewRecorder()
	h.ContainerStart(rec, startRequest(crewID, wsID, "OWNER"))

	if act.calls != 0 {
		t.Errorf("reported activity for a container that was never started")
	}
}

// No orchestrator wired (a server started without one) must not panic.
func TestContainerStart_NoActivityNoterIsFine(t *testing.T) {
	cp := &startFakeProvider{}
	h, wsID, crewID := startTestHandler(t, cp)

	rec := httptest.NewRecorder()
	h.ContainerStart(rec, startRequest(crewID, wsID, "OWNER"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 with no activity noter", rec.Code)
	}
}

func startTestHandler(t *testing.T, cp provider.ContainerProvider) (*CrewHandler, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	mustExec(t, db, `INSERT INTO crews (id, workspace_id, name, slug, services_json)
		VALUES (?, ?, ?, ?, ?)`, "crew-start-1", wsID, "Uctarna", "uctarna", redisServicesJSON)

	h := NewCrewHandler(db, testLogger())
	if cp != nil {
		h.SetContainer(cp)
	}
	return h, wsID, "crew-start-1"
}

func startRequest(crewID, wsID, role string) *http.Request {
	req := httptest.NewRequest("POST", "/api/v1/crews/"+crewID+"/container-start", nil)
	req.SetPathValue("crewId", crewID)
	ctx := context.WithValue(req.Context(), ctxWorkspaceID, wsID)
	ctx = context.WithValue(ctx, ctxRole, role)
	return req.WithContext(ctx)
}

func TestContainerStart_StartsTheCrewTheWayARunWould(t *testing.T) {
	cp := &startFakeProvider{}
	h, wsID, crewID := startTestHandler(t, cp)
	prov := &startFakeProvisioner{}
	h.SetProvisioner(prov)

	rec := httptest.NewRecorder()
	h.ContainerStart(rec, startRequest(crewID, wsID, "OWNER"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["container_id"] != "container-started" {
		t.Errorf("container_id = %v, want the started container", body["container_id"])
	}
	if body["status"] != "running" {
		t.Errorf("status = %v, want running", body["status"])
	}

	if prov.ensureCalls != 1 {
		t.Errorf("EnsureProvisioned called %d times, want 1 — skipping it starts a cold crew from "+
			"the bare runtime image, which has no agent CLI", prov.ensureCalls)
	}
	if cp.calls != 1 {
		t.Fatalf("EnsureCrewRuntime called %d times, want 1", cp.calls)
	}
	// The config must be the resolved one, not a bare {slug, id}: that is
	// the whole reason internal/crewstart is a chokepoint.
	if cp.gotConfig.ID != crewID {
		t.Errorf("CrewConfig.ID = %q, want %q", cp.gotConfig.ID, crewID)
	}
	if len(cp.gotConfig.Services) != 1 {
		t.Errorf("Services = %d, want the crew's declared sidecar — a crew started without its "+
			"declared datastore is exactly the #1708 failure", len(cp.gotConfig.Services))
	}
}

// Calling it on a crew that is already up must be a no-op that succeeds.
// EnsureCrewRuntime is get-or-create, so this is really a guard against
// someone adding a "already running" rejection later: an operator (or a
// script) retrying start should never have to branch on it.
func TestContainerStart_IsIdempotent(t *testing.T) {
	cp := &startFakeProvider{}
	h, wsID, crewID := startTestHandler(t, cp)

	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		h.ContainerStart(rec, startRequest(crewID, wsID, "OWNER"))
		if rec.Code != http.StatusOK {
			t.Fatalf("call %d: status = %d, want 200: %s", i+1, rec.Code, rec.Body.String())
		}
	}
	if cp.calls != 3 {
		t.Errorf("EnsureCrewRuntime calls = %d, want 3 (get-or-create each time)", cp.calls)
	}
}

func TestContainerStart_ScopesToTheCallersWorkspace(t *testing.T) {
	cp := &startFakeProvider{}
	h, _, crewID := startTestHandler(t, cp)

	rec := httptest.NewRecorder()
	h.ContainerStart(rec, startRequest(crewID, "some-other-workspace", "OWNER"))

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for a crew in another workspace", rec.Code)
	}
	if cp.calls != 0 {
		t.Errorf("started a crew belonging to another workspace")
	}
}

func TestContainerStart_RequiresCreateRole(t *testing.T) {
	cp := &startFakeProvider{}
	h, wsID, crewID := startTestHandler(t, cp)

	rec := httptest.NewRecorder()
	h.ContainerStart(rec, startRequest(crewID, wsID, "VIEWER"))

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 for VIEWER — starting a container spends host resources",
			rec.Code)
	}
	if cp.calls != 0 {
		t.Errorf("VIEWER started a container")
	}
}

// No container runtime wired (--no-docker, tests, an unreachable daemon)
// is a 503 naming the cause, not a 500 and not a silent success.
func TestContainerStart_NoRuntimeConfigured(t *testing.T) {
	h, wsID, crewID := startTestHandler(t, nil)

	rec := httptest.NewRecorder()
	h.ContainerStart(rec, startRequest(crewID, wsID, "OWNER"))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, "container runtime") {
		t.Errorf("503 does not name the cause: %s", body)
	}
}

// A failed provision must not be reported as a started crew.
func TestContainerStart_ProvisionFailureSurfaces(t *testing.T) {
	cp := &startFakeProvider{}
	h, wsID, crewID := startTestHandler(t, cp)
	h.SetProvisioner(&startFakeProvisioner{ensureErr: errors.New("build blew up")})

	rec := httptest.NewRecorder()
	h.ContainerStart(rec, startRequest(crewID, wsID, "OWNER"))

	if rec.Code == http.StatusOK {
		t.Fatalf("a failed provision reported success: %s", rec.Body.String())
	}
	if cp.calls != 0 {
		t.Errorf("started the crew after provisioning failed — that is the cold-crew exit-127 path")
	}
}

func TestContainerStart_StartFailureSurfaces(t *testing.T) {
	cp := &startFakeProvider{ensureErr: errors.New("no such image")}
	h, wsID, crewID := startTestHandler(t, cp)

	rec := httptest.NewRecorder()
	h.ContainerStart(rec, startRequest(crewID, wsID, "OWNER"))

	if rec.Code == http.StatusOK {
		t.Errorf("a failed start reported success: %s", rec.Body.String())
	}
}

// EnsureCrewRuntime is get-or-create and normally restarts a stopped
// container — but not always. Two consecutive `crew stop` calls followed
// by a start reproducibly leave the crew `exited` while the handler was
// about to answer `"status": "running"`.
//
// That answer IS the endpoint's promise: a caller reads it and
// immediately writes files, which is a 409 against a stopped crew. A
// command reporting a state it never checked is precisely the defect
// `crew start` exists to fix in `crew provision`, so it must not be the
// defect `crew start` ships with.
func TestContainerStart_RefusesToClaimRunningWhenItIsNot(t *testing.T) {
	cp := &startFakeProvider{statusState: "stopped"}
	h, wsID, crewID := startTestHandler(t, cp)
	act := &startFakeActivity{}
	h.SetActivityNoter(act)

	rec := httptest.NewRecorder()
	h.ContainerStart(rec, startRequest(crewID, wsID, "OWNER"))

	if rec.Code == http.StatusOK {
		t.Fatalf("reported success for a container that is not running: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "did not come up") {
		t.Errorf("failure does not say what happened: %s", rec.Body.String())
	}
	// Nothing to reap, so nothing to report.
	if act.calls != 0 {
		t.Errorf("registered a container that never came up with the idle reaper")
	}
}

func TestContainerStart_AcceptsARunningContainer(t *testing.T) {
	cp := &startFakeProvider{statusState: "running"}
	h, wsID, crewID := startTestHandler(t, cp)

	rec := httptest.NewRecorder()
	h.ContainerStart(rec, startRequest(crewID, wsID, "OWNER"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

// containerRunning used to return true on ANY ContainerStatus error,
// including "no such container" — which is not "cannot tell", it is the
// most definitive possible no and the exact state the check exists to
// catch. Failing open there meant reporting `"status": "running"` for a
// container the runtime says does not exist.
func TestContainerStart_TreatsNoSuchContainerAsNotRunning(t *testing.T) {
	cp := &startFakeProvider{statusErr: errors.New("Error: No such container: crew-uctarna")}
	h, wsID, crewID := startTestHandler(t, cp)

	rec := httptest.NewRecorder()
	h.ContainerStart(rec, startRequest(crewID, wsID, "OWNER"))

	if rec.Code == http.StatusOK {
		t.Fatalf("claimed running for a container the runtime says is gone: %s", rec.Body.String())
	}
}

// Any OTHER status error is genuinely "cannot tell". One transient
// daemon hiccup must not fail a start that worked — providers whose
// status probe is weaker than Docker's would never be able to start a
// crew.
func TestContainerStart_TolerantOfAnUninformativeStatusProbe(t *testing.T) {
	cp := &startFakeProvider{statusErr: errors.New("connection reset by peer")}
	h, wsID, crewID := startTestHandler(t, cp)

	rec := httptest.NewRecorder()
	h.ContainerStart(rec, startRequest(crewID, wsID, "OWNER"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — an unreadable probe is not evidence the crew is down: %s",
			rec.Code, rec.Body.String())
	}
}
