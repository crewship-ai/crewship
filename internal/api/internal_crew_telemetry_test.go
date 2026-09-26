package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

func TestWorkspaceCrewTelemetry_RealMetricsAndWorkspaceFence(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	now := time.Now().UTC().Format(time.RFC3339)
	for _, crew := range []struct{ id, workspace, name, slug string }{
		{"crew-lab", wsID, "Ops", "ops"},
		{"crew-foreign", "ws-foreign", "Foreign", "foreign"},
	} {
		if crew.workspace == "ws-foreign" {
			if _, err := db.Exec(`INSERT INTO workspaces(id,name,slug) VALUES ('ws-foreign','Foreign','foreign')`); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := db.Exec(`INSERT INTO crews(id,workspace_id,name,slug,created_at,updated_at) VALUES (?,?,?,?,?,?)`, crew.id, crew.workspace, crew.name, crew.slug, now, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO crews(id,workspace_id,name,slug,kind,created_at,updated_at) VALUES ('crew-setup',?,'Setup','_crewship-setup','setup',?,?)`, wsID, now, now); err != nil {
		t.Fatal(err)
	}
	lister := newFakeCrewContainerLister([]provider.CrewContainerInfo{{ID: "container-1", Name: "crew-ops", Kind: provider.CrewContainerKindCrew, State: "running"}}, nil)
	lister.stats["container-1"] = &provider.ContainerMetrics{CPUPercent: 13.26, MemoryUsed: 96 * bytesPerMiB}
	h := NewInternalHandler(db, "test", newTestLogger())
	h.SetContainer(lister)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/crews/telemetry?workspace_id="+wsID, nil)
	rec := httptest.NewRecorder()
	h.WorkspaceCrewTelemetry(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Crews []struct {
			Name       string `json:"name"`
			Available  bool   `json:"available"`
			Containers []struct {
				CPU    *float64 `json:"cpu_percent"`
				Memory *int     `json:"memory_mb"`
			} `json:"containers"`
		} `json:"crews"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Crews) != 1 || got.Crews[0].Name != "Ops" || !got.Crews[0].Available || len(got.Crews[0].Containers) != 1 {
		t.Fatalf("wrong workspace inventory: %+v", got)
	}
	if c := got.Crews[0].Containers[0]; c.CPU == nil || *c.CPU != 13.3 || c.Memory == nil || *c.Memory != 96 {
		t.Fatalf("wrong metrics: %+v", c)
	}
	if lister.lastCrewID != "crew-lab" {
		t.Errorf("queried foreign crew: %s", lister.lastCrewID)
	}
}

// TestWorkspaceCrewTelemetry_StatsAreConcurrentAndJoinedBeforeReturn holds up
// the two claims the handler's spawn-site entry in unregisteredSpawnSites
// makes, the same way TestCrewContainers_StatsAreConcurrentAndJoinedBeforeReturn
// does for the per-crew endpoint.
//
// CONCURRENT, because docker's stats call collects two samples a second
// apart: a serial fleet pass costs one second per running container, which
// is the snapshot-deadline exhaustion the bounded pool exists to avoid.
// Asserted with a barrier rather than a stopwatch — a serialized fan-out
// deadlocks its way to a clean, named failure instead of a timing flake.
//
// JOINED BEFORE RETURN, because request-scoped goroutines cannot outlive
// the test and race its teardown (#1596). The syntactic guard in
// background_guard_test.go checks the site is accounted for, not that the
// reason is true; this is what makes it true.
func TestWorkspaceCrewTelemetry_StatsAreConcurrentAndJoinedBeforeReturn(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.Exec(`INSERT INTO crews(id,workspace_id,name,slug,created_at,updated_at) VALUES ('crew-fleet',?,'Fleet','fleet',?,?)`, wsID, now, now); err != nil {
		t.Fatal(err)
	}

	const running = 3
	lister := newFakeCrewContainerLister([]provider.CrewContainerInfo{
		{ID: "fleet-cid", Name: "crew-fleet", Kind: provider.CrewContainerKindCrew, State: "running"},
		{ID: "pg-cid", Name: "svc-postgres", Kind: provider.CrewContainerKindSidecar, State: "running"},
		{ID: "redis-cid", Name: "svc-redis", Kind: provider.CrewContainerKindSidecar, State: "running"},
		// Stopped: never asked, so it must not be counted at the barrier.
		{ID: "old-cid", Name: "svc-old", Kind: provider.CrewContainerKindSidecar, State: "stopped"},
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

	h := NewInternalHandler(db, "test", newTestLogger())
	h.SetContainer(lister)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/crews/telemetry?workspace_id="+wsID, nil)
	rec := httptest.NewRecorder()
	h.WorkspaceCrewTelemetry(rec, req)

	// Read WITHOUT synchronising against the goroutines: if the handler
	// returned while any of them were still running, -race reports it here,
	// and the counts below are short.
	if got := timedOut.Load(); got != 0 {
		t.Fatalf("%d stats read(s) waited 5s for peers that never arrived — the fleet fan-out is "+
			"serialized, which is the ~1s-per-container cost the pool exists to avoid", got)
	}
	if got := finished.Load(); got != running {
		t.Fatalf("WorkspaceCrewTelemetry returned with %d of %d stats reads finished — the goroutines "+
			"outlived the request, which is exactly the detached work the spawn-site entry says this is not",
			got, running)
	}
	if got := len(lister.statsRequests()); got != running {
		t.Errorf("stats asked for %d containers, want %d (the stopped one must not be asked)", got, running)
	}

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Crews []struct {
			Name       string `json:"name"`
			Available  bool   `json:"available"`
			Containers []struct {
				Name       string   `json:"name"`
				Status     string   `json:"status"`
				CPUPercent *float64 `json:"cpu_percent"`
				MemoryMB   *int     `json:"memory_mb"`
			} `json:"containers"`
		} `json:"crews"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Crews) != 1 || !got.Crews[0].Available || len(got.Crews[0].Containers) != 4 {
		t.Fatalf("wrong fleet inventory: %+v", got)
	}
	// Every running row carries the numbers its goroutine produced — the
	// join is what makes them present, not a lucky schedule.
	for _, c := range got.Crews[0].Containers {
		if c.Status != "running" {
			continue
		}
		if c.CPUPercent == nil || c.MemoryMB == nil {
			t.Errorf("row %q has no usage: the response was written before its read finished", c.Name)
		}
	}
}
