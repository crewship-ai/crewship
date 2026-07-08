package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/crewship-ai/crewship/internal/journal"
)

// #829: after a devcontainer feature build fails, the BuildKit stderr tail
// must survive the in-memory job's 1h TTL / a server restart. ProvisionStatus
// falls back to the durable `provisioning.build_failed` journal entry when no
// in-memory job exists, so `crewship crew provision status` still shows the
// failing-step output. RED on main: ProvisionStatus never reads the journal,
// so status collapses to "idle" with no error/log_tail.
func TestProvisionStatus_SurfacesBuildFailureFromJournal(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h := NewProvisioningHandler(db, logger, nil, nil, nil, "", nil)
	t.Cleanup(h.Stop)

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, 'Eng', 'eng-829')`, "crew-829", wsID)

	// A durable build-failed entry with the scrubbed tail — no in-memory job.
	w := journal.NewWriter(db, logger, journal.WriterOptions{})
	tail := "#8 12.34 E: Unable to locate package nonexistent-pkg\n#8 ERROR: process \"/bin/sh -c apt-get install -y nonexistent-pkg\" did not complete successfully: exit code: 100"
	if _, err := w.Emit(context.Background(), journal.Entry{
		WorkspaceID: wsID,
		CrewID:      "crew-829",
		Type:        journal.EntryProvisioningBuildFailed,
		Severity:    journal.SeverityWarn,
		ActorType:   journal.ActorOrchestrator,
		Summary:     "provision build failed for crew crew-829",
		Payload:     map[string]any{"error": "building feature image: exit code 100", "detail": tail, "crew_id": "crew-829"},
	}); err != nil {
		t.Fatalf("emit: %v", err)
	}
	if err := w.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/crews/{crewId}/provisioning", h.ProvisionStatus)
	req := httptest.NewRequest("GET", "/api/v1/crews/crew-829/provisioning", nil)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body: %s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Status  string   `json:"status"`
		Error   string   `json:"error"`
		LogTail []string `json:"log_tail"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Status != "failed" {
		t.Errorf("status = %q, want %q (durable failure must survive the in-memory job)", resp.Status, "failed")
	}
	if !strings.Contains(resp.Error, "building feature image") {
		t.Errorf("error = %q, want the build error surfaced", resp.Error)
	}
	if joined := strings.Join(resp.LogTail, "\n"); !strings.Contains(joined, "nonexistent-pkg") {
		t.Errorf("log_tail missing the failing-step output: %q", joined)
	}
}

// ctxCaptureEmitter records, per entry type, whether the context handed to
// Emit was already cancelled. Lets us assert DETERMINISTICALLY that the
// build_failed write is detached from cancellation — rather than probabilistically
// via the journal.Emit queue-vs-ctx.Done race.
type ctxCaptureEmitter struct {
	mu        sync.Mutex
	cancelled map[journal.EntryType]bool
}

func (c *ctxCaptureEmitter) Emit(ctx context.Context, e journal.Entry) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cancelled == nil {
		c.cancelled = map[journal.EntryType]bool{}
	}
	c.cancelled[e.Type] = ctx.Err() != nil
	return e.ID, nil
}

func (c *ctxCaptureEmitter) Flush(context.Context) error { return nil }

func (c *ctxCaptureEmitter) wasCancelled(t journal.EntryType) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cancelled[t]
}

// The durable build-failed row MUST be written on a context that isn't
// cancelled even when the incoming provisioning ctx already is (build timeout /
// client disconnect) — journal.Emit races the queue send against ctx.Done(),
// so a cancelled ctx can drop the entry. emitProvisionEvent detaches ONLY the
// build_failed write with context.WithoutCancel; every other step keeps the
// caller's ctx.
func TestEmitProvisionEvent_BuildFailedDetachesFromCancellation(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h := NewProvisioningHandler(db, logger, nil, nil, nil, "", nil)
	t.Cleanup(h.Stop)
	capture := &ctxCaptureEmitter{}
	h.SetJournal(capture)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled — mirrors a provisioning timeout / disconnect

	// build_failed must be emitted on a NON-cancelled ctx (WithoutCancel).
	h.emitProvisionEvent(ctx, "crew-cancel", "ws-1", devcontainer.ProvisionEvent{
		Step:   devcontainer.ProvStepBuildFailed,
		Status: devcontainer.ProvStatusFailed,
		Error:  "building feature image: exit code 100",
		Detail: "#8 ERROR: did not complete",
	})
	if capture.wasCancelled(journal.EntryProvisioningBuildFailed) {
		t.Error("build_failed must be emitted on a non-cancelled ctx so the durable row survives")
	}

	// A regular step keeps the caller's (cancelled) ctx — we only detach the
	// one durable diagnostic row, not every provisioning emit.
	h.emitProvisionEvent(ctx, "crew-cancel", "ws-1", devcontainer.ProvisionEvent{
		Step:   devcontainer.ProvStepFeatureInstall,
		Status: devcontainer.ProvStatusStarted,
	})
	if !capture.wasCancelled(journal.EntryProvisioningStep) {
		t.Error("only build_failed should be detached; a regular step must keep the caller's ctx")
	}
}

// Fail-open gap closed: a build that failed with an EMPTY BuildKit tail emits
// no provisioning.build_failed row, only the coarse provisioning.failed from
// markJobFailed. ProvisionStatus must still surface status=failed with the
// error (just no log_tail) rather than collapsing to idle.
func TestProvisionStatus_SurfacesPlainFailedWithoutTail(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h := NewProvisioningHandler(db, logger, nil, nil, nil, "", nil)
	t.Cleanup(h.Stop)

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, 'C', 'plainfail-829')`, "crew-pf", wsID)

	w := journal.NewWriter(db, logger, journal.WriterOptions{})
	if _, err := w.Emit(context.Background(), journal.Entry{
		WorkspaceID: wsID, CrewID: "crew-pf",
		Type:      journal.EntryProvisioningFailed,
		Severity:  journal.SeverityWarn,
		ActorType: journal.ActorOrchestrator,
		Summary:   "provisioning failed for crew crew-pf",
		Payload:   map[string]any{"error": "provision: mise install failed", "crew_id": "crew-pf"},
	}); err != nil {
		t.Fatalf("emit: %v", err)
	}
	if err := w.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/crews/{crewId}/provisioning", h.ProvisionStatus)
	req := httptest.NewRequest("GET", "/api/v1/crews/crew-pf/provisioning", nil)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body: %s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Status  string   `json:"status"`
		Error   string   `json:"error"`
		LogTail []string `json:"log_tail"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Status != "failed" {
		t.Errorf("a plain provisioning.failed must surface status=failed, got %q", resp.Status)
	}
	if !strings.Contains(resp.Error, "mise install failed") {
		t.Errorf("error must surface, got %q", resp.Error)
	}
	if len(resp.LogTail) != 0 {
		t.Errorf("no build tail was captured, log_tail must be empty, got %v", resp.LogTail)
	}
}
