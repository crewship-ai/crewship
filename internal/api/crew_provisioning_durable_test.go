package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
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

// The durable build-failed row MUST land even when the provisioning ctx was
// already cancelled (build timeout / client disconnect). journal.Emit races
// the queue send against ctx.Done(), so a cancelled ctx can drop the entry;
// emitProvisionEvent detaches the build_failed write with context.WithoutCancel.
// Emitting several under an already-cancelled ctx amplifies the race so a
// regression (dropping the WithoutCancel) reliably fails.
func TestEmitProvisionEvent_BuildFailedSurvivesCancelledCtx(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h := NewProvisioningHandler(db, logger, nil, nil, nil, "", nil)
	t.Cleanup(h.Stop)
	w := journal.NewWriter(db, logger, journal.WriterOptions{})
	h.SetJournal(w)

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, 'C', 'cancel-829')`, "crew-cancel", wsID)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled — mirrors a provisioning timeout / disconnect

	const n = 5
	for i := 0; i < n; i++ {
		h.emitProvisionEvent(ctx, "crew-cancel", wsID, devcontainer.ProvisionEvent{
			Step:   devcontainer.ProvStepBuildFailed,
			Status: devcontainer.ProvStatusFailed,
			Tag:    "crewship-feat:test",
			Error:  "building feature image: exit code 100",
			Detail: "#8 E: Unable to locate package nope\n#8 ERROR: did not complete",
		})
	}
	if err := w.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}

	entries, _, err := journal.List(context.Background(), db, journal.Query{
		WorkspaceID: wsID,
		CrewID:      "crew-cancel",
		Types:       []journal.EntryType{journal.EntryProvisioningBuildFailed},
		Limit:       100,
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != n {
		t.Errorf("build-failed rows must survive a cancelled ctx: got %d, want %d", len(entries), n)
	}
}
