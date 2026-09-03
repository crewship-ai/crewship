package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// The inbox row an escalation projects must say WHAT is being approved.
//
// A LINK escalation carried the URL only on the escalations row, so the
// inbox — the surface with the Approve button — showed "Escalation type LINK"
// and nothing else, and a person approved an address they could not see. A
// CREDENTIAL proposal likewise named the credential only in the title. Both
// now travel in the payload, where the inbox detail and `crewship inbox get`
// read them. The secret itself never does: the proposal's value is redacted
// before anything is stored, and this test pins that too.

func escInboxPayload(t *testing.T, h *QueryHandler, escalationID string) map[string]any {
	t.Helper()
	var raw string
	if err := h.db.QueryRow(`SELECT payload_json FROM inbox_items WHERE source_id = ?`, escalationID).Scan(&raw); err != nil {
		t.Fatalf("load inbox payload for %s: %v", escalationID, err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("decode payload %q: %v", raw, err)
	}
	return payload
}

func escIDOf(t *testing.T, body []byte) string {
	t.Helper()
	var out struct {
		EscalationID string `json:"escalation_id"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.EscalationID == "" {
		t.Fatalf("decode create response %s: %v", body, err)
	}
	return out.EscalationID
}

func TestCreateEscalation_InboxPayloadCarriesTheLink(t *testing.T) {
	h, _, wsID, crewID, agentID := covEscFixture(t)
	seedChat(t, h, "covesc-chat", agentID, wsID)
	const link = "https://github.com/crewship-ai/crewship/settings/access"
	rr := createEsc(h, wsID, map[string]string{
		"from_slug": "covesc-ag", "reason": "need write access to the docs repo", "crew_id": crewID,
		"workspace_id": wsID, "chat_id": "covesc-chat", "type": "LINK", "metadata": link,
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	payload := escInboxPayload(t, h, escIDOf(t, rr.Body.Bytes()))
	if got, _ := payload["link_url"].(string); got != link {
		t.Errorf("payload.link_url = %q, want %q", got, link)
	}
	if _, has := payload["credential_name"]; has {
		t.Errorf("payload = %v, a LINK must not claim a credential", payload)
	}
}

func TestCreateEscalation_InboxPayloadCarriesTheCredentialName_NeverTheValue(t *testing.T) {
	ensureEncryptionKey(t)
	h, _, wsID, crewID, agentID := covEscFixture(t)
	seedChat(t, h, "covesc-chat", agentID, wsID)
	const canary = "payload-canary-value-4242" //gitleaks:allow — fake fixture, asserts the value is absent
	rr := createEsc(h, wsID, map[string]string{
		"from_slug": "covesc-ag", "reason": "need the stripe test key", "crew_id": crewID,
		"workspace_id": wsID, "chat_id": "covesc-chat", "type": "CREDENTIAL",
		"metadata": `{"name":"STRIPE_TEST_KEY","type":"SECRET","provider":"NONE","value":"` + canary + `"}`,
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	payload := escInboxPayload(t, h, escIDOf(t, rr.Body.Bytes()))
	if got, _ := payload["credential_name"].(string); got != "STRIPE_TEST_KEY" {
		t.Errorf("payload.credential_name = %q, want STRIPE_TEST_KEY", got)
	}
	if _, has := payload["link_url"]; has {
		t.Errorf("payload = %v, a CREDENTIAL must not claim a link", payload)
	}
	raw, _ := json.Marshal(payload)
	if strings.Contains(string(raw), canary) {
		t.Errorf("the credential value leaked into the inbox payload: %s", raw)
	}
}

func TestCreateEscalation_TextInboxPayloadHasNeither(t *testing.T) {
	h, _, wsID, crewID, agentID := covEscFixture(t)
	seedChat(t, h, "covesc-chat", agentID, wsID)
	rr := createEsc(h, wsID, map[string]string{
		"from_slug": "covesc-ag", "reason": "delete the stale branches?", "crew_id": crewID,
		"workspace_id": wsID, "chat_id": "covesc-chat",
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	payload := escInboxPayload(t, h, escIDOf(t, rr.Body.Bytes()))
	for _, key := range []string{"link_url", "credential_name"} {
		if _, has := payload[key]; has {
			t.Errorf("payload = %v, a TEXT question must not carry %s", payload, key)
		}
	}
}
