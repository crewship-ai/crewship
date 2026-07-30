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
	"testing"

	"github.com/crewship-ai/crewship/internal/testutil"
)

// setupTestDB returns a fully-migrated SQLite database that belongs to this
// test alone.
//
// The migrate-once-into-a-template trick this package invented — the full
// migration set was dominating its wall-clock once the handler-coverage suites
// pushed setupTestDB past 1,200 calls, and the CI Go job started brushing its
// 15-minute cap — now lives in internal/testutil so the other twenty-odd
// packages that were still replaying 155 migrations per test can use it too.
// testutil.MigratedSQLDB carries all of it forward: the process-wide template,
// the per-test file copy, the database.Open pragmas, the non-t.TempDir cleanup
// contract that stopped this package misattributing "directory not empty"
// failures to bystander tests, and the deterministic quiescing that the
// detached handler workers here require (EvalHandler.Replay/Regression fire a
// context.WithoutCancel goroutine that keeps writing after the test body
// returns — see #892). The reasoning for each is recorded at the helper.
func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()
	resetCapabilityCache()
	return testutil.MigratedSQLDB(t)
}

// resetCapabilityCache empties the process-wide capability cache.
//
// defaultCapabilityCache is a deliberate singleton — RBAC answers must not
// diverge between router instances in one binary (see capabilities_check.go) —
// and it is keyed on (workspaceID, userID) with a 30s TTL. seedTestUser and
// seedTestWorkspace hand every test the SAME two ids, so although each test
// gets a private database, they all share one cache identity: a capability set
// computed against one test's rows is served to the next test's router, whose
// database says something else. Tests that call InvalidateCapabilityCache
// happen to escape it; the rest are hostage to whoever ran before them, which
// is invisible in source order and a coin flip under -shuffle=on
// (TestCovICICredAdapterCreate_Happy, 403 instead of 201, is the recorded case).
//
// Clearing here rather than renaming the ids: the two constants are written out
// as literals in ~110 places across the package's tests, and per-test ids would
// change what those tests assert about their own fixtures. The cache is the
// shared state, so the cache is what gets reset — a fresh database deserves a
// cache that knows nothing about the previous one.
func resetCapabilityCache() {
	defaultCapabilityCache.mu.Lock()
	defer defaultCapabilityCache.mu.Unlock()
	defaultCapabilityCache.store = map[string]capabilityCacheEntry{}
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

func TestCrewCreate_WithNetworkPolicy(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	handler := NewCrewHandler(db, logger)

	// Create crew with restricted network mode
	body := bytes.NewBufferString(`{"name":"Secure Team","slug":"secure-team","network_mode":"restricted","allowed_domains":["github.com","api.github.com"]}`)
	req := httptest.NewRequest("POST", "/api/v1/crews?workspace_id="+wsID, body)
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.Create(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want %d, body: %s", rr.Code, http.StatusCreated, rr.Body.String())
	}

	var crew crewResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &crew); err != nil {
		t.Fatalf("decode crew response: %v", err)
	}
	if crew.NetworkMode != "restricted" {
		t.Errorf("network_mode = %q, want restricted", crew.NetworkMode)
	}
	if len(crew.AllowedDomains) != 2 {
		t.Fatalf("allowed_domains length = %d, want 2", len(crew.AllowedDomains))
	}
	if crew.AllowedDomains[0] != "github.com" || crew.AllowedDomains[1] != "api.github.com" {
		t.Errorf("allowed_domains = %v, want [github.com api.github.com]", crew.AllowedDomains)
	}

	// Verify default create (no network_mode) returns the fail-safe "restricted"
	// with an empty allowlist (LLM providers still reachable via DefaultAllowedDomains).
	body2 := bytes.NewBufferString(`{"name":"Default Team","slug":"default-team"}`)
	req2 := httptest.NewRequest("POST", "/api/v1/crews?workspace_id="+wsID, body2)
	ctx2 := withUser(req2.Context(), &AuthUser{ID: userID})
	ctx2 = withWorkspace(ctx2, wsID, "OWNER")
	req2 = req2.WithContext(ctx2)
	rr2 := httptest.NewRecorder()

	handler.Create(rr2, req2)

	if rr2.Code != http.StatusCreated {
		t.Fatalf("create2 status = %d, want %d, body: %s", rr2.Code, http.StatusCreated, rr2.Body.String())
	}

	var crew2 crewResponse
	if err := json.Unmarshal(rr2.Body.Bytes(), &crew2); err != nil {
		t.Fatalf("decode crew2 response: %v", err)
	}
	if crew2.NetworkMode != "restricted" {
		t.Errorf("default network_mode = %q, want restricted (fail-safe default)", crew2.NetworkMode)
	}
	if crew2.AllowedDomains == nil || len(crew2.AllowedDomains) != 0 {
		t.Errorf("default allowed_domains = %v, want [] (empty allowlist)", crew2.AllowedDomains)
	}

	// Verify invalid network_mode is rejected
	body3 := bytes.NewBufferString(`{"name":"Bad Team","slug":"bad-team","network_mode":"yolo"}`)
	req3 := httptest.NewRequest("POST", "/api/v1/crews?workspace_id="+wsID, body3)
	ctx3 := withUser(req3.Context(), &AuthUser{ID: userID})
	ctx3 = withWorkspace(ctx3, wsID, "OWNER")
	req3 = req3.WithContext(ctx3)
	rr3 := httptest.NewRecorder()

	handler.Create(rr3, req3)

	if rr3.Code != http.StatusBadRequest {
		t.Errorf("invalid network_mode status = %d, want %d", rr3.Code, http.StatusBadRequest)
	}
}

