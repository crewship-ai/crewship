package api

// Coverage tests for the internal credential handlers
// (internal_credentials.go + internal_credentials_mutate.go) and the
// remaining crew-integration CRUD / migrate branches
// (crew_integrations.go / crew_integrations_crud.go /
// crew_integrations_migrate.go).
//
// All test functions are prefixed TestCovICI and all new helpers covICI
// to avoid colliding with the existing covII* / covInt* style tests and
// the shared harness (setupTestDB, seedTestUser, seedTestWorkspace,
// setTestEncryptionKey, emitRecorder, makeReq, seedCrew, ...).
//
// Skipped (live-network only): none of the handlers under test perform
// outbound network I/O, so nothing is skipped on that basis. The
// integration_test_connection.go probe path (which DOES hit the network)
// is out of scope for this file.

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

	"github.com/crewship-ai/crewship/internal/encryption"
)

// covICILogger returns a quiet logger for the handlers under test.
func covICILogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// covICISeedCredential inserts a credentials row with the given type /
// provider / status and an encrypted value, returning nothing. The
// caller controls id so list / status assertions can target it.
func covICISeedCredential(t *testing.T, db *sql.DB, id, wsID, name, typ, provider, status, plaintext string) {
	t.Helper()
	enc, err := encryption.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("encrypt %q: %v", id, err)
	}
	_, err = db.ExecContext(context.Background(),
		`INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, provider, scope, status, created_by, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, 'WORKSPACE', ?, 'test-user-id', datetime('now'), datetime('now'))`,
		id, wsID, name, enc, typ, provider, status)
	if err != nil {
		t.Fatalf("insert credential %q: %v", id, err)
	}
}

// covICILoopbackReq stamps a loopback RemoteAddr onto a request so
// requestIsLoopback() returns true (httptest defaults to 192.0.2.1).
func covICILoopbackReq(req *http.Request) *http.Request {
	req.RemoteAddr = "127.0.0.1:54321"
	return req
}

// ---------------------------------------------------------------------------
// ListCredentials (internal_credentials.go)
// ---------------------------------------------------------------------------

func TestCovICIListCredentials_MetadataOnly(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	covICISeedCredential(t, db, "cred-a", wsID, "Anthropic Key", "AI_CLI_TOKEN", "ANTHROPIC", "ACTIVE", "sk-ant-secret")

	h := NewInternalHandler(db, "tok", covICILogger())
	req := httptest.NewRequest("GET", "/api/v1/internal/credentials?workspace_id="+wsID, nil)
	rr := httptest.NewRecorder()
	h.ListCredentials(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", rr.Code, rr.Body.String())
	}
	var result []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("len = %d, want 1", len(result))
	}
	// Metadata-only: access_token must be omitted (pointer + omitempty).
	if _, present := result[0]["access_token"]; present {
		t.Errorf("access_token should be withheld for non-loopback metadata request, got %v", result[0]["access_token"])
	}
	if result[0]["name"] != "Anthropic Key" {
		t.Errorf("name = %v, want 'Anthropic Key'", result[0]["name"])
	}
}

func TestCovICIListCredentials_IncludeValuesLoopback(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	covICISeedCredential(t, db, "cred-lv", wsID, "Loopback Key", "API_KEY", "ANTHROPIC", "ACTIVE", "sk-plain-value")
	// Give it a refresh token so the refresh-decrypt arm runs too.
	encRefresh, err := encryption.Encrypt("refresh-plain")
	if err != nil {
		t.Fatalf("encrypt refresh: %v", err)
	}
	if _, err := db.Exec("UPDATE credentials SET encrypted_refresh_token = ? WHERE id = 'cred-lv'", encRefresh); err != nil {
		t.Fatalf("set refresh: %v", err)
	}

	h := NewInternalHandler(db, "tok", covICILogger())
	req := httptest.NewRequest("GET", "/api/v1/internal/credentials?workspace_id="+wsID+"&include_values=true", nil)
	req = covICILoopbackReq(req)
	rr := httptest.NewRecorder()
	h.ListCredentials(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", rr.Code, rr.Body.String())
	}
	var result []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("len = %d, want 1", len(result))
	}
	if result[0]["access_token"] != "sk-plain-value" {
		t.Errorf("access_token = %v, want decrypted 'sk-plain-value'", result[0]["access_token"])
	}
	if result[0]["refresh_token"] != "refresh-plain" {
		t.Errorf("refresh_token = %v, want decrypted 'refresh-plain'", result[0]["refresh_token"])
	}

	// The USE audit row should have been recorded (debounced CAS path).
	var auditCount int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM credential_audit WHERE credential_id = 'cred-lv' AND event_type = ?",
		string(AuditEventUse)).Scan(&auditCount); err != nil {
		t.Fatalf("query audit log: %v", err)
	}
	if auditCount == 0 {
		t.Error("expected a USE audit row from the loopback include_values fetch")
	}
}

