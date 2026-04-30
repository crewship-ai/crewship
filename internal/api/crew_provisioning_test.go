package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/crewship-ai/crewship/internal/devcontainer"
)

func newTestProvisioningHandler(t *testing.T) *ProvisioningHandler {
	t.Helper()
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	return NewProvisioningHandler(db, logger, nil, nil, nil, "", nil)
}

func TestCatalogList(t *testing.T) {
	h := newTestProvisioningHandler(t)

	req := httptest.NewRequest("GET", "/api/v1/features/catalog", nil)
	rr := httptest.NewRecorder()

	h.CatalogList(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var resp struct {
		Features []json.RawMessage `json:"features"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Features) == 0 {
		t.Error("expected non-empty feature catalog")
	}
}

func TestCatalogListSearch(t *testing.T) {
	h := newTestProvisioningHandler(t)

	// Search for "python" — should return results.
	req := httptest.NewRequest("GET", "/api/v1/features/catalog?search=python", nil)
	rr := httptest.NewRecorder()

	h.CatalogList(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var resp struct {
		Features []struct {
			Name string `json:"name"`
		} `json:"features"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Features) == 0 {
		t.Error("expected at least one result for 'python' search")
	}

	// Search for nonsense — should return empty list.
	req2 := httptest.NewRequest("GET", "/api/v1/features/catalog?search=zzz_nonexistent_zzz", nil)
	rr2 := httptest.NewRecorder()

	h.CatalogList(rr2, req2)

	if rr2.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr2.Code, http.StatusOK)
	}

	var resp2 struct {
		Features []json.RawMessage `json:"features"`
	}
	if err := json.NewDecoder(rr2.Body).Decode(&resp2); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp2.Features) != 0 {
		t.Errorf("expected empty results for nonsense query, got %d", len(resp2.Features))
	}
}

func TestProvisionTrigger_NoDockerClient(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	// docker client == nil -> provisioner is nil -> trigger returns 503.
	h := NewProvisioningHandler(db, logger, nil, nil, nil, "", nil)

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	crewID := "crew-noprov"
	if _, err := db.Exec(
		`INSERT INTO crews (id, workspace_id, name, slug, devcontainer_config)
		 VALUES (?, ?, 'Devs', 'devs', ?)`,
		crewID, wsID, `{"image":"ubuntu:22.04"}`,
	); err != nil {
		t.Fatalf("seed crew: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/crews/{crewId}/provision", h.ProvisionTrigger)

	req := httptest.NewRequest("POST", "/api/v1/crews/"+crewID+"/provision", nil)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d; body: %s", rr.Code, http.StatusServiceUnavailable, rr.Body.String())
	}
	var resp struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Error == "" {
		t.Error("expected non-empty error message in 503 response")
	}
}

// TestEnqueueForCrew_RateLimitDoesNotPublishGhostJob verifies the
// TOCTOU window between "publish pending job" and "acquire rate-limit
// slot" is closed. Previously a caller rejected by the limiter would
// leave a transient pending row visible to a concurrent caller, who
// would falsely report AlreadyRunning for a build that never started.
// After the fix, the rejected caller never published a row and a
// follow-up call sees a clean slate.
func TestEnqueueForCrew_RateLimitDoesNotPublishGhostJob(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	// We need a non-nil provisioner; pass a synthetic one and don't
	// actually run any builds — the test exits before runProvisioning
	// would touch Docker because tryAcquire pre-empts it.
	h := newProvisioningHandlerForRateLimitTest(t, db, logger)

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	crewID := "crew-ghost"
	if _, err := db.Exec(
		`INSERT INTO crews (id, workspace_id, name, slug, devcontainer_config)
		 VALUES (?, ?, 'Devs', 'devs', ?)`,
		crewID, wsID, `{"image":"ubuntu:22.04","features":{"ghcr.io/devcontainers/features/go:1":{}}}`,
	); err != nil {
		t.Fatalf("seed crew: %v", err)
	}

	// Saturate the "starts in last minute" budget. Acquire and immediately
	// release so the running-count budget (maxConcurrentProvisionsPerWorkspace,
	// hit first when all slots stay live) doesn't trigger before we've
	// recorded enough recent-start timestamps to fail the per-minute check.
	for i := 0; i < maxProvisionStartsPerMinute; i++ {
		if err := h.rateLimiter.tryAcquire(wsID); err != nil {
			t.Fatalf("seed rate-limiter slot %d: %v", i, err)
		}
		h.rateLimiter.release(wsID)
	}

	// First call must hit the limiter and bail. Crucially it must NOT
	// leave a "pending" row in h.jobs.
	_, err := h.EnqueueForCrew(context.Background(), crewID, wsID)
	if err == nil {
		t.Fatal("expected ErrRateLimited, got nil")
	}
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("expected ErrRateLimited, got %v", err)
	}

	h.mu.Lock()
	job, ok := h.jobs[crewID]
	h.mu.Unlock()
	if ok {
		t.Errorf("rejected caller leaked a pending job into h.jobs: %+v", job)
	}
}

// newProvisioningHandlerForRateLimitTest builds a handler with a
// non-nil provisioner so EnqueueForCrew advances past the
// ErrProvisionerUnavailable guard. The test bails before runProvisioning
// would actually use it, so the provisioner's deps can be nil.
func newProvisioningHandlerForRateLimitTest(t *testing.T, db *sql.DB, logger *slog.Logger) *ProvisioningHandler {
	t.Helper()
	h := NewProvisioningHandler(db, logger, nil, nil, nil, "", nil)
	h.provisioner = devcontainer.NewProvisioner(nil, nil, nil, logger)
	return h
}

func TestProvisionStatus_NoCrew(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h := NewProvisioningHandler(db, logger, nil, nil, nil, "", nil)

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/crews/{crewId}/provisioning", h.ProvisionStatus)

	req := httptest.NewRequest("GET", "/api/v1/crews/nonexistent-crew-id/provisioning", nil)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()

	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d; body: %s", rr.Code, http.StatusNotFound, rr.Body.String())
	}

	var resp struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Error == "" {
		t.Error("expected non-empty error message in 404 response")
	}
}
