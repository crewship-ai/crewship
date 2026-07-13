package api

// Tests for the webhook-secret rotate endpoint (#999).
//
// The webhook signing secret follows show-once semantics: it is NEVER
// readable back from any endpoint (the internal plaintext read was
// removed in the same change). Rotation is the only way to obtain a
// secret — mint new, return once, old secret stops validating.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func seedRotateAgent(t *testing.T, h *AgentHandler, wsID, id, secret string) {
	t.Helper()
	if _, err := h.db.Exec(
		`INSERT INTO agents (id, workspace_id, name, slug, status, webhook_secret)
		 VALUES (?, ?, 'Hooked', ?, 'IDLE', ?)`, id, wsID, id, secret); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
}

func rotateReq(t *testing.T, h *AgentHandler, userID, wsID, role, agentID string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("POST", "/api/v1/agents/"+agentID+"/webhook-secret/rotate", nil)
	r.SetPathValue("agentId", agentID)
	r = withWorkspaceUser(r, userID, wsID, role)
	rr := httptest.NewRecorder()
	h.RotateWebhookSecret(rr, r)
	return rr
}

func TestRotateWebhookSecret_MintsAndReturnsOnce(t *testing.T) {
	h, userID, wsID := covAUHandler(t)
	seedRotateAgent(t, h, wsID, "agent-rot", "whsec_old")

	rr := rotateReq(t, h, userID, wsID, "OWNER", "agent-rot")
	if rr.Code != http.StatusOK {
		t.Fatalf("rotate: status = %d, body: %s", rr.Code, rr.Body.String())
	}
	var resp struct {
		WebhookSecret string `json:"webhook_secret"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.WebhookSecret == "" || resp.WebhookSecret == "whsec_old" {
		t.Fatalf("rotate must mint a NEW secret; got %q", resp.WebhookSecret)
	}

	// The DB row now holds the returned secret — the old one is dead.
	var stored string
	if err := h.db.QueryRow(`SELECT webhook_secret FROM agents WHERE id = 'agent-rot'`).Scan(&stored); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if stored != resp.WebhookSecret {
		t.Errorf("stored secret %q != returned %q", stored, resp.WebhookSecret)
	}
}

func TestRotateWebhookSecret_MemberForbidden(t *testing.T) {
	h, userID, wsID := covAUHandler(t)
	seedRotateAgent(t, h, wsID, "agent-rot2", "whsec_old")

	rr := rotateReq(t, h, userID, wsID, "MEMBER", "agent-rot2")
	if rr.Code != http.StatusForbidden {
		t.Fatalf("MEMBER rotate: status = %d, want 403", rr.Code)
	}
	// Secret unchanged.
	var stored string
	_ = h.db.QueryRow(`SELECT webhook_secret FROM agents WHERE id = 'agent-rot2'`).Scan(&stored)
	if stored != "whsec_old" {
		t.Errorf("MEMBER rotate must not change the secret; got %q", stored)
	}
}

func TestRotateWebhookSecret_CrossWorkspace404(t *testing.T) {
	h, userID, wsID := covAUHandler(t)
	if _, err := h.db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws-other', 'O', 'o')`); err != nil {
		t.Fatalf("insert ws-other: %v", err)
	}
	seedRotateAgent(t, h, "ws-other", "agent-foreign", "whsec_foreign")

	rr := rotateReq(t, h, userID, wsID, "OWNER", "agent-foreign")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("cross-workspace rotate: status = %d, want 404", rr.Code)
	}
	var stored string
	_ = h.db.QueryRow(`SELECT webhook_secret FROM agents WHERE id = 'agent-foreign'`).Scan(&stored)
	if stored != "whsec_foreign" {
		t.Errorf("cross-workspace rotate must not change the secret; got %q", stored)
	}
}

func TestRotateWebhookSecret_UnknownAgent404(t *testing.T) {
	h, userID, wsID := covAUHandler(t)
	rr := rotateReq(t, h, userID, wsID, "OWNER", "ghost")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("unknown agent: status = %d, want 404", rr.Code)
	}
}
