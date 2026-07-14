package api

import (
	"context"
	"testing"

	"github.com/crewship-ai/crewship/internal/encryption"
)

// TestGovModelCredentialLookup covers the production governance.CredentialLookup
// seam — the runtime half of §4.4 revoke-safety (#1001). It must return the
// usable value for an ACTIVE credential and an ERROR (so ResolveGovModel
// degrades) for anything revoked / soft-deleted / inactive / missing / pending.
func TestGovModelCredentialLookup(t *testing.T) {
	ensureEncryptionKey(t)
	db := setupTestDB(t)
	ownerID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, ownerID)

	enc := func(plain string) string {
		t.Helper()
		e, err := encryption.Encrypt(plain)
		if err != nil {
			t.Fatalf("encrypt: %v", err)
		}
		return e
	}
	insert := func(id, credType, encVal, status, deletedAt string) {
		t.Helper()
		execOrFatal(t, db, `INSERT INTO credentials
			(id, workspace_id, name, encrypted_value, type, status, deleted_at, created_by)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			id, wsID, id, encVal, credType, status, nilIfEmpty(deletedAt), ownerID)
	}

	insert("ep_bare", CredTypeEndpointURL, enc("https://llm.example/v1/chat/completions"), "ACTIVE", "")
	insert("ep_json", CredTypeEndpointURL, enc(`{"baseURL":"https://tenant.example/v1/chat/completions","apiKey":"sk-embedded"}`), "ACTIVE", "")
	insert("key_active", CredTypeAPIKey, enc("sk-live-123"), "ACTIVE", "")
	insert("revoked", CredTypeAPIKey, enc("sk-old"), "ACTIVE", "2026-07-14T00:00:00Z") // soft-deleted
	insert("inactive", CredTypeAPIKey, enc("sk-off"), "REVOKED", "")                   // status flip

	l := newGovModelCredentialLookup(db)
	ctx := context.Background()

	t.Run("active ENDPOINT_URL (bare) returns base URL", func(t *testing.T) {
		ct, v, err := l.LookupCredential(ctx, wsID, "ep_bare")
		if err != nil {
			t.Fatalf("lookup: %v", err)
		}
		if ct != CredTypeEndpointURL || v != "https://llm.example/v1/chat/completions" {
			t.Errorf("got (%q,%q)", ct, v)
		}
	})

	t.Run("active ENDPOINT_URL (json) returns only base URL, strips embedded key", func(t *testing.T) {
		ct, v, err := l.LookupCredential(ctx, wsID, "ep_json")
		if err != nil {
			t.Fatalf("lookup: %v", err)
		}
		if ct != CredTypeEndpointURL || v != "https://tenant.example/v1/chat/completions" {
			t.Errorf("got (%q,%q); embedded apiKey must not leak into the value", ct, v)
		}
	})

	t.Run("active API_KEY returns the key", func(t *testing.T) {
		ct, v, err := l.LookupCredential(ctx, wsID, "key_active")
		if err != nil {
			t.Fatalf("lookup: %v", err)
		}
		if ct != CredTypeAPIKey || v != "sk-live-123" {
			t.Errorf("got (%q,%q)", ct, v)
		}
	})

	// The degrade triggers: each must return an error so ResolveGovModel falls
	// back to the default local judge instead of building a broken provider.
	for _, tc := range []struct{ name, id, ws string }{
		{"revoked (soft-deleted)", "revoked", wsID},
		{"inactive status", "inactive", wsID},
		{"missing id", "does-not-exist", wsID},
		{"wrong workspace", "key_active", "other-ws"},
	} {
		t.Run(tc.name+" -> error", func(t *testing.T) {
			if _, _, err := l.LookupCredential(ctx, tc.ws, tc.id); err == nil {
				t.Errorf("expected an error (so the resolver degrades), got nil")
			}
		})
	}
}
