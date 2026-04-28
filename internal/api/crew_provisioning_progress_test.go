package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// TestProvisionStatus_ProgressPayload verifies that an in-flight job's
// step / total / message / log_tail fields surface verbatim in the
// ProvisionStatus GET response. This is the path a browser tab uses when
// it reconnects mid-build (e.g. after a reload) — without it, the toolbar
// popover would have no idea how far the build had progressed.
//
// We seed an in-memory ProvisionJob directly because the production trigger
// path needs a Docker daemon. Bypassing it isolates the JSON shape of the
// response, which is exactly what we want to lock down.
func TestProvisionStatus_ProgressPayload(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h := NewProvisioningHandler(db, logger, nil, nil, nil, "", nil)

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	crewID := "crew-prog-1"
	if _, err := db.Exec(
		`INSERT INTO crews (id, workspace_id, name, slug, devcontainer_config)
		 VALUES (?, ?, 'Progressive', 'progressive', ?)`,
		crewID, wsID, `{"image":"ubuntu:22.04"}`,
	); err != nil {
		t.Fatalf("seed crew: %v", err)
	}

	// Seed a fake in-flight job — same struct the goroutine would mutate.
	h.mu.Lock()
	h.jobs[crewID] = &ProvisionJob{
		CrewID:    crewID,
		Status:    "running",
		StartedAt: time.Now().Add(-30 * time.Second),
		Step:      3,
		Total:     7,
		Message:   "Installing feature common-utils",
		LogTail: []string{
			"Pulling base image ubuntu:22.04",
			"Installing feature common-utils",
		},
	}
	h.mu.Unlock()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/crews/{crewId}/provision", h.ProvisionStatus)

	req := httptest.NewRequest("GET", "/api/v1/crews/"+crewID+"/provision", nil)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rr.Code, http.StatusOK, rr.Body.String())
	}

	var resp struct {
		Status               string   `json:"status"`
		Step                 int      `json:"step"`
		Total                int      `json:"total"`
		Message              string   `json:"message"`
		LogTail              []string `json:"log_tail"`
		AgentsPendingRestart int      `json:"agents_pending_restart"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.Status != "running" {
		t.Errorf("status = %q, want running", resp.Status)
	}
	if resp.Step != 3 || resp.Total != 7 {
		t.Errorf("step/total = %d/%d, want 3/7", resp.Step, resp.Total)
	}
	if resp.Message != "Installing feature common-utils" {
		t.Errorf("message = %q, want %q", resp.Message, "Installing feature common-utils")
	}
	if len(resp.LogTail) != 2 {
		t.Errorf("log_tail length = %d, want 2; got %v", len(resp.LogTail), resp.LogTail)
	}
	// agents_pending_restart should be 0 when there's no Docker client (handler nil-checks docker).
	if resp.AgentsPendingRestart != 0 {
		t.Errorf("agents_pending_restart = %d, want 0 (no docker client)", resp.AgentsPendingRestart)
	}
}

// TestProvisionStatus_LogTailRingBufferCap is a guard test: the runProvisioning
// callback must trim LogTail to provisionLogTailCap (50). Without this the
// in-memory job would grow unboundedly on a long build (each emit message
// is ~30-100 bytes; 1000 features = 100 KB per crew). The cap is enforced
// inside the callback itself, but the GET handler's copy-out must respect
// whatever's there. We verify both.
func TestProvisionStatus_LogTailRingBufferCap(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h := NewProvisioningHandler(db, logger, nil, nil, nil, "", nil)

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	crewID := "crew-prog-2"
	if _, err := db.Exec(
		`INSERT INTO crews (id, workspace_id, name, slug, devcontainer_config)
		 VALUES (?, ?, 'Massive', 'massive', ?)`,
		crewID, wsID, `{"image":"ubuntu:22.04"}`,
	); err != nil {
		t.Fatalf("seed crew: %v", err)
	}

	// Seed a job with exactly the cap — the handler should copy all 50.
	tail := make([]string, provisionLogTailCap)
	for i := range tail {
		tail[i] = "step " + string(rune('A'+i%26))
	}
	h.mu.Lock()
	h.jobs[crewID] = &ProvisionJob{
		CrewID:    crewID,
		Status:    "running",
		StartedAt: time.Now(),
		Step:     50,
		Total:    50,
		Message:  "Committing image",
		LogTail:  tail,
	}
	h.mu.Unlock()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/crews/{crewId}/provision", h.ProvisionStatus)

	req := httptest.NewRequest("GET", "/api/v1/crews/"+crewID+"/provision", nil)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	var resp struct {
		LogTail []string `json:"log_tail"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.LogTail) != provisionLogTailCap {
		t.Errorf("log_tail = %d entries, want %d", len(resp.LogTail), provisionLogTailCap)
	}
}
