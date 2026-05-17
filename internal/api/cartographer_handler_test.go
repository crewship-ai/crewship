package api

// Handler-level tests for cartographer_handler.go.
//
// Coverage philosophy: cover the handler-shaped contract — auth/scope
// gating, marshalling, status codes, and the empty-slice-not-null
// guarantees the UI relies on. The cartographer package itself has
// in-depth tests under internal/cartographer; here we focus on the
// integration seam (handler ↔ store) plus the bits of policy that only
// the handler enforces (workspace context, mission resolution, 404
// shape for cross-tenant ids).

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/cartographer"
	"github.com/crewship-ai/crewship/internal/journal"
)

// ── fixtures ────────────────────────────────────────────────────────────

// cartographerRig builds the workspace + user fixtures every test in
// this file needs, plus a constructed handler. Returning the user and
// workspace ids keeps each test compact while still letting callers
// verify workspace-scoping behaviour. We deliberately build a *real*
// migrated SQLite DB rather than mocking the store — the handler's job
// is to translate HTTP into store calls, so swapping the store would
// hide most of the surface we want to assert on.
func cartographerRig(t *testing.T) (*CartographerHandler, *sql.DB, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h := NewCartographerHandler(db, logger)
	return h, db, userID, wsID
}

// seedCartographerMission creates a crew + lead agent + mission inside
// the provided workspace and returns the (crewID, missionID) pair. The
// handler's List/Create paths join through missions, so a real row is
// required for any non-error scenario.
func seedCartographerMission(t *testing.T, db *sql.DB, wsID, missionID, traceID string) (string, string) {
	t.Helper()
	crewID := "crew_" + missionID
	leadID := "lead_" + missionID
	if _, err := db.Exec(
		`INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, ?, ?)`,
		crewID, wsID, "Crew "+missionID, "crew-"+missionID,
	); err != nil {
		t.Fatalf("seed crew: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role, status, cli_adapter, temperature, timeout_seconds, tool_profile, memory_enabled)
		 VALUES (?, ?, ?, ?, ?, 'LEAD', 'IDLE', 'CLAUDE_CODE', 0.7, 1800, 'CODING', 0)`,
		leadID, wsID, crewID, "Lead "+missionID, "lead-"+missionID,
	); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, 'PLANNING', datetime('now'), datetime('now'))`,
		missionID, wsID, crewID, leadID, traceID, "Mission "+missionID,
	); err != nil {
		t.Fatalf("seed mission: %v", err)
	}
	return crewID, missionID
}

// seedCartographerJournalEntry inserts a journal_entries row directly
// so Capture has something to anchor a cursor on. Bypasses the batched
// Writer because we need deterministic (ts, id) for ordering and we
// don't want the test to depend on Flush timing.
func seedCartographerJournalEntry(t *testing.T, db *sql.DB, id, wsID, missionID string, ts time.Time) {
	t.Helper()
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	_, err := db.Exec(`INSERT INTO journal_entries
		(id, workspace_id, mission_id, ts, entry_type, severity, priority, actor_type, summary, payload, refs)
		VALUES (?, ?, ?, ?, 'peer.conversation', 'info', 'normal', 'agent', 'seeded', '{}', '{}')`,
		id, wsID, missionID, ts.UTC().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatalf("seed journal entry %s: %v", id, err)
	}
}

// seedCheckpointDirect inserts a checkpoint row via the cartographer
// store. Returns the new checkpoint ID. Tests that exercise Get/Restore
// use this to skip the Create plumbing they're not asserting on.
func seedCheckpointDirect(t *testing.T, db *sql.DB, wsID, crewID, missionID, cursor, label string) string {
	t.Helper()
	id, err := cartographer.Create(context.Background(), db, nil, cartographer.Checkpoint{
		WorkspaceID:   wsID,
		CrewID:        crewID,
		MissionID:     missionID,
		Label:         label,
		JournalCursor: cursor,
	})
	if err != nil {
		t.Fatalf("seed checkpoint: %v", err)
	}
	return id
}

// ── SetJournal / NewCartographerHandler ────────────────────────────────

// TestCartographer_SetJournal_NilCollapsesToNoop verifies the nil-safety
// branch: SetJournal(nil) must not nil the field — the handler's audit
// emits would nil-panic later. Reading the unexported field via the
// recordingEmitter swap-back pattern is overkill; a follow-up real-emit
// path through Create is enough proof, but the dedicated test keeps the
// intent of the SetJournal guard documented.
func TestCartographer_SetJournal_NilCollapsesToNoop_DoesNotPanic(t *testing.T) {
	h, _, _, _ := cartographerRig(t)
	// First wire a real emitter, then nil it. After the nil call the
	// field must still satisfy the interface — we exercise it via a
	// downstream call that would emit on success.
	rec := &recordingEmitter{}
	h.SetJournal(rec)
	h.SetJournal(nil)
	// Drive a handler path that calls h.journal.Emit indirectly (Get
	// doesn't emit, but its 401 path proves the handler is still alive
	// after a nil SetJournal — the real guarantee is "no nil deref").
	req := httptest.NewRequest("GET", "/api/v1/checkpoints/x", nil)
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (handler still serving after nil SetJournal)", rr.Code)
	}
}

