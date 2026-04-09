package api

import (
	"bytes"
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
)

func TestQueryCreate_MissingFields(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	h := NewQueryHandler(db, nil, nil, "token", logger)

	body := bytes.NewBufferString(`{"target_slug":"nela","question":"hello?"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/queries", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.Create(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestQueryCreate_TargetNotFound(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	execOrFatal(t, db,
		`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew1', ?, 'Crew', 'crew')`, wsID)
	execOrFatal(t, db,
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('ag1', 'crew1', ?, 'Lead', 'lead')`, wsID)
	execOrFatal(t, db,
		`INSERT INTO chats (id, agent_id, workspace_id, mode, status) VALUES ('chat1', 'ag1', ?, 'CHAT', 'ACTIVE')`, wsID)

	h := NewQueryHandler(db, nil, nil, "token", logger)

	body := bytes.NewBufferString(`{"target_slug":"nonexistent","question":"hello?","from_slug":"lead","crew_id":"crew1","workspace_id":"` + wsID + `","chat_id":"chat1"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/queries", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.Create(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestQueryCreate_NilOrchestrator(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	execOrFatal(t, db,
		`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew1', ?, 'Eng', 'eng')`, wsID)
	execOrFatal(t, db,
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('ag1', 'crew1', ?, 'Viktor', 'viktor')`, wsID)
	execOrFatal(t, db,
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('ag2', 'crew1', ?, 'Nela', 'nela')`, wsID)
	execOrFatal(t, db,
		`INSERT INTO chats (id, agent_id, workspace_id, mode, status) VALUES ('chat1', 'ag1', ?, 'CHAT', 'ACTIVE')`, wsID)

	h := NewQueryHandler(db, nil, nil, "token", logger)

	body := bytes.NewBufferString(`{"target_slug":"nela","question":"What CSS framework?","from_slug":"viktor","crew_id":"crew1","workspace_id":"` + wsID + `","chat_id":"chat1"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/queries", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.Create(w, req)

	// Should fail with 503 since orchestrator is nil
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d; body: %s", w.Code, w.Body.String())
	}

	// Verify peer_conversations record was created and marked FAILED
	var convStatus string
	err := db.QueryRowContext(context.Background(),
		`SELECT status FROM peer_conversations WHERE to_agent_id = ?`, "ag2",
	).Scan(&convStatus)
	if err != nil {
		t.Fatalf("expected peer_conversations record, got error: %v", err)
	}
	if convStatus != "FAILED" {
		t.Errorf("expected status=FAILED (nil orchestrator), got %s", convStatus)
	}
}

func TestListPeerConversations_Empty(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	execOrFatal(t, db,
		`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew1', ?, 'Crew', 'crew')`, wsID)

	h := NewQueryHandler(db, nil, nil, "token", logger)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/crews/crew1/peer-conversations", nil)
	req.SetPathValue("crewId", "crew1")
	ctx := context.WithValue(req.Context(), ctxWorkspaceID, wsID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.ListPeerConversations(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", w.Code, w.Body.String())
	}
	var result []map[string]interface{}
	json.NewDecoder(w.Body).Decode(&result)
	if len(result) != 0 {
		t.Errorf("expected 0 conversations, got %d", len(result))
	}
}

func TestListPeerConversations_ReturnsData(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	execOrFatal(t, db,
		`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew1', ?, 'Eng', 'eng')`, wsID)
	execOrFatal(t, db,
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('ag1', 'crew1', ?, 'Viktor', 'viktor')`, wsID)
	execOrFatal(t, db,
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('ag2', 'crew1', ?, 'Nela', 'nela')`, wsID)
	execOrFatal(t, db,
		`INSERT INTO chats (id, agent_id, workspace_id, mode, status) VALUES ('chat1', 'ag1', ?, 'CHAT', 'ACTIVE')`, wsID)
	execOrFatal(t, db,
		`INSERT INTO peer_conversations (id, workspace_id, crew_id, chat_id, from_agent_id, to_agent_id, question, response, status, created_at)
		 VALUES ('pc1', ?, 'crew1', 'chat1', 'ag1', 'ag2', 'What framework?', 'Tailwind', 'COMPLETED', '2025-01-01T12:00:00Z')`, wsID)

	h := NewQueryHandler(db, nil, nil, "token", logger)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/crews/crew1/peer-conversations", nil)
	req.SetPathValue("crewId", "crew1")
	ctx := context.WithValue(req.Context(), ctxWorkspaceID, wsID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.ListPeerConversations(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", w.Code, w.Body.String())
	}
	var result []map[string]interface{}
	json.NewDecoder(w.Body).Decode(&result)
	if len(result) != 1 {
		t.Fatalf("expected 1 conversation, got %d", len(result))
	}
	if result[0]["from_slug"] != "viktor" {
		t.Errorf("expected from_slug=viktor, got %v", result[0]["from_slug"])
	}
	if result[0]["to_slug"] != "nela" {
		t.Errorf("expected to_slug=nela, got %v", result[0]["to_slug"])
	}
	if result[0]["question"] != "What framework?" {
		t.Errorf("expected question='What framework?', got %v", result[0]["question"])
	}
}

func TestCreateEscalation_MissingFields(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	h := NewQueryHandler(db, nil, nil, "token", logger)

	body := bytes.NewBufferString(`{"from_slug":"nela","reason":"conflict"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/escalations", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.CreateEscalation(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestCreateEscalation_AgentNotFound(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	execOrFatal(t, db,
		`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew1', ?, 'Crew', 'crew')`, wsID)

	h := NewQueryHandler(db, nil, nil, "token", logger)

	body := bytes.NewBufferString(`{"from_slug":"nonexistent","reason":"conflict","crew_id":"crew1","workspace_id":"` + wsID + `","chat_id":"chat1"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/escalations", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.CreateEscalation(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestCreateEscalation_Success(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	execOrFatal(t, db,
		`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew1', ?, 'Eng', 'eng')`, wsID)
	execOrFatal(t, db,
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('ag1', 'crew1', ?, 'Nela', 'nela')`, wsID)
	execOrFatal(t, db,
		`INSERT INTO chats (id, agent_id, workspace_id, mode, status) VALUES ('chat1', 'ag1', ?, 'CHAT', 'ACTIVE')`, wsID)

	h := NewQueryHandler(db, nil, nil, "token", logger)

	body := bytes.NewBufferString(`{"from_slug":"nela","reason":"API conflict","context":"Viktor changed endpoints","crew_id":"crew1","workspace_id":"` + wsID + `","chat_id":"chat1"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/escalations", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.CreateEscalation(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d; body: %s", w.Code, w.Body.String())
	}

	var result map[string]string
	json.NewDecoder(w.Body).Decode(&result)
	if result["status"] != "PENDING" {
		t.Errorf("expected status=PENDING, got %v", result["status"])
	}
	if result["escalation_id"] == "" {
		t.Error("expected non-empty escalation_id")
	}

	// Verify DB record
	var reason, status string
	err := db.QueryRowContext(context.Background(),
		`SELECT reason, status FROM escalations WHERE from_agent_id = ?`, "ag1",
	).Scan(&reason, &status)
	if err != nil {
		t.Fatalf("expected escalation record, got error: %v", err)
	}
	if reason != "API conflict" {
		t.Errorf("expected reason='API conflict', got %s", reason)
	}
	if status != "PENDING" {
		t.Errorf("expected status=PENDING, got %s", status)
	}
}

func TestListEscalations_Empty(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	execOrFatal(t, db,
		`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew1', ?, 'Crew', 'crew')`, wsID)

	h := NewQueryHandler(db, nil, nil, "token", logger)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/crews/crew1/escalations", nil)
	req.SetPathValue("crewId", "crew1")
	ctx := context.WithValue(req.Context(), ctxWorkspaceID, wsID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.ListEscalations(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var result []map[string]interface{}
	json.NewDecoder(w.Body).Decode(&result)
	if len(result) != 0 {
		t.Errorf("expected 0 escalations, got %d", len(result))
	}
}

func TestListEscalations_ReturnsData(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	execOrFatal(t, db,
		`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew1', ?, 'Eng', 'eng')`, wsID)
	execOrFatal(t, db,
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('ag1', 'crew1', ?, 'Nela', 'nela')`, wsID)
	execOrFatal(t, db,
		`INSERT INTO escalations (id, workspace_id, crew_id, chat_id, from_agent_id, reason, context, status, created_at)
		 VALUES ('esc1', ?, 'crew1', 'chat1', 'ag1', 'API conflict', 'Details here', 'PENDING', '2025-01-01T15:00:00Z')`, wsID)

	h := NewQueryHandler(db, nil, nil, "token", logger)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/crews/crew1/escalations", nil)
	req.SetPathValue("crewId", "crew1")
	ctx := context.WithValue(req.Context(), ctxWorkspaceID, wsID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.ListEscalations(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var result []map[string]interface{}
	json.NewDecoder(w.Body).Decode(&result)
	if len(result) != 1 {
		t.Fatalf("expected 1 escalation, got %d", len(result))
	}
	if result[0]["from_slug"] != "nela" {
		t.Errorf("expected from_slug=nela, got %v", result[0]["from_slug"])
	}
	if result[0]["reason"] != "API conflict" {
		t.Errorf("expected reason='API conflict', got %v", result[0]["reason"])
	}
	if result[0]["status"] != "PENDING" {
		t.Errorf("expected status=PENDING, got %v", result[0]["status"])
	}
}

func TestResolveEscalation_NotFound(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	h := NewQueryHandler(db, nil, nil, "token", logger)

	body := strings.NewReader(`{"resolution":"done"}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/escalations/nonexistent/resolve", body)
	req.SetPathValue("escalationId", "nonexistent")
	ctx := context.WithValue(context.WithValue(req.Context(), ctxWorkspaceID, wsID), ctxRole, "OWNER")
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.ResolveEscalation(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestResolveEscalation_Success(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	execOrFatal(t, db,
		`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew1', ?, 'Eng', 'eng')`, wsID)
	execOrFatal(t, db,
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('ag1', 'crew1', ?, 'Nela', 'nela')`, wsID)
	execOrFatal(t, db,
		`INSERT INTO escalations (id, workspace_id, crew_id, chat_id, from_agent_id, reason, status, created_at)
		 VALUES ('esc1', ?, 'crew1', 'chat1', 'ag1', 'Need GitHub token', 'PENDING', '2025-01-01T15:00:00Z')`, wsID)

	h := NewQueryHandler(db, nil, nil, "token", logger)

	body := strings.NewReader(`{"resolution":"Here is the token: done"}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/escalations/esc1/resolve", body)
	req.SetPathValue("escalationId", "esc1")
	ctx := context.WithValue(context.WithValue(req.Context(), ctxWorkspaceID, wsID), ctxRole, "OWNER")
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.ResolveEscalation(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var result map[string]string
	json.NewDecoder(w.Body).Decode(&result)
	if result["status"] != "RESOLVED" {
		t.Errorf("expected RESOLVED, got %s", result["status"])
	}

	// Verify DB
	var status, resolution string
	err := db.QueryRowContext(context.Background(),
		`SELECT status, resolution FROM escalations WHERE id = 'esc1'`,
	).Scan(&status, &resolution)
	if err != nil {
		t.Fatalf("DB query error: %v", err)
	}
	if status != "RESOLVED" {
		t.Errorf("expected status=RESOLVED, got %s", status)
	}
	if resolution != "Here is the token: done" {
		t.Errorf("expected resolution text, got %s", resolution)
	}
}

func TestResolveEscalation_AlreadyResolved(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	execOrFatal(t, db,
		`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew1', ?, 'Eng', 'eng')`, wsID)
	execOrFatal(t, db,
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('ag1', 'crew1', ?, 'Nela', 'nela')`, wsID)
	execOrFatal(t, db,
		`INSERT INTO escalations (id, workspace_id, crew_id, chat_id, from_agent_id, reason, status, resolution, resolved_at, created_at)
		 VALUES ('esc1', ?, 'crew1', 'chat1', 'ag1', 'Old issue', 'RESOLVED', 'Fixed', '2025-01-01T16:00:00Z', '2025-01-01T15:00:00Z')`, wsID)

	h := NewQueryHandler(db, nil, nil, "token", logger)

	body := strings.NewReader(`{"resolution":"try again"}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/escalations/esc1/resolve", body)
	req.SetPathValue("escalationId", "esc1")
	ctx := context.WithValue(context.WithValue(req.Context(), ctxWorkspaceID, wsID), ctxRole, "OWNER")
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.ResolveEscalation(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestResolveEscalation_MissingResolution(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	h := NewQueryHandler(db, nil, nil, "token", logger)

	body := strings.NewReader(`{}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/escalations/esc1/resolve", body)
	req.SetPathValue("escalationId", "esc1")
	ctx := context.WithValue(context.WithValue(req.Context(), ctxWorkspaceID, wsID), ctxRole, "OWNER")
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.ResolveEscalation(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestResolveEscalation_WithAction(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	execOrFatal(t, db,
		`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew1', ?, 'Eng', 'eng')`, wsID)
	execOrFatal(t, db,
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('ag1', 'crew1', ?, 'Nela', 'nela')`, wsID)
	execOrFatal(t, db,
		`INSERT INTO escalations (id, workspace_id, crew_id, chat_id, from_agent_id, reason, status, created_at)
		 VALUES ('esc1', ?, 'crew1', 'chat1', 'ag1', 'Need approval', 'PENDING', '2025-01-01T15:00:00Z')`, wsID)

	h := NewQueryHandler(db, nil, nil, "token", logger)

	body := strings.NewReader(`{"resolution":"Rejected due to security concerns","action":"reject"}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/escalations/esc1/resolve", body)
	req.SetPathValue("escalationId", "esc1")
	ctx := context.WithValue(context.WithValue(req.Context(), ctxWorkspaceID, wsID), ctxRole, "OWNER")
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.ResolveEscalation(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Verify action stored in DB
	var action string
	err := db.QueryRowContext(context.Background(),
		`SELECT action FROM escalations WHERE id = 'esc1'`,
	).Scan(&action)
	if err != nil {
		t.Fatalf("DB query error: %v", err)
	}
	if action != "reject" {
		t.Errorf("expected action=reject, got %s", action)
	}
}

func TestResolveEscalation_RedirectAction(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	execOrFatal(t, db,
		`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew1', ?, 'Eng', 'eng')`, wsID)
	execOrFatal(t, db,
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('ag1', 'crew1', ?, 'Nela', 'nela')`, wsID)
	execOrFatal(t, db,
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('ag2', 'crew1', ?, 'Viktor', 'viktor')`, wsID)
	execOrFatal(t, db,
		`INSERT INTO escalations (id, workspace_id, crew_id, chat_id, from_agent_id, reason, status, created_at)
		 VALUES ('esc1', ?, 'crew1', 'chat1', 'ag1', 'Wrong agent', 'PENDING', '2025-01-01T15:00:00Z')`, wsID)

	h := NewQueryHandler(db, nil, nil, "token", logger)

	body := strings.NewReader(`{"resolution":"Viktor should handle this","action":"redirect","redirect_to":"viktor"}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/escalations/esc1/resolve", body)
	req.SetPathValue("escalationId", "esc1")
	ctx := context.WithValue(context.WithValue(req.Context(), ctxWorkspaceID, wsID), ctxRole, "OWNER")
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.ResolveEscalation(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Verify action and redirect_to stored in DB
	var action string
	var redirectTo sql.NullString
	err := db.QueryRowContext(context.Background(),
		`SELECT action, redirect_to FROM escalations WHERE id = 'esc1'`,
	).Scan(&action, &redirectTo)
	if err != nil {
		t.Fatalf("DB query error: %v", err)
	}
	if action != "redirect" {
		t.Errorf("expected action=redirect, got %s", action)
	}
	if !redirectTo.Valid || redirectTo.String != "viktor" {
		t.Errorf("expected redirect_to=viktor, got %v", redirectTo)
	}
}

func TestResolveEscalation_InvalidAction(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	execOrFatal(t, db,
		`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew1', ?, 'Eng', 'eng')`, wsID)
	execOrFatal(t, db,
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('ag1', 'crew1', ?, 'Nela', 'nela')`, wsID)
	execOrFatal(t, db,
		`INSERT INTO escalations (id, workspace_id, crew_id, chat_id, from_agent_id, reason, status, created_at)
		 VALUES ('esc1', ?, 'crew1', 'chat1', 'ag1', 'Need help', 'PENDING', '2025-01-01T15:00:00Z')`, wsID)

	h := NewQueryHandler(db, nil, nil, "token", logger)

	body := strings.NewReader(`{"resolution":"test","action":"invalid"}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/escalations/esc1/resolve", body)
	req.SetPathValue("escalationId", "esc1")
	ctx := context.WithValue(context.WithValue(req.Context(), ctxWorkspaceID, wsID), ctxRole, "OWNER")
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.ResolveEscalation(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestResolveEscalation_RedirectToNonexistentAgent(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	execOrFatal(t, db,
		`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew1', ?, 'Eng', 'eng')`, wsID)
	execOrFatal(t, db,
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('ag1', 'crew1', ?, 'Nela', 'nela')`, wsID)
	execOrFatal(t, db,
		`INSERT INTO escalations (id, workspace_id, crew_id, chat_id, from_agent_id, reason, status, created_at)
		 VALUES ('esc1', ?, 'crew1', 'chat1', 'ag1', 'Wrong agent', 'PENDING', '2025-01-01T15:00:00Z')`, wsID)

	h := NewQueryHandler(db, nil, nil, "token", logger)

	body := strings.NewReader(`{"resolution":"Send to ghost","action":"redirect","redirect_to":"nonexistent"}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/escalations/esc1/resolve", body)
	req.SetPathValue("escalationId", "esc1")
	ctx := context.WithValue(context.WithValue(req.Context(), ctxWorkspaceID, wsID), ctxRole, "OWNER")
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.ResolveEscalation(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for nonexistent redirect target, got %d: %s", w.Code, w.Body.String())
	}
}

func TestResolveEscalation_NotifiesWaiter(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	execOrFatal(t, db,
		`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew1', ?, 'Eng', 'eng')`, wsID)
	execOrFatal(t, db,
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('ag1', 'crew1', ?, 'Nela', 'nela')`, wsID)
	execOrFatal(t, db,
		`INSERT INTO escalations (id, workspace_id, crew_id, chat_id, from_agent_id, reason, status, created_at)
		 VALUES ('esc1', ?, 'crew1', 'chat1', 'ag1', 'Need help', 'PENDING', '2025-01-01T15:00:00Z')`, wsID)

	h := NewQueryHandler(db, nil, nil, "token", logger)

	// Register a waiter before resolving
	ch := h.registerEscalationWaiter("esc1")

	// Resolve in a goroutine
	done := make(chan struct{})
	go func() {
		defer close(done)
		body := strings.NewReader(`{"resolution":"Approved","action":"approve"}`)
		req := httptest.NewRequest(http.MethodPatch, "/api/v1/escalations/esc1/resolve", body)
		req.SetPathValue("escalationId", "esc1")
		ctx := context.WithValue(context.WithValue(req.Context(), ctxWorkspaceID, wsID), ctxRole, "OWNER")
		req = req.WithContext(ctx)
		w := httptest.NewRecorder()
		h.ResolveEscalation(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
		}
	}()

	// Wait for the notification
	select {
	case result := <-ch:
		if result.Resolution != "Approved" {
			t.Errorf("expected resolution=Approved, got %s", result.Resolution)
		}
		if result.Action != "approve" {
			t.Errorf("expected action=approve, got %s", result.Action)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for escalation notification")
	}

	<-done
}

func TestWaitForEscalationResponse_ResolvesBeforeTimeout(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	execOrFatal(t, db,
		`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew1', ?, 'Eng', 'eng')`, wsID)
	execOrFatal(t, db,
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('ag1', 'crew1', ?, 'Nela', 'nela')`, wsID)
	execOrFatal(t, db,
		`INSERT INTO escalations (id, workspace_id, crew_id, chat_id, from_agent_id, reason, status, created_at)
		 VALUES ('esc1', ?, 'crew1', 'chat1', 'ag1', 'Need approval', 'PENDING', '2025-01-01T15:00:00Z')`, wsID)

	h := NewQueryHandler(db, nil, nil, "token", logger)

	// Start wait in goroutine
	var waitCode int
	var waitBody map[string]interface{}
	waitDone := make(chan struct{})
	go func() {
		defer close(waitDone)
		req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/escalations/esc1/wait", nil)
		req.SetPathValue("escalationId", "esc1")
		w := httptest.NewRecorder()
		h.WaitForEscalationResponse(w, req)
		waitCode = w.Code
		json.NewDecoder(w.Body).Decode(&waitBody)
	}()

	// Give waiter time to register
	time.Sleep(50 * time.Millisecond)

	// Resolve the escalation
	body := strings.NewReader(`{"resolution":"Go ahead","action":"approve"}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/escalations/esc1/resolve", body)
	req.SetPathValue("escalationId", "esc1")
	ctx := context.WithValue(context.WithValue(req.Context(), ctxWorkspaceID, wsID), ctxRole, "OWNER")
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()
	h.ResolveEscalation(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("resolve failed: %d: %s", w.Code, w.Body.String())
	}

	// Wait for the waiter to finish
	select {
	case <-waitDone:
	case <-time.After(5 * time.Second):
		t.Fatal("wait handler didn't return")
	}

	if waitCode != http.StatusOK {
		t.Errorf("expected wait 200, got %d", waitCode)
	}
	if waitBody["resolution"] != "Go ahead" {
		t.Errorf("expected resolution='Go ahead', got %v", waitBody["resolution"])
	}
	if waitBody["action"] != "approve" {
		t.Errorf("expected action=approve, got %v", waitBody["action"])
	}
}

func TestWaitForEscalationResponse_AlreadyResolved(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	execOrFatal(t, db,
		`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew1', ?, 'Eng', 'eng')`, wsID)
	execOrFatal(t, db,
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('ag1', 'crew1', ?, 'Nela', 'nela')`, wsID)
	execOrFatal(t, db,
		`INSERT INTO escalations (id, workspace_id, crew_id, chat_id, from_agent_id, reason, status, resolution, action, resolved_at, created_at)
		 VALUES ('esc1', ?, 'crew1', 'chat1', 'ag1', 'Old issue', 'RESOLVED', 'Already done', 'approve', '2025-01-01T16:00:00Z', '2025-01-01T15:00:00Z')`, wsID)

	h := NewQueryHandler(db, nil, nil, "token", logger)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/escalations/esc1/wait", nil)
	req.SetPathValue("escalationId", "esc1")
	w := httptest.NewRecorder()

	h.WaitForEscalationResponse(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var result map[string]interface{}
	json.NewDecoder(w.Body).Decode(&result)
	if result["resolution"] != "Already done" {
		t.Errorf("expected resolution='Already done', got %v", result["resolution"])
	}
	if result["action"] != "approve" {
		t.Errorf("expected action=approve, got %v", result["action"])
	}
	if result["status"] != "RESOLVED" {
		t.Errorf("expected status=RESOLVED, got %v", result["status"])
	}
}

func TestWaitForEscalationResponse_Timeout(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	execOrFatal(t, db,
		`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew1', ?, 'Eng', 'eng')`, wsID)
	execOrFatal(t, db,
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('ag1', 'crew1', ?, 'Nela', 'nela')`, wsID)
	execOrFatal(t, db,
		`INSERT INTO escalations (id, workspace_id, crew_id, chat_id, from_agent_id, reason, status, created_at)
		 VALUES ('esc1', ?, 'crew1', 'chat1', 'ag1', 'Need approval', 'PENDING', '2025-01-01T15:00:00Z')`, wsID)

	h := NewQueryHandler(db, nil, nil, "token", logger)

	// Use a short-lived context to simulate timeout
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/escalations/esc1/wait", nil)
	req = req.WithContext(ctx)
	req.SetPathValue("escalationId", "esc1")
	w := httptest.NewRecorder()

	h.WaitForEscalationResponse(w, req)

	if w.Code != http.StatusRequestTimeout {
		t.Fatalf("expected 408, got %d: %s", w.Code, w.Body.String())
	}
}

func TestListEscalations_IncludesAction(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	execOrFatal(t, db,
		`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew1', ?, 'Eng', 'eng')`, wsID)
	execOrFatal(t, db,
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('ag1', 'crew1', ?, 'Nela', 'nela')`, wsID)
	execOrFatal(t, db,
		`INSERT INTO escalations (id, workspace_id, crew_id, chat_id, from_agent_id, reason, status, resolution, action, redirect_to, resolved_at, created_at)
		 VALUES ('esc1', ?, 'crew1', 'chat1', 'ag1', 'Wrong agent', 'RESOLVED', 'Viktor handles this', 'redirect', 'viktor', '2025-01-01T16:00:00Z', '2025-01-01T15:00:00Z')`, wsID)

	h := NewQueryHandler(db, nil, nil, "token", logger)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/crews/crew1/escalations", nil)
	req.SetPathValue("crewId", "crew1")
	ctx := context.WithValue(context.WithValue(req.Context(), ctxWorkspaceID, wsID), ctxRole, "OWNER")
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.ListEscalations(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var result []map[string]interface{}
	json.NewDecoder(w.Body).Decode(&result)
	if len(result) != 1 {
		t.Fatalf("expected 1 escalation, got %d", len(result))
	}
	if result[0]["action"] != "redirect" {
		t.Errorf("expected action=redirect, got %v", result[0]["action"])
	}
	if result[0]["redirect_to"] != "viktor" {
		t.Errorf("expected redirect_to=viktor, got %v", result[0]["redirect_to"])
	}
}

func TestStandup_MissingCrewID(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	h := NewQueryHandler(db, nil, nil, "token", logger)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/standup", nil)
	w := httptest.NewRecorder()

	h.Standup(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestStandup_EmptyCrew(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	execOrFatal(t, db,
		`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew1', ?, 'Crew', 'crew')`, wsID)

	h := NewQueryHandler(db, nil, nil, "token", logger)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/standup?crew_id=crew1", nil)
	w := httptest.NewRecorder()

	h.Standup(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", w.Code, w.Body.String())
	}
	var result map[string]string
	json.NewDecoder(w.Body).Decode(&result)
	if result["standup"] == "" {
		t.Error("expected non-empty standup text")
	}
	if result["crew_id"] != "crew1" {
		t.Errorf("expected crew_id=crew1, got %q", result["crew_id"])
	}
}

func TestStandup_WithConversationsAndEscalations(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	execOrFatal(t, db,
		`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew1', ?, 'Eng', 'eng')`, wsID)
	execOrFatal(t, db,
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('ag1', 'crew1', ?, 'Viktor', 'viktor')`, wsID)
	execOrFatal(t, db,
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('ag2', 'crew1', ?, 'Nela', 'nela')`, wsID)

	// Insert a peer conversation
	execOrFatal(t, db,
		`INSERT INTO peer_conversations (id, workspace_id, crew_id, chat_id, from_agent_id, to_agent_id, question, response, status, escalated, created_at)
		 VALUES ('pc1', ?, 'crew1', 'chat1', 'ag1', 'ag2', 'What CSS?', 'Tailwind', 'COMPLETED', 0, '2025-01-01T14:30:00Z')`, wsID)

	// Insert an escalation
	execOrFatal(t, db,
		`INSERT INTO escalations (id, workspace_id, crew_id, chat_id, from_agent_id, reason, status, created_at)
		 VALUES ('esc1', ?, 'crew1', 'chat1', 'ag2', 'API breaking changes', 'PENDING', '2025-01-01T15:50:00Z')`, wsID)

	h := NewQueryHandler(db, nil, nil, "token", logger)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/standup?crew_id=crew1&since=2025-01-01T00:00:00Z", nil)
	w := httptest.NewRecorder()

	h.Standup(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var result map[string]string
	json.NewDecoder(w.Body).Decode(&result)

	standup := result["standup"]
	if standup == "" {
		t.Fatal("expected non-empty standup")
	}

	// Check that it contains peer interaction data and correct summary counts
	for _, want := range []string{"Viktor", "Nela", "What CSS?", "Tailwind", "API breaking changes", "[CREW STANDUP]", "1 escalations"} {
		if !containsStr(standup, want) {
			t.Errorf("standup missing %q\nfull standup:\n%s", want, standup)
		}
	}
}

func TestListAllActivity_Empty(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	h := NewQueryHandler(db, nil, nil, "token", logger)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/activity?limit=30", nil)
	ctx := context.WithValue(req.Context(), ctxWorkspaceID, wsID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.ListAllActivity(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", w.Code, w.Body.String())
	}
	var result []map[string]interface{}
	json.NewDecoder(w.Body).Decode(&result)
	if len(result) != 0 {
		t.Errorf("expected 0 activity items, got %d", len(result))
	}
}

func TestListAllActivity_ReturnsUnified(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	execOrFatal(t, db,
		`INSERT INTO crews (id, workspace_id, name, slug, color) VALUES ('crew1', ?, 'Eng', 'eng', '#3B82F6')`, wsID)
	execOrFatal(t, db,
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('ag1', 'crew1', ?, 'Viktor', 'viktor')`, wsID)
	execOrFatal(t, db,
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('ag2', 'crew1', ?, 'Nela', 'nela')`, wsID)
	execOrFatal(t, db,
		`INSERT INTO chats (id, agent_id, workspace_id, mode, status) VALUES ('chat1', 'ag1', ?, 'CHAT', 'ACTIVE')`, wsID)

	// Insert an assignment (assignments table has no crew_id column)
	execOrFatal(t, db,
		`INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, created_at)
		 VALUES ('asgn1', ?, 'chat1', 'ag1', 'ag2', 'Build API', 'COMPLETED', '2025-01-01T10:00:00Z')`, wsID)

	// Insert a peer conversation
	execOrFatal(t, db,
		`INSERT INTO peer_conversations (id, workspace_id, crew_id, chat_id, from_agent_id, to_agent_id, question, status, created_at)
		 VALUES ('pc1', ?, 'crew1', 'chat1', 'ag1', 'ag2', 'What framework?', 'COMPLETED', '2025-01-01T11:00:00Z')`, wsID)

	// Insert an escalation
	execOrFatal(t, db,
		`INSERT INTO escalations (id, workspace_id, crew_id, chat_id, from_agent_id, reason, status, created_at)
		 VALUES ('esc1', ?, 'crew1', 'chat1', 'ag1', 'API conflict', 'PENDING', '2025-01-01T12:00:00Z')`, wsID)

	h := NewQueryHandler(db, nil, nil, "token", logger)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/activity?limit=30", nil)
	ctx := context.WithValue(req.Context(), ctxWorkspaceID, wsID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.ListAllActivity(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", w.Code, w.Body.String())
	}
	var result []map[string]interface{}
	json.NewDecoder(w.Body).Decode(&result)
	if len(result) != 3 {
		t.Fatalf("expected 3 activity items, got %d", len(result))
	}

	// Verify all types are present
	types := map[string]bool{}
	for _, item := range result {
		types[item["type"].(string)] = true
	}
	for _, typ := range []string{"assignment", "peer_conversation", "escalation"} {
		if !types[typ] {
			t.Errorf("missing activity type %q", typ)
		}
	}
}

func TestListAllActivity_SortedByCreatedAt(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	execOrFatal(t, db,
		`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew1', ?, 'Eng', 'eng')`, wsID)
	execOrFatal(t, db,
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('ag1', 'crew1', ?, 'Viktor', 'viktor')`, wsID)
	execOrFatal(t, db,
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('ag2', 'crew1', ?, 'Nela', 'nela')`, wsID)
	execOrFatal(t, db,
		`INSERT INTO chats (id, agent_id, workspace_id, mode, status) VALUES ('chat1', 'ag1', ?, 'CHAT', 'ACTIVE')`, wsID)

	// Insert items with known timestamps: escalation newest, assignment oldest
	execOrFatal(t, db,
		`INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, created_at)
		 VALUES ('asgn1', ?, 'chat1', 'ag1', 'ag2', 'Build API', 'COMPLETED', '2025-01-01T10:00:00Z')`, wsID)
	execOrFatal(t, db,
		`INSERT INTO escalations (id, workspace_id, crew_id, chat_id, from_agent_id, reason, status, created_at)
		 VALUES ('esc1', ?, 'crew1', 'chat1', 'ag1', 'Conflict', 'PENDING', '2025-01-01T12:00:00Z')`, wsID)

	h := NewQueryHandler(db, nil, nil, "token", logger)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/activity?limit=30", nil)
	ctx := context.WithValue(req.Context(), ctxWorkspaceID, wsID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.ListAllActivity(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var result []map[string]interface{}
	json.NewDecoder(w.Body).Decode(&result)
	if len(result) != 2 {
		t.Fatalf("expected 2 items, got %d", len(result))
	}

	// First should be escalation (newest), second should be assignment (oldest)
	if result[0]["type"] != "escalation" {
		t.Errorf("expected first item type=escalation, got %v", result[0]["type"])
	}
	if result[1]["type"] != "assignment" {
		t.Errorf("expected second item type=assignment, got %v", result[1]["type"])
	}
}

func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstr(s, substr))
}

func containsSubstr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
