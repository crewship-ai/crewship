package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/keepercfg"
	"github.com/crewship-ai/crewship/internal/llm"
)

// The production keepercfg.AuxCredentialLookup seam (#1554).
//
// Two halves, and they are deliberately scoped differently:
//
//   - the BUILD-time lookup is instance-global (by id), because
//     keeper_aux_settings is instance-global. Scoping it to whichever workspace
//     happened to trigger the evaluation would make one setting work in one
//     workspace and silently degrade in every other — the exact silent failure
//     the issue was filed about.
//   - the WRITE-time check is workspace-scoped, so an admin can only bind a key
//     their own workspace holds. That is where cross-tenant selection is refused.
func TestAuxCredentialLookup(t *testing.T) {
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

	insert("key_active", CredTypeAPIKey, enc("sk-live-123"), "ACTIVE", "")
	insert("key_revoked", CredTypeAPIKey, enc("sk-old"), "ACTIVE", "2026-07-14T00:00:00Z") // soft delete
	insert("key_inactive", CredTypeAPIKey, enc("sk-off"), "REVOKED", "")
	insert("an_endpoint", CredTypeEndpointURL, enc("https://llm.example/v1/chat/completions"), "ACTIVE", "")

	lookup := NewAuxCredentialLookup(db)
	ctx := context.Background()

	t.Run("an active API_KEY yields the key, without naming a workspace", func(t *testing.T) {
		got, err := lookup(ctx, "key_active")
		if err != nil {
			t.Fatalf("lookup: %v", err)
		}
		if got != "sk-live-123" {
			t.Errorf("key = %q, want sk-live-123", got)
		}
	})

	// Every one of these must ERROR, because an error is what makes the resolver
	// degrade to the process-env key rather than dial with a stale id.
	for _, tc := range []struct{ name, id, want string }{
		{"revoked is a SOFT delete, so the id still resolves to a row", "key_revoked", "revoked"},
		{"an inactive status", "key_inactive", "revoked"},
		{"an id that names nothing", "no_such_credential", "not found"},
		{"an ENDPOINT_URL, which is not a key", "an_endpoint", "API_KEY"},
		{"an empty id", "", "empty"},
	} {
		t.Run(tc.name+" -> error", func(t *testing.T) {
			_, err := lookup(ctx, tc.id)
			if err == nil {
				t.Fatalf("no error, so the evaluator would dial with an unusable credential")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %q, want it to mention %q", err.Error(), tc.want)
			}
		})
	}

	t.Run("the error never contains the secret", func(t *testing.T) {
		_, err := lookup(ctx, "key_revoked")
		if err != nil && strings.Contains(err.Error(), "sk-old") {
			t.Errorf("the error leaked the key: %v", err)
		}
	})
}

// auxStatusStoreDB is a throwaway DB with just the aux table, for the status
// tests — they need a store, not a workspace.
func auxStatusStoreDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`CREATE TABLE keeper_aux_settings (
		slot TEXT PRIMARY KEY, provider TEXT NOT NULL DEFAULT '', model TEXT NOT NULL DEFAULT '',
		timeout_ms INTEGER, updated_by TEXT, created_at TEXT NOT NULL DEFAULT '',
		updated_at TEXT NOT NULL DEFAULT '', credential_id TEXT)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	return db
}

// auxStatusRow drives GET /api/v1/system/aux-status and returns one slot's row.
func auxStatusRow(t *testing.T, h *AuxStatusHandler, slot string) auxSubsystemRow {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/v1/system/aux-status", nil)
	req = req.WithContext(context.WithValue(req.Context(), ctxUser, &AuthUser{ID: "admin-1"}))
	rr := httptest.NewRecorder()
	h.Status(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status got %d: %s", rr.Code, rr.Body.String())
	}
	var resp auxStatusResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, s := range resp.Subsystems {
		if s.ID == slot {
			return s
		}
	}
	t.Fatalf("slot %q missing from aux-status", slot)
	return auxSubsystemRow{}
}

// The write-time half: an admin may only bind a credential their OWN workspace
// holds, and only one that is actually usable as an evaluator key.
func TestAuxCredentialWritable(t *testing.T) {
	ensureEncryptionKey(t)
	db := setupTestDB(t)
	ownerID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, ownerID)

	e, err := encryption.Encrypt("sk-live-123")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	execOrFatal(t, db, `INSERT INTO credentials
		(id, workspace_id, name, encrypted_value, type, status, created_by)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"key_active", wsID, "prod-anthropic", e, CredTypeAPIKey, "ACTIVE", ownerID)

	check := newAuxCredentialCheck(db)
	ctx := context.Background()

	if err := check(ctx, wsID, "key_active"); err != nil {
		t.Errorf("refused a usable credential from the caller's own workspace: %v", err)
	}
	// Instance-global setting, workspace-scoped vault: this is the seam where the
	// two meet, and it has to refuse rather than let one workspace's admin spend
	// another's key.
	if err := check(ctx, "some-other-workspace", "key_active"); err == nil {
		t.Error("accepted a credential from a workspace the caller is not in")
	}
	if err := check(ctx, wsID, "no_such_credential"); err == nil {
		t.Error("accepted an id that names nothing")
	}
	// "" is the documented clear, not a lookup.
	if err := check(ctx, wsID, ""); err != nil {
		t.Errorf("clearing the credential was treated as a lookup: %v", err)
	}
}

