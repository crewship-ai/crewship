package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/license"
	"github.com/crewship-ai/crewship/internal/ws"
)

// ---------------------------------------------------------------------------
// agents.go — setter wiring (SetHub/SetJournal/SetLicense/SetScheduler)
// + CrewsStatus aggregation contract used by the toolbar status widget.
// ---------------------------------------------------------------------------

func newAgentHandlerForTest(t *testing.T) (*AgentHandler, string, string) {
	t.Helper()
	h := NewAgentHandler(setupTestDB(t), newTestLogger())
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)
	return h, userID, wsID
}

// ---- Setters ----

func TestAgentHandler_SetHub_AssignsHub(t *testing.T) {
	h, _, _ := newAgentHandlerForTest(t)
	if h.hub != nil {
		t.Fatal("hub should be nil pre-SetHub")
	}
	hub := ws.NewHub(newTestLogger(), nil, ws.NopValidatorForTests, ws.NopSessionsForTests)
	h.SetHub(hub)
	if h.hub != hub {
		t.Errorf("hub = %p, want %p", h.hub, hub)
	}
}

// fakeJournalEmitter is a tiny journal.Emitter for verifying SetJournal
// actually wires the supplied emitter, not just any non-nil placeholder.
type fakeJournalEmitter struct{ calls int }

func (f *fakeJournalEmitter) Emit(_ context.Context, _ journal.Entry) (string, error) {
	f.calls++
	return "id", nil
}

func (f *fakeJournalEmitter) Flush(_ context.Context) error { return nil }

func TestAgentHandler_SetJournal_StoresEmitter(t *testing.T) {
	h, _, _ := newAgentHandlerForTest(t)
	fake := &fakeJournalEmitter{}
	h.SetJournal(fake)
	if _, err := h.journal.Emit(context.Background(), journal.Entry{}); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if fake.calls != 1 {
		t.Errorf("emitter calls = %d, want 1 (SetJournal must store the supplied emitter)", fake.calls)
	}
}

func TestAgentHandler_SetJournal_NilFallsBackToNoop(t *testing.T) {
	h, _, _ := newAgentHandlerForTest(t)
	// Wire a real emitter first…
	fake := &fakeJournalEmitter{}
	h.SetJournal(fake)
	// …then nil. Must fall back to a noopEmitter (no panic on .Emit).
	h.SetJournal(nil)
	if _, err := h.journal.Emit(context.Background(), journal.Entry{}); err != nil {
		t.Errorf("noop emitter Emit returned error: %v", err)
	}
	if fake.calls != 0 {
		t.Errorf("nil reset still routed to fake emitter (%d calls); SetJournal(nil) must replace it", fake.calls)
	}
}

func TestAgentHandler_SetLicense_AssignsLicense(t *testing.T) {
	h, _, _ := newAgentHandlerForTest(t)
	if h.license != nil {
		t.Fatal("license should be nil pre-SetLicense")
	}
	lic := license.New()
	h.SetLicense(lic)
	if h.license != lic {
		t.Errorf("license = %p, want %p", h.license, lic)
	}
}

// fakeScheduler is a stand-in for the scheduler that just records the
// last call. Confirms SetScheduler stores the value and broadcastAgentEvent
// doesn't depend on it (the field is purely a sink for downstream calls).
type fakeScheduler struct {
	gotAgentID string
	gotEnabled bool
	calls      int
}

func (f *fakeScheduler) UpdateSchedule(_ context.Context, agentID, _ string, _ string, enabled bool) error {
	f.calls++
	f.gotAgentID = agentID
	f.gotEnabled = enabled
	return nil
}

func TestAgentHandler_SetScheduler_StoresUpdater(t *testing.T) {
	h, _, _ := newAgentHandlerForTest(t)
	if h.scheduleUpdater != nil {
		t.Fatal("scheduleUpdater should be nil pre-SetScheduler")
	}
	f := &fakeScheduler{}
	h.SetScheduler(f)
	if h.scheduleUpdater == nil {
		t.Fatal("SetScheduler did not store the updater")
	}
	// Sanity: call through the interface to confirm dispatch lands on the
	// fake. Doesn't exercise any agent flow; just pins the wiring.
	if err := h.scheduleUpdater.UpdateSchedule(context.Background(), "ag-1", "* * * * *", "p", true); err != nil {
		t.Fatalf("UpdateSchedule: %v", err)
	}
	if f.calls != 1 || f.gotAgentID != "ag-1" || !f.gotEnabled {
		t.Errorf("fake = %+v, want 1 call with agentID=ag-1 enabled=true", f)
	}
}

// ---- CrewsStatus ----

func seedAgentForStatus(t *testing.T, h *AgentHandler, agentID, wsID, crewID, status string, deleted bool) {
	t.Helper()
	var del interface{}
	if deleted {
		del = time.Now().UTC().Format(time.RFC3339)
	}
	// crew_id is REFERENCES crews(id) — passing "" trips the FK gate.
	// Translate empty string to SQL NULL, matching the convention in
	// seedAgentRow (core_handlers_test.go).
	var crew interface{} = crewID
	if crewID == "" {
		crew = nil
	}
	_, err := h.db.Exec(`INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role, status,
		cli_adapter, tool_profile, timeout_seconds, memory_enabled, deleted_at)
		VALUES (?, ?, ?, ?, ?, 'AGENT', ?, 'CLAUDE_CODE', 'CODING', 1800, 0, ?)`,
		agentID, wsID, crew, "N-"+agentID, agentID, status, del)
	if err != nil {
		t.Fatalf("seed agent %s: %v", agentID, err)
	}
}

