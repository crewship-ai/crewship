package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/orchestrator"
)

// execOrFatal is a helper that fails the test if a DB exec fails.
func execOrFatal(t *testing.T, db *sql.DB, query string, args ...interface{}) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), query, args...); err != nil {
		t.Fatalf("exec %q: %v", query[:min(len(query), 60)], err)
	}
}

func TestAssignmentGet_NotFound(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	h := NewAssignmentHandler(db, nil, nil, "token", logger)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/assignments/does-not-exist?workspace_id=ws-any", nil)
	req.SetPathValue("assignmentId", "does-not-exist")
	w := httptest.NewRecorder()

	h.Get(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

// #1040: workspace_id is now required — without it the handler cannot scope the
// row and must 400 rather than run an unscoped SELECT (the IDOR primitive).
func TestAssignmentGet_MissingWorkspace_400(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h := NewAssignmentHandler(db, nil, nil, "token", logger)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/assignments/assign1", nil)
	req.SetPathValue("assignmentId", "assign1")
	w := httptest.NewRecorder()
	h.Get(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 without workspace_id, got %d", w.Code)
	}
}

// #1040: an assignment id from another workspace must NOT be readable even with
// a valid (different) workspace_id — the cross-workspace IDOR.
func TestAssignmentGet_CrossWorkspace_404(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userA := seedTestUser(t, db)
	wsA := seedTestWorkspace(t, db, userA)
	ctx := context.Background()
	db.ExecContext(ctx, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crewA', ?, 'C', 'c')`, wsA)
	db.ExecContext(ctx, `INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('agA', 'crewA', ?, 'A', 'a')`, wsA)
	db.ExecContext(ctx, `INSERT INTO chats (id, agent_id, workspace_id, mode, status) VALUES ('chatA', 'agA', ?, 'CHAT', 'ACTIVE')`, wsA)
	if _, err := db.ExecContext(ctx,
		`INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, created_at)
		 VALUES ('assignA', ?, 'chatA', 'agA', 'agA', 'secret brief', 'PENDING', datetime('now'))`, wsA); err != nil {
		t.Fatalf("seed assignment: %v", err)
	}

	h := NewAssignmentHandler(db, nil, nil, "token", logger)
	// Caller bound to a DIFFERENT workspace requests A's assignment id.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/assignments/assignA?workspace_id=ws-other", nil)
	req.SetPathValue("assignmentId", "assignA")
	w := httptest.NewRecorder()
	h.Get(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("cross-workspace assignment must 404, got %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "secret brief") {
		t.Errorf("task brief leaked across workspaces: %s", w.Body.String())
	}
}

func TestAssignmentGet_Found(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	// Seed minimal data
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	_, err := db.ExecContext(context.Background(),
		`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew1', ?, 'Crew', 'crew')`, wsID)
	if err != nil {
		t.Fatalf("insert crew: %v", err)
	}
	_, err = db.ExecContext(context.Background(),
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('ag1', 'crew1', ?, 'A1', 'a1')`, wsID)
	if err != nil {
		t.Fatalf("insert agent: %v", err)
	}
	_, err = db.ExecContext(context.Background(),
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('ag2', 'crew1', ?, 'A2', 'a2')`, wsID)
	if err != nil {
		t.Fatalf("insert agent: %v", err)
	}
	_, err = db.ExecContext(context.Background(),
		`INSERT INTO chats (id, agent_id, workspace_id, mode, status) VALUES ('chat1', 'ag1', ?, 'CHAT', 'ACTIVE')`, wsID)
	if err != nil {
		t.Fatalf("insert chat: %v", err)
	}
	_, err = db.ExecContext(context.Background(),
		`INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, created_at)
		 VALUES ('assign1', ?, 'chat1', 'ag1', 'ag2', 'write code', 'PENDING', datetime('now'))`, wsID)
	if err != nil {
		t.Fatalf("insert assignment: %v", err)
	}

	h := NewAssignmentHandler(db, nil, nil, "token", logger)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/assignments/assign1?workspace_id="+wsID, nil)
	req.SetPathValue("assignmentId", "assign1")
	w := httptest.NewRecorder()

	h.Get(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d; body: %s", w.Code, w.Body.String())
	}
	var result map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if result["id"] != "assign1" {
		t.Errorf("expected id=assign1, got %v", result["id"])
	}
	if result["status"] != "PENDING" {
		t.Errorf("expected status=PENDING, got %v", result["status"])
	}
	if result["task"] != "write code" {
		t.Errorf("expected task=write code, got %v", result["task"])
	}
}

func TestAssignmentList_Empty(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	execOrFatal(t, db,
		`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew1', ?, 'Crew', 'crew')`, wsID)

	h := NewAssignmentHandler(db, nil, nil, "token", logger)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/crews/crew1/assignments?workspace_id="+wsID, nil)
	req.SetPathValue("crewId", "crew1")
	ctx := context.WithValue(req.Context(), ctxWorkspaceID, wsID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.List(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", w.Code, w.Body.String())
	}
	var result []map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected 0 assignments, got %d", len(result))
	}
}

func TestAssignmentList_ReturnsCrewAssignments(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	// Seed two crews
	execOrFatal(t, db,
		`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew1', ?, 'Alpha', 'alpha')`, wsID)
	execOrFatal(t, db,
		`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew2', ?, 'Beta', 'beta')`, wsID)

	// Agents in crew1
	execOrFatal(t, db,
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('ag1', 'crew1', ?, 'Lead', 'lead')`, wsID)
	execOrFatal(t, db,
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('ag2', 'crew1', ?, 'Worker', 'worker')`, wsID)

	// Agent in crew2
	execOrFatal(t, db,
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('ag3', 'crew2', ?, 'Other', 'other')`, wsID)

	// Chats
	execOrFatal(t, db,
		`INSERT INTO chats (id, agent_id, workspace_id, mode, status) VALUES ('chat1', 'ag1', ?, 'CHAT', 'ACTIVE')`, wsID)
	execOrFatal(t, db,
		`INSERT INTO chats (id, agent_id, workspace_id, mode, status) VALUES ('chat2', 'ag3', ?, 'CHAT', 'ACTIVE')`, wsID)

	// Assignment in crew1
	execOrFatal(t, db,
		`INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, created_at)
		 VALUES ('a1', ?, 'chat1', 'ag1', 'ag2', 'write tests', 'COMPLETED', '2025-01-01T00:00:00Z')`, wsID)

	// Assignment in crew2 (should NOT appear for crew1)
	execOrFatal(t, db,
		`INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, created_at)
		 VALUES ('a2', ?, 'chat2', 'ag3', 'ag3', 'other task', 'PENDING', '2025-01-02T00:00:00Z')`, wsID)

	h := NewAssignmentHandler(db, nil, nil, "token", logger)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/crews/crew1/assignments?workspace_id="+wsID, nil)
	req.SetPathValue("crewId", "crew1")
	ctx := context.WithValue(req.Context(), ctxWorkspaceID, wsID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.List(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", w.Code, w.Body.String())
	}
	var result []map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 assignment for crew1, got %d", len(result))
	}
	if result[0]["id"] != "a1" {
		t.Errorf("expected id=a1, got %v", result[0]["id"])
	}
	if result[0]["task"] != "write tests" {
		t.Errorf("expected task='write tests', got %v", result[0]["task"])
	}
	if result[0]["assigned_by_name"] != "Lead" {
		t.Errorf("expected assigned_by_name=Lead, got %v", result[0]["assigned_by_name"])
	}
	if result[0]["assigned_to_slug"] != "worker" {
		t.Errorf("expected assigned_to_slug=worker, got %v", result[0]["assigned_to_slug"])
	}
}

func TestAssignmentList_Pagination(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	execOrFatal(t, db,
		`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew1', ?, 'C', 'c')`, wsID)
	execOrFatal(t, db,
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('ag1', 'crew1', ?, 'A1', 'a1')`, wsID)
	execOrFatal(t, db,
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('ag2', 'crew1', ?, 'A2', 'a2')`, wsID)
	execOrFatal(t, db,
		`INSERT INTO chats (id, agent_id, workspace_id, mode, status) VALUES ('chat1', 'ag1', ?, 'CHAT', 'ACTIVE')`, wsID)

	// Insert 3 assignments
	for i := 1; i <= 3; i++ {
		execOrFatal(t, db,
			`INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, created_at)
			 VALUES (?, ?, 'chat1', 'ag1', 'ag2', ?, 'PENDING', ?)`,
			fmt.Sprintf("pa%d", i), wsID, fmt.Sprintf("task %d", i),
			fmt.Sprintf("2025-01-%02dT00:00:00Z", i))
	}

	h := NewAssignmentHandler(db, nil, nil, "token", logger)

	// Fetch with limit=2
	req := httptest.NewRequest(http.MethodGet, "/api/v1/crews/crew1/assignments?workspace_id="+wsID+"&limit=2", nil)
	req.SetPathValue("crewId", "crew1")
	ctx := context.WithValue(req.Context(), ctxWorkspaceID, wsID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.List(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", w.Code, w.Body.String())
	}
	var result []map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result) != 2 {
		t.Errorf("expected 2 assignments with limit=2, got %d", len(result))
	}

	// Fetch with offset=2
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/crews/crew1/assignments?workspace_id="+wsID+"&limit=2&offset=2", nil)
	req2.SetPathValue("crewId", "crew1")
	ctx2 := context.WithValue(req2.Context(), ctxWorkspaceID, wsID)
	req2 = req2.WithContext(ctx2)
	w2 := httptest.NewRecorder()

	h.List(w2, req2)

	var result2 []map[string]interface{}
	if err := json.NewDecoder(w2.Body).Decode(&result2); err != nil {
		t.Fatalf("decode offset response: %v", err)
	}
	if len(result2) != 1 {
		t.Errorf("expected 1 assignment with offset=2, got %d", len(result2))
	}
}

func TestRunAssignment_CreatesAgentRunRecord(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	execOrFatal(t, db,
		`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew1', ?, 'Eng', 'eng')`, wsID)
	execOrFatal(t, db,
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('lead1', 'crew1', ?, 'Tomas', 'tomas')`, wsID)
	execOrFatal(t, db,
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('worker1', 'crew1', ?, 'Viktor', 'viktor')`, wsID)
	execOrFatal(t, db,
		`INSERT INTO chats (id, agent_id, workspace_id, mode, status) VALUES ('chat1', 'lead1', ?, 'CHAT', 'ACTIVE')`, wsID)
	// The assignment row must exist: finishAssignment's terminal CAS
	// refuses to emit completion signals for a row it could not
	// transition (guard against late drivers overwriting swept rows).
	insertAssignment(t, db, "assign-test", wsID, "chat1", "lead1", "worker1", "PENDING")

	h := NewAssignmentHandler(db, nil, nil, "token", logger)
	// Wire a real journal writer so runAssignment's emits land in DB
	// before we read them back.
	jw := journal.NewWriter(db, newTestLogger(), journal.WriterOptions{FlushSize: 1})
	t.Cleanup(func() { _ = jw.Close() })
	h.SetJournal(jw)

	body := createAssignmentBody{
		TargetSlug:  "viktor",
		Task:        "create dummy.md",
		CrewID:      "crew1",
		WorkspaceID: wsID,
		ChatID:      "chat1",
	}
	target := targetAgentInfo{
		ID:       "worker1",
		Slug:     "viktor",
		Name:     "Viktor",
		CrewSlug: "eng",
	}

	// Call runAssignment directly — it will fail at orchestrator (nil) but the journal entries should exist
	h.runAssignment(context.Background(), "assign-test", body, target)
	_ = jw.Flush(context.Background())

	// Verify run.started + run.failed journal entries exist with the target agent.
	var traceID, agentID, entryType string
	var startedPayload string
	err := db.QueryRowContext(context.Background(),
		`SELECT trace_id, agent_id, entry_type, payload FROM journal_entries
		 WHERE agent_id = ? AND entry_type = 'run.started'`, "worker1",
	).Scan(&traceID, &agentID, &entryType, &startedPayload)
	if err != nil {
		t.Fatalf("expected run.started journal entry for worker1, got error: %v", err)
	}
	if agentID != "worker1" {
		t.Errorf("expected agent_id=worker1, got %s", agentID)
	}
	// trigger_type lives inside the started payload now, not on a row column.
	if !strings.Contains(startedPayload, `"trigger_type":"ASSIGNMENT"`) {
		t.Errorf("expected trigger_type ASSIGNMENT in payload, got %s", startedPayload)
	}

	// Should have a run.failed terminal entry because orchestrator is nil.
	var terminalType string
	err = db.QueryRowContext(context.Background(),
		`SELECT entry_type FROM journal_entries
		 WHERE trace_id = ? AND entry_type IN ('run.completed','run.failed','run.cancelled','run.timeout')`, traceID,
	).Scan(&terminalType)
	if err != nil {
		t.Fatalf("expected terminal run entry for trace %s: %v", traceID, err)
	}
	if terminalType != "run.failed" {
		t.Errorf("expected run.failed (nil orchestrator), got %s", terminalType)
	}
}

func TestAssignmentCreate_MissingFields(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	h := NewAssignmentHandler(db, nil, nil, "token", logger)

	body := bytes.NewBufferString(`{"target_slug":"viktor","task":"do something"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/assignments", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.Create(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestAssignmentCreate_ChatNotFound(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	h := NewAssignmentHandler(db, nil, nil, "token", logger)

	body := bytes.NewBufferString(`{"target_slug":"viktor","task":"do","crew_id":"c1","workspace_id":"w1","chat_id":"nonexistent"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/assignments", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.Create(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestAssignmentCreate_TargetNotFound(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	db.ExecContext(context.Background(),
		`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew1', ?, 'C', 'c')`, wsID)
	db.ExecContext(context.Background(),
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('ag1', 'crew1', ?, 'Lead', 'lead')`, wsID)
	db.ExecContext(context.Background(),
		`INSERT INTO chats (id, agent_id, workspace_id, mode, status) VALUES ('chat1', 'ag1', ?, 'CHAT', 'ACTIVE')`, wsID)

	h := NewAssignmentHandler(db, nil, nil, "token", logger)

	body := bytes.NewBufferString(`{"target_slug":"nonexistent","task":"do","crew_id":"crew1","workspace_id":"` + wsID + `","chat_id":"chat1"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/assignments", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.Create(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

// TestAssignmentCreate_HeldTargetIsNotRun closes the OTHER half of the
// PENDING_REVIEW sentinel (#1768).
//
// internal_status.go stages an agent-created agent as PENDING_REVIEW and says
// it "cannot serve a single message until an operator approves". Only
// chatbridge honoured that; /assign never read agents.status, so a lead could
// hand work to a held agent and it ran — a pre-existing hole that the agent
// creation gate newly made reachable, since the agent whose prompt another
// agent wrote is exactly the one being held.
//
// The assertion is on the ROW and the run, not on the status column, and the
// mutation is the approval: flip the target to IDLE and the identical request
// is accepted.
func TestAssignmentCreate_HeldTargetIsNotRun(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crewH', ?, 'C', 'c')`, wsID)
	execOrFatal(t, db,
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug, status) VALUES ('leadH', 'crewH', ?, 'Lead', 'leadh', 'IDLE')`, wsID)
	execOrFatal(t, db,
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug, status) VALUES ('heldH', 'crewH', ?, 'Held', 'heldh', 'PENDING_REVIEW')`, wsID)
	execOrFatal(t, db,
		`INSERT INTO chats (id, agent_id, workspace_id, mode, status) VALUES ('chatH', 'leadH', ?, 'CHAT', 'ACTIVE')`, wsID)

	h := NewAssignmentHandler(db, nil, nil, "token", logger)
	t.Cleanup(h.WaitDispatches)

	post := func() *httptest.ResponseRecorder {
		body := bytes.NewBufferString(`{"target_slug":"heldh","task":"do the thing","crew_id":"crewH",` +
			`"workspace_id":"` + wsID + `","chat_id":"chatH"}`)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/assignments", body)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.Create(w, req)
		h.WaitDispatches()
		return w
	}

	w := post()
	if w.Code == http.StatusCreated {
		t.Fatalf("Create returned 201 for a PENDING_REVIEW target — the held agent was given work")
	}
	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want %d; body=%s", w.Code, http.StatusConflict, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "PENDING_REVIEW") {
		t.Errorf("refusal %q does not name the status that held the agent", w.Body.String())
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM assignments WHERE assigned_to_id = 'heldH'`).Scan(&n); err != nil {
		t.Fatalf("count assignments: %v", err)
	}
	if n != 0 {
		t.Fatalf("assignments to the held agent = %d, want 0", n)
	}

	// MUTATION: approve it and the identical request is accepted.
	execOrFatal(t, db, `UPDATE agents SET status = 'IDLE' WHERE id = 'heldH'`)
	if w := post(); w.Code != http.StatusCreated {
		t.Errorf("status after approval = %d, want 201; body=%s — the guard refuses unconditionally",
			w.Code, w.Body.String())
	}
}

// TestDispatchAssignment_HeldTargetIsRefused covers the third door into
// runAssignment: the mission engine's task list.
//
// This file's NOTE says DispatchAssignment carries no dispatch decision because
// the row already exists. That is true of the delegation CAPS and false of the
// hold — a plan can name an agent an operator has not approved, and the
// approval is the only thing standing between that agent's self-written system
// prompt and a container.
//
// The mission engine now refuses a hold it can SEE before writing anything
// (orchestrator/agent_hold.go); this door catches the one staged in the race
// window between that read and the dispatch. The error must be a DEFERRAL, not
// a plain failure — the engine reads that marker to decide between "this task
// is dead" and "wait for the operator", and getting it wrong is what broke the
// guided ephemeral hire.
func TestDispatchAssignment_HeldTargetIsRefused(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crewM', ?, 'C', 'cm')`, wsID)
	execOrFatal(t, db,
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug, status) VALUES ('heldM', 'crewM', ?, 'Held', 'heldm', 'PENDING_REVIEW')`, wsID)

	h := NewAssignmentHandler(db, nil, nil, "token", logger)
	t.Cleanup(h.WaitDispatches)

	err := h.DispatchAssignment(context.Background(), orchestrator.DispatchRequest{
		AssignmentID: "asg-held",
		AgentID:      "heldM",
		AgentSlug:    "heldm",
		CrewID:       "crewM",
		WorkspaceID:  wsID,
		ChatID:       "chatM",
		Task:         "do the thing",
	})
	if err == nil {
		t.Fatal("DispatchAssignment ran a PENDING_REVIEW agent")
	}
	if !strings.Contains(err.Error(), "PENDING_REVIEW") {
		t.Errorf("error = %q, want the hold named so the waiting task says why", err)
	}
	if _, ok := err.(interface{ DispatchDeferred() }); !ok {
		t.Errorf("error is %T, which does not carry DispatchDeferred — the mission engine will "+
			"read this hold as a terminal failure and an approved hire will never run", err)
	}

	// MUTATION: approve the agent and the identical dispatch is admitted past
	// the hold. (It goes on to fail on the missing crew/chat fixtures, which is
	// a different error — the assertion is that it is no longer the hold.)
	execOrFatal(t, db, `UPDATE agents SET status = 'IDLE' WHERE id = 'heldM'`)
	err = h.DispatchAssignment(context.Background(), orchestrator.DispatchRequest{
		AssignmentID: "asg-held",
		AgentID:      "heldM",
		AgentSlug:    "heldm",
		CrewID:       "crewM",
		WorkspaceID:  wsID,
		ChatID:       "chatM",
		Task:         "do the thing",
	})
	if err != nil && strings.Contains(err.Error(), "PENDING_REVIEW") {
		t.Errorf("still refused after approval: %v — the guard refuses unconditionally", err)
	}
}