// The run_summary slot is built once at boot rather than per request, so it
// reaches the vault through its own path (Router.resolveRunVerdict). It has to
// honour the same credential — otherwise the console would offer a key picker on
// a row that quietly keeps spending the process env's key.
func TestBuildAuxWithCredential(t *testing.T) {
	ctx := context.Background()
	logger := newTestLogger()

	model := llm.AuxModel{Provider: "anthropic", Model: "claude-haiku-4-5"}
	var asked []string
	vault := keepercfg.AuxCredentialLookup(func(_ context.Context, id string) (string, error) {
		asked = append(asked, id)
		if id == "cred_revoked" {
			return "", fmt.Errorf("revoked")
		}
		return "sk-ant-from-the-vault", nil
	})

	t.Run("a named credential is what the provider is built from", func(t *testing.T) {
		t.Setenv("ANTHROPIC_API_KEY", "")
		asked = nil
		p, err := buildAuxWithCredential(ctx, model, "", "cred_live", vault, logger)
		if err != nil {
			t.Fatalf("build: %v", err)
		}
		if p == nil {
			t.Fatal("nil provider")
		}
		if len(asked) != 1 || asked[0] != "cred_live" {
			t.Errorf("looked up %v, want exactly [cred_live]", asked)
		}
	})

	t.Run("no credential is the process-env path, untouched", func(t *testing.T) {
		t.Setenv("ANTHROPIC_API_KEY", "sk-ant-from-the-env")
		asked = nil
		if _, err := buildAuxWithCredential(ctx, model, "", "", vault, logger); err != nil {
			t.Fatalf("build: %v", err)
		}
		if len(asked) != 0 {
			t.Errorf("consulted the vault %v for a slot that names no credential", asked)
		}
	})

	t.Run("a revoked credential degrades to the env key, it does not fail the build", func(t *testing.T) {
		t.Setenv("ANTHROPIC_API_KEY", "sk-ant-from-the-env")
		if p, err := buildAuxWithCredential(ctx, model, "", "cred_revoked", vault, logger); err != nil || p == nil {
			t.Errorf("a revoked credential took the run-summary verdict down: p=%v err=%v", p, err)
		}
	})
}

// The status card has to agree with what actually runs.
//
// It reports each evaluator healthy or not by BUILDING its provider, and that
// build used to read the key from the environment only. A slot pinned to a vault
// key on a box with no ANTHROPIC_API_KEY therefore rendered "ANTHROPIC_API_KEY
// env not set" against an evaluator that works — the same class of lie, pointing
// the opposite way, as the one this issue was filed about.
func TestAuxStatus_HonoursThePinnedKey(t *testing.T) {
	db := auxStatusStoreDB(t)
	store := keepercfg.NewAuxStore(db, llm.DefaultAuxiliaryModels())
	if err := store.Load(context.Background()); err != nil {
		t.Fatalf("load: %v", err)
	}
	provider, model, cred := "anthropic", "claude-haiku-4-5", "cred_prod"
	if _, err := store.Apply(context.Background(), "curator", keepercfg.AuxPatch{
		Provider: &provider, Model: &model, CredentialID: &cred,
	}, ""); err != nil {
		t.Fatalf("apply: %v", err)
	}

	t.Setenv("ANTHROPIC_API_KEY", "")
	vault := keepercfg.AuxCredentialLookup(func(_ context.Context, id string) (string, error) {
		if id != "cred_prod" {
			return "", fmt.Errorf("unknown credential %q", id)
		}
		return "sk-ant-from-the-vault", nil
	})
	h := NewAuxStatusHandler(store.Resolved(), nil, newTestLogger()).WithCredentials(store, vault)

	row := auxStatusRow(t, h, "curator")
	if !row.Healthy {
		t.Errorf("a slot with a working pinned key reports unhealthy: %q", row.Detail)
	}

	// And when the pinned key is the broken part, the row must say THAT rather
	// than blame an environment variable the operator deliberately stopped using.
	broken := keepercfg.AuxCredentialLookup(func(context.Context, string) (string, error) {
		return "", fmt.Errorf("not found, inactive, or revoked")
	})
	h = NewAuxStatusHandler(store.Resolved(), nil, newTestLogger()).WithCredentials(store, broken)
	row = auxStatusRow(t, h, "curator")
	if row.Healthy {
		t.Fatal("a revoked key with no env key to fall back on reports healthy")
	}
	if !strings.Contains(row.Detail, "revoked") {
		t.Errorf("detail = %q, want the credential's own reason", row.Detail)
	}
}
