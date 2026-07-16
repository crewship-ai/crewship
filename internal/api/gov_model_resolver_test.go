package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/keeper/governance"
	"github.com/crewship-ai/crewship/internal/llm"
)

// TestGovModelResolver_OpenAICompat_NoServerKeyExfil is a security regression:
// the openai_compat endpoint is tenant-admin-controlled, so the server's
// OPENAI_API_KEY must NEVER be attached to it. With no vault key configured, the
// built provider must send no server secret to the endpoint.
func TestGovModelResolver_OpenAICompat_NoServerKeyExfil(t *testing.T) {
	const serverSecret = "sk-server-secret-must-not-leak"
	t.Setenv("OPENAI_API_KEY", serverSecret)
	// Allow the loopback httptest endpoint past the SSRF fence for this test.
	t.Setenv("CREWSHIP_ALLOW_PRIVATE_ENDPOINTS", "1")

	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer srv.Close()

	r := NewGovModelResolver(nil, nil, newTestLogger(), testOllamaURL, testOllamaModel)
	p, err := r.buildProvider(governance.ResolvedGovModel{
		Provider:    governance.ProviderOpenAICompat,
		Model:       "gpt-x",
		EndpointURL: srv.URL + "/v1/chat/completions",
		APIKey:      "", // no vault key -> must NOT fall back to the server env key
	})
	if err != nil {
		t.Fatalf("buildProvider: %v", err)
	}
	_, _ = p.Complete(context.Background(), llm.Request{
		Model:    "gpt-x",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	})
	if strings.Contains(gotAuth, serverSecret) {
		t.Errorf("openai_compat endpoint received the server key in %q — key exfiltration", gotAuth)
	}
}

const (
	testOllamaURL   = "http://127.0.0.1:11434"
	testOllamaModel = "qwen2.5:3b-instruct"
)

// TestGovModelResolver_Resolve exercises the request-time wiring end-to-end:
// unconfigured → default, configured openai_compat → tenant provider, and the
// §4.4 revoke path → degrade to the OLLAMA default + a WARN journal entry.
func TestGovModelResolver_Resolve(t *testing.T) {
	ensureEncryptionKey(t)
	ctx := context.Background()

	t.Run("unconfigured workspace -> nil (gatekeeper keeps default)", func(t *testing.T) {
		db := setupTestDB(t)
		owner := seedTestUser(t, db)
		ws := seedTestWorkspace(t, db, owner)
		r := NewGovModelResolver(db, nil, newTestLogger(), testOllamaURL, testOllamaModel)

		p, model := r.Resolve(ctx, ws)
		if p != nil || model != "" {
			t.Errorf("unconfigured resolve = (%v, %q), want (nil, \"\")", p, model)
		}
	})

	t.Run("configured openai_compat -> tenant provider + model", func(t *testing.T) {
		db := setupTestDB(t)
		owner := seedTestUser(t, db)
		ws := seedTestWorkspace(t, db, owner)
		enc, _ := encryption.Encrypt("https://llm.tenant.example/v1/chat/completions")
		execOrFatal(t, db, `INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, created_by)
			VALUES ('gc1', ?, 'gov-endpoint', ?, 'ENDPOINT_URL', ?)`, ws, enc, owner)
		if err := governance.Upsert(ctx, db, ws, governance.Settings{
			GovModelProvider:     governance.ProviderOpenAICompat,
			GovModelID:           "gpt-4o-mini",
			GovModelCredentialID: "gc1",
		}, owner); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		r := NewGovModelResolver(db, nil, newTestLogger(), testOllamaURL, testOllamaModel)

		p, model := r.Resolve(ctx, ws)
		if p == nil {
			t.Fatal("configured resolve returned nil provider")
		}
		if model != "gpt-4o-mini" {
			t.Errorf("model = %q, want gpt-4o-mini", model)
		}
	})

	t.Run("revoked credential -> degrade to OLLAMA default + WARN journal (§4.4)", func(t *testing.T) {
		db := setupTestDB(t)
		owner := seedTestUser(t, db)
		ws := seedTestWorkspace(t, db, owner)
		enc, _ := encryption.Encrypt("https://llm.tenant.example/v1/chat/completions")
		// Seed already soft-deleted (revoked).
		execOrFatal(t, db, `INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, deleted_at, created_by)
			VALUES ('gc2', ?, 'gov-endpoint', ?, 'ENDPOINT_URL', '2026-07-14T00:00:00Z', ?)`, ws, enc, owner)
		if err := governance.Upsert(ctx, db, ws, governance.Settings{
			GovModelProvider:     governance.ProviderOpenAICompat,
			GovModelID:           "gpt-4o-mini",
			GovModelCredentialID: "gc2",
		}, owner); err != nil {
			t.Fatalf("upsert: %v", err)
		}

		jw := journal.NewWriter(db, newTestLogger(), journal.WriterOptions{FlushSize: 1})
		t.Cleanup(func() { _ = jw.Close() })
		r := NewGovModelResolver(db, jw, newTestLogger(), testOllamaURL, testOllamaModel)

		p, model := r.Resolve(ctx, ws)
		if p == nil {
			t.Fatal("degraded resolve must still return a working provider, got nil")
		}
		if model != testOllamaModel {
			t.Errorf("degraded model = %q, want the OLLAMA default %q", model, testOllamaModel)
		}
		_ = jw.Flush(ctx)

		var summary string
		err := db.QueryRowContext(ctx,
			`SELECT summary FROM journal_entries WHERE workspace_id = ? AND entry_type = ? AND severity = 'warn'`,
			ws, string(journal.EntryKeeperDecision)).Scan(&summary)
		if err != nil {
			t.Fatalf("expected a degrade WARN journal entry: %v", err)
		}
	})
}

