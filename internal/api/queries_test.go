package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
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
