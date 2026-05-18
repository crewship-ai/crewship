package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/encryption"
)

func newCredHandler(t *testing.T) (*CredentialHandler, *sql.DB) {
	t.Helper()
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	return NewCredentialHandler(db, logger), db
}

// ---- Create ----

func TestCredCreate_Forbidden(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	body := bytes.NewBufferString(`{"name":"x","value":"v"}`)
	req := httptest.NewRequest("POST", "/api/v1/credentials", body)
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "VIEWER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rr.Code)
	}
}

func TestCredCreate_Validation(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	tests := []struct {
		name string
		body string
		want int
	}{
		{"bad json", `not json`, http.StatusBadRequest},
		{"missing name", `{"value":"v"}`, http.StatusBadRequest},
		{"missing value", `{"name":"x"}`, http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/api/v1/credentials", strings.NewReader(tt.body))
			ctx := withUser(req.Context(), &AuthUser{ID: userID})
			ctx = withWorkspace(ctx, wsID, "OWNER")
			req = req.WithContext(ctx)
			rr := httptest.NewRecorder()
			h.Create(rr, req)
			if rr.Code != tt.want {
				t.Errorf("status = %d, want %d, body: %s", rr.Code, tt.want, rr.Body.String())
			}
		})
	}
}

func TestCredCreate_Success(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	body := bytes.NewBufferString(`{"name":"github-token","value":"ghp_xxxx","type":"SECRET","provider":"GITHUB"}`)
	req := httptest.NewRequest("POST", "/api/v1/credentials", body)
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, body: %s", rr.Code, rr.Body.String())
	}

	var resp credentialResponse
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.ID == "" || resp.Name != "github-token" {
		t.Errorf("response missing fields: %+v", resp)
	}
	// Verify response NEVER contains the plaintext value (security!)
	if strings.Contains(rr.Body.String(), "ghp_xxxx") {
		t.Error("response leaked plaintext credential value")
	}

	// Verify encrypted at rest
	var encVal string
	db.QueryRow("SELECT encrypted_value FROM credentials WHERE id = ?", resp.ID).Scan(&encVal)
	if !strings.HasPrefix(encVal, "v1:") {
		t.Errorf("encrypted value lacks v1: prefix: %q", encVal)
	}
	plain, err := encryption.Decrypt(encVal)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if plain != "ghp_xxxx" {
		t.Errorf("decrypted = %q, want ghp_xxxx", plain)
	}
}

func TestCredCreate_OAuth2Pending(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	body := bytes.NewBufferString(`{"name":"oauth-cred","type":"OAUTH2","oauth_client_id":"cid","oauth_client_secret":"csec","oauth_auth_url":"https://p/a","oauth_token_url":"https://p/t"}`)
	req := httptest.NewRequest("POST", "/api/v1/credentials", body)
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, body: %s", rr.Code, rr.Body.String())
	}

	// Verify OAuth fields stored + secret encrypted
	var clientID, encSecret string
	db.QueryRow("SELECT oauth_client_id, oauth_client_secret_enc FROM credentials WHERE name = 'oauth-cred'").Scan(&clientID, &encSecret)
	if clientID != "cid" {
		t.Errorf("client_id = %q", clientID)
	}
	plain, err := encryption.Decrypt(encSecret)
	if err != nil {
		t.Fatalf("decrypt secret: %v", err)
	}
	if plain != "csec" {
		t.Errorf("decrypted secret = %q", plain)
	}
}

func TestCredCreate_DuplicateName(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	body := bytes.NewBufferString(`{"name":"dup","value":"v1"}`)
	req := httptest.NewRequest("POST", "/api/v1/credentials", body)
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("setup status = %d", rr.Code)
	}

	// Second create should conflict
	body = bytes.NewBufferString(`{"name":"dup","value":"v2"}`)
	req = httptest.NewRequest("POST", "/api/v1/credentials", body)
	ctx = withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "OWNER")
	req = req.WithContext(ctx)
	rr = httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", rr.Code)
	}
}

