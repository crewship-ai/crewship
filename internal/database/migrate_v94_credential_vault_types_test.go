package database

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
)

// TestMigrateV94_CredentialVaultTypes asserts the v94 schema lands —
// credentials.username exists as a nullable TEXT, agent_credentials
// gains a mount_type column defaulting to 'env' and gated by a CHECK
// constraint, and pre-existing insert shapes still work (additive
// migration must not break legacy callers).
//
// Split into focused subtests so a single failure (e.g. CHECK
// constraint not applied) names exactly what regressed instead of
// blaming a downstream insert.
func TestMigrateV94_CredentialVaultTypes(t *testing.T) {
	dir := t.TempDir()
	db, err := Open("file:" + filepath.Join(dir, "v94.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	silent := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := Migrate(context.Background(), db.DB, silent); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// Shared seed: workspaces + users + crews + agents only. Per-test
	// inserts into credentials / agent_credentials live in the subtest
	// they're exercising so isolated failures are easier to triage.
	mustExec(t, db.DB, `INSERT INTO workspaces (id, name, slug) VALUES ('ws1', 'WS', 'ws1')`)
	mustExec(t, db.DB, `INSERT INTO users (id, email, full_name) VALUES ('u1', 'a@b.c', 'A')`)
	mustExec(t, db.DB, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('cr1', 'ws1', 'crew', 'crew')`)
	mustExec(t, db.DB, `INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('a1', 'cr1', 'ws1', 'agent', 'agent')`)

	t.Run("schema/credentials.username_is_nullable_text", func(t *testing.T) {
		if got := columnType(t, db.DB, "credentials", "username"); strings.ToUpper(got) != "TEXT" {
			t.Errorf("credentials.username type = %q, want TEXT", got)
		}
	})

	t.Run("schema/agent_credentials.mount_type_defaults_to_env", func(t *testing.T) {
		// The discriminator only works if existing rows resolve as
		// env-mounted, so verify the default is literally 'env'.
		if got := columnDefault(t, db.DB, "agent_credentials", "mount_type"); strings.Trim(got, "'\"") != "env" {
			t.Errorf("agent_credentials.mount_type default = %q, want 'env'", got)
		}
	})

	t.Run("legacy_credential_insert_without_username_works", func(t *testing.T) {
		// Additive nullable column must not break the pre-v94 insert
		// shape. This is the whole point of the migration being
		// additive — every existing caller keeps working.
		mustExec(t, db.DB, `
INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, created_by)
VALUES ('c_legacy', 'ws1', 'legacy api key', 'v1:abc', 'API_KEY', 'u1')`)
	})

	t.Run("userpass_insert_carries_cleartext_username", func(t *testing.T) {
		mustExec(t, db.DB, `
INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, username, created_by)
VALUES ('c_userpass', 'ws1', 'gmail', 'v1:enc', 'USERPASS', 'user@gmail.com', 'u1')`)
		var got sql.NullString
		if err := db.QueryRow(`SELECT username FROM credentials WHERE id = 'c_userpass'`).Scan(&got); err != nil {
			t.Fatalf("read back: %v", err)
		}
		if !got.Valid || got.String != "user@gmail.com" {
			t.Errorf("username = %v, want user@gmail.com", got)
		}
	})

	t.Run("ssh_key_reuses_encrypted_value_no_extra_column", func(t *testing.T) {
		// PEM body lives in the existing encrypted_value column —
		// no second column was added in v94 for SSH/CERT.
		mustExec(t, db.DB, `
INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, created_by)
VALUES ('c_ssh', 'ws1', 'github deploy', 'v1:pem', 'SSH_KEY', 'u1')`)
	})

	t.Run("agent_credentials_mount_type_default_applies_when_omitted", func(t *testing.T) {
		// Keeping the legacy agent_credentials insert path working
		// untouched across the migration boundary is the entire
		// reason for the DEFAULT 'env'.
		mustExec(t, db.DB, `
INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name)
VALUES ('ac1', 'a1', 'c_legacy', 'GITHUB_TOKEN')`)
		var mt string
		if err := db.QueryRow(`SELECT mount_type FROM agent_credentials WHERE id = 'ac1'`).Scan(&mt); err != nil {
			t.Fatalf("read back mount_type: %v", err)
		}
		if mt != "env" {
			t.Errorf("mount_type after omitted-default insert = %q, want 'env'", mt)
		}
	})

	t.Run("agent_credentials_explicit_file_mount_binding_accepted", func(t *testing.T) {
		// SSH key → ~/.ssh/keys/github inside the container.
		// env_var_name carries the basename in file-mount mode,
		// not an actual env var name.
		mustExec(t, db.DB, `
INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, mount_type)
VALUES ('ac2', 'a1', 'c_ssh', 'github', 'file')`)
	})

	t.Run("agent_credentials_mount_type_CHECK_rejects_unknown_values", func(t *testing.T) {
		// Defense in depth — the API validator already rejects
		// non-{env,file}, but the DB constraint also fails closed
		// for manual writes / future backfills / migrations that
		// bypass the API.
		_, err := db.Exec(`
INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, mount_type)
VALUES ('ac_bogus', 'a1', 'c_ssh', 'bogus', 'filesystem')`)
		if err == nil {
			t.Fatal("expected CHECK constraint to reject mount_type='filesystem', got nil error")
		}
		if !strings.Contains(strings.ToLower(err.Error()), "check") {
			t.Errorf("error should mention CHECK constraint, got: %v", err)
		}
	})
}

// columnType returns the declared SQLite type for a column, or empty
// string if the column doesn't exist on the table.
func columnType(t *testing.T, db *sql.DB, table, col string) string {
	t.Helper()
	rows, err := db.Query(`SELECT name, type FROM pragma_table_info(?)`, table)
	if err != nil {
		t.Fatalf("pragma_table_info(%s): %v", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var n, ty string
		if err := rows.Scan(&n, &ty); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if n == col {
			return ty
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("pragma iter: %v", err)
	}
	return ""
}

// columnDefault returns the dflt_value for a column, or empty string
// if absent / NULL.
func columnDefault(t *testing.T, db *sql.DB, table, col string) string {
	t.Helper()
	rows, err := db.Query(`SELECT name, dflt_value FROM pragma_table_info(?)`, table)
	if err != nil {
		t.Fatalf("pragma_table_info(%s): %v", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		var dflt sql.NullString
		if err := rows.Scan(&n, &dflt); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if n == col {
			return dflt.String
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("pragma iter: %v", err)
	}
	return ""
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("exec %q: %v", strings.SplitN(q, "\n", 2)[0], err)
	}
}
