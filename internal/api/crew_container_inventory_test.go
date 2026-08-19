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
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

// fakeCrewContainerLister implements provider.ContainerProvider (via the
// embedded mockContainerExec) plus provider.CrewContainerLister, so it
// satisfies the type assertion in Containers. It also overrides
// ContainerStats, which the embedded mock answers (nil, nil) to.
type fakeCrewContainerLister struct {
	*mockContainerExec
	containers []provider.CrewContainerInfo
	err        error

	stats    map[string]*provider.ContainerMetrics
	statsErr error

	lastCrewID  string
	lastSlug    string
	statsAsked  []string
	listerCalls int
}

func (f *fakeCrewContainerLister) ListCrewContainers(_ context.Context, crewID, slug string) ([]provider.CrewContainerInfo, error) {
	f.lastCrewID, f.lastSlug = crewID, slug
	f.listerCalls++
	if f.err != nil {
		return nil, f.err
	}
	return f.containers, nil
}

func (f *fakeCrewContainerLister) ContainerStats(_ context.Context, id string) (*provider.ContainerMetrics, error) {
	f.statsAsked = append(f.statsAsked, id)
	if f.statsErr != nil {
		return nil, f.statsErr
	}
	return f.stats[id], nil
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

	lister := newFakeCrewContainerLister([]provider.CrewContainerInfo{
		{ID: "team-cid", Name: "crewship-team-acct-crew-ctr", Image: "crewship/agent:latest", Kind: provider.CrewContainerKindCrew, State: "running"},
		{ID: "pg-cid", Name: "crewship-svc-acct-crew-ctr-postgres", Image: "postgres:16", Kind: provider.CrewContainerKindSidecar, State: "stopped"},
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
	if len(out.Containers) != 2 {
		t.Fatalf("expected 2 containers, got %d: %+v", len(out.Containers), out.Containers)
	}

	runtime := out.Containers[0]
	if runtime.Kind != provider.CrewContainerKindCrew {
		t.Fatalf("first row kind = %q, want %q", runtime.Kind, provider.CrewContainerKindCrew)
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
	for _, asked := range lister.statsAsked {
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