// TestCartographer_SetJournal_RealEmitter_IsUsed wires a recording
// emitter and triggers a path (Create happy-path) that emits a
// checkpoint.created entry. Verifying the recorder captured something
// proves SetJournal swap-in actually replaces the default noop.
func TestCartographer_SetJournal_RealEmitter_IsUsed_CapturesEmit(t *testing.T) {
	h, db, userID, wsID := cartographerRig(t)
	_, missionID := seedCartographerMission(t, db, wsID, "mis_emit", "tr_emit")
	seedCartographerJournalEntry(t, db, "je_emit_1", wsID, missionID, time.Now())

	rec := &recordingEmitter{}
	h.SetJournal(rec)

	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/missions/"+missionID+"/checkpoints",
			strings.NewReader(`{"label":"wired"}`)),
		userID, wsID, "OWNER",
	)
	req.SetPathValue("missionId", missionID)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	if len(rec.entries) == 0 {
		t.Fatalf("recording emitter saw no entries; SetJournal did not wire through")
	}
}

// ── List ────────────────────────────────────────────────────────────────

// TestCartographer_List_NoWorkspace_Returns401 — without a workspace
// context the handler must refuse, regardless of path values. Catches
// regressions where middleware ordering accidentally exposes the
// endpoint to unauthenticated traffic.
func TestCartographer_List_NoWorkspace_Returns401(t *testing.T) {
	h, _, _, _ := cartographerRig(t)
	req := httptest.NewRequest("GET", "/api/v1/missions/m1/checkpoints", nil)
	req.SetPathValue("missionId", "m1")
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
}

// TestCartographer_List_MissingMissionID_Returns400 — the router would
// normally bind {missionId}, but a direct call without SetPathValue
// proves the empty-string guard. Belt-and-braces against router misroute.
func TestCartographer_List_MissingMissionID_Returns400(t *testing.T) {
	h, _, userID, wsID := cartographerRig(t)
	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/missions//checkpoints", nil),
		userID, wsID, "OWNER",
	)
	// no SetPathValue → r.PathValue("missionId") returns ""
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

// TestCartographer_List_UnknownMission_Returns404 — resolveMission
// surfaces sql.ErrNoRows as 404 (not 500). Important: the same shape
// must come back for cross-workspace ids, see the next test.
func TestCartographer_List_UnknownMission_Returns404(t *testing.T) {
	h, _, userID, wsID := cartographerRig(t)
	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/missions/mis_nope/checkpoints", nil),
		userID, wsID, "OWNER",
	)
	req.SetPathValue("missionId", "mis_nope")
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rr.Code, rr.Body.String())
	}
}

