package database

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
)

// migrationVersionCredentialHandleOnly is the version the assertions below
// pin. Spelled once so a renumber on merge is a one-line change here too.
const migrationVersionCredentialHandleOnly = 20260905091400

// TestMigrate_CredentialHandleOnly_Column asserts credentials gained
// handle_only, NOT NULL with default 0 — every pre-existing row keeps the
// delivery it had, and only rows written by the ask path opt in (#2376).
func TestMigrate_CredentialHandleOnly_Column(t *testing.T) {
	t.Parallel()
	db := migrateChainSetup(t)

	found, def := columnInfo(t, db.DB, "credentials", "handle_only")
	if !found {
		t.Fatal("credentials missing handle_only column")
	}
	if def == nil || *def != "0" {
		t.Errorf("handle_only default = %v, want 0 (existing rows keep their delivery)", def)
	}
}

// TestMigrate_CredentialHandleOnly_ReplacesStoredResolutions lands the schema
// as it stood before this migration, writes a CREDENTIAL escalation the way
// ResolveEscalation used to (an encrypted secret in `resolution`), then
// applies the rest of the chain and checks the ciphertext is gone.
//
// A TEXT escalation's resolution is an operator note and must survive; an
// unresolved CREDENTIAL row has nothing to replace.
func TestMigrate_CredentialHandleOnly_ReplacesStoredResolutions(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	db, err := Open("file:" + filepath.Join(dir, "handle_only.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()

	if err := applyMigrationsUpTo(ctx, db.DB, migrationVersionCredentialHandleOnly-1, logger); err != nil {
		t.Fatalf("apply migrations before handle_only: %v", err)
	}
	if found, _ := columnInfo(t, db.DB, "credentials", "handle_only"); found {
		t.Fatal("handle_only present before its migration — the version pin is wrong")
	}

	const ciphertext = "v1:aW52YWxpZA==" // shape ResolveEscalation wrote, never decrypted here
	for _, row := range []struct{ id, typ, status, resolution string }{
		{"esc_cred_resolved", "CREDENTIAL", "RESOLVED", ciphertext},
		{"esc_cred_pending", "CREDENTIAL", "PENDING", ""},
		{"esc_text_resolved", "TEXT", "RESOLVED", "restarted the container"},
	} {
		var res interface{}
		if row.resolution != "" {
			res = row.resolution
		}
		if _, err := db.Exec(`INSERT INTO escalations (id, workspace_id, crew_id, chat_id, from_agent_id, reason, type, status, resolution, created_at)
			VALUES (?, 'ws', 'crew', 'chat', 'agent', 'r', ?, ?, ?, datetime('now'))`,
			row.id, row.typ, row.status, res); err != nil {
			t.Fatalf("seed %s: %v", row.id, err)
		}
	}

	if err := Migrate(ctx, db.DB, logger); err != nil {
		t.Fatalf("Migrate rest of chain: %v", err)
	}

	read := func(id string) sql.NullString {
		var s sql.NullString
		if err := db.QueryRow(`SELECT resolution FROM escalations WHERE id = ?`, id).Scan(&s); err != nil {
			t.Fatalf("read %s: %v", id, err)
		}
		return s
	}
	if got := read("esc_cred_resolved"); got.String != "[credential submitted]" {
		t.Errorf("resolved CREDENTIAL resolution = %q, want the marker (ciphertext must not survive)", got.String)
	}
	if got := read("esc_cred_pending"); got.Valid {
		t.Errorf("pending CREDENTIAL resolution = %q, want NULL", got.String)
	}
	if got := read("esc_text_resolved"); got.String != "restarted the container" {
		t.Errorf("TEXT resolution = %q, want untouched", got.String)
	}
}