func TestCrewUpdate_WithNetworkPolicy(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	handler := NewCrewHandler(db, logger)

	// Seed a crew
	crewID := "crew-net-test"
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug, network_mode, created_at, updated_at)
		VALUES (?, ?, 'Net Test', 'net-test', 'free', datetime('now'), datetime('now'))`, crewID, wsID); err != nil {
		t.Fatalf("seed crew: %v", err)
	}

	// Update to restricted with allowed_domains
	body := bytes.NewBufferString(`{"network_mode":"restricted","allowed_domains":["github.com","npm.pkg.dev"]}`)
	req := httptest.NewRequest("PATCH", "/api/v1/crews/"+crewID, body)
	req.SetPathValue("crewId", crewID)
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.Update(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("update status = %d, want %d, body: %s", rr.Code, http.StatusOK, rr.Body.String())
	}

	var crew crewResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &crew); err != nil {
		t.Fatalf("decode update response: %v", err)
	}
	if crew.NetworkMode != "restricted" {
		t.Errorf("network_mode = %q, want restricted", crew.NetworkMode)
	}
	if len(crew.AllowedDomains) != 2 {
		t.Fatalf("allowed_domains length = %d, want 2", len(crew.AllowedDomains))
	}

	// Switch back to free — allowed_domains should be auto-cleared
	body2 := bytes.NewBufferString(`{"network_mode":"free"}`)
	req2 := httptest.NewRequest("PATCH", "/api/v1/crews/"+crewID, body2)
	req2.SetPathValue("crewId", crewID)
	ctx2 := withUser(req2.Context(), &AuthUser{ID: userID})
	ctx2 = withWorkspace(ctx2, wsID, "OWNER")
	req2 = req2.WithContext(ctx2)
	rr2 := httptest.NewRecorder()

	handler.Update(rr2, req2)

	if rr2.Code != http.StatusOK {
		t.Fatalf("update2 status = %d, want %d, body: %s", rr2.Code, http.StatusOK, rr2.Body.String())
	}

	var crew2 crewResponse
	if err := json.Unmarshal(rr2.Body.Bytes(), &crew2); err != nil {
		t.Fatalf("decode update2 response: %v", err)
	}
	if crew2.NetworkMode != "free" {
		t.Errorf("network_mode = %q, want free", crew2.NetworkMode)
	}
	if len(crew2.AllowedDomains) != 0 {
		t.Errorf("allowed_domains should be cleared after switching to free, got %v", crew2.AllowedDomains)
	}

	// Verify in DB that allowed_domains is actually NULL
	var dbDomains sql.NullString
	if err := db.QueryRow("SELECT allowed_domains FROM crews WHERE id = ?", crewID).Scan(&dbDomains); err != nil {
		t.Fatalf("scan DB allowed_domains: %v", err)
	}
	if dbDomains.Valid {
		t.Errorf("DB allowed_domains should be NULL after switching to free, got %q", dbDomains.String)
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

func TestWorkspaceGet_PreferredLanguage(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	// Set preferred_language
	_, err := db.Exec("UPDATE workspaces SET preferred_language = 'Czech' WHERE id = ?", wsID)
	if err != nil {
		t.Fatalf("update preferred_language: %v", err)
	}

	handler := NewWorkspaceHandler(db, logger)

	req := httptest.NewRequest("GET", "/api/v1/workspaces/"+wsID, nil)
	ctx := withWorkspace(req.Context(), wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.Get(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", rr.Code, http.StatusOK, rr.Body.String())
	}

	var ws workspaceResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &ws); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ws.PreferredLanguage == nil || *ws.PreferredLanguage != "Czech" {
		t.Errorf("preferred_language = %v, want 'Czech'", ws.PreferredLanguage)
	}
}

func TestWorkspaceGet_NoPreferredLanguage(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	handler := NewWorkspaceHandler(db, logger)

	req := httptest.NewRequest("GET", "/api/v1/workspaces/"+wsID, nil)
	ctx := withWorkspace(req.Context(), wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.Get(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", rr.Code, http.StatusOK, rr.Body.String())
	}

	var ws workspaceResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &ws); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ws.PreferredLanguage != nil {
		t.Errorf("preferred_language = %v, want nil", ws.PreferredLanguage)
	}
}

func TestWorkspaceUpdate_PreferredLanguage(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	handler := NewWorkspaceHandler(db, logger)

	body := bytes.NewBufferString(`{"preferred_language":"Czech"}`)
	req := httptest.NewRequest("PATCH", "/api/v1/workspaces/"+wsID, body)
	ctx := withWorkspace(req.Context(), wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.Update(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", rr.Code, http.StatusOK, rr.Body.String())
	}

	var ws workspaceResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &ws); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ws.PreferredLanguage == nil || *ws.PreferredLanguage != "Czech" {
		t.Errorf("preferred_language = %v, want 'Czech'", ws.PreferredLanguage)
	}

	// Verify in DB
	var lang sql.NullString
	db.QueryRow("SELECT preferred_language FROM workspaces WHERE id = ?", wsID).Scan(&lang)
	if !lang.Valid || lang.String != "Czech" {
		t.Errorf("DB preferred_language = %v, want 'Czech'", lang)
	}
}

func TestWorkspaceUpdate_ClearPreferredLanguage(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	// Set it first
	db.Exec("UPDATE workspaces SET preferred_language = 'Czech' WHERE id = ?", wsID)

	handler := NewWorkspaceHandler(db, logger)

	// Clear with empty string
	body := bytes.NewBufferString(`{"preferred_language":""}`)
	req := httptest.NewRequest("PATCH", "/api/v1/workspaces/"+wsID, body)
	ctx := withWorkspace(req.Context(), wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.Update(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", rr.Code, http.StatusOK, rr.Body.String())
	}

	// Verify cleared in DB
	var lang sql.NullString
	db.QueryRow("SELECT preferred_language FROM workspaces WHERE id = ?", wsID).Scan(&lang)
	if lang.Valid {
		t.Errorf("DB preferred_language should be NULL after clearing, got %v", lang.String)
	}
}

// TestWorkspaceUpdate_AllowPrivilegedCredentials is issue #1032's opt-in
// flag round-trip: default off, explicit true persists and reflects on the
// response, explicit false flips it back.
func TestWorkspaceUpdate_AllowPrivilegedCredentials(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	handler := NewWorkspaceHandler(db, logger)

	patch := func(bodyJSON string) workspaceResponse {
		req := httptest.NewRequest("PATCH", "/api/v1/workspaces/"+wsID, bytes.NewBufferString(bodyJSON))
		req = req.WithContext(withWorkspace(req.Context(), wsID, "OWNER"))
		rr := httptest.NewRecorder()
		handler.Update(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200, body: %s", rr.Code, rr.Body.String())
		}
		var ws workspaceResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &ws); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		return ws
	}

	if ws := patch(`{"name":"Acme Corp"}`); ws.AllowPrivilegedCredentials {
		t.Errorf("allow_privileged_credentials should default false, got true")
	}

	if ws := patch(`{"allow_privileged_credentials":true}`); !ws.AllowPrivilegedCredentials {
		t.Errorf("allow_privileged_credentials = false after opting in, want true")
	}
	var flag int
	db.QueryRow("SELECT allow_privileged_credentials FROM workspaces WHERE id = ?", wsID).Scan(&flag)
	if flag != 1 {
		t.Errorf("DB allow_privileged_credentials = %d, want 1", flag)
	}

	if ws := patch(`{"allow_privileged_credentials":false}`); ws.AllowPrivilegedCredentials {
		t.Errorf("allow_privileged_credentials = true after opting out, want false")
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
