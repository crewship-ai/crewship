package api

// Tests for the /api/v1/consolidate/run endpoint.
//
// Coverage focus:
//   - handler returns 503 when no consolidator is wired
//   - happy-path (summarizer configured) returns 202 + worker_id
//   - summarizer unconfigured → 202 + "no summarizer configured, skipping"
//   - cross-workspace crew_id → 404 (no existence leak)
//   - non-OWNER/ADMIN → 403

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/crewship-ai/crewship/internal/consolidate"
)

// stubSummarizer is a canned-response SummarizerClient used in the
// happy-path test. The handler kicks a goroutine against it — we don't
// block on the goroutine in the test (the 202 is the contract) so the
// stub just returns "[]" quickly when it does get called.
type stubSummarizer struct{}

func (s *stubSummarizer) Summarize(_ context.Context, _ string) (string, error) {
	return "[]", nil
}

func TestConsolidateRun_ReturnsServiceUnavailableWhenUnconfigured(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	// No consolidator set — should come back 503.
	h := NewConsolidateHandler(db, newTestLogger())

	body := bytes.NewBufferString(`{}`)
	req := httptest.NewRequest("POST", "/api/v1/consolidate/run", body)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Run(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d (no consolidator)", rr.Code, http.StatusServiceUnavailable)
	}
}

func TestConsolidateRun_SummarizerMissing_Returns202WithNote(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	h := NewConsolidateHandler(db, newTestLogger())
	h.SetConsolidator(&consolidate.Consolidator{
		DB:         db,
		Journal:    noopEmitter{},
		Summarizer: nil, // no summarizer configured
		Logger:     slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	})

	body := bytes.NewBufferString(`{}`)
	req := httptest.NewRequest("POST", "/api/v1/consolidate/run", body)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Run(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Accepted bool   `json:"accepted"`
		Note     string `json:"note"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Accepted {
		t.Errorf("accepted = false, want true")
	}
	if resp.Note == "" {
		t.Errorf("note = %q, want non-empty explanation", resp.Note)
	}
}

func TestConsolidateRun_HappyPath_ReturnsWorkerID(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	h := NewConsolidateHandler(db, newTestLogger())
	h.SetConsolidator(&consolidate.Consolidator{
		DB:         db,
		Journal:    noopEmitter{},
		Summarizer: &stubSummarizer{},
		Logger:     slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	})
	h.SetMemoryRoot(t.TempDir())

	body := bytes.NewBufferString(`{"since":"6h"}`)
	req := httptest.NewRequest("POST", "/api/v1/consolidate/run", body)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Run(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Triggered bool   `json:"triggered"`
		WorkerID  string `json:"worker_id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Triggered {
		t.Errorf("triggered = false, want true")
	}
	if resp.WorkerID == "" {
		t.Errorf("worker_id empty; handler must stamp one")
	}
}

func TestConsolidateRun_CrossTenantCrewID_404(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	otherWS := "other-ws"
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Other', 'other')`, otherWS); err != nil {
		t.Fatalf("seed other ws: %v", err)
	}
	foreignCrew := seedCrewRow(t, db, "crew-x", otherWS, "X", "x")

	h := NewConsolidateHandler(db, newTestLogger())
	h.SetConsolidator(&consolidate.Consolidator{
		DB:         db,
		Journal:    noopEmitter{},
		Summarizer: &stubSummarizer{},
	})

	body := bytes.NewBufferString(`{"crew_id":"` + foreignCrew + `"}`)
	req := httptest.NewRequest("POST", "/api/v1/consolidate/run", body)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Run(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (cross-tenant crew_id must not leak)", rr.Code)
	}
}

func TestConsolidateRun_NonAdmin_Forbidden(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	h := NewConsolidateHandler(db, newTestLogger())
	h.SetConsolidator(&consolidate.Consolidator{
		DB:         db,
		Journal:    noopEmitter{},
		Summarizer: &stubSummarizer{},
	})

	body := bytes.NewBufferString(`{}`)
	req := httptest.NewRequest("POST", "/api/v1/consolidate/run", body)
	req = withWorkspaceUser(req, userID, wsID, "MEMBER")
	rr := httptest.NewRecorder()
	h.Run(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d (member can't trigger consolidation)", rr.Code, http.StatusForbidden)
	}
}