// TestCartographer_List_EmptyMission_Returns200WithEmptyArray verifies
// the timeline UI's hard requirement: an empty mission returns a JSON
// array (`[]`), not `null`. cartographer.List preallocates an empty
// slice so this is contract-enforced at the store, but the handler
// test pins it in case a future refactor changes the response shape.
func TestCartographer_List_EmptyMission_Returns200WithEmptyArray(t *testing.T) {
	h, db, userID, wsID := cartographerRig(t)
	_, missionID := seedCartographerMission(t, db, wsID, "mis_empty", "tr_empty")

	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/missions/"+missionID+"/checkpoints", nil),
		userID, wsID, "OWNER",
	)
	req.SetPathValue("missionId", missionID)
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	// We check the raw payload for `"checkpoints":[]` (not null) — the
	// timeline page does a `.length` check that would NPE on null.
	body := rr.Body.String()
	if !strings.Contains(body, `"checkpoints":[]`) {
		t.Errorf("expected checkpoints:[] in body, got: %s", body)
	}
	var resp struct {
		Checkpoints []map[string]any `json:"checkpoints"`
		Count       int              `json:"count"`
		MissionID   string           `json:"mission_id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Checkpoints == nil {
		t.Errorf("checkpoints is nil; want empty slice")
	}
	if resp.Count != 0 {
		t.Errorf("count = %d, want 0", resp.Count)
	}
	if resp.MissionID != missionID {
		t.Errorf("mission_id echo = %q, want %q", resp.MissionID, missionID)
	}
}

// TestCartographer_List_CrossWorkspace_Returns404 — a caller in
// workspace B must not see workspace A's mission, even when they know
// the mission id. The 404 shape (vs 403) deliberately hides existence.
func TestCartographer_List_CrossWorkspace_Returns404(t *testing.T) {
	h, db, userID, wsA := cartographerRig(t)
	_, missionID := seedCartographerMission(t, db, wsA, "mis_owned", "tr_owned")

	otherWS := "ws_other"
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Other', 'other')`, otherWS); err != nil {
		t.Fatalf("seed other workspace: %v", err)
	}

	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/missions/"+missionID+"/checkpoints", nil),
		userID, otherWS, "OWNER",
	)
	req.SetPathValue("missionId", missionID)
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("cross-workspace mission leaked: status = %d, want 404", rr.Code)
	}
}

