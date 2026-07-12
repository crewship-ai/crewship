package api

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/crewship-ai/crewship/internal/encryption"
)

// seedEndpointCred inserts an ACTIVE ENDPOINT_URL credential with the given
// stored value (bare URL or JSON envelope).
func seedEndpointCred(t *testing.T, db *sql.DB, credID, wsID, userID, value string) {
	t.Helper()
	enc, err := encryption.Encrypt(value)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, provider, scope, status, created_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'ENDPOINT_URL', 'OLLAMA', 'WORKSPACE', 'ACTIVE', ?, datetime('now'), datetime('now'))`,
		credID, wsID, "ollama-"+credID, enc, userID); err != nil {
		t.Fatalf("seed endpoint cred: %v", err)
	}
}

// #974 S1: rotating an ENDPOINT_URL credential with a bare token would (before
// this fix) overwrite the {baseURL,apiKey,headers} object with a non-URL string
// and silently break the endpoint at run time. The server must now reject a
// non-URL rotate value for ENDPOINT_URL, and accept a valid endpoint value.
func TestRotate_EndpointURL_Validated(t *testing.T) {
	t.Parallel()
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	h := NewCredentialHandler(db, logger)

	rotate := func(credID, value string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]any{"value": value})
		req := rotationReq(t, "POST", "/api/v1/credentials/"+credID+"/rotate", string(body), userID, wsID)
		req.SetPathValue("credentialId", credID)
		rr := httptest.NewRecorder()
		h.Rotate(rr, req)
		return rr
	}

	// A bare token (not a URL) must be rejected, not silently stored.
	seedEndpointCred(t, db, "ep-rot-bad", wsID, userID, `{"baseURL":"https://llm.example.com/v1","apiKey":"old"}`)
	if rr := rotate("ep-rot-bad", "sk-just-a-bare-token"); rr.Code != http.StatusBadRequest {
		t.Errorf("bare-token rotate: status = %d, want 400 (body=%s)", rr.Code, rr.Body.String())
	}
	// The original value must be untouched after the rejected rotate.
	var enc string
	if err := db.QueryRow(`SELECT encrypted_value FROM credentials WHERE id = ?`, "ep-rot-bad").Scan(&enc); err != nil {
		t.Fatalf("read cred: %v", err)
	}
	dec, _ := encryption.Decrypt(enc)
	if dec != `{"baseURL":"https://llm.example.com/v1","apiKey":"old"}` {
		t.Errorf("value mutated by rejected rotate: %q", dec)
	}

	// A valid endpoint value (bare URL) is accepted.
	seedEndpointCred(t, db, "ep-rot-ok", wsID, userID, `https://llm.example.com/v1`)
	if rr := rotate("ep-rot-ok", `https://new.example.com/v1`); rr.Code != http.StatusOK {
		t.Errorf("valid-URL rotate: status = %d, want 200 (body=%s)", rr.Code, rr.Body.String())
	}

	// A full JSON envelope with a new token is accepted (the CLI --auth-token path).
	if rr := rotate("ep-rot-ok", `{"baseURL":"https://new.example.com/v1","apiKey":"rotated"}`); rr.Code != http.StatusOK {
		t.Errorf("JSON rotate: status = %d, want 200 (body=%s)", rr.Code, rr.Body.String())
	}
}
