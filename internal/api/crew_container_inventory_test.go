package api

// Tests for GET /api/v1/crews/{crewId}/containers — the live-runtime-read
// whole-crew container inventory that the bottom panel's Docker tab reads
// (#1697).

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

// fakeCrewContainerLister implements provider.ContainerProvider (via the
// embedded mockContainerExec) plus provider.CrewContainerLister, so it
// satisfies the type assertion in Containers. It also overrides
// ContainerStats, which the embedded mock answers (nil, nil) to.
// Every field below the mutex is touched from the handler's stats goroutines,
// so the fake needs its own synchronisation — the concurrency under test is
// real concurrency, and a fake that raced would report the race instead of the
// behaviour.
type fakeCrewContainerLister struct {
	*mockContainerExec
	containers []provider.CrewContainerInfo
	err        error

	stats    map[string]*provider.ContainerMetrics
	statsErr error
	// statsFunc, when set, replaces the canned answers entirely — used to
	// hold calls at a barrier and observe how many run at once.
	statsFunc func(context.Context, string) (*provider.ContainerMetrics, error)

	mu          sync.Mutex
	lastCrewID  string
	lastSlug    string
	statsAsked  []string
	listerCalls int
}

func (f *fakeCrewContainerLister) ListCrewContainers(_ context.Context, crewID, slug string) ([]provider.CrewContainerInfo, error) {
	f.mu.Lock()
	f.lastCrewID, f.lastSlug = crewID, slug
	f.listerCalls++
	f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	return f.containers, nil
}

func (f *fakeCrewContainerLister) ContainerStats(ctx context.Context, id string) (*provider.ContainerMetrics, error) {
	f.mu.Lock()
	f.statsAsked = append(f.statsAsked, id)
	metrics := f.stats[id]
	statsFunc, statsErr := f.statsFunc, f.statsErr
	f.mu.Unlock()

	if statsFunc != nil {
		return statsFunc(ctx, id)
	}
	if statsErr != nil {
		return nil, statsErr
	}
	return metrics, nil
}

// statsRequests returns the container ids stats were asked for, copied under
// the lock so a caller can read it while goroutines may still be appending.
func (f *fakeCrewContainerLister) statsRequests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.statsAsked...)
}

func newFakeCrewContainerLister(containers []provider.CrewContainerInfo, err error) *fakeCrewContainerLister {
	return &fakeCrewContainerLister{
		mockContainerExec: &mockContainerExec{},
		containers:        containers,
		err:               err,
		stats:             map[string]*provider.ContainerMetrics{},
	}
}

// containersBody is the response shape, decoded with pointers so the tests
// can tell "absent" from "zero" — the distinction the endpoint exists to keep.
type containersBody struct {
	Containers []struct {
		Name       string   `json:"name"`
		Image      string   `json:"image"`
		Kind       string   `json:"kind"`
		Status     string   `json:"status"`
		CPUPercent *float64 `json:"cpu_percent"`
		MemoryMB   *int     `json:"memory_mb"`
		AgentCount *int     `json:"agent_count"`
	} `json:"containers"`
}

// doContainers drives the handler for one crew and returns the recorder.
func doContainers(h *CrewHandler, wsID, crewID string) *httptest.ResponseRecorder {
	req := withWorkspaceCtx(httptest.NewRequest("GET", "/api/v1/crews/"+crewID+"/containers", nil), wsID)
	req.SetPathValue("crewId", crewID)
	w := httptest.NewRecorder()
	h.Containers(w, req)
	return w
}

func decodeContainers(t *testing.T, w *httptest.ResponseRecorder) containersBody {
	t.Helper()
	var out containersBody
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, w.Body.String())
	}
	return out
}

