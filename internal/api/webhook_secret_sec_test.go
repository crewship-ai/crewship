package api

import (
	"context"
	"testing"
)

// TestSecWebhookCrossCrewDenied is the regression guard for the cross-tenant
// webhook-secret leak. The secret used to be fetched via the internal IPC
// endpoint (GET .../agents/{agentId}/webhook-secret) by agent id, so the raw
// value round-tripped over IPC in plaintext JSON and, pre-scoping, ANY caller
// could fetch ANY agent's secret and forge signed webhooks. Post-#999 that
// endpoint is gone: (*WebhookHandler).lookupSecret reads the agents table
// directly, scoped by the crew named in the webhook URL (`AND crew_id = ?`).
//
// Seed the victim agent in crew-a with a secret; a lookup under crew-b must
// return an error and an empty secret — never the value.
func TestSecWebhookCrossCrewDenied(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedWebhookSecretAgent(t, db, wsID, "crew-a", "victim-agent", "whsec_victim")

	h := NewWebhookHandler(db, newTestLogger(), &fakeChatResolver{}, nil, nil, nil, nil)

	secret, err := h.lookupSecret(context.Background(), "crew-b", "victim-agent")
	if err == nil {
		t.Fatal("cross-crew lookup succeeded; want error (crew scoping must engage)")
	}
	if secret != "" {
		t.Fatalf("cross-crew lookup leaked the secret: %q", secret)
	}
}

// TestSecWebhookSameCrewAllowed is the positive companion: the crew the agent
// actually lives in still resolves the secret, and so does the legacy id-only
// form (empty crewID — webhook URLs minted before crew scoping existed). The
// fix must not break legitimate deliveries.
func TestSecWebhookSameCrewAllowed(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedWebhookSecretAgent(t, db, wsID, "crew-a", "victim-agent", "whsec_victim")

	h := NewWebhookHandler(db, newTestLogger(), &fakeChatResolver{}, nil, nil, nil, nil)

	got, err := h.lookupSecret(context.Background(), "crew-a", "victim-agent")
	if err != nil {
		t.Fatalf("same-crew lookup: %v", err)
	}
	if got != "whsec_victim" {
		t.Errorf("same-crew secret = %q, want whsec_victim", got)
	}

	got, err = h.lookupSecret(context.Background(), "", "victim-agent")
	if err != nil {
		t.Fatalf("legacy id-only lookup: %v", err)
	}
	if got != "whsec_victim" {
		t.Errorf("legacy id-only secret = %q, want whsec_victim", got)
	}
}
