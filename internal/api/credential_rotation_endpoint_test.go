package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/crewship-ai/crewship/internal/encryption"
)

// seedEndpointCred inserts an ENDPOINT_URL credential holding the given value.
func seedEndpointCred(t *testing.T, h *CredentialHandler, wsID, userID, credID, value string) {
	t.Helper()
	enc, err := encryption.Encrypt(value)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if _, err := h.db.Exec(`
		INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, provider, scope, status, created_by, created_at, updated_at)
		VALUES (?, ?, 'ollama-ep', ?, 'ENDPOINT_URL', 'OLLAMA', 'WORKSPACE', 'ACTIVE', ?, datetime('now'), datetime('now'))`,
		credID, wsID, enc, userID); err != nil {
		t.Fatalf("seed endpoint cred: %v", err)
	}
}

// #974 S1: rotating an ENDPOINT_URL credential must go through the same shape
// gate as create — a bare token (non-URL) would otherwise overwrite the stored
// {baseURL,apiKey,headers} JSON and the endpoint silently vanishes at run time.
func TestRotate_EndpointURL_RejectsNonURLValue(t *testing.T) {
	t.Parallel()
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	h := NewCredentialHandler(db, logger)

	credID := "cred-ep-rot"
	seedEndpointCred(t, h, wsID, userID, credID, `{"baseURL":"https://proxy/v1","apiKey":"sk-old"}`)

	// A naive rotate of just a token (not a URL) must be rejected, not stored.
	body, _ := json.Marshal(map[string]any{"value": "sk-brand-new-token"})
	req := rotationReq(t, "POST", "/api/v1/credentials/"+credID+"/rotate", string(body), userID, wsID)
	req.SetPathValue("credentialId", credID)
	rr := httptest.NewRecorder()
	h.Rotate(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("rotate ENDPOINT_URL with a bare token = %d, want 400 (body=%s)", rr.Code, rr.Body.String())
	}

	// A proper new endpoint value (JSON object) rotates fine.
	body, _ = json.Marshal(map[string]any{"value": `{"baseURL":"https://proxy/v1","apiKey":"sk-new"}`})
	req = rotationReq(t, "POST", "/api/v1/credentials/"+credID+"/rotate", string(body), userID, wsID)
	req.SetPathValue("credentialId", credID)
	rr = httptest.NewRecorder()
	h.Rotate(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("rotate ENDPOINT_URL with a valid JSON value = %d, want 200 (body=%s)", rr.Code, rr.Body.String())
	}
}