func TestCovICIListCredentials_IncludeValuesNonLoopbackRejected(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	covICISeedCredential(t, db, "cred-nl", wsID, "Remote Key", "API_KEY", "ANTHROPIC", "ACTIVE", "sk-should-stay-hidden")

	h := NewInternalHandler(db, "tok", covICILogger())
	// include_values=true but RemoteAddr is the httptest default (non-loopback).
	req := httptest.NewRequest("GET", "/api/v1/internal/credentials?include_values=true", nil)
	rr := httptest.NewRecorder()
	h.ListCredentials(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", rr.Code, rr.Body.String())
	}
	var result []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("len = %d, want 1", len(result))
	}
	if _, present := result[0]["access_token"]; present {
		t.Errorf("non-loopback include_values must be downgraded to metadata-only; got token %v", result[0]["access_token"])
	}
}

func TestCovICIListCredentials_ProviderFilter(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	covICISeedCredential(t, db, "cred-anthropic", wsID, "A", "API_KEY", "ANTHROPIC", "ACTIVE", "v1")
	covICISeedCredential(t, db, "cred-openai", wsID, "B", "API_KEY", "OPENAI", "ACTIVE", "v2")

	h := NewInternalHandler(db, "tok", covICILogger())
	req := httptest.NewRequest("GET", "/api/v1/internal/credentials?provider=OPENAI", nil)
	rr := httptest.NewRecorder()
	h.ListCredentials(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", rr.Code, rr.Body.String())
	}
	var result []map[string]any
	json.Unmarshal(rr.Body.Bytes(), &result)
	if len(result) != 1 {
		t.Fatalf("len = %d, want 1 (provider filter)", len(result))
	}
	if result[0]["provider"] != "OPENAI" {
		t.Errorf("provider = %v, want OPENAI", result[0]["provider"])
	}
}