func TestCrewsStatus_EmptyWorkspace_AllZeros(t *testing.T) {
	h, userID, wsID := newAgentHandlerForTest(t)
	req := httptest.NewRequest("GET", "/api/v1/crews/status", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.CrewsStatus(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var body map[string]int
	json.Unmarshal(rr.Body.Bytes(), &body)
	for _, key := range []string{"total", "running", "error", "idle", "queued"} {
		if body[key] != 0 {
			t.Errorf("%s = %d on empty workspace, want 0", key, body[key])
		}
	}
}

func TestCrewsStatus_AggregatesByStatusAndQueued(t *testing.T) {
	h, userID, wsID := newAgentHandlerForTest(t)
	seedCrewRow(t, h.db, "crew-s", wsID, "C", "c-status")

	// 2 RUNNING, 1 ERROR, 3 IDLE-ish (PROVISIONING + IDLE + READY all
	// roll into idle), 1 soft-deleted (excluded), 1 in another workspace (excluded).
	seedAgentForStatus(t, h, "r1", wsID, "crew-s", "RUNNING", false)
	seedAgentForStatus(t, h, "r2", wsID, "crew-s", "RUNNING", false)
	seedAgentForStatus(t, h, "e1", wsID, "crew-s", "ERROR", false)
	seedAgentForStatus(t, h, "i1", wsID, "crew-s", "IDLE", false)
	seedAgentForStatus(t, h, "i2", wsID, "crew-s", "PROVISIONING", false)
	seedAgentForStatus(t, h, "i3", wsID, "crew-s", "READY", false)
	seedAgentForStatus(t, h, "gone", wsID, "crew-s", "RUNNING", true) // soft-deleted

	// Cross-workspace agent — must NOT be counted.
	otherWS := "ws-foreign-status"
	if _, err := h.db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'F', 'f-status')`, otherWS); err != nil {
		t.Fatalf("seed other ws: %v", err)
	}
	seedCrewRow(t, h.db, "crew-foreign", otherWS, "F", "f-status")
	seedAgentForStatus(t, h, "foreign", otherWS, "crew-foreign", "RUNNING", false)

	// Seed assignments: 3 QUEUED in our workspace + 1 RUNNING + 1 QUEUED in foreign ws.
	// chats FK requires existing agent — reuse r1.
	if _, err := h.db.Exec(`INSERT INTO chats (id, workspace_id, agent_id, status, started_at)
		VALUES ('chat-s', ?, 'r1', 'ACTIVE', datetime('now'))`, wsID); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	for i, status := range []string{"QUEUED", "QUEUED", "QUEUED", "RUNNING"} {
		if _, err := h.db.Exec(`INSERT INTO assignments
			(id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, created_at)
			VALUES (?, ?, 'chat-s', 'r1', 'r2', 'do', ?, datetime('now'))`,
			"as-"+string(rune('a'+i)), wsID, status); err != nil {
			t.Fatalf("seed assignment %d: %v", i, err)
		}
	}
	// Foreign workspace assignment in QUEUED — must NOT bleed in.
	if _, err := h.db.Exec(`INSERT INTO chats (id, workspace_id, agent_id, status, started_at)
		VALUES ('chat-foreign', ?, 'foreign', 'ACTIVE', datetime('now'))`, otherWS); err != nil {
		t.Fatalf("seed foreign chat: %v", err)
	}
	if _, err := h.db.Exec(`INSERT INTO assignments
		(id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, created_at)
		VALUES ('as-foreign', ?, 'chat-foreign', 'foreign', 'foreign', 'do', 'QUEUED', datetime('now'))`,
		otherWS); err != nil {
		t.Fatalf("seed foreign assignment: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/v1/crews/status", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.CrewsStatus(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var body struct {
		Total, Running, Error, Idle, Queued int
	}
	json.Unmarshal(rr.Body.Bytes(), &body)

	if body.Running != 2 {
		t.Errorf("running = %d, want 2", body.Running)
	}
	if body.Error != 1 {
		t.Errorf("error = %d, want 1", body.Error)
	}
	if body.Idle != 3 {
		t.Errorf("idle = %d, want 3 (IDLE + PROVISIONING + READY all bucket as idle)", body.Idle)
	}
	if body.Total != 6 {
		t.Errorf("total = %d, want 6 (foreign + soft-deleted excluded)", body.Total)
	}
	if body.Queued != 3 {
		t.Errorf("queued = %d, want 3 (only our workspace's QUEUED assignments)", body.Queued)
	}
}

func TestCrewsStatus_NoCrossWorkspaceAgentLeak(t *testing.T) {
	// Dedicated isolation check independent of the larger aggregation test.
	h, userID, wsID := newAgentHandlerForTest(t)
	otherWS := "ws-iso-status"
	if _, err := h.db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'I', 'i-iso')`, otherWS); err != nil {
		t.Fatalf("seed other ws: %v", err)
	}
	seedCrewRow(t, h.db, "crew-iso", otherWS, "I", "iso")
	seedAgentForStatus(t, h, "iso-r", otherWS, "crew-iso", "RUNNING", false)
	seedAgentForStatus(t, h, "iso-e", otherWS, "crew-iso", "ERROR", false)

	req := httptest.NewRequest("GET", "/api/v1/crews/status", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.CrewsStatus(rr, req)
	var body struct {
		Total int
	}
	json.Unmarshal(rr.Body.Bytes(), &body)
	if body.Total != 0 {
		t.Errorf("total = %d, want 0 (foreign workspace agents must not leak)", body.Total)
	}
}
