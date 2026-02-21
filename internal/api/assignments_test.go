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
	"testing"
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

	req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/assignments/does-not-exist", nil)
	req.SetPathValue("assignmentId", "does-not-exist")
	w := httptest.NewRecorder()

	h.Get(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
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

	req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/assignments/assign1", nil)
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

	h := NewAssignmentHandler(db, nil, nil, "token", logger)

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

	// Call runAssignment directly — it will fail at orchestrator (nil) but the run record should exist
	h.runAssignment(context.Background(), "assign-test", body, target, nil)

	// Verify agent_runs record was created with the target agent's ID
	var runAgentID, runStatus, runTrigger string
	err := db.QueryRowContext(context.Background(),
		`SELECT agent_id, status, trigger_type FROM agent_runs WHERE agent_id = ?`, "worker1",
	).Scan(&runAgentID, &runStatus, &runTrigger)
	if err != nil {
		t.Fatalf("expected agent_runs record for worker1, got error: %v", err)
	}
	if runAgentID != "worker1" {
		t.Errorf("expected agent_id=worker1, got %s", runAgentID)
	}
	if runTrigger != "ASSIGNMENT" {
		t.Errorf("expected trigger_type=ASSIGNMENT, got %s", runTrigger)
	}
	// Should be FAILED because orchestrator is nil
	if runStatus != "FAILED" {
		t.Errorf("expected status=FAILED (nil orchestrator), got %s", runStatus)
	}

	// Verify finished_at is set
	var finishedAt *string
	err = db.QueryRowContext(context.Background(),
		`SELECT finished_at FROM agent_runs WHERE agent_id = ?`, "worker1",
	).Scan(&finishedAt)
	if err != nil {
		t.Fatalf("query finished_at: %v", err)
	}
	if finishedAt == nil {
		t.Error("expected finished_at to be set for failed run")
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