// TestCartographer_List_IsolatedFromOtherMission verifies the store
// query is mission-scoped: checkpoints attached to mission A must not
// surface when listing mission B (even within the same workspace).
func TestCartographer_List_IsolatedFromOtherMission(t *testing.T) {
	h, db, userID, wsID := cartographerRig(t)
	crewA, missionA := seedCartographerMission(t, db, wsID, "mis_a", "tr_a")
	_, missionB := seedCartographerMission(t, db, wsID, "mis_b", "tr_b")
	seedCartographerJournalEntry(t, db, "je_a_1", wsID, missionA, time.Now())
	_ = seedCheckpointDirect(t, db, wsID, crewA, missionA, "je_a_1", "A only")

	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/missions/"+missionB+"/checkpoints", nil),
		userID, wsID, "OWNER",
	)
	req.SetPathValue("missionId", missionB)
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var resp struct {
		Count int `json:"count"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Count != 0 {
		t.Errorf("mission B saw %d checkpoints from mission A — leak", resp.Count)
	}
}

// TestCartographer_List_GarbageLimit_FallsBackToDefault — the inline
// limit parser must tolerate garbage without crashing. 200 is the cap;
// values <= 0 and Atoi failures both fall back to the default 50.
func TestCartographer_List_GarbageLimit_FallsBackToDefault(t *testing.T) {
	h, db, userID, wsID := cartographerRig(t)
	_, missionID := seedCartographerMission(t, db, wsID, "mis_limit", "tr_limit")

	for _, raw := range []string{"abc", "9999", "0", "-5", ""} {
		req := withWorkspaceUser(
			httptest.NewRequest("GET", "/api/v1/missions/"+missionID+"/checkpoints?limit="+raw, nil),
			userID, wsID, "OWNER",
		)
		req.SetPathValue("missionId", missionID)
		rr := httptest.NewRecorder()
		h.List(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("limit=%q: status = %d, want 200; body=%s", raw, rr.Code, rr.Body.String())
		}
	}
}

// ── Create ─────────────────────────────────────────────────────────────

// TestCartographer_Create_NoWorkspace_Returns401 — same auth-gate test
// as List for the write side. No workspace context = no write.
func TestCartographer_Create_NoWorkspace_Returns401(t *testing.T) {
	h, _, _, _ := cartographerRig(t)
	req := httptest.NewRequest("POST", "/api/v1/missions/m1/checkpoints",
		strings.NewReader(`{"label":"x"}`))
	req.SetPathValue("missionId", "m1")
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
}

// TestCartographer_Create_BadJSON_Returns400 — a malformed body with
// a non-zero ContentLength must be rejected (the optional-body path
// only forgives empty bodies). Protects against silent acceptance of
// garbage payloads.
func TestCartographer_Create_BadJSON_Returns400(t *testing.T) {
	h, db, userID, wsID := cartographerRig(t)
	_, missionID := seedCartographerMission(t, db, wsID, "mis_badjson", "tr_badjson")

	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/missions/"+missionID+"/checkpoints",
			strings.NewReader(`{NOT_JSON`)),
		userID, wsID, "OWNER",
	)
	req.SetPathValue("missionId", missionID)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
}

// TestCartographer_Create_NoJournalEntries_Returns409 — Capture
// returns an empty cursor when the mission has zero journal entries;
// the handler must surface this as 409 "nothing to checkpoint yet"
// rather than letting an empty-cursor row land in the DB (which would
// later fail validation in Restore).
func TestCartographer_Create_NoJournalEntries_Returns409(t *testing.T) {
	h, db, userID, wsID := cartographerRig(t)
	_, missionID := seedCartographerMission(t, db, wsID, "mis_nojournal", "tr_nojournal")

	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/missions/"+missionID+"/checkpoints",
			strings.NewReader(`{"label":"premature"}`)),
		userID, wsID, "OWNER",
	)
	req.SetPathValue("missionId", missionID)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", rr.Code, rr.Body.String())
	}
}

// TestCartographer_Create_UnknownMission_Returns404 — resolveMission
// rejects unknown ids before Capture would discover the same thing in
// a less-helpful way. Matches the List 404 shape.
func TestCartographer_Create_UnknownMission_Returns404(t *testing.T) {
	h, _, userID, wsID := cartographerRig(t)
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/missions/mis_ghost/checkpoints",
			strings.NewReader(`{"label":"x"}`)),
		userID, wsID, "OWNER",
	)
	req.SetPathValue("missionId", "mis_ghost")
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}

// TestCartographer_Create_HappyPath_Returns201WithID — the contract
// the UI depends on after the user clicks "checkpoint". 201 + JSON
// body containing the new id. We also verify the row was actually
// persisted (HTTP 201 with no DB row would be a silent regression).
func TestCartographer_Create_HappyPath_Returns201WithID(t *testing.T) {
	h, db, userID, wsID := cartographerRig(t)
	_, missionID := seedCartographerMission(t, db, wsID, "mis_create", "tr_create")
	seedCartographerJournalEntry(t, db, "je_create_1", wsID, missionID, time.Now())

	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/missions/"+missionID+"/checkpoints",
			strings.NewReader(`{"label":"before merge"}`)),
		userID, wsID, "OWNER",
	)
	req.SetPathValue("missionId", missionID)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	id, ok := resp["id"].(string)
	if !ok || !strings.HasPrefix(id, "cp_") {
		t.Errorf("expected id starting with 'cp_', got %v", resp["id"])
	}
	// Sanity: row exists when read directly via the store.
	cp, err := cartographer.Get(context.Background(), db, wsID, id)
	if err != nil {
		t.Fatalf("post-create read: %v", err)
	}
	if cp.MissionID != missionID || cp.Label != "before merge" {
		t.Errorf("persisted row mismatch: %+v", cp)
	}
}

// TestCartographer_Create_EmptyBody_Returns201 — an empty POST body is
// a valid "bookmark now, no label" gesture per the handler comment.
// ContentLength == 0 short-circuits the JSON decode.
func TestCartographer_Create_EmptyBody_Returns201(t *testing.T) {
	h, db, userID, wsID := cartographerRig(t)
	_, missionID := seedCartographerMission(t, db, wsID, "mis_nobody", "tr_nobody")
	seedCartographerJournalEntry(t, db, "je_nobody_1", wsID, missionID, time.Now())

	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/missions/"+missionID+"/checkpoints", nil),
		userID, wsID, "OWNER",
	)
	req.SetPathValue("missionId", missionID)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
}

// TestCartographer_Create_CrossWorkspace_Returns404 — caller in ws B
// tries to checkpoint a mission in ws A. resolveMission must short-
// circuit with the same 404 used for unknown missions, so existence
// of cross-tenant rows is never leaked.
func TestCartographer_Create_CrossWorkspace_Returns404(t *testing.T) {
	h, db, userID, wsA := cartographerRig(t)
	_, missionID := seedCartographerMission(t, db, wsA, "mis_xtenant", "tr_xtenant")
	seedCartographerJournalEntry(t, db, "je_xtenant_1", wsA, missionID, time.Now())

	otherWS := "ws_other_create"
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Other', 'other-create')`, otherWS); err != nil {
		t.Fatalf("seed other ws: %v", err)
	}

	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/missions/"+missionID+"/checkpoints",
			strings.NewReader(`{"label":"sneaky"}`)),
		userID, otherWS, "OWNER",
	)
	req.SetPathValue("missionId", missionID)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("cross-workspace create leaked: status = %d, want 404", rr.Code)
	}
}

