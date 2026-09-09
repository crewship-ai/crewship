package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestProviderLoginUpdateReplacesPartsPreservesIdentity(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	code, created := plCreate(t, h, userID, wsID, map[string]any{"name": "existing-account", "type": "PROVIDER_LOGIN", "provider": "OPENAI", "mode": "subscription", "value": plCodexAuthJSON(t, plFakeJWT(t, "plus", time.Now().Add(time.Hour)))})
	if code != http.StatusCreated {
		t.Fatalf("create: %d", code)
	}
	id := created["id"].(string)
	execOrFatal(t, db, `INSERT INTO credential_bindings (id,workspace_id,credential_id,scope,slot,created_at,updated_at) VALUES ('relogin-binding',?,?,'WORKSPACE','OPENAI_API_KEY',datetime('now'),datetime('now'))`, wsID, id)
	access := plFakeJWT(t, "team", time.Now().Add(48*time.Hour))
	body, _ := json.Marshal(map[string]any{"value": plCodexAuthJSON(t, access), "mode": "subscription"})
	req := plRequest(t, "PATCH", "/api/v1/credentials/"+id, string(body), userID, wsID, "OWNER")
	req.SetPathValue("credentialId", id)
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("update: %d %s", rr.Code, rr.Body.String())
	}
	if got := plDecryptColumn(t, db, `SELECT encrypted_value FROM credentials WHERE id = ?`, id); got != access {
		t.Fatal("did not store the replacement access token")
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM credential_bindings WHERE credential_id = ?`, id).Scan(&count); err != nil || count != 1 {
		t.Fatalf("binding lost: %d %v", count, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM credentials WHERE workspace_id = ?`, wsID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("duplicate account: %d %v", count, err)
	}
	// A malformed import must leave the entire previous login usable.
	bad := plRequest(t, "PATCH", "/api/v1/credentials/"+id, `{"value":"{}","mode":"subscription"}`, userID, wsID, "OWNER")
	bad.SetPathValue("credentialId", id)
	rr = httptest.NewRecorder()
	h.Update(rr, bad)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("malformed import: %d", rr.Code)
	}
	if got := plDecryptColumn(t, db, `SELECT encrypted_value FROM credentials WHERE id = ?`, id); got != access {
		t.Fatal("invalid import changed the saved login")
	}
}
