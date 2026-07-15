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
	"time"

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

	// Run kicks the actual work off in a background goroutine (see
	// consolidate_handler.go) and returns 202 immediately. Wait for it
	// to finish before this test returns — otherwise t.TempDir()'s own
	// cleanup can race the goroutine's writes into that same directory,
	// flaking as "TempDir RemoveAll cleanup: directory not empty".
	done := make(chan struct{}, 1)
	h.testRunDone = done
	defer func() { <-done }()

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
		Accepted  bool   `json:"accepted"`
		WorkerID  string `json:"worker_id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Triggered {
		t.Errorf("triggered = false, want true")
	}
	// Regression for #1206: a successful trigger must also report
	// accepted=true. The CLI's `consolidate run --format json` decodes
	// this field independently of `triggered`, and the table renderer
	// is being taught to surface it too — a response that omits
	// "accepted" silently zero-values to false, making a genuinely
	// successful run look rejected.
	if !resp.Accepted {
		t.Errorf("accepted = false, want true (issue #1206: success path must report accepted)")
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

// TestConsolidateRun_AlreadyRunning_RejectsWithClearError covers the one
// genuine "not accepted" path the handler has: the per-workspace
// in-flight guard. It must surface as an unambiguous 409 error — not a
// 202 with accepted:false the way the #1206 bug made every *successful*
// trigger look. The CLI's cli.CheckError() already turns any non-2xx
// into a proper error before the accepted/note envelope is ever decoded,
// so this stays a plain error body rather than adopting the 202
// accepted/note shape.
func TestConsolidateRun_AlreadyRunning_RejectsWithClearError(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	h := NewConsolidateHandler(db, newTestLogger())
	h.SetConsolidator(&consolidate.Consolidator{
		DB:         db,
		Journal:    noopEmitter{},
		Summarizer: &stubSummarizer{},
	})
	h.SetMemoryRoot(t.TempDir())

	// Simulate an in-flight run for this workspace directly, rather than
	// racing a goroutine against the handler.
	h.mu.Lock()
	h.running[wsID] = struct{}{}
	h.mu.Unlock()

	body := bytes.NewBufferString(`{}`)
	req := httptest.NewRequest("POST", "/api/v1/consolidate/run", body)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Run(rr, req)

	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d (already running)", rr.Code, http.StatusConflict)
	}
	var resp struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Error == "" {
		t.Errorf("error message empty; a 409 must explain the rejection")
	}
}

// TestConsolidateRun_EmitsAuditLogOnCompletion covers #1207: six manual
// `consolidate run` calls in a 24h QA window produced zero audit_logs
// rows. runOnce's terminal emitCompleted call must now also write a
// "consolidate.run" audit_logs row, once per run (not per crew).
func TestConsolidateRun_EmitsAuditLogOnCompletion(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	h := NewConsolidateHandler(db, newTestLogger())
	h.SetConsolidator(&consolidate.Consolidator{
		DB:         db,
		Journal:    noopEmitter{},
		Summarizer: &stubSummarizer{},
	})
	h.SetMemoryRoot(t.TempDir())

	// Drive runOnce directly (synchronous) rather than through Run's
	// background goroutine, so the audit row is guaranteed to exist by
	// the time we assert on it.
	workerID := "test-worker-1207"
	h.runOnce(context.Background(), wsID, "", 6*time.Hour, workerID)

	var count int
	var gotWorkspace, metaJSON string
	if err := db.QueryRow(
		`SELECT COUNT(*), workspace_id, metadata FROM audit_logs WHERE action = 'consolidate.run' AND entity_id = ? GROUP BY workspace_id, metadata`,
		workerID,
	).Scan(&count, &gotWorkspace, &metaJSON); err != nil {
		t.Fatalf("query audit_logs: %v", err)
	}
	if count != 1 {
		t.Fatalf("audit_logs rows for consolidate.run = %d, want 1", count)
	}
	if gotWorkspace != wsID {
		t.Errorf("audit workspace_id = %q, want %q", gotWorkspace, wsID)
	}
	var meta struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(metaJSON), &meta); err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	if meta.Status != "ok" {
		t.Errorf("metadata status = %q, want ok", meta.Status)
	}
}