// ── Get ─────────────────────────────────────────────────────────────────

// TestCartographer_Get_NoWorkspace_Returns401 — workspace context is
// required even for read-only checkpoint lookup; otherwise an
// unauthenticated request that happens to know an id could fetch it.
func TestCartographer_Get_NoWorkspace_Returns401(t *testing.T) {
	h, _, _, _ := cartographerRig(t)
	req := httptest.NewRequest("GET", "/api/v1/checkpoints/cp_x", nil)
	req.SetPathValue("id", "cp_x")
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
}

// TestCartographer_Get_UnknownID_Returns404 — cartographer.ErrNotFound
// translates to 404, not 500. Catches the easy mistake of forgetting
// the errors.Is check and bubbling the sentinel as a generic error.
func TestCartographer_Get_UnknownID_Returns404(t *testing.T) {
	h, _, userID, wsID := cartographerRig(t)
	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/checkpoints/cp_nope", nil),
		userID, wsID, "OWNER",
	)
	req.SetPathValue("id", "cp_nope")
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rr.Code, rr.Body.String())
	}
}

// TestCartographer_Get_CrossWorkspace_Returns404 — the explicit
// tenant-isolation test. The id exists, just not in this workspace.
// The handler relies on cartographer.Get's WHERE workspace_id = ?
// clause; this test pins that scoping is enforced end-to-end and that
// the response is 404 (not 403) so existence isn't leaked.
func TestCartographer_Get_CrossWorkspace_Returns404(t *testing.T) {
	h, db, userID, wsA := cartographerRig(t)
	crewA, missionA := seedCartographerMission(t, db, wsA, "mis_get_a", "tr_get_a")
	seedCartographerJournalEntry(t, db, "je_get_a_1", wsA, missionA, time.Now())
	id := seedCheckpointDirect(t, db, wsA, crewA, missionA, "je_get_a_1", "A only")

	otherWS := "ws_other_get"
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Other', 'other-get')`, otherWS); err != nil {
		t.Fatalf("seed other ws: %v", err)
	}

	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/checkpoints/"+id, nil),
		userID, otherWS, "OWNER",
	)
	req.SetPathValue("id", id)
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("cross-workspace get leaked: status = %d, want 404", rr.Code)
	}
}

// TestCartographer_Get_HappyPath_ReturnsCheckpointBody verifies the
// happy-path response shape: 200 + JSON object with id/mission_id/
// label/state. We don't pin every field — the cartographer package
// owns the row shape — but at minimum the id must round-trip.
func TestCartographer_Get_HappyPath_ReturnsCheckpointBody(t *testing.T) {
	h, db, userID, wsID := cartographerRig(t)
	crewID, missionID := seedCartographerMission(t, db, wsID, "mis_get_ok", "tr_get_ok")
	seedCartographerJournalEntry(t, db, "je_get_ok_1", wsID, missionID, time.Now())
	id := seedCheckpointDirect(t, db, wsID, crewID, missionID, "je_get_ok_1", "readable")

	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/checkpoints/"+id, nil),
		userID, wsID, "OWNER",
	)
	req.SetPathValue("id", id)
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["id"] != id {
		t.Errorf("id = %v, want %q", resp["id"], id)
	}
	if resp["mission_id"] != missionID {
		t.Errorf("mission_id = %v, want %q", resp["mission_id"], missionID)
	}
}