func TestCredCreate_InvalidCrewID(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	body := bytes.NewBufferString(`{"name":"x","value":"v","crew_ids":["nonexistent"]}`)
	req := httptest.NewRequest("POST", "/api/v1/credentials", body)
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

// ---- List / Get ----

func seedCredentialEnc(t *testing.T, db *sql.DB, wsID, userID, credID, name, plainValue string) {
	t.Helper()
	enc, err := encryption.Encrypt(plainValue)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, provider, scope, status, created_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'SECRET', 'GITHUB', 'WORKSPACE', 'ACTIVE', ?, datetime('now'), datetime('now'))`,
		credID, wsID, name, enc, userID); err != nil {
		t.Fatalf("seed cred: %v", err)
	}
}

func TestCredList_Empty(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	req := httptest.NewRequest("GET", "/api/v1/credentials", nil)
	ctx := withWorkspace(req.Context(), wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if rr.Body.String() != "[]\n" {
		t.Errorf("expected [] empty list, got %s", rr.Body.String())
	}
}

func TestCredList_WithEntries(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCredentialEnc(t, db, wsID, userID, "c1", "github-token", "ghp_secret_value")

	req := httptest.NewRequest("GET", "/api/v1/credentials", nil)
	ctx := withWorkspace(req.Context(), wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rr.Code, rr.Body.String())
	}

	// Plaintext value MUST NOT appear in list response
	if strings.Contains(rr.Body.String(), "ghp_secret_value") {
		t.Error("list response leaked plaintext credential value")
	}

	var creds []credentialResponse
	json.Unmarshal(rr.Body.Bytes(), &creds)
	if len(creds) != 1 || creds[0].Name != "github-token" {
		t.Errorf("creds = %+v", creds)
	}
}

func TestCredGet_NotFound(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	req := httptest.NewRequest("GET", "/api/v1/credentials/missing", nil)
	req.SetPathValue("credentialId", "missing")
	ctx := withWorkspace(req.Context(), wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

func TestCredGet_Success(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCredentialEnc(t, db, wsID, userID, "c1", "my-cred", "secret-val")

	req := httptest.NewRequest("GET", "/api/v1/credentials/c1", nil)
	req.SetPathValue("credentialId", "c1")
	ctx := withWorkspace(req.Context(), wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "secret-val") {
		t.Error("get response leaked plaintext value")
	}
}

func TestCredGet_OtherWorkspaceDenied(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCredentialEnc(t, db, wsID, userID, "c1", "my-cred", "secret-val")

	// Try to access from different workspace
	req := httptest.NewRequest("GET", "/api/v1/credentials/c1", nil)
	req.SetPathValue("credentialId", "c1")
	ctx := withWorkspace(req.Context(), "other-ws", "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("cross-workspace access status = %d, want 404", rr.Code)
	}
}

// ---- Update ----

func TestCredUpdate_Forbidden(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCredentialEnc(t, db, wsID, userID, "c1", "x", "v")

	body := bytes.NewBufferString(`{"name":"new"}`)
	req := httptest.NewRequest("PATCH", "/api/v1/credentials/c1", body)
	req.SetPathValue("credentialId", "c1")
	ctx := withWorkspace(req.Context(), wsID, "VIEWER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rr.Code)
	}
}

func TestCredUpdate_NotFound(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	body := bytes.NewBufferString(`{"name":"new"}`)
	req := httptest.NewRequest("PATCH", "/api/v1/credentials/missing", body)
	req.SetPathValue("credentialId", "missing")
	ctx := withWorkspace(req.Context(), wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

func TestCredUpdate_RotateValue(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCredentialEnc(t, db, wsID, userID, "c1", "name", "old-secret")

	body := bytes.NewBufferString(`{"value":"new-secret"}`)
	req := httptest.NewRequest("PATCH", "/api/v1/credentials/c1", body)
	req.SetPathValue("credentialId", "c1")
	ctx := withWorkspace(req.Context(), wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rr.Code, rr.Body.String())
	}

	var encVal string
	db.QueryRow("SELECT encrypted_value FROM credentials WHERE id = 'c1'").Scan(&encVal)
	plain, _ := encryption.Decrypt(encVal)
	if plain != "new-secret" {
		t.Errorf("decrypted = %q, want new-secret", plain)
	}
}

func TestCredUpdate_NoFields(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCredentialEnc(t, db, wsID, userID, "c1", "n", "v")

	body := bytes.NewBufferString(`{}`)
	req := httptest.NewRequest("PATCH", "/api/v1/credentials/c1", body)
	req.SetPathValue("credentialId", "c1")
	ctx := withWorkspace(req.Context(), wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

// ---- Delete ----

func TestCredDelete_Forbidden(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCredentialEnc(t, db, wsID, userID, "c1", "n", "v")

	req := httptest.NewRequest("DELETE", "/api/v1/credentials/c1", nil)
	req.SetPathValue("credentialId", "c1")
	ctx := withWorkspace(req.Context(), wsID, "VIEWER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rr.Code)
	}
}

func TestCredDelete_NotFound(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	req := httptest.NewRequest("DELETE", "/api/v1/credentials/missing", nil)
	req.SetPathValue("credentialId", "missing")
	ctx := withWorkspace(req.Context(), wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

func TestCredDelete_Success(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCredentialEnc(t, db, wsID, userID, "c1", "n", "v")

	req := httptest.NewRequest("DELETE", "/api/v1/credentials/c1", nil)
	req.SetPathValue("credentialId", "c1")
	ctx := withWorkspace(req.Context(), wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rr.Code, rr.Body.String())
	}

	// Soft delete: row remains but deleted_at is set
	var deletedAt sql.NullString
	db.QueryRow("SELECT deleted_at FROM credentials WHERE id = 'c1'").Scan(&deletedAt)
	if !deletedAt.Valid {
		t.Error("expected deleted_at to be set")
	}
}

// ---- Test handler (validation endpoint) ----

func TestCredTest_BadRequest(t *testing.T) {
	t.Parallel()
	h, _ := newCredHandler(t)

	tests := []string{`bad json`, `{}`}
	for _, body := range tests {
		req := httptest.NewRequest("POST", "/api/v1/credentials/test", strings.NewReader(body))
		rr := httptest.NewRecorder()
		h.Test(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("body=%q status = %d, want 400", body, rr.Code)
		}
	}
}

func TestCredTest_AnthropicOAuthToken(t *testing.T) {
	t.Parallel()
	h, _ := newCredHandler(t)
	body := `{"provider":"ANTHROPIC","type":"AI_CLI_TOKEN","value":"sk-ant-oat01-xxxx"}`
	req := httptest.NewRequest("POST", "/api/v1/credentials/test", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.Test(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "OAuth token") {
		t.Errorf("expected OAuth token mention: %s", rr.Body.String())
	}
}

func TestCredTest_DefaultProvider(t *testing.T) {
	t.Parallel()
	h, _ := newCredHandler(t)
	body := `{"provider":"WHATEVER","value":"v"}`
	req := httptest.NewRequest("POST", "/api/v1/credentials/test", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.Test(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "No validation") {
		t.Errorf("expected 'No validation' message: %s", rr.Body.String())
	}
}

// ---- DefaultEnvVar ----

func TestCredDefaultEnvVar(t *testing.T) {
	t.Parallel()
	h, _ := newCredHandler(t)

	tests := map[string]string{
		"GITHUB":     "GH_TOKEN",
		"GITLAB":     "GITLAB_TOKEN",
		"VERCEL":     "VERCEL_TOKEN",
		"AWS":        "AWS_ACCESS_KEY_ID",
		"KUBERNETES": "KUBECONFIG",
		"UNKNOWN":    "",
	}
	for prov, want := range tests {
		req := httptest.NewRequest("GET", "/api/v1/credentials/default-env-var?provider="+prov, nil)
		rr := httptest.NewRecorder()
		h.DefaultEnvVar(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("provider=%s status=%d", prov, rr.Code)
			continue
		}
		var resp map[string]string
		json.Unmarshal(rr.Body.Bytes(), &resp)
		if resp["env_var"] != want {
			t.Errorf("provider=%s env_var=%q, want %q", prov, resp["env_var"], want)
		}
	}
}

func TestIsAnthropicOAuthToken(t *testing.T) {
	t.Parallel()
	tests := []struct {
		val  string
		want bool
	}{
		{"sk-ant-oat01-xxxx", true},
		{"sk-ant-api03-xxxx", false},
		{"", false},
		{"oat-xxx", false},
	}
	for _, tt := range tests {
		got := isAnthropicOAuthToken(tt.val)
		if got != tt.want {
			t.Errorf("isAnthropicOAuthToken(%q) = %v, want %v", tt.val, got, tt.want)
		}
	}
}

func TestDefaultEnvVarForCLIProvider(t *testing.T) {
	t.Parallel()
	if got := defaultEnvVarForCLIProvider("GITHUB"); got != "GH_TOKEN" {
		t.Errorf("got %q, want GH_TOKEN", got)
	}
	if got := defaultEnvVarForCLIProvider(""); got != "" {
		t.Errorf("empty provider should return empty, got %q", got)
	}
}

// ---- Crew-scoped credential ----

func TestCredUpdate_Metadata(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCredentialEnc(t, db, wsID, userID, "c1", "n", "v")

	body := bytes.NewBufferString(`{"name":"renamed","description":"new desc","provider":"GITHUB"}`)
	req := httptest.NewRequest("PATCH", "/api/v1/credentials/c1", body)
	req.SetPathValue("credentialId", "c1")
	ctx := withWorkspace(req.Context(), wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rr.Code, rr.Body.String())
	}

	var name string
	db.QueryRow("SELECT name FROM credentials WHERE id = 'c1'").Scan(&name)
	if name != "renamed" {
		t.Errorf("name = %q, want renamed", name)
	}
}

func TestCredUpdate_BadJSON(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCredentialEnc(t, db, wsID, userID, "c1", "n", "v")

	req := httptest.NewRequest("PATCH", "/api/v1/credentials/c1", strings.NewReader(`bad`))
	req.SetPathValue("credentialId", "c1")
	ctx := withWorkspace(req.Context(), wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestCredUpdate_CrewIDsAutoScope(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCredentialEnc(t, db, wsID, userID, "c1", "n", "v")
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug, created_at, updated_at) VALUES ('crew-1', ?, 'C', 'c', datetime('now'), datetime('now'))`, wsID); err != nil {
		t.Fatalf("seed crew: %v", err)
	}

	// Add crew_ids via update — should auto-set scope to CREW
	body := bytes.NewBufferString(`{"crew_ids":["crew-1"]}`)
	req := httptest.NewRequest("PATCH", "/api/v1/credentials/c1", body)
	req.SetPathValue("credentialId", "c1")
	ctx := withWorkspace(req.Context(), wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rr.Code, rr.Body.String())
	}

	var scope string
	db.QueryRow("SELECT scope FROM credentials WHERE id = 'c1'").Scan(&scope)
	if scope != "CREW" {
		t.Errorf("scope = %q, want CREW", scope)
	}

	// Verify junction table updated
	var count int
	db.QueryRow("SELECT COUNT(*) FROM credential_crews WHERE credential_id = 'c1'").Scan(&count)
	if count != 1 {
		t.Errorf("credential_crews = %d, want 1", count)
	}
}