// TestGovModelResolver_CacheKeyRotation pins the #954 fix for CodeQL
// go/weak-sensitive-data-hashing (alert 667): the build cache must not derive
// its map key from the API key via a fast hash. The key lives on the cache
// entry and is compared directly, so:
//   - same config + same key    -> cache hit (same provider instance),
//   - same config + rotated key -> rebuild that REPLACES the entry in place
//     (exactly one entry per non-secret fingerprint, no stale-key leak).
func TestGovModelResolver_CacheKeyRotation(t *testing.T) {
	ensureEncryptionKey(t)
	ctx := context.Background()

	db := setupTestDB(t)
	owner := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, owner)
	enc, _ := encryption.Encrypt("sk-ant-old")
	execOrFatal(t, db, `INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, created_by)
		VALUES ('gk1', ?, 'gov-key', ?, 'API_KEY', ?)`, ws, enc, owner)
	if err := governance.Upsert(ctx, db, ws, governance.Settings{
		GovModelProvider:     governance.ProviderAnthropic,
		GovModelID:           "claude-sonnet-4-5",
		GovModelCredentialID: "gk1",
	}, owner); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	r := NewGovModelResolver(db, nil, newTestLogger(), testOllamaURL, testOllamaModel)

	p1, _ := r.Resolve(ctx, ws)
	if p1 == nil {
		t.Fatal("configured resolve returned nil provider")
	}
	p2, _ := r.Resolve(ctx, ws)
	if p2 != p1 {
		t.Error("same config + same key must hit the cache (same provider instance)")
	}

	// Rotate the vault key: same non-secret config, new secret.
	encNew, _ := encryption.Encrypt("sk-ant-new")
	execOrFatal(t, db, `UPDATE credentials SET encrypted_value = ? WHERE id = 'gk1'`, encNew)

	p3, _ := r.Resolve(ctx, ws)
	if p3 == nil {
		t.Fatal("resolve after key rotation returned nil provider")
	}
	if p3 == p1 {
		t.Error("rotated key must rebuild the provider, got the stale cached instance")
	}

	r.mu.Lock()
	entries := len(r.cache)
	var fp string
	for k := range r.cache {
		fp = k
	}
	r.mu.Unlock()
	if entries != 1 {
		t.Errorf("cache holds %d entries after rotation, want 1 (entry replaced in place, no stale-key leak)", entries)
	}
	if strings.Contains(fp, "sk-ant-old") || strings.Contains(fp, "sk-ant-new") {
		t.Errorf("cache key %q contains secret material", fp)
	}
}

// TestGovModelResolver_Status covers the read-only status seam the keeper status
// card uses: unconfigured → zero value, revoked credential → Configured + Degraded.
func TestGovModelResolver_Status(t *testing.T) {
	ensureEncryptionKey(t)
	ctx := context.Background()

	t.Run("unconfigured -> zero value", func(t *testing.T) {
		db := setupTestDB(t)
		owner := seedTestUser(t, db)
		ws := seedTestWorkspace(t, db, owner)
		r := NewGovModelResolver(db, nil, newTestLogger(), testOllamaURL, testOllamaModel)
		if got := r.Status(ctx, ws); got.Configured {
			t.Errorf("unconfigured Status = %+v, want zero", got)
		}
	})

	t.Run("revoked credential -> configured + degraded", func(t *testing.T) {
		db := setupTestDB(t)
		owner := seedTestUser(t, db)
		ws := seedTestWorkspace(t, db, owner)
		enc, _ := encryption.Encrypt("https://llm.tenant.example/v1/chat/completions")
		execOrFatal(t, db, `INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, deleted_at, created_by)
			VALUES ('gs1', ?, 'gov-endpoint', ?, 'ENDPOINT_URL', '2026-07-14T00:00:00Z', ?)`, ws, enc, owner)
		if err := governance.Upsert(ctx, db, ws, governance.Settings{
			GovModelProvider:     governance.ProviderOpenAICompat,
			GovModelID:           "gpt-4o-mini",
			GovModelCredentialID: "gs1",
		}, owner); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		r := NewGovModelResolver(db, nil, newTestLogger(), testOllamaURL, testOllamaModel)
		got := r.Status(ctx, ws)
		if !got.Configured || !got.Degraded {
			t.Errorf("Status = %+v, want Configured && Degraded", got)
		}
		if got.Reason == "" {
			t.Error("degraded Status must carry a reason for the card")
		}
	})
}