// TestCrewContainers_RuntimeAndSidecars is the load-bearing case: the tab's
// six columns — container, image, status, CPU, RAM, agents — all come back,
// with the crew's own runtime container present. Before #1697 this response
// did not exist and the tab read a field nothing ever sent.
func TestCrewContainers_RuntimeAndSidecars(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug, created_at, updated_at)
		VALUES (?, ?, 'Acct', 'acct', ?, ?)`, "crew-ctr", wsID, now, now); err != nil {
		t.Fatalf("seed crew: %v", err)
	}
	for _, a := range []string{"agent-1", "agent-2"} {
		if _, err := db.Exec(`INSERT INTO agents (id, workspace_id, crew_id, name, slug, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, a, wsID, "crew-ctr", a, a, now, now); err != nil {
			t.Fatalf("seed agent: %v", err)
		}
	}

	// Deliberately runtime-LAST, the order docker's newest-first listing can
	// produce: the response must still put the crew's own container first.
	lister := newFakeCrewContainerLister([]provider.CrewContainerInfo{
		{ID: "redis-cid", Name: "crewship-svc-acct-crew-ctr-redis", Image: "redis:7", Kind: provider.CrewContainerKindSidecar, State: "stopped"},
		{ID: "pg-cid", Name: "crewship-svc-acct-crew-ctr-postgres", Image: "postgres:16", Kind: provider.CrewContainerKindSidecar, State: "stopped"},
		{ID: "team-cid", Name: "crewship-team-acct-crew-ctr", Image: "crewship/agent:latest", Kind: provider.CrewContainerKindCrew, State: "running"},
	}, nil)
	lister.stats["team-cid"] = &provider.ContainerMetrics{CPUPercent: 3.4666, MemoryUsed: 412 * 1024 * 1024}

	h := NewCrewHandler(db, newTestLogger())
	h.SetContainer(lister)

	w := doContainers(h, wsID, "crew-ctr")
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d body=%s", w.Code, w.Body.String())
	}
	if lister.lastCrewID != "crew-ctr" || lister.lastSlug != "acct" {
		t.Errorf("lister called with (%q, %q), want (crew-ctr, acct)", lister.lastCrewID, lister.lastSlug)
	}

	out := decodeContainers(t, w)
	if len(out.Containers) != 3 {
		t.Fatalf("expected 3 containers, got %d: %+v", len(out.Containers), out.Containers)
	}

	runtime := out.Containers[0]
	if runtime.Kind != provider.CrewContainerKindCrew {
		t.Fatalf("first row kind = %q, want %q — the crew's own container leads, "+
			"whatever order the daemon listed in", runtime.Kind, provider.CrewContainerKindCrew)
	}
	// Sidecars follow, by name, so the table does not reshuffle on every poll.
	if out.Containers[1].Name != "crewship-svc-acct-crew-ctr-postgres" ||
		out.Containers[2].Name != "crewship-svc-acct-crew-ctr-redis" {
		t.Errorf("sidecars are not name-ordered: %q then %q",
			out.Containers[1].Name, out.Containers[2].Name)
	}
	if runtime.Name != "crewship-team-acct-crew-ctr" || runtime.Image != "crewship/agent:latest" {
		t.Errorf("runtime row = %+v", runtime)
	}
	if runtime.Status != "running" {
		t.Errorf("runtime status = %q, want running", runtime.Status)
	}
	if runtime.CPUPercent == nil || *runtime.CPUPercent != 3.5 {
		t.Errorf("cpu_percent = %v, want 3.5 (rounded once, server-side)", runtime.CPUPercent)
	}
	if runtime.MemoryMB == nil || *runtime.MemoryMB != 412 {
		t.Errorf("memory_mb = %v, want 412", runtime.MemoryMB)
	}
	if runtime.AgentCount == nil || *runtime.AgentCount != 2 {
		t.Errorf("agent_count = %v, want 2", runtime.AgentCount)
	}

	sidecar := out.Containers[1]
	if sidecar.Kind != provider.CrewContainerKindSidecar {
		t.Errorf("second row kind = %q", sidecar.Kind)
	}
	if sidecar.Status != "stopped" {
		t.Errorf("sidecar status = %q, want stopped (live, not a configured snapshot)", sidecar.Status)
	}
	// A sidecar runs no agents, and a stopped container has no usage. All
	// three must be null rather than 0 — "—" in the UI, not "0%".
	if sidecar.AgentCount != nil {
		t.Errorf("sidecar agent_count = %v, want null", *sidecar.AgentCount)
	}
	if sidecar.CPUPercent != nil || sidecar.MemoryMB != nil {
		t.Errorf("stopped sidecar reported usage: cpu=%v mem=%v", sidecar.CPUPercent, sidecar.MemoryMB)
	}
	for _, asked := range lister.statsRequests() {
		if asked == "pg-cid" {
			t.Errorf("stats were requested for a stopped container")
		}
	}
}

