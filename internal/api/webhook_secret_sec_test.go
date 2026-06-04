package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSecWebhookCrossWorkspaceDenied is the regression guard for the
// cross-workspace webhook-secret leak: GetWebhookSecret used to look up
// `SELECT webhook_secret FROM agents WHERE id = ?` with the agentID
// straight from the path and no tenant scoping, so any internal caller
// (or the public webhook trigger flow) could fetch ANY agent's webhook
// secret across workspace boundaries and forge signed webhooks.
//
// Seed two workspaces; the target agent lives in WS-B and has a secret.
// A caller scoped to WS-A asks for WS-B's agent → must get 404 (not 403:
// we don't leak existence) and the secret must NOT appear in the body.
//
// Pre-fix this returned 200 + the secret. Post-fix it returns 404.
func TestSecWebhookCrossWorkspaceDenied(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsA := seedTestWorkspace(t, db, userID) // "test-workspace-id"

	// Second workspace (seedTestWorkspace uses a fixed ID, so insert WS-B
	// directly).
	wsB := "ws-b"
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'B', 'b')`, wsB); err != nil {
		t.Fatalf("insert ws-b: %v", err)
	}

	// Victim agent in WS-B with a secret.
	if _, err := db.Exec(
		`INSERT INTO agents (id, workspace_id, name, slug, status, webhook_secret)
		 VALUES ('victim-agent', ?, 'Victim', 'victim', 'IDLE', 'whsec_victim')`, wsB); err != nil {
		t.Fatalf("insert victim agent: %v", err)
	}

	h := NewInternalHandler(db, "tok", covICILogger())

	// Attacker scoped to WS-A reaches for WS-B's agent.
	req := httptest.NewRequest("GET",
		"/api/v1/internal/agents/victim-agent/webhook-secret?workspace_id="+wsA, nil)
	req.SetPathValue("agentId", "victim-agent")
	rr := httptest.NewRecorder()
	h.GetWebhookSecret(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("cross-workspace fetch: status = %d, want 404, body: %s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "whsec_victim") {
		t.Fatalf("cross-workspace fetch leaked the secret: body = %s", rr.Body.String())
	}
}

// TestSecWebhookSameWorkspaceAllowed is the positive companion: a caller
// scoped to the agent's own workspace still gets the secret (the fix must
// not break the legitimate same-tenant lookup).
func TestSecWebhookSameWorkspaceAllowed(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsB := "ws-b"
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'B', 'b')`, wsB); err != nil {
		t.Fatalf("insert ws-b: %v", err)
	}
	_ = userID

	if _, err := db.Exec(
		`INSERT INTO agents (id, workspace_id, name, slug, status, webhook_secret)
		 VALUES ('victim-agent', ?, 'Victim', 'victim', 'IDLE', 'whsec_victim')`, wsB); err != nil {
		t.Fatalf("insert agent: %v", err)
	}

	h := NewInternalHandler(db, "tok", covICILogger())

	req := httptest.NewRequest("GET",
		"/api/v1/internal/agents/victim-agent/webhook-secret?workspace_id="+wsB, nil)
	req.SetPathValue("agentId", "victim-agent")
	rr := httptest.NewRecorder()
	h.GetWebhookSecret(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("same-workspace fetch: status = %d, want 200, body: %s", rr.Code, rr.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["webhook_secret"] != "whsec_victim" {
		t.Errorf("webhook_secret = %q, want whsec_victim", resp["webhook_secret"])
	}
}