func TestCovICIListCredentials_DBError(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	_ = wsID

	h := NewInternalHandler(db, "tok", covICILogger())
	db.Close() // fault injection → query fails → 500

	req := httptest.NewRequest("GET", "/api/v1/internal/credentials", nil)
	rr := httptest.NewRecorder()
	h.ListCredentials(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// UpdateCredentialStatus (internal_credentials.go)
// ---------------------------------------------------------------------------

func TestCovICIUpdateCredentialStatus_InvalidJSON(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	h := NewInternalHandler(db, "tok", covICILogger())

	req := httptest.NewRequest("PATCH", "/api/v1/internal/credentials/cred-x/status",
		strings.NewReader("{not-json"))
	req.SetPathValue("credentialId", "cred-x")
	rr := httptest.NewRecorder()
	h.UpdateCredentialStatus(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestCovICIUpdateCredentialStatus_InvalidStatus(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	h := NewInternalHandler(db, "tok", covICILogger())

	req := httptest.NewRequest("PATCH", "/api/v1/internal/credentials/cred-x/status",
		strings.NewReader(`{"status":"BOGUS"}`))
	req.SetPathValue("credentialId", "cred-x")
	rr := httptest.NewRecorder()
	h.UpdateCredentialStatus(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestCovICIUpdateCredentialStatus_NotFound(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	h := NewInternalHandler(db, "tok", covICILogger())

	req := httptest.NewRequest("PATCH", "/api/v1/internal/credentials/missing/status",
		strings.NewReader(`{"status":"ACTIVE"}`))
	req.SetPathValue("credentialId", "missing")
	rr := httptest.NewRecorder()
	h.UpdateCredentialStatus(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body: %s", rr.Code, rr.Body.String())
	}
}

func TestCovICIUpdateCredentialStatus_HappyWithTokens(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	covICISeedCredential(t, db, "cred-up", wsID, "Rotating", "AI_CLI_TOKEN", "ANTHROPIC", "ACTIVE", "old-value")

	h := NewInternalHandler(db, "tok", covICILogger())
	body := `{"status":"EXPIRED","last_error":"token rejected",` +
		`"access_token":"new-access","refresh_token":"new-refresh","token_expires_at":"2030-01-01T00:00:00Z"}`
	// workspace_id query param exercises the optional WHERE clause arm.
	req := httptest.NewRequest("PATCH", "/api/v1/internal/credentials/cred-up/status?workspace_id="+wsID,
		strings.NewReader(body))
	req.SetPathValue("credentialId", "cred-up")
	rr := httptest.NewRecorder()
	h.UpdateCredentialStatus(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", rr.Code, rr.Body.String())
	}

	// Assert DB state: status, last_error, expiry, and re-encrypted tokens.
	var status, lastErr, expires string
	var encVal, encRefresh string
	if err := db.QueryRow(
		`SELECT status, last_error, token_expires_at, encrypted_value, encrypted_refresh_token
		   FROM credentials WHERE id = 'cred-up'`).
		Scan(&status, &lastErr, &expires, &encVal, &encRefresh); err != nil {
		t.Fatalf("query updated credential: %v", err)
	}
	if status != "EXPIRED" {
		t.Errorf("status = %q, want EXPIRED", status)
	}
	if lastErr != "token rejected" {
		t.Errorf("last_error = %q, want 'token rejected'", lastErr)
	}
	if expires != "2030-01-01T00:00:00Z" {
		t.Errorf("token_expires_at = %q", expires)
	}
	dec, err := encryption.Decrypt(encVal)
	if err != nil || dec != "new-access" {
		t.Errorf("decrypted access = %q (err=%v), want 'new-access'", dec, err)
	}
	decR, err := encryption.Decrypt(encRefresh)
	if err != nil || decR != "new-refresh" {
		t.Errorf("decrypted refresh = %q (err=%v), want 'new-refresh'", decR, err)
	}
}

func TestCovICIUpdateCredentialStatus_DBError(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	h := NewInternalHandler(db, "tok", covICILogger())
	db.Close() // BeginTx fails → 500

	req := httptest.NewRequest("PATCH", "/api/v1/internal/credentials/cred-x/status",
		strings.NewReader(`{"status":"ACTIVE"}`))
	req.SetPathValue("credentialId", "cred-x")
	rr := httptest.NewRecorder()
	h.UpdateCredentialStatus(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// GetWebhookSecret (internal_credentials.go)
// ---------------------------------------------------------------------------

func TestCovICIGetWebhookSecret_NotConfigured(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	// Agent row exists but webhook_secret is NULL → 404 "not configured".
	if _, err := db.Exec(
		`INSERT INTO agents (id, workspace_id, name, slug, status) VALUES ('agent-ws', ?, 'WS', 'ws', 'IDLE')`, wsID); err != nil {
		t.Fatalf("insert agent: %v", err)
	}

	h := NewInternalHandler(db, "tok", covICILogger())
	req := httptest.NewRequest("GET", "/api/v1/internal/agents/agent-ws/webhook-secret", nil)
	req.SetPathValue("agentId", "agent-ws")
	rr := httptest.NewRecorder()
	h.GetWebhookSecret(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body: %s", rr.Code, rr.Body.String())
	}
}

func TestCovICIGetWebhookSecret_AgentNotFound(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	h := NewInternalHandler(db, "tok", covICILogger())

	req := httptest.NewRequest("GET", "/api/v1/internal/agents/nope/webhook-secret", nil)
	req.SetPathValue("agentId", "nope")
	rr := httptest.NewRecorder()
	h.GetWebhookSecret(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}

func TestCovICIGetWebhookSecret_Happy(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	if _, err := db.Exec(
		`INSERT INTO agents (id, workspace_id, name, slug, status, webhook_secret)
		 VALUES ('agent-hook', ?, 'Hook', 'hook', 'IDLE', 'whsec_abc')`, wsID); err != nil {
		t.Fatalf("insert agent: %v", err)
	}

	h := NewInternalHandler(db, "tok", covICILogger())
	req := httptest.NewRequest("GET", "/api/v1/internal/agents/agent-hook/webhook-secret", nil)
	req.SetPathValue("agentId", "agent-hook")
	rr := httptest.NewRecorder()
	h.GetWebhookSecret(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", rr.Code, rr.Body.String())
	}
	var resp map[string]string
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp["webhook_secret"] != "whsec_abc" {
		t.Errorf("webhook_secret = %q, want whsec_abc", resp["webhook_secret"])
	}
}

// ---------------------------------------------------------------------------
// CredentialInternalAdapter (internal_credentials_mutate.go)
// ---------------------------------------------------------------------------

func covICINewCredAdapter(db *sql.DB) *CredentialInternalAdapter {
	return NewCredentialInternalAdapter(NewCredentialHandler(db, covICILogger()))
}

func TestCovICICredAdapterCreate_MissingWorkspace(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	adapter := covICINewCredAdapter(db)

	req := httptest.NewRequest("POST", "/api/v1/internal/credentials", strings.NewReader(`{}`))
	req.Header.Set("X-Caller-User-Id", "test-user-id")
	rr := httptest.NewRecorder()
	adapter.Create(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (missing workspace_id)", rr.Code)
	}
}

func TestCovICICredAdapterCreate_MissingCaller(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	adapter := covICINewCredAdapter(db)

	// workspace_id present, but no X-Caller-User-Id → 401.
	req := httptest.NewRequest("POST", "/api/v1/internal/credentials?workspace_id="+wsID,
		strings.NewReader(`{"name":"x","value":"y"}`))
	rr := httptest.NewRecorder()
	adapter.Create(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (missing X-Caller-User-Id)", rr.Code)
	}
}

func TestCovICICredAdapterCreate_NilGuard(t *testing.T) {
	var adapter *CredentialInternalAdapter // nil receiver
	req := httptest.NewRequest("POST", "/api/v1/internal/credentials", strings.NewReader(`{}`))
	rr := httptest.NewRecorder()
	adapter.Create(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (nil adapter)", rr.Code)
	}
}

func TestCovICICredAdapterCreate_Happy(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db) // seeded as workspace OWNER → has credential.create capability
	wsID := seedTestWorkspace(t, db, userID)
	adapter := covICINewCredAdapter(db)

	body := `{"name":"Adapter Key","value":"sk-adapter","type":"API_KEY","provider":"ANTHROPIC"}`
	req := httptest.NewRequest("POST", "/api/v1/internal/credentials?workspace_id="+wsID,
		strings.NewReader(body))
	req.Header.Set("X-Caller-User-Id", userID)
	rr := httptest.NewRecorder()
	adapter.Create(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body: %s", rr.Code, rr.Body.String())
	}
	// Assert DB state: the row landed in this workspace.
	var count int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM credentials WHERE workspace_id = ? AND name = 'Adapter Key'", wsID).Scan(&count); err != nil {
		t.Fatalf("query credential: %v", err)
	}
	if count != 1 {
		t.Errorf("credential row count = %d, want 1", count)
	}
}

func TestCovICICredAdapterCreate_ValidationMissingName(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	adapter := covICINewCredAdapter(db)

	// Passes envelope + capability, fails inner Create validation (no name).
	req := httptest.NewRequest("POST", "/api/v1/internal/credentials?workspace_id="+wsID,
		strings.NewReader(`{"value":"sk-x"}`))
	req.Header.Set("X-Caller-User-Id", userID)
	rr := httptest.NewRecorder()
	adapter.Create(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (missing name), body: %s", rr.Code, rr.Body.String())
	}
}

func TestCovICICredAdapterRotate_MissingCaller(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	adapter := covICINewCredAdapter(db)

	req := httptest.NewRequest("POST", "/api/v1/internal/credentials/cred-r/rotate?workspace_id="+wsID,
		strings.NewReader(`{"value":"new"}`))
	req.SetPathValue("credentialId", "cred-r")
	rr := httptest.NewRecorder()
	adapter.Rotate(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (missing X-Caller-User-Id)", rr.Code)
	}
}

func TestCovICICredAdapterRotate_NilGuard(t *testing.T) {
	var adapter *CredentialInternalAdapter
	req := httptest.NewRequest("POST", "/api/v1/internal/credentials/x/rotate", strings.NewReader(`{}`))
	rr := httptest.NewRecorder()
	adapter.Rotate(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (nil adapter)", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// UpdateCrewIntegration (crew_integrations_crud.go)
// ---------------------------------------------------------------------------

// covICISeedCrewIntegration inserts a crew_mcp_servers row directly and
// returns its id.
func covICISeedCrewIntegration(t *testing.T, db *sql.DB, id, crewID, name, transport string, endpoint, command *string) {
	t.Helper()
	_, err := db.Exec(`
		INSERT INTO crew_mcp_servers (id, crew_id, name, display_name, transport, endpoint, command, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 1, datetime('now'), datetime('now'))`,
		id, crewID, name, name, transport, endpoint, command)
	if err != nil {
		t.Fatalf("insert crew integration %q: %v", id, err)
	}
}

func TestCovICIUpdateCrewIntegration_Forbidden(t *testing.T) {
	db, h, wsID, _ := setupIntegrationTest(t)
	seedCrew(t, db, "crewU", wsID, "Crew", "crew")

	req := makeReq(t, "PATCH", "/api/v1/crews/crewU/integrations/int1", map[string]any{
		"display_name": "x",
	}, wsID, "MEMBER")
	req.SetPathValue("crewId", "crewU")
	req.SetPathValue("integrationId", "int1")
	rr := httptest.NewRecorder()
	h.UpdateCrewIntegration(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
}

func TestCovICIUpdateCrewIntegration_InvalidJSON(t *testing.T) {
	db, h, wsID, _ := setupIntegrationTest(t)
	seedCrew(t, db, "crewU", wsID, "Crew", "crew")

	req := httptest.NewRequest("PATCH", "/api/v1/crews/crewU/integrations/int1",
		strings.NewReader("{bad"))
	ctx := withWorkspace(req.Context(), wsID, "ADMIN")
	ctx = withUser(ctx, &AuthUser{ID: "test-user-id"})
	req = req.WithContext(ctx)
	req.SetPathValue("crewId", "crewU")
	req.SetPathValue("integrationId", "int1")
	rr := httptest.NewRecorder()
	h.UpdateCrewIntegration(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestCovICIUpdateCrewIntegration_NotFound(t *testing.T) {
	db, h, wsID, _ := setupIntegrationTest(t)
	seedCrew(t, db, "crewU", wsID, "Crew", "crew")

	req := makeReq(t, "PATCH", "/api/v1/crews/crewU/integrations/missing", map[string]any{
		"display_name": "x",
	}, wsID, "ADMIN")
	req.SetPathValue("crewId", "crewU")
	req.SetPathValue("integrationId", "missing")
	rr := httptest.NewRecorder()
	h.UpdateCrewIntegration(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body: %s", rr.Code, rr.Body.String())
	}
}

func TestCovICIUpdateCrewIntegration_InvalidTransport(t *testing.T) {
	db, h, wsID, _ := setupIntegrationTest(t)
	seedCrew(t, db, "crewU", wsID, "Crew", "crew")
	ep := "https://mcp.example.com/x"
	covICISeedCrewIntegration(t, db, "int-t", "crewU", "svc", "streamable-http", &ep, nil)

	req := makeReq(t, "PATCH", "/api/v1/crews/crewU/integrations/int-t", map[string]any{
		"transport": "grpc",
	}, wsID, "ADMIN")
	req.SetPathValue("crewId", "crewU")
	req.SetPathValue("integrationId", "int-t")
	rr := httptest.NewRecorder()
	h.UpdateCrewIntegration(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (invalid transport)", rr.Code)
	}
}

func TestCovICIUpdateCrewIntegration_TransportToStdioMissingCommand(t *testing.T) {
	db, h, wsID, _ := setupIntegrationTest(t)
	seedCrew(t, db, "crewU", wsID, "Crew", "crew")
	ep := "https://mcp.example.com/x"
	covICISeedCrewIntegration(t, db, "int-s", "crewU", "svc", "streamable-http", &ep, nil)

	// Switch to stdio without providing a command and no existing command →
	// the merged-state validation must reject.
	req := makeReq(t, "PATCH", "/api/v1/crews/crewU/integrations/int-s", map[string]any{
		"transport": "stdio",
	}, wsID, "ADMIN")
	req.SetPathValue("crewId", "crewU")
	req.SetPathValue("integrationId", "int-s")
	rr := httptest.NewRecorder()
	h.UpdateCrewIntegration(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (stdio needs command), body: %s", rr.Code, rr.Body.String())
	}
}

func TestCovICIUpdateCrewIntegration_HappyTransition(t *testing.T) {
	db, h, wsID, _ := setupIntegrationTest(t)
	seedCrew(t, db, "crewU", wsID, "Crew", "crew")
	cmd := "npx"
	covICISeedCrewIntegration(t, db, "int-h", "crewU", "svc", "stdio", nil, &cmd)

	// Switch stdio → streamable-http and supply the endpoint in the same
	// request; also flip enabled off and set a new display name + env.
	ep := "https://mcp.example.com/new"
	req := makeReq(t, "PATCH", "/api/v1/crews/crewU/integrations/int-h", map[string]any{
		"transport":    "streamable-http",
		"endpoint":     ep,
		"display_name": "Renamed",
		"env_json":     `{"K":"V"}`,
		"enabled":      false,
	}, wsID, "ADMIN")
	req.SetPathValue("crewId", "crewU")
	req.SetPathValue("integrationId", "int-h")
	rr := httptest.NewRecorder()
	h.UpdateCrewIntegration(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", rr.Code, rr.Body.String())
	}
	var resp crewMCPServerResponse
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Transport != "streamable-http" {
		t.Errorf("transport = %q, want streamable-http", resp.Transport)
	}
	if resp.DisplayName != "Renamed" {
		t.Errorf("display_name = %q, want Renamed", resp.DisplayName)
	}
	if resp.Enabled {
		t.Error("enabled should be false after update")
	}

	// DB assertion.
	var transport, display string
	var enabled int
	if err := db.QueryRow(
		"SELECT transport, display_name, enabled FROM crew_mcp_servers WHERE id = 'int-h'").
		Scan(&transport, &display, &enabled); err != nil {
		t.Fatalf("query updated integration: %v", err)
	}
	if transport != "streamable-http" || display != "Renamed" || enabled != 0 {
		t.Errorf("DB state transport=%q display=%q enabled=%d", transport, display, enabled)
	}
}

// ---------------------------------------------------------------------------
// DeleteCrewIntegration (crew_integrations_crud.go)
// ---------------------------------------------------------------------------

func TestCovICIDeleteCrewIntegration_Forbidden(t *testing.T) {
	db, h, wsID, _ := setupIntegrationTest(t)
	seedCrew(t, db, "crewD", wsID, "Crew", "crew")

	req := makeReq(t, "DELETE", "/api/v1/crews/crewD/integrations/int1", nil, wsID, "MEMBER")
	req.SetPathValue("crewId", "crewD")
	req.SetPathValue("integrationId", "int1")
	rr := httptest.NewRecorder()
	h.DeleteCrewIntegration(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
}

func TestCovICIDeleteCrewIntegration_NotFound(t *testing.T) {
	db, h, wsID, _ := setupIntegrationTest(t)
	seedCrew(t, db, "crewD", wsID, "Crew", "crew")

	req := makeReq(t, "DELETE", "/api/v1/crews/crewD/integrations/missing", nil, wsID, "ADMIN")
	req.SetPathValue("crewId", "crewD")
	req.SetPathValue("integrationId", "missing")
	rr := httptest.NewRecorder()
	h.DeleteCrewIntegration(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body: %s", rr.Code, rr.Body.String())
	}
}

func TestCovICIDeleteCrewIntegration_CascadesOAuthCredential(t *testing.T) {
	setTestEncryptionKey(t)
	db, h, wsID, _ := setupIntegrationTest(t)
	seedCrew(t, db, "crewD", wsID, "Crew", "crew")
	seedAgent(t, db, "agentD", wsID, "crewD", "A", "a")
	ep := "https://mcp.example.com/x"
	covICISeedCrewIntegration(t, db, "int-del", "crewD", "svc", "streamable-http", &ep, nil)

	// OAuth credential created for this integration's auto-connect flow.
	covICISeedCredential(t, db, "cred-oauth", wsID, "svc oauth token", "OAUTH2", "NONE", "ACTIVE", "tok")
	// Binding referencing both the crew server and the OAuth credential.
	if _, err := db.Exec(`
		INSERT INTO agent_mcp_bindings (id, agent_id, mcp_server_id, mcp_server_scope, credential_id, enabled, created_at)
		VALUES ('bind-del', 'agentD', 'int-del', 'crew', 'cred-oauth', 1, datetime('now'))`); err != nil {
		t.Fatalf("insert binding: %v", err)
	}

	req := makeReq(t, "DELETE", "/api/v1/crews/crewD/integrations/int-del", nil, wsID, "ADMIN")
	req.SetPathValue("crewId", "crewD")
	req.SetPathValue("integrationId", "int-del")
	rr := httptest.NewRecorder()
	h.DeleteCrewIntegration(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", rr.Code, rr.Body.String())
	}

	// Server gone.
	var serverCount int
	db.QueryRow("SELECT COUNT(*) FROM crew_mcp_servers WHERE id = 'int-del'").Scan(&serverCount)
	if serverCount != 0 {
		t.Errorf("crew server should be deleted, count = %d", serverCount)
	}
	// Binding gone.
	var bindCount int
	db.QueryRow("SELECT COUNT(*) FROM agent_mcp_bindings WHERE id = 'bind-del'").Scan(&bindCount)
	if bindCount != 0 {
		t.Errorf("binding should be deleted, count = %d", bindCount)
	}
	// OAuth credential cascade-deleted (no remaining bindings reference it).
	var credCount int
	db.QueryRow("SELECT COUNT(*) FROM credentials WHERE id = 'cred-oauth'").Scan(&credCount)
	if credCount != 0 {
		t.Errorf("orphan OAuth credential should be cascade-deleted, count = %d", credCount)
	}
}

// ---------------------------------------------------------------------------
// ListAllCrewIntegrations + auto-migrate (crew_integrations.go)
// ---------------------------------------------------------------------------

func TestCovICIListAllCrewIntegrations_WithBlobMigration(t *testing.T) {
	db, h, wsID, _ := setupIntegrationTest(t)
	seedCrew(t, db, "crewM", wsID, "Migrating", "migrating")

	// Seed a legacy mcp_config_json blob on the crew so ListAll auto-migrates.
	blob := `{"mcpServers":{"weather":{"url":"https://mcp.example.com/weather","type":"http"}}}`
	if _, err := db.Exec("UPDATE crews SET mcp_config_json = ? WHERE id = 'crewM'", blob); err != nil {
		t.Fatalf("seed blob: %v", err)
	}

	req := makeReq(t, "GET", "/api/v1/integrations/crews", nil, wsID, "MEMBER")
	rr := httptest.NewRecorder()
	h.ListAllCrewIntegrations(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", rr.Code, rr.Body.String())
	}
	var result []crewIntegrationOverview
	json.Unmarshal(rr.Body.Bytes(), &result)
	if len(result) != 1 {
		t.Fatalf("len = %d, want 1 (migrated weather server)", len(result))
	}
	if result[0].Name != "weather" {
		t.Errorf("name = %q, want weather", result[0].Name)
	}
	// auth_status should be "missing" (streamable-http, endpoint set, no creds).
	if result[0].AuthStatus != "missing" {
		t.Errorf("auth_status = %q, want missing", result[0].AuthStatus)
	}

	// Blob should be cleared after successful migration.
	var blobAfter sql.NullString
	db.QueryRow("SELECT mcp_config_json FROM crews WHERE id = 'crewM'").Scan(&blobAfter)
	if blobAfter.Valid && blobAfter.String != "" {
		t.Errorf("mcp_config_json should be cleared after migration, got %q", blobAfter.String)
	}
}

// ---------------------------------------------------------------------------
// Migration helpers (crew_integrations_migrate.go)
// ---------------------------------------------------------------------------

func TestCovICIParseMCPConfigBlob_Edges(t *testing.T) {
	// Empty → nil, no error.
	servers, err := parseMCPConfigBlob("")
	if err != nil || servers != nil {
		t.Errorf("empty blob: servers=%v err=%v, want nil,nil", servers, err)
	}

	// Invalid JSON → error.
	if _, err := parseMCPConfigBlob("{not json"); err == nil {
		t.Error("invalid JSON should return an error")
	}

	// Valid but no servers → nil, no error.
	servers, err = parseMCPConfigBlob(`{"mcpServers":{}}`)
	if err != nil || servers != nil {
		t.Errorf("empty mcpServers: servers=%v err=%v, want nil,nil", servers, err)
	}

	// stdio + http mix with args/env → two parsed servers.
	blob := `{"mcpServers":{
		"local-tool":{"command":"npx","args":["-y","tool"],"env":{"TOKEN":"x"}},
		"remote-tool":{"url":"https://mcp.example.com/r","transport":"streamable-http"}
	}}`
	servers, err = parseMCPConfigBlob(blob)
	if err != nil {
		t.Fatalf("parse mixed blob: %v", err)
	}
	if len(servers) != 2 {
		t.Fatalf("len = %d, want 2", len(servers))
	}
	byName := map[string]parsedMCPServer{}
	for _, s := range servers {
		byName[s.name] = s
	}
	local := byName["local-tool"]
	if local.transport != "stdio" || local.command == nil || *local.command != "npx" {
		t.Errorf("local-tool parsed wrong: %+v", local)
	}
	if local.argsJSON == nil || local.envJSON == nil {
		t.Errorf("local-tool should carry args/env JSON: %+v", local)
	}
	remote := byName["remote-tool"]
	if remote.transport != "streamable-http" || remote.endpoint == nil || *remote.endpoint != "https://mcp.example.com/r" {
		t.Errorf("remote-tool parsed wrong: %+v", remote)
	}
}

func TestCovICIMigrateJSONBlobToCrewServers_ClearsBlob(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCrew(t, db, "crewMig", wsID, "Mig", "mig")

	blob := `{"mcpServers":{"alpha":{"command":"npx"},"beta":{"url":"https://mcp.example.com/b","type":"http"}}}`
	if _, err := db.Exec("UPDATE crews SET mcp_config_json = ? WHERE id = 'crewMig'", blob); err != nil {
		t.Fatalf("seed blob: %v", err)
	}

	if err := MigrateJSONBlobToCrewServers(context.Background(), db, covICILogger(), "crewMig", wsID, blob); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var count int
	db.QueryRow("SELECT COUNT(*) FROM crew_mcp_servers WHERE crew_id = 'crewMig'").Scan(&count)
	if count != 2 {
		t.Errorf("crew_mcp_servers count = %d, want 2", count)
	}
	var blobAfter sql.NullString
	db.QueryRow("SELECT mcp_config_json FROM crews WHERE id = 'crewMig'").Scan(&blobAfter)
	if blobAfter.Valid && blobAfter.String != "" {
		t.Errorf("blob should be cleared, got %q", blobAfter.String)
	}

	// Idempotency: re-running with the same blob must not duplicate rows.
	if err := MigrateJSONBlobToCrewServers(context.Background(), db, covICILogger(), "crewMig", wsID, blob); err != nil {
		t.Fatalf("migrate (2nd): %v", err)
	}
	db.QueryRow("SELECT COUNT(*) FROM crew_mcp_servers WHERE crew_id = 'crewMig'").Scan(&count)
	if count != 2 {
		t.Errorf("after idempotent re-run count = %d, want 2", count)
	}
}

func TestCovICIMigrateJSONBlobToCrewServers_EmptyBlobNoop(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCrew(t, db, "crewEmpty", wsID, "Empty", "empty")

	// Empty blob → early return, no rows, no error.
	if err := MigrateJSONBlobToCrewServers(context.Background(), db, covICILogger(), "crewEmpty", wsID, ""); err != nil {
		t.Fatalf("migrate empty: %v", err)
	}
	var count int
	db.QueryRow("SELECT COUNT(*) FROM crew_mcp_servers WHERE crew_id = 'crewEmpty'").Scan(&count)
	if count != 0 {
		t.Errorf("empty blob should insert 0 rows, got %d", count)
	}
}
