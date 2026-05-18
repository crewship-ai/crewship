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

// TestMigrateV94_CredentialVaultTypes asserts the v94 schema lands:
// credentials.username exists as a nullable TEXT, agent_credentials
// gains a mount_type column defaulting to 'env', and pre-existing
// insert shapes still work (additive migration must not break legacy
// callers).
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

	// credentials.username — new nullable TEXT column.
	if got := columnType(t, db.DB, "credentials", "username"); strings.ToUpper(got) != "TEXT" {
		t.Errorf("credentials.username type = %q, want TEXT", got)
	}

	// agent_credentials.mount_type — new TEXT NOT NULL DEFAULT 'env'.
	// The discriminator only works if existing rows resolve as env-mounted,
	// so verify the default is what we expect.
	if got := columnDefault(t, db.DB, "agent_credentials", "mount_type"); strings.Trim(got, "'\"") != "env" {
		t.Errorf("agent_credentials.mount_type default = %q, want 'env'", got)
	}

	// Seed enough rows to exercise FK-bound inserts.
	mustExec(t, db.DB, `INSERT INTO workspaces (id, name, slug) VALUES ('ws1', 'WS', 'ws1')`)
	mustExec(t, db.DB, `INSERT INTO users (id, email, full_name) VALUES ('u1', 'a@b.c', 'A')`)

	// Legacy insert shape (no username, no mount_type) must still work —
	// that's the whole point of additive nullable + DEFAULT columns.
	mustExec(t, db.DB, `
INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, created_by)
VALUES ('c_legacy', 'ws1', 'legacy api key', 'v1:abc', 'API_KEY', 'u1')`)

	// New USERPASS shape carries username as cleartext (identifier, not secret).
	mustExec(t, db.DB, `
INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, username, created_by)
VALUES ('c_userpass', 'ws1', 'gmail', 'v1:enc', 'USERPASS', 'user@gmail.com', 'u1')`)

	// SSH_KEY reuses encrypted_value for the PEM body — no extra column.
	mustExec(t, db.DB, `
INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, created_by)
VALUES ('c_ssh', 'ws1', 'github deploy', 'v1:pem', 'SSH_KEY', 'u1')`)

	mustExec(t, db.DB, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('cr1', 'ws1', 'crew', 'crew')`)
	mustExec(t, db.DB, `INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('a1', 'cr1', 'ws1', 'agent', 'agent')`)

	// Default mount_type='env' must apply when caller omits the column.
	// This is what keeps the legacy agent_credentials insert path
	// working untouched across the migration boundary.
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

	// Explicit file-mount binding (SSH key → ~/.ssh/keys/github inside
	// the container) — env_var_name carries the basename, not an env var.
	mustExec(t, db.DB, `
INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, mount_type)
VALUES ('ac2', 'a1', 'c_ssh', 'github', 'file')`)
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