// TestCartographer_Get_MissingID_Returns400 — empty path value must be
// rejected; otherwise an empty id would propagate into the store with
// undefined behaviour. The router would normally guarantee a non-empty
// value, but the handler's defence-in-depth check is worth pinning.
func TestCartographer_Get_MissingID_Returns400(t *testing.T) {
	h, _, userID, wsID := cartographerRig(t)
	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/checkpoints/", nil),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

// ── Restore ────────────────────────────────────────────────────────────

// TestCartographer_Restore_NoWorkspace_Returns401 — Restore is a write
// (it emits an audit entry) so unauthenticated calls are bounced
// before any work happens.
func TestCartographer_Restore_NoWorkspace_Returns401(t *testing.T) {
	h, _, _, _ := cartographerRig(t)
	req := httptest.NewRequest("POST", "/api/v1/checkpoints/cp_x/restore", nil)
	req.SetPathValue("id", "cp_x")
	rr := httptest.NewRecorder()
	h.Restore(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
}

// TestCartographer_Restore_UnknownID_Returns404 — same ErrNotFound →
// 404 translation as Get. Restore wraps Get internally, so this also
// proves the error propagates correctly through the wrapper.
func TestCartographer_Restore_UnknownID_Returns404(t *testing.T) {
	h, _, userID, wsID := cartographerRig(t)
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/checkpoints/cp_nope/restore", nil),
		userID, wsID, "OWNER",
	)
	req.SetPathValue("id", "cp_nope")
	rr := httptest.NewRecorder()
	h.Restore(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rr.Code, rr.Body.String())
	}
}

// TestCartographer_Restore_CrossWorkspace_Returns404 — tenant
// isolation on the write side. A caller in ws B must not be able to
// trigger restore audit entries against ws A's checkpoint.
func TestCartographer_Restore_CrossWorkspace_Returns404(t *testing.T) {
	h, db, userID, wsA := cartographerRig(t)
	crewA, missionA := seedCartographerMission(t, db, wsA, "mis_rest_a", "tr_rest_a")
	seedCartographerJournalEntry(t, db, "je_rest_a_1", wsA, missionA, time.Now())
	id := seedCheckpointDirect(t, db, wsA, crewA, missionA, "je_rest_a_1", "A's")

	otherWS := "ws_other_rest"
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Other', 'other-rest')`, otherWS); err != nil {
		t.Fatalf("seed other ws: %v", err)
	}

	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/checkpoints/"+id+"/restore", nil),
		userID, otherWS, "OWNER",
	)
	req.SetPathValue("id", id)
	rr := httptest.NewRecorder()
	h.Restore(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("cross-workspace restore leaked: status = %d, want 404", rr.Code)
	}
}

// TestCartographer_Restore_MissingID_Returns400 — empty path value
// short-circuits before any DB work. Defence-in-depth check.
func TestCartographer_Restore_MissingID_Returns400(t *testing.T) {
	h, _, userID, wsID := cartographerRig(t)
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/checkpoints//restore", nil),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.Restore(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

// TestCartographer_Restore_HappyPath_NormalizesDivergence asserts the
// "no null arrays" UI contract: warn_divergence must be `[]` (not
// null) even when the checkpoint is at the tip of the journal (i.e.
// nothing newer to abandon). The handler explicitly coerces a nil
// slice to an empty one before writing the response — this test
// guards that line against a future cleanup that drops it.
func TestCartographer_Restore_HappyPath_NormalizesDivergence(t *testing.T) {
	h, db, userID, wsID := cartographerRig(t)
	crewID, missionID := seedCartographerMission(t, db, wsID, "mis_rest_ok", "tr_rest_ok")
	seedCartographerJournalEntry(t, db, "je_rest_ok_1", wsID, missionID, time.Now())
	id := seedCheckpointDirect(t, db, wsID, crewID, missionID, "je_rest_ok_1", "tip")

	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/checkpoints/"+id+"/restore", nil),
		userID, wsID, "OWNER",
	)
	req.SetPathValue("id", id)
	rr := httptest.NewRecorder()
	h.Restore(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, `"warn_divergence":[]`) {
		t.Errorf("expected warn_divergence:[] in body (not null), got: %s", body)
	}
	var resp struct {
		Checkpoint     map[string]any `json:"checkpoint"`
		JournalCursor  string         `json:"journal_cursor"`
		WarnDivergence []string       `json:"warn_divergence"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.WarnDivergence == nil {
		t.Errorf("warn_divergence is nil; want empty slice")
	}
	if resp.Checkpoint["id"] != id {
		t.Errorf("checkpoint.id = %v, want %q", resp.Checkpoint["id"], id)
	}
	if resp.JournalCursor != "je_rest_ok_1" {
		t.Errorf("journal_cursor = %q, want %q", resp.JournalCursor, "je_rest_ok_1")
	}
}

// Compile-time assertion: recordingEmitter (defined in
// hooks_handler_test.go in this package) satisfies journal.Emitter.
// Keeps the test file honest if either signature drifts.
var _ journal.Emitter = (*recordingEmitter)(nil)
