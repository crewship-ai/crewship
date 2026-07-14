package api

import (
	"context"
	"testing"

	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/keeper/governance"
)

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
