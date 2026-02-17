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
	"path/filepath"
	"testing"

	"github.com/crewship-ai/crewship/internal/database"
)

func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	db, err := database.Open("file:" + filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	if err := database.Migrate(context.Background(), db.DB, logger); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	t.Cleanup(func() { db.Close() })
	return db.DB
}

func seedTestUser(t *testing.T, db *sql.DB) string {
	t.Helper()
	userID := "test-user-id"
	_, err := db.Exec(`INSERT INTO users (id, email, full_name) VALUES (?, 'test@example.com', 'Test User')`, userID)
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
	return userID
}

func seedTestWorkspace(t *testing.T, db *sql.DB, userID string) string {
	t.Helper()
	wsID := "test-workspace-id"
	_, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Test', 'test')`, wsID)
	if err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	_, err = db.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('m1', ?, ?, 'OWNER')`, wsID, userID)
	if err != nil {
		t.Fatalf("insert member: %v", err)
	}
	return wsID
}

func withUser(ctx context.Context, user *AuthUser) context.Context {
	return context.WithValue(ctx, ctxUser, user)
}

func withWorkspace(ctx context.Context, wsID, role string) context.Context {
	ctx = context.WithValue(ctx, ctxWorkspaceID, wsID)
	return context.WithValue(ctx, ctxRole, role)
}

func TestWorkspaceList_Unauthenticated(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	handler := NewWorkspaceHandler(db, logger)

	req := httptest.NewRequest("GET", "/api/v1/workspaces", nil)
	rr := httptest.NewRecorder()

	handler.List(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestWorkspaceList_Authenticated(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	seedTestWorkspace(t, db, userID)

	handler := NewWorkspaceHandler(db, logger)

	req := httptest.NewRequest("GET", "/api/v1/workspaces", nil)
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID, Email: "test@example.com"}))
	rr := httptest.NewRecorder()

	handler.List(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", rr.Code, http.StatusOK, rr.Body.String())
	}

	var result []workspaceResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(result) != 1 {
		t.Errorf("len = %d, want 1", len(result))
	}
	if result[0].Name != "Test" {
		t.Errorf("name = %q, want Test", result[0].Name)
	}
}

func TestWorkspaceCreate(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)

	handler := NewWorkspaceHandler(db, logger)

	body := bytes.NewBufferString(`{"name":"New Workspace","slug":"new-ws"}`)
	req := httptest.NewRequest("POST", "/api/v1/workspaces", body)
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID, Email: "test@example.com"}))
	rr := httptest.NewRecorder()

	handler.Create(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d, body: %s", rr.Code, http.StatusCreated, rr.Body.String())
	}

	var ws workspaceResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &ws); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ws.Name != "New Workspace" {
		t.Errorf("name = %q, want 'New Workspace'", ws.Name)
	}

	var role string
	err := db.QueryRow("SELECT role FROM workspace_members WHERE workspace_id = ? AND user_id = ?", ws.ID, userID).Scan(&role)
	if err != nil {
		t.Fatalf("query member: %v", err)
	}
	if role != "OWNER" {
		t.Errorf("role = %q, want OWNER", role)
	}
}

func TestWorkspaceCreate_DuplicateSlug(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	seedTestWorkspace(t, db, userID)

	handler := NewWorkspaceHandler(db, logger)

	body := bytes.NewBufferString(`{"name":"Another","slug":"test"}`)
	req := httptest.NewRequest("POST", "/api/v1/workspaces", body)
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
	rr := httptest.NewRecorder()

	handler.Create(rr, req)

	if rr.Code != http.StatusConflict {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusConflict)
	}
}

func TestCrewCreate(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	handler := NewCrewHandler(db, logger)

	body := bytes.NewBufferString(`{"name":"Engineering","slug":"engineering"}`)
	req := httptest.NewRequest("POST", "/api/v1/crews?workspace_id="+wsID, body)
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.Create(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d, body: %s", rr.Code, http.StatusCreated, rr.Body.String())
	}

	var crew crewResponse
	json.Unmarshal(rr.Body.Bytes(), &crew)
	if crew.Name != "Engineering" {
		t.Errorf("name = %q, want Engineering", crew.Name)
	}
}

func TestCrewCreate_Forbidden(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	handler := NewCrewHandler(db, logger)

	body := bytes.NewBufferString(`{"name":"Test","slug":"test-crew"}`)
	req := httptest.NewRequest("POST", "/api/v1/crews?workspace_id="+wsID, body)
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "VIEWER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.Create(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusForbidden)
	}
}

func TestAgentCreate(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	handler := NewAgentHandler(db, logger)

	body := bytes.NewBufferString(`{"name":"Code Bot","slug":"code-bot"}`)
	req := httptest.NewRequest("POST", "/api/v1/agents?workspace_id="+wsID, body)
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.Create(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d, body: %s", rr.Code, http.StatusCreated, rr.Body.String())
	}

	var agent agentResponse
	json.Unmarshal(rr.Body.Bytes(), &agent)
	if agent.Name != "Code Bot" {
		t.Errorf("name = %q, want 'Code Bot'", agent.Name)
	}
	if agent.AgentRole != "AGENT" {
		t.Errorf("role = %q, want AGENT", agent.AgentRole)
	}
	if agent.Status != "IDLE" {
		t.Errorf("status = %q, want IDLE", agent.Status)
	}
}

func TestAgentList(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	_, _ = db.Exec(`INSERT INTO agents (id, workspace_id, name, slug, agent_role, status, cli_adapter, temperature, timeout_seconds, tool_profile, memory_enabled)
		VALUES ('a1', ?, 'Bot1', 'bot-1', 'AGENT', 'IDLE', 'CLAUDE_CODE', 0.7, 1800, 'CODING', 0)`, wsID)

	handler := NewAgentHandler(db, logger)

	req := httptest.NewRequest("GET", "/api/v1/agents?workspace_id="+wsID, nil)
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.List(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", rr.Code, http.StatusOK, rr.Body.String())
	}

	var result []agentResponse
	json.Unmarshal(rr.Body.Bytes(), &result)
	if len(result) != 1 {
		t.Errorf("len = %d, want 1", len(result))
	}
}

func TestCRUDFlow(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)

	// Create workspace
	wsHandler := NewWorkspaceHandler(db, logger)
	body := bytes.NewBufferString(`{"name":"Acme","slug":"acme"}`)
	req := httptest.NewRequest("POST", "/api/v1/workspaces", body)
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
	rr := httptest.NewRecorder()
	wsHandler.Create(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create workspace: %d", rr.Code)
	}
	var ws workspaceResponse
	json.Unmarshal(rr.Body.Bytes(), &ws)

	// Create crew
	crewHandler := NewCrewHandler(db, logger)
	body = bytes.NewBufferString(`{"name":"Devs","slug":"devs"}`)
	req = httptest.NewRequest("POST", "/api/v1/crews", body)
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, ws.ID, "OWNER")
	req = req.WithContext(ctx)
	rr = httptest.NewRecorder()
	crewHandler.Create(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create crew: %d, body: %s", rr.Code, rr.Body.String())
	}
	var crew crewResponse
	json.Unmarshal(rr.Body.Bytes(), &crew)

	// Create agent in crew
	agentHandler := NewAgentHandler(db, logger)
	body = bytes.NewBufferString(`{"name":"Builder","slug":"builder","crew_id":"` + crew.ID + `"}`)
	req = httptest.NewRequest("POST", "/api/v1/agents", body)
	ctx = withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, ws.ID, "OWNER")
	req = req.WithContext(ctx)
	rr = httptest.NewRecorder()
	agentHandler.Create(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create agent: %d, body: %s", rr.Code, rr.Body.String())
	}

	// Verify counts
	var crewCount, agentCount, memberCount int
	db.QueryRow("SELECT COUNT(*) FROM crews WHERE workspace_id = ?", ws.ID).Scan(&crewCount)
	db.QueryRow("SELECT COUNT(*) FROM agents WHERE workspace_id = ?", ws.ID).Scan(&agentCount)
	db.QueryRow("SELECT COUNT(*) FROM workspace_members WHERE workspace_id = ?", ws.ID).Scan(&memberCount)

	if crewCount != 1 {
		t.Errorf("crews = %d, want 1", crewCount)
	}
	if agentCount != 1 {
		t.Errorf("agents = %d, want 1", agentCount)
	}
	if memberCount != 1 {
		t.Errorf("members = %d, want 1", memberCount)
	}
}
