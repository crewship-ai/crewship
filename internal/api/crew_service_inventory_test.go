package api

// Tests for GET /api/v1/crews/{crewId}/services — the live-Docker-read
// service inventory (as opposed to the crews.services_json DB snapshot
// covered by crew_services_test.go's validateServicesJSON tests).

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

// fakeServiceLister implements provider.ContainerProvider (via the embedded
// mockContainerExec) plus provider.ServiceLister, so it satisfies the
// CrewHandler.container field's type assertion in Services.
type fakeServiceLister struct {
	*mockContainerExec
	services []provider.CrewServiceStatus
	err      error
	lastSlug string
}

func (f *fakeServiceLister) ListCrewServices(_ context.Context, slug string) ([]provider.CrewServiceStatus, error) {
	f.lastSlug = slug
	if f.err != nil {
		return nil, f.err
	}
	return f.services, nil
}

func newFakeServiceLister(services []provider.CrewServiceStatus, err error) *fakeServiceLister {
	return &fakeServiceLister{mockContainerExec: &mockContainerExec{}, services: services, err: err}
}

func TestCrewServicesInventory_LiveInventory_MapsTypeAndPorts(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug, created_at, updated_at)
		VALUES (?, ?, 'Acct', 'acct', ?, ?)`, "crew-svc", wsID, now, now); err != nil {
		t.Fatalf("seed crew: %v", err)
	}

	lister := newFakeServiceLister([]provider.CrewServiceStatus{
		{Name: "postgres", Image: "postgres:16", State: "running", Ports: []string{"5432/tcp"}},
		{Name: "redis", Image: "redis:7-alpine", State: "stopped", Ports: []string{"6379/tcp"}},
	}, nil)

	h := NewCrewHandler(db, newTestLogger())
	h.SetContainer(lister)

	req := withWorkspaceCtx(httptest.NewRequest("GET", "/api/v1/crews/crew-svc/services", nil), wsID)
	req.SetPathValue("crewId", "crew-svc")
	w := httptest.NewRecorder()
	h.Services(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d body=%s", w.Code, w.Body.String())
	}
	if lister.lastSlug != "acct" {
		t.Errorf("ListCrewServices called with slug %q, want acct", lister.lastSlug)
	}

	var out struct {
		Services []struct {
			Name   string   `json:"name"`
			Image  string   `json:"image"`
			Type   string   `json:"type"`
			Status string   `json:"status"`
			Ports  []string `json:"ports"`
		} `json:"services"`
	}
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Services) != 2 {
		t.Fatalf("expected 2 services, got %d: %+v", len(out.Services), out.Services)
	}

	byName := map[string]int{}
	for i, s := range out.Services {
		byName[s.Name] = i
	}

	pg := out.Services[byName["postgres"]]
	if pg.Type != "postgres" {
		t.Errorf("postgres type = %q, want postgres (inferDatastoreType)", pg.Type)
	}
	if pg.Status != "running" {
		t.Errorf("postgres status = %q, want running", pg.Status)
	}
	if len(pg.Ports) != 1 || pg.Ports[0] != "5432/tcp" {
		t.Errorf("postgres ports = %v", pg.Ports)
	}

	redis := out.Services[byName["redis"]]
	if redis.Type != "redis" {
		t.Errorf("redis type = %q, want redis", redis.Type)
	}
	// The load-bearing assertion: a stopped sidecar reports LIVE status,
	// not a stale "configured" snapshot from services_json.
	if redis.Status != "stopped" {
		t.Errorf("redis status = %q, want stopped (live, not stale)", redis.Status)
	}
}

func TestCrewServicesInventory_NotFound(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	h := NewCrewHandler(db, newTestLogger())
	req := withWorkspaceCtx(httptest.NewRequest("GET", "/x", nil), wsID)
	req.SetPathValue("crewId", "ghost")
	w := httptest.NewRecorder()
	h.Services(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for unknown crew, got %d", w.Code)
	}
}

// TestCrewServicesInventory_CrossWorkspace_404 is the IDOR guard: a crew
// that exists but belongs to ANOTHER workspace must answer 404 — never
// that workspace's services — and the container provider must never be
// asked for the foreign crew's slug (so no foreign slug ever reaches the
// shared docker daemon). This pins the `AND workspace_id = ?` scoping in
// the crew-resolve query.
func TestCrewServicesInventory_CrossWorkspace_404(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID) // the caller's workspace

	// A SECOND, foreign workspace with its own crew.
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws-other', 'Other', 'other')`); err != nil {
		t.Fatalf("insert other workspace: %v", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug, created_at, updated_at)
		VALUES (?, 'ws-other', 'Secret', 'secret', ?, ?)`, "crew-foreign", now, now); err != nil {
		t.Fatalf("seed foreign crew: %v", err)
	}

	lister := newFakeServiceLister([]provider.CrewServiceStatus{
		{Name: "postgres", Image: "postgres:16", State: "running"},
	}, nil)
	h := NewCrewHandler(db, newTestLogger())
	h.SetContainer(lister)

	// Caller is scoped to wsID but asks for the foreign crew's id.
	req := withWorkspaceCtx(httptest.NewRequest("GET", "/x", nil), wsID)
	req.SetPathValue("crewId", "crew-foreign")
	w := httptest.NewRecorder()
	h.Services(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("cross-workspace IDOR: expected 404, got %d body=%s", w.Code, w.Body.String())
	}
	if lister.lastSlug != "" {
		t.Errorf("container provider was queried with foreign crew slug %q — must never reach the daemon", lister.lastSlug)
	}
}

// TestCrewServicesInventory_DeletedCrew_404 pins the `deleted_at IS NULL`
// clause: a soft-deleted crew must 404, not surface its (possibly still
// lingering) sidecars.
func TestCrewServicesInventory_DeletedCrew_404(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug, created_at, updated_at, deleted_at)
		VALUES (?, ?, 'Gone', 'gone', ?, ?, ?)`, "crew-del", wsID, now, now, now); err != nil {
		t.Fatalf("seed deleted crew: %v", err)
	}

	lister := newFakeServiceLister([]provider.CrewServiceStatus{
		{Name: "redis", Image: "redis:7", State: "running"},
	}, nil)
	h := NewCrewHandler(db, newTestLogger())
	h.SetContainer(lister)

	req := withWorkspaceCtx(httptest.NewRequest("GET", "/x", nil), wsID)
	req.SetPathValue("crewId", "crew-del")
	w := httptest.NewRecorder()
	h.Services(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("deleted crew: expected 404, got %d body=%s", w.Code, w.Body.String())
	}
	if lister.lastSlug != "" {
		t.Errorf("container provider queried for a deleted crew (slug %q)", lister.lastSlug)
	}
}

