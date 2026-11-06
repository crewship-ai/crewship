package api

import (
	"context"
	"errors"
	"testing"

	"github.com/crewship-ai/crewship/internal/encryption"
)

func seedBoundKey(t *testing.T, id, wsID, ownerID, provider, hosts string, exec func(string, ...any)) {
	t.Helper()
	enc, err := encryption.Encrypt("sk-live-123")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	exec(`INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, provider, status, allowed_hosts, created_by)
		VALUES (?, ?, ?, ?, ?, ?, 'ACTIVE', ?, ?)`, id, wsID, id, enc, CredTypeAPIKey, provider, hosts, ownerID)
}

// An agent can reach skill generation through the sidecar, so a key bound to
// other hosts must not be sent to the provider.
func TestResolveAnthropicProvider_HostGate(t *testing.T) {
	cases := []struct {
		name  string
		hosts string
		ok    bool
	}{
		{"unbound", "[]", true},
		{"approved", `["api.anthropic.com"]`, true},
		{"other host", `["api.partner.com"]`, false},
		{"unreadable", "not json", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ensureEncryptionKey(t)
			db := setupTestDB(t)
			ownerID := seedTestUser(t, db)
			wsID := seedTestWorkspace(t, db, ownerID)
			seedBoundKey(t, "k1", wsID, ownerID, "ANTHROPIC", tc.hosts, func(q string, a ...any) {
				t.Helper()
				execOrFatal(t, db, q, a...)
			})
			h := &SkillGenerateHandler{db: db, logger: testLogger()}
			_, err := h.resolveAnthropicProvider(context.Background(), wsID)
			if tc.ok && err != nil {
				t.Fatalf("want a provider, got %v", err)
			}
			if !tc.ok && !errors.Is(err, errNoActiveAnthropicCredential) {
				t.Fatalf("want the key withheld, got %v", err)
			}
		})
	}
}

// The Keeper reaches this lookup on an agent's request and no host is known
// here, so any host-bound credential is refused.
func TestGovModelCredentialLookup_HostBoundRefused(t *testing.T) {
	ensureEncryptionKey(t)
	db := setupTestDB(t)
	ownerID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, ownerID)
	exec := func(q string, a ...any) {
		t.Helper()
		execOrFatal(t, db, q, a...)
	}
	seedBoundKey(t, "free_key", wsID, ownerID, "OPENAI", "[]", exec)
	seedBoundKey(t, "bound_key", wsID, ownerID, "OPENAI", `["api.partner.com"]`, exec)
	l := &govModelCredentialLookup{db: db}
	if _, _, err := l.LookupCredential(context.Background(), wsID, "free_key"); err != nil {
		t.Fatalf("unbound key must keep working: %v", err)
	}
	if _, value, err := l.LookupCredential(context.Background(), wsID, "bound_key"); err == nil {
		t.Fatalf("host-bound key released: %q", value)
	}
}

// Clearing an unreadable list widens access, so it must leave an audit row.
func TestSetAllowedHosts_ClearingUnreadableListIsAudited(t *testing.T) {
	db, wsID, ownerID := seedHostBoundRig(t, "restricted", `["api.github.com"]`, "")
	execOrFatal(t, db, `UPDATE credentials SET allowed_hosts = 'not json'`)
	h := NewCredentialHandler(db, newTestLogger())
	if rr := hostsPut(t, h, ownerID, wsID, AuthKindSession, nil, "", `{"allowed_hosts":[]}`, nil); rr.Code != 200 {
		t.Fatalf("PUT: %d %s", rr.Code, rr.Body.String())
	}
	if rows := auditRows(t, db); len(rows) != 1 {
		t.Fatalf("audit rows = %d (%v), want 1", len(rows), rows)
	}
}