// TestCrewContainers_StatsFailureIsSoft: a stats call that fails must leave
// that row's numbers null, not fail the whole listing. Knowing the container
// exists and is running is the answer the caller came for.
func TestCrewContainers_StatsFailureIsSoft(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug, created_at, updated_at)
		VALUES (?, ?, 'Acct', 'acct', ?, ?)`, "crew-nostats", wsID, now, now); err != nil {
		t.Fatalf("seed crew: %v", err)
	}

	lister := newFakeCrewContainerLister([]provider.CrewContainerInfo{
		{ID: "team-cid", Name: "team", Image: "img", Kind: provider.CrewContainerKindCrew, State: "running"},
	}, nil)
	lister.statsErr = errors.New("stats unsupported on this runtime")

	h := NewCrewHandler(db, newTestLogger())
	h.SetContainer(lister)

	w := doContainers(h, wsID, "crew-nostats")
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d body=%s", w.Code, w.Body.String())
	}
	out := decodeContainers(t, w)
	if len(out.Containers) != 1 {
		t.Fatalf("expected the container to still be listed, got %+v", out.Containers)
	}
	if out.Containers[0].CPUPercent != nil || out.Containers[0].MemoryMB != nil {
		t.Errorf("unreadable stats must serialise as null, got cpu=%v mem=%v",
			out.Containers[0].CPUPercent, out.Containers[0].MemoryMB)
	}
	if out.Containers[0].Status != "running" {
		t.Errorf("status = %q, want running", out.Containers[0].Status)
	}
}

// TestCrewContainers_StatsAreConcurrentAndJoinedBeforeReturn holds up the two
// claims the handler's spawn site makes, because one of them is why that site
// is allowed to skip beginBackgroundWork.
//
// CONCURRENT, because the reason for spawning at all is that docker's stats
// call collects two samples a second apart: serial reads cost one second per
// running container. Asserted with a barrier rather than a stopwatch — each
// stats call waits for its peers to arrive, so genuinely concurrent reads
// release each other immediately, and a serialized fan-out deadlocks its way
// to a clean, named failure instead of a timing flake on a loaded box.
//
// JOINED BEFORE RETURN, because that is the entry in unregisteredSpawnSites
// (background_guard_test.go): these goroutines are request-scoped, so they
// cannot outlive the test and race its teardown (#1596). The guard is
// syntactic — it checks that the site is accounted for, not that the reason
// given is true. This is what makes the reason true rather than asserted.
// Delete the wg.Wait() and the guard still passes; this fails.
func TestCrewContainers_StatsAreConcurrentAndJoinedBeforeReturn(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug, created_at, updated_at)
		VALUES (?, ?, 'Acct', 'acct', ?, ?)`, "crew-conc", wsID, now, now); err != nil {
		t.Fatalf("seed crew: %v", err)
	}

	const running = 3
	lister := newFakeCrewContainerLister([]provider.CrewContainerInfo{
		{ID: "team-cid", Name: "team", Image: "img", Kind: provider.CrewContainerKindCrew, State: "running"},
		{ID: "pg-cid", Name: "svc-postgres", Image: "postgres:16", Kind: provider.CrewContainerKindSidecar, State: "running"},
		{ID: "redis-cid", Name: "svc-redis", Image: "redis:7", Kind: provider.CrewContainerKindSidecar, State: "running"},
		// Stopped: never asked, so it must not be counted at the barrier.
		{ID: "old-cid", Name: "svc-old", Image: "mysql:8", Kind: provider.CrewContainerKindSidecar, State: "stopped"},
	}, nil)

	// arrived closes once every running container's stats call is in flight
	// at the same moment. A serial pass can never close it.
	var mu sync.Mutex
	inFlight := 0
	arrived := make(chan struct{})
	var finished atomic.Int32
	var timedOut atomic.Int32

	lister.statsFunc = func(ctx context.Context, id string) (*provider.ContainerMetrics, error) {
		mu.Lock()
		inFlight++
		if inFlight == running {
			close(arrived)
		}
		mu.Unlock()

		select {
		case <-arrived:
		case <-time.After(5 * time.Second):
			// Serialized: this call is waiting for peers that will not start
			// until it returns.
			timedOut.Add(1)
		}
		finished.Add(1)
		return &provider.ContainerMetrics{CPUPercent: 1.5, MemoryUsed: 100 * 1024 * 1024}, nil
	}

	h := NewCrewHandler(db, newTestLogger())
	h.SetContainer(lister)

	w := doContainers(h, wsID, "crew-conc")

	// Read WITHOUT synchronising against the goroutines: if the handler
	// returned while any of them were still running, -race reports it here,
	// and the counts below are short.
	if got := timedOut.Load(); got != 0 {
		t.Fatalf("%d stats read(s) waited 5s for peers that never arrived — the fan-out is "+
			"serialized, which is the ~1s-per-container draw time the goroutines exist to avoid", got)
	}
	if got := finished.Load(); got != running {
		t.Fatalf("Containers returned with %d of %d stats reads finished — the goroutines outlived "+
			"the request, which is exactly the detached work unregisteredSpawnSites says this is not",
			got, running)
	}
	if got := len(lister.statsRequests()); got != running {
		t.Errorf("stats asked for %d containers, want %d (the stopped one must not be asked)", got, running)
	}

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d body=%s", w.Code, w.Body.String())
	}
	out := decodeContainers(t, w)
	if len(out.Containers) != 4 {
		t.Fatalf("expected 4 rows, got %d", len(out.Containers))
	}
	// Every running row carries the numbers its goroutine produced — the join
	// is what makes them present, not a lucky schedule.
	for _, c := range out.Containers {
		if c.Status != "running" {
			continue
		}
		if c.CPUPercent == nil || c.MemoryMB == nil {
			t.Errorf("row %q has no usage: the response was written before its read finished", c.Name)
		}
	}
}