// TestCrewServicesInventory_NoContainerProvider_EmptyList covers
// --no-docker / unwired-provider builds: the endpoint answers 200 with
// an empty list rather than erroring.
func TestCrewServicesInventory_NoContainerProvider_EmptyList(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug, created_at, updated_at)
		VALUES (?, ?, 'Acct', 'acct', ?, ?)`, "crew-nodock", wsID, now, now); err != nil {
		t.Fatalf("seed crew: %v", err)
	}

	h := NewCrewHandler(db, newTestLogger()) // no SetContainer
	req := withWorkspaceCtx(httptest.NewRequest("GET", "/x", nil), wsID)
	req.SetPathValue("crewId", "crew-nodock")
	w := httptest.NewRecorder()
	h.Services(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d body=%s", w.Code, w.Body.String())
	}
	var out struct {
		Services []any `json:"services"`
	}
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Services) != 0 {
		t.Errorf("expected empty services, got %+v", out.Services)
	}
}

// TestCrewServicesInventory_ProviderNotServiceLister_EmptyList covers a
// container provider that doesn't implement ServiceLister (apple-container
// today).
func TestCrewServicesInventory_ProviderNotServiceLister_EmptyList(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug, created_at, updated_at)
		VALUES (?, ?, 'Acct', 'acct', ?, ?)`, "crew-plain", wsID, now, now); err != nil {
		t.Fatalf("seed crew: %v", err)
	}

	h := NewCrewHandler(db, newTestLogger())
	h.SetContainer(&mockContainerExec{}) // does NOT implement ServiceLister
	req := withWorkspaceCtx(httptest.NewRequest("GET", "/x", nil), wsID)
	req.SetPathValue("crewId", "crew-plain")
	w := httptest.NewRecorder()
	h.Services(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d body=%s", w.Code, w.Body.String())
	}
	var out struct {
		Services []any `json:"services"`
	}
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Services) != 0 {
		t.Errorf("expected empty services, got %+v", out.Services)
	}
}

func TestCrewServicesInventory_ListerError_500(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug, created_at, updated_at)
		VALUES (?, ?, 'Acct', 'acct', ?, ?)`, "crew-err", wsID, now, now); err != nil {
		t.Fatalf("seed crew: %v", err)
	}

	h := NewCrewHandler(db, newTestLogger())
	h.SetContainer(newFakeServiceLister(nil, context.DeadlineExceeded))
	req := withWorkspaceCtx(httptest.NewRequest("GET", "/x", nil), wsID)
	req.SetPathValue("crewId", "crew-err")
	w := httptest.NewRecorder()
	h.Services(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d body=%s", w.Code, w.Body.String())
	}
}