func TestCredUpdate_InvalidCrewID(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCredentialEnc(t, db, wsID, userID, "c1", "n", "v")

	body := bytes.NewBufferString(`{"crew_id":"nonexistent-crew"}`)
	req := httptest.NewRequest("PATCH", "/api/v1/credentials/c1", body)
	req.SetPathValue("credentialId", "c1")
	ctx := withWorkspace(req.Context(), wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestCredCreate_WithCrewIDs(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	// Insert a crew
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug, created_at, updated_at) VALUES ('crew-1', ?, 'C', 'c', datetime('now'), datetime('now'))`, wsID); err != nil {
		t.Fatalf("seed crew: %v", err)
	}

	body := bytes.NewBufferString(fmt.Sprintf(`{"name":"crew-cred","value":"v","crew_ids":["%s"]}`, "crew-1"))
	req := httptest.NewRequest("POST", "/api/v1/credentials", body)
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, body: %s", rr.Code, rr.Body.String())
	}

	var resp credentialResponse
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Scope != "CREW" {
		t.Errorf("scope = %q, want CREW (auto-set when crew_ids provided)", resp.Scope)
	}

	// Verify junction table row
	var count int
	db.QueryRow("SELECT COUNT(*) FROM credential_crews WHERE credential_id = ?", resp.ID).Scan(&count)
	if count != 1 {
		t.Errorf("credential_crews count = %d, want 1", count)
	}
}

// ---- Update — merged-payload validation ----
//
// Guards the invariant that Update enforces the same per-type rules as
// Create: a PATCH can't drop a credential into a state that the
// resolver / sidecar mount path can't handle. Each subtest seeds a
// row, fires a PATCH, and asserts the right status + an unchanged DB
// row when the patch is rejected.
//
// See the merged-payload validation block at the top of
// CredentialHandler.Update in credentials_mutate.go.

// seedTypedCredential is a USERPASS-aware variant of seedCredentialEnc
// — lets the PATCH-validation suite express "this row was a USERPASS
// with username U and encrypted password P" without copy-pasting the
// INSERT.
func seedTypedCredential(t *testing.T, db *sql.DB, wsID, userID, credID, name, credType, username, plainValue string) {
	t.Helper()
	enc, err := encryption.Encrypt(plainValue)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	var usernameArg any
	if username != "" {
		usernameArg = username
	}
	if _, err := db.Exec(`
		INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, provider, scope, status, username, created_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 'NONE', 'WORKSPACE', 'ACTIVE', ?, ?, datetime('now'), datetime('now'))`,
		credID, wsID, name, enc, credType, usernameArg, userID); err != nil {
		t.Fatalf("seed typed cred: %v", err)
	}
}

func TestCredUpdate_RejectsBadTypeChange(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	// Existing API_KEY row; PATCH tries to flip type to USERPASS
	// without providing a username — invalid because USERPASS rows
	// must always have a username.
	seedTypedCredential(t, db, wsID, userID, "c1", "GH", "API_KEY", "", "ghp_legacy")

	body := bytes.NewBufferString(`{"type":"USERPASS"}`)
	req := httptest.NewRequest("PATCH", "/api/v1/credentials/c1", body)
	req.SetPathValue("credentialId", "c1")
	req = req.WithContext(withWorkspace(req.Context(), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "username is required") {
		t.Errorf("body should mention 'username is required', got: %s", rr.Body.String())
	}

	// DB unchanged.
	var got string
	db.QueryRow("SELECT type FROM credentials WHERE id = 'c1'").Scan(&got)
	if got != "API_KEY" {
		t.Errorf("type after rejected PATCH = %q, want API_KEY", got)
	}
}

func TestCredUpdate_RejectsTypeChangeToSSHWithoutValue(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedTypedCredential(t, db, wsID, userID, "c1", "GH", "API_KEY", "", "ghp_legacy")

	// Flipping to SSH_KEY without sending a new PEM value is invalid:
	// the existing ghp_legacy isn't PEM-shaped, and we can't validate
	// what we can't see (encrypted blob).
	body := bytes.NewBufferString(`{"type":"SSH_KEY"}`)
	req := httptest.NewRequest("PATCH", "/api/v1/credentials/c1", body)
	req.SetPathValue("credentialId", "c1")
	req = req.WithContext(withWorkspace(req.Context(), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "requires a new value") {
		t.Errorf("body should explain new value is required, got: %s", rr.Body.String())
	}
}

func TestCredUpdate_RejectsNonPEMValueOnSSH(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	// Existing SSH_KEY row; PATCH tries to swap in a non-PEM value
	// (common foot-gun: pasting an OpenSSH public key, "ssh-rsa AAAA…").
	seedTypedCredential(t, db, wsID, userID, "c1", "DEPLOY", "SSH_KEY", "", pemFixture("OPENSSH PRIVATE KEY", "abc"))

	body := bytes.NewBufferString(`{"value":"ssh-rsa AAAAB3NzaC1yc2example"}`)
	req := httptest.NewRequest("PATCH", "/api/v1/credentials/c1", body)
	req.SetPathValue("credentialId", "c1")
	req = req.WithContext(withWorkspace(req.Context(), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "PEM-encoded private key") {
		t.Errorf("body should mention PEM, got: %s", rr.Body.String())
	}
}

func TestCredUpdate_AllowsTypeChangeToUserPassWithUsername(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedTypedCredential(t, db, wsID, userID, "c1", "GMAIL", "SECRET", "", "old-password")

	// Flipping to USERPASS while supplying both username and value
	// (re-encrypted as the new password) is the legitimate
	// migration path — accept it.
	body := bytes.NewBufferString(`{"type":"USERPASS","username":"user@gmail.com","value":"new-pass"}`)
	req := httptest.NewRequest("PATCH", "/api/v1/credentials/c1", body)
	req.SetPathValue("credentialId", "c1")
	req = req.WithContext(withWorkspace(req.Context(), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rr.Code, rr.Body.String())
	}

	var gotType string
	var gotUsername sql.NullString
	db.QueryRow("SELECT type, username FROM credentials WHERE id = 'c1'").Scan(&gotType, &gotUsername)
	if gotType != "USERPASS" {
		t.Errorf("type = %q, want USERPASS", gotType)
	}
	if !gotUsername.Valid || gotUsername.String != "user@gmail.com" {
		t.Errorf("username = %v, want user@gmail.com", gotUsername)
	}
}

func TestCredUpdate_RejectsNullingUsernameOnUserPass(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedTypedCredential(t, db, wsID, userID, "c1", "GMAIL", "USERPASS", "user@gmail.com", "pwd")

	// PATCH that explicitly sets username to null on a USERPASS row
	// must be rejected — the row would otherwise become invalid.
	body := bytes.NewBufferString(`{"username":null}`)
	req := httptest.NewRequest("PATCH", "/api/v1/credentials/c1", body)
	req.SetPathValue("credentialId", "c1")
	req = req.WithContext(withWorkspace(req.Context(), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rr.Code, rr.Body.String())
	}
}

func TestCredUpdate_MetadataOnlyPatchSkipsValueValidation(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	// Existing SSH_KEY — encrypted blob isn't PEM-checked at rest,
	// but the row was validated at Create time. A pure-metadata
	// PATCH (just renaming) must not re-validate the value.
	seedTypedCredential(t, db, wsID, userID, "c1", "DEPLOY", "SSH_KEY", "", pemFixture("OPENSSH PRIVATE KEY", "abc"))

	body := bytes.NewBufferString(`{"description":"updated desc"}`)
	req := httptest.NewRequest("PATCH", "/api/v1/credentials/c1", body)
	req.SetPathValue("credentialId", "c1")
	req = req.WithContext(withWorkspace(req.Context(), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rr.Code, rr.Body.String())
	}
}

func TestCredUpdate_RejectsUnknownType(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedTypedCredential(t, db, wsID, userID, "c1", "GH", "API_KEY", "", "ghp_x")

	body := bytes.NewBufferString(`{"type":"BANANA"}`)
	req := httptest.NewRequest("PATCH", "/api/v1/credentials/c1", body)
	req.SetPathValue("credentialId", "c1")
	req = req.WithContext(withWorkspace(req.Context(), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rr.Code, rr.Body.String())
	}
}
