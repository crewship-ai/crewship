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

// TestCredUpdate_MergedValidation is the table-driven suite for
// CredentialHandler.Update's merged-payload validation. Every row
// seeds a typed credential, fires a PATCH, and asserts the expected
// HTTP status. Failure-case subtests also verify the DB row was NOT
// mutated; success-case subtests verify the new column values are
// what they should be.
//
// Coverage targets:
//   - closed-enum gate (unknown type rejected)
//   - non-string JSON gate for type/username/value (must be strings,
//     not the silent fall-through that lets {"type": 123} through)
//   - per-type field requirements (USERPASS needs username, SSH_KEY
//     needs PEM-shaped value)
//   - type transitions that can/can't validate the existing value
//   - metadata-only PATCH on a vault-type row skips value re-check
func TestCredUpdate_MergedValidation(t *testing.T) {
	t.Parallel()

	pemSSH := pemFixture("OPENSSH PRIVATE KEY", "abc")

	tests := []struct {
		name string
		// seed describes the starting row — type, username, encrypted-value
		// plaintext source. Empty username → NULL.
		seedType, seedUsername, seedPlain string
		body                              string
		wantStatus                        int
		// wantBodyContains: substring the response body must contain
		// (skipped if empty). Useful for asserting the user-facing reason.
		wantBodyContains string
		// dbAssert (optional) runs after the handler returns. Lets a
		// success case inspect the rotated columns, or a failure case
		// confirm the row wasn't mutated.
		dbAssert func(t *testing.T, db *sql.DB)
	}{
		{
			name:             "rejects type change to USERPASS without username",
			seedType:         "API_KEY",
			seedPlain:        "ghp_legacy",
			body:             `{"type":"USERPASS"}`,
			wantStatus:       http.StatusBadRequest,
			wantBodyContains: "username is required",
			dbAssert: func(t *testing.T, db *sql.DB) {
				var got string
				db.QueryRow("SELECT type FROM credentials WHERE id = 'c1'").Scan(&got)
				if got != "API_KEY" {
					t.Errorf("type after rejected PATCH = %q, want API_KEY", got)
				}
			},
		},
		{
			name:             "rejects type change to SSH_KEY without new value",
			seedType:         "API_KEY",
			seedPlain:        "ghp_legacy",
			body:             `{"type":"SSH_KEY"}`,
			wantStatus:       http.StatusBadRequest,
			wantBodyContains: "requires a new value",
		},
		{
			name:             "rejects type change to CERTIFICATE without new value",
			seedType:         "API_KEY",
			seedPlain:        "ghp_legacy",
			body:             `{"type":"CERTIFICATE"}`,
			wantStatus:       http.StatusBadRequest,
			wantBodyContains: "requires a new value",
		},
		{
			name:             "rejects non-PEM value swapped onto existing SSH_KEY",
			seedType:         "SSH_KEY",
			seedPlain:        pemSSH,
			body:             `{"value":"ssh-rsa AAAAB3NzaC1yc2example"}`,
			wantStatus:       http.StatusBadRequest,
			wantBodyContains: "PEM-encoded private key",
		},
		{
			name:             "rejects nulling username on existing USERPASS",
			seedType:         "USERPASS",
			seedUsername:     "user@gmail.com",
			seedPlain:        "pwd",
			body:             `{"username":null}`,
			wantStatus:       http.StatusBadRequest,
			wantBodyContains: "username is required",
		},
		{
			name:             "rejects unknown type string",
			seedType:         "API_KEY",
			seedPlain:        "ghp_x",
			body:             `{"type":"BANANA"}`,
			wantStatus:       http.StatusBadRequest,
			wantBodyContains: "type must be one of",
		},
		{
			// Defense against JSON-shape smuggling — without the
			// explicit type assertion in Update, a numeric "type"
			// would skip the validator (cast to string fails →
			// treated as absent → mergedType=currentType=valid) but
			// still flow through the generic ub.Set loop and end up
			// in the column as a string. Reject at the boundary.
			name:             "rejects numeric type (non-string JSON)",
			seedType:         "API_KEY",
			seedPlain:        "ghp_x",
			body:             `{"type":123}`,
			wantStatus:       http.StatusBadRequest,
			wantBodyContains: "type must be a string",
		},
		{
			name:             "rejects numeric username on USERPASS",
			seedType:         "USERPASS",
			seedUsername:     "user@gmail.com",
			seedPlain:        "pwd",
			body:             `{"username":42}`,
			wantStatus:       http.StatusBadRequest,
			wantBodyContains: "username must be a string",
		},
		{
			name:             "rejects array value on SSH_KEY",
			seedType:         "SSH_KEY",
			seedPlain:        pemSSH,
			body:             `{"value":["array","not","string"]}`,
			wantStatus:       http.StatusBadRequest,
			wantBodyContains: "value must be a string",
		},
		{
			name:       "allows type change to USERPASS with username + value",
			seedType:   "SECRET",
			seedPlain:  "old-password",
			body:       `{"type":"USERPASS","username":"user@gmail.com","value":"new-pass"}`,
			wantStatus: http.StatusOK,
			dbAssert: func(t *testing.T, db *sql.DB) {
				var gotType string
				var gotUsername sql.NullString
				db.QueryRow("SELECT type, username FROM credentials WHERE id = 'c1'").Scan(&gotType, &gotUsername)
				if gotType != "USERPASS" {
					t.Errorf("type = %q, want USERPASS", gotType)
				}
				if !gotUsername.Valid || gotUsername.String != "user@gmail.com" {
					t.Errorf("username = %v, want user@gmail.com", gotUsername)
				}
			},
		},
		{
			name:       "metadata-only PATCH on existing SSH_KEY skips value re-check",
			seedType:   "SSH_KEY",
			seedPlain:  pemSSH,
			body:       `{"description":"updated desc"}`,
			wantStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h, db := newCredHandler(t)
			userID := seedTestUser(t, db)
			wsID := seedTestWorkspace(t, db, userID)
			seedTypedCredential(t, db, wsID, userID, "c1", "c1-name", tt.seedType, tt.seedUsername, tt.seedPlain)

			req := httptest.NewRequest("PATCH", "/api/v1/credentials/c1", bytes.NewBufferString(tt.body))
			req.SetPathValue("credentialId", "c1")
			req = req.WithContext(withWorkspace(req.Context(), wsID, "OWNER"))
			rr := httptest.NewRecorder()
			h.Update(rr, req)

			if rr.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body: %s", rr.Code, tt.wantStatus, rr.Body.String())
			}
			if tt.wantBodyContains != "" && !strings.Contains(rr.Body.String(), tt.wantBodyContains) {
				t.Errorf("body missing substring %q, got: %s", tt.wantBodyContains, rr.Body.String())
			}
			// Default DB-immutability check for rejection cases that
			// don't override dbAssert. Catches a class of partial-
			// write regressions where the validator returns 400 but
			// some upstream code path (ub.Set, tx unflushed) already
			// mutated the row before the error bubbled up.
			if rr.Code >= 400 && tt.dbAssert == nil {
				var gotType string
				var gotUsername sql.NullString
				if err := db.QueryRow(`SELECT type, username FROM credentials WHERE id = 'c1'`).Scan(&gotType, &gotUsername); err != nil {
					t.Fatalf("reload row for immutability check: %v", err)
				}
				if gotType != tt.seedType {
					t.Errorf("type after rejected PATCH = %q, want %q (row mutated despite 400)", gotType, tt.seedType)
				}
				switch {
				case tt.seedUsername == "" && gotUsername.Valid:
					t.Errorf("username after rejected PATCH = %q, want NULL", gotUsername.String)
				case tt.seedUsername != "" && (!gotUsername.Valid || gotUsername.String != tt.seedUsername):
					t.Errorf("username after rejected PATCH = %v, want %q", gotUsername, tt.seedUsername)
				}
			}
			if tt.dbAssert != nil {
				tt.dbAssert(t, db)
			}
		})
	}
}
