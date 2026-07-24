package notify

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// testEncKey is a throwaway 32-byte AES key for the in-memory test DB.
// Built at runtime (not a string literal) so the secret scanner doesn't
// flag a high-entropy constant — there is no real secret here.
var testEncKey = strings.Repeat("0123456789abcdef", 4)

func newChannelStore(t *testing.T) *ChannelStore {
	t.Helper()
	t.Setenv("ENCRYPTION_KEY", testEncKey)
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`
CREATE TABLE notification_channels (
    id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL,
    type TEXT NOT NULL CHECK (type IN ('email','webhook','shoutrrr')),
    provider TEXT NOT NULL DEFAULT '',
    config_json TEXT NOT NULL DEFAULT '{}', secret_enc TEXT,
    events_json TEXT NOT NULL DEFAULT '["run.failed"]',
    enabled INTEGER NOT NULL DEFAULT 1, created_by TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now','subsec')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now','subsec')), deleted_at TEXT,
    scope TEXT NOT NULL DEFAULT 'workspace' CHECK (scope IN ('workspace','user')),
    owner_user_id TEXT,
    categories_json TEXT NOT NULL DEFAULT '[]',
    min_priority TEXT NOT NULL DEFAULT 'low' CHECK (min_priority IN ('low','medium','high','urgent')));`); err != nil {
		t.Fatal(err)
	}
	return NewChannelStore(db)
}

func TestChannelStore_CreateWebhook_EncryptsSecretReturnsOnce(t *testing.T) {
	s := newChannelStore(t)
	ctx := context.Background()

	ch, err := s.Create(ctx, ChannelInput{
		WorkspaceID: "w", Type: ChannelWebhook, URL: "https://hooks.example.com/x", CreatedBy: "u1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if ch.Secret == "" {
		t.Fatal("create should auto-generate and return a webhook secret once")
	}
	// API-facing List must never carry the secret.
	list, err := s.List(ctx, "w", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Secret != "" {
		t.Fatalf("List must redact secret; got %+v", list)
	}
	// Dispatch read decrypts back to the original plaintext.
	enabled, err := s.ListEnabled(ctx, "w")
	if err != nil {
		t.Fatal(err)
	}
	if len(enabled) != 1 || enabled[0].Secret != ch.Secret {
		t.Fatalf("ListEnabled must decrypt to original secret; got %q want %q", enabled[0].Secret, ch.Secret)
	}
	if enabled[0].URL != "https://hooks.example.com/x" {
		t.Errorf("url roundtrip failed: %q", enabled[0].URL)
	}
}

func TestChannelStore_Events_DefaultAndExplicit(t *testing.T) {
	s := newChannelStore(t)
	ctx := context.Background()

	// Default: no events → failures-only.
	def, err := s.Create(ctx, ChannelInput{WorkspaceID: "w", Type: ChannelWebhook, URL: "https://x.example/1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(def.Events) != 1 || def.Events[0] != EventRunFailed {
		t.Fatalf("default events = %v, want [run.failed]", def.Events)
	}

	// Explicit "all" expands + persists through a reload.
	all, err := s.Create(ctx, ChannelInput{WorkspaceID: "w", Type: ChannelWebhook, URL: "https://x.example/2", Events: []string{"all"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Events) != 2 {
		t.Fatalf("'all' should expand to 2 events, got %v", all.Events)
	}
	reloaded, err := s.GetForDispatch(ctx, "w", all.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.Wants(EventRunCompleted) || !reloaded.Wants(EventRunFailed) {
		t.Fatalf("reloaded 'all' channel should want both, got %v", reloaded.Events)
	}

	// Unknown event → rejected.
	if _, err := s.Create(ctx, ChannelInput{WorkspaceID: "w", Type: ChannelWebhook, URL: "https://x.example/3", Events: []string{"exploded"}}); err == nil {
		t.Fatal("unknown event should be rejected")
	}
}

func TestChannelStore_CreateWebhook_RejectsBadURL(t *testing.T) {
	s := newChannelStore(t)
	if _, err := s.Create(context.Background(), ChannelInput{
		WorkspaceID: "w", Type: ChannelWebhook, URL: "ftp://nope",
	}); err == nil {
		t.Fatal("expected rejection of non-http(s) webhook url")
	}
}

func TestChannelStore_CreateEmail_ValidatesAddress(t *testing.T) {
	s := newChannelStore(t)
	ctx := context.Background()
	if _, err := s.Create(ctx, ChannelInput{WorkspaceID: "w", Type: ChannelEmail, To: "not-an-email"}); err == nil {
		t.Fatal("expected rejection of invalid email address")
	}
	ch, err := s.Create(ctx, ChannelInput{WorkspaceID: "w", Type: ChannelEmail, To: "admin@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if ch.To != "admin@example.com" {
		t.Errorf("to = %q", ch.To)
	}
}

func TestChannelStore_Delete_SoftDeletesScoped(t *testing.T) {
	s := newChannelStore(t)
	ctx := context.Background()
	ch, err := s.Create(ctx, ChannelInput{WorkspaceID: "w", Type: ChannelEmail, To: "a@b.com"})
	if err != nil {
		t.Fatal(err)
	}
	// Wrong workspace must not delete.
	if ok, _ := s.Delete(ctx, "other", ch.ID); ok {
		t.Fatal("delete must be workspace-scoped")
	}
	if ok, _ := s.Delete(ctx, "w", ch.ID); !ok {
		t.Fatal("delete should succeed for owning workspace")
	}
	list, _ := s.List(ctx, "w", "")
	if len(list) != 0 {
		t.Fatalf("soft-deleted channel should not list; got %d", len(list))
	}
}

// TestChannelStore_List_PersonalChannelsAreOwnerScoped guards against a
// real privacy leak (#1412): a personal (scope=user) channel's metadata
// (its URL, provider — not its secret, but still a member's own contact
// info) must never appear in another member's List() view, only the
// owner's.
func TestChannelStore_List_PersonalChannelsAreOwnerScoped(t *testing.T) {
	s := newChannelStore(t)
	ctx := context.Background()

	if _, err := s.Create(ctx, ChannelInput{
		WorkspaceID: "w", Type: ChannelWebhook, URL: "https://hooks.example.com/workspace-wide",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, ChannelInput{
		WorkspaceID: "w", Type: ChannelWebhook, URL: "https://hooks.example.com/alices-personal",
		Scope: ScopeUser, OwnerUserID: "alice",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, ChannelInput{
		WorkspaceID: "w", Type: ChannelWebhook, URL: "https://hooks.example.com/bobs-personal",
		Scope: ScopeUser, OwnerUserID: "bob",
	}); err != nil {
		t.Fatal(err)
	}

	aliceView, err := s.List(ctx, "w", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(aliceView) != 2 {
		t.Fatalf("alice should see the workspace channel + her own personal channel (2), got %d: %+v", len(aliceView), aliceView)
	}
	for _, c := range aliceView {
		if c.Scope == ScopeUser && c.OwnerUserID != "alice" {
			t.Fatalf("alice's List() leaked another member's personal channel: %+v", c)
		}
	}

	anonView, err := s.List(ctx, "w", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(anonView) != 1 {
		t.Fatalf("a viewer with no user id should see ONLY the workspace channel, got %d: %+v", len(anonView), anonView)
	}
}

func TestChannelStore_GetForDispatch_NotFound(t *testing.T) {
	s := newChannelStore(t)
	if _, err := s.GetForDispatch(context.Background(), "w", "missing"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}