// TestCrewContainers_SoftAndHardFailures is the table of everything that is
// not a happy path: the endpoint must be soft where a capability is missing
// and hard where a crew is not the caller's.
func TestCrewContainers_SoftAndHardFailures(t *testing.T) {
	tests := []struct {
		name string
		// setup returns the crew id to request and the handler's provider.
		crewID     string
		seedCrew   bool
		deleted    bool
		foreignWS  bool
		provider   func() provider.ContainerProvider
		wantStatus int
		wantEmpty  bool
		// wantDaemonUntouched asserts the provider was never asked — no
		// foreign or deleted crew's identity may reach the shared daemon.
		wantDaemonUntouched bool
	}{
		{
			name:       "unknown crew",
			crewID:     "ghost",
			wantStatus: http.StatusNotFound,
		},
		{
			name:                "another workspace's crew (IDOR)",
			crewID:              "crew-foreign",
			foreignWS:           true,
			provider:            func() provider.ContainerProvider { return newFakeCrewContainerLister(nil, nil) },
			wantStatus:          http.StatusNotFound,
			wantDaemonUntouched: true,
		},
		{
			name:                "soft-deleted crew",
			crewID:              "crew-del",
			seedCrew:            true,
			deleted:             true,
			provider:            func() provider.ContainerProvider { return newFakeCrewContainerLister(nil, nil) },
			wantStatus:          http.StatusNotFound,
			wantDaemonUntouched: true,
		},
		{
			name:       "no container provider (--no-docker)",
			crewID:     "crew-ok",
			seedCrew:   true,
			wantStatus: http.StatusOK,
			wantEmpty:  true,
		},
		{
			name:       "provider without the listing capability (apple-container)",
			crewID:     "crew-ok",
			seedCrew:   true,
			provider:   func() provider.ContainerProvider { return &mockContainerExec{} },
			wantStatus: http.StatusOK,
			wantEmpty:  true,
		},
		{
			name:     "daemon listing failure",
			crewID:   "crew-ok",
			seedCrew: true,
			provider: func() provider.ContainerProvider {
				return newFakeCrewContainerLister(nil, context.DeadlineExceeded)
			},
			wantStatus: http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := setupTestDB(t)
			defer db.Close()
			userID := seedTestUser(t, db)
			wsID := seedTestWorkspace(t, db, userID)
			now := time.Now().UTC().Format(time.RFC3339)

			if tt.seedCrew {
				deletedAt := any(nil)
				if tt.deleted {
					deletedAt = now
				}
				if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug, created_at, updated_at, deleted_at)
					VALUES (?, ?, 'Acct', 'acct', ?, ?, ?)`, tt.crewID, wsID, now, now, deletedAt); err != nil {
					t.Fatalf("seed crew: %v", err)
				}
			}
			if tt.foreignWS {
				if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws-other', 'Other', 'other')`); err != nil {
					t.Fatalf("seed foreign workspace: %v", err)
				}
				if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug, created_at, updated_at)
					VALUES (?, 'ws-other', 'Secret', 'secret', ?, ?)`, tt.crewID, now, now); err != nil {
					t.Fatalf("seed foreign crew: %v", err)
				}
			}

			h := NewCrewHandler(db, newTestLogger())
			var lister *fakeCrewContainerLister
			if tt.provider != nil {
				cp := tt.provider()
				h.SetContainer(cp)
				lister, _ = cp.(*fakeCrewContainerLister)
			}

			w := doContainers(h, wsID, tt.crewID)
			if w.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body=%s)", w.Code, tt.wantStatus, w.Body.String())
			}
			if tt.wantEmpty {
				// The envelope must be present even when empty — the client
				// treats a missing `containers` field as a shape error, which
				// is the whole point of #1697.
				body := w.Body.String()
				if !strings.Contains(body, `"containers"`) {
					t.Errorf("response omitted the containers envelope: %s", body)
				}
				out := decodeContainers(t, w)
				if len(out.Containers) != 0 {
					t.Errorf("expected an empty containers array, got %+v", out.Containers)
				}
			}
			if tt.wantDaemonUntouched && lister != nil && lister.listerCalls != 0 {
				t.Errorf("container provider was queried for a crew the caller may not see (crew_id=%q)", lister.lastCrewID)
			}
		})
	}
}
