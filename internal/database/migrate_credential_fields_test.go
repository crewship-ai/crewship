package database

import (
	"database/sql"
	"strings"
	"testing"
)

// credential_fields is the multi-part credential store from
// PRD-CREDENTIALS-V2-2026 §2.2: one credential, N named parts, each part
// either a secret (encrypted) or an identifier (cleartext, like the existing
// credentials.username).
//
// The tests below pin the two properties that cannot be recovered later if
// the migration gets them wrong:
//
//  1. the storage split is enforced by the DATABASE, not by the handler. A
//     handler bug that writes a secret into the cleartext column has to be
//     rejected by the engine, because the handler is exactly the thing under
//     suspicion when that bug exists.
//  2. the key is unique per credential at the schema level. The API also
//     checks, but two concurrent POSTs both pass an application-level check
//     before either inserts — only the constraint closes that race.

// seedCredentialForFields inserts the minimum credential row the FK needs.
func seedCredentialForFields(t *testing.T, db *sql.DB, credID string) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO workspaces (id, name, slug) VALUES ('ws-cf', 'CF', 'cf')`); err != nil &&
		!strings.Contains(err.Error(), "UNIQUE") {
		t.Fatalf("seed workspace: %v", err)
	}
	if _, err := db.Exec(
		`INSERT OR IGNORE INTO users (id, email, full_name) VALUES ('u-cf', 'cf@example.com', 'CF')`); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, provider, scope, status, created_by)
		VALUES (?, 'ws-cf', ?, 'enc', 'SECRET', 'NONE', 'WORKSPACE', 'ACTIVE', 'u-cf')`,
		credID, credID); err != nil {
		t.Fatalf("seed credential: %v", err)
	}
}

func TestMigrateCredentialFields_TableShape(t *testing.T) {
	db := openMigratedDB(t)

	var ddl string
	if err := db.QueryRow(
		`SELECT sql FROM sqlite_master WHERE type='table' AND name='credential_fields'`).Scan(&ddl); err != nil {
		t.Fatalf("credential_fields table missing after Migrate: %v", err)
	}
	for _, col := range []string{"credential_id", "key", "value", "encrypted_value", "is_secret", "ordinal"} {
		if !strings.Contains(ddl, col) {
			t.Errorf("credential_fields DDL is missing column %q:\n%s", col, ddl)
		}
	}
	// Timestamps must be the T-form the rest of the schema settled on
	// (migrate_v144_datetime_default_tform). A space-form DEFAULT here would
	// sort before every other timestamp in the database.
	if !strings.Contains(ddl, "strftime('%Y-%m-%dT%H:%M:%fZ','now')") {
		t.Errorf("credential_fields timestamps must default to the T-form literal:\n%s", ddl)
	}
}

// The invariant that makes "is_secret decides which column holds the value"
// true rather than aspirational.
func TestMigrateCredentialFields_StorageSplitIsEnforcedByTheEngine(t *testing.T) {
	db := openMigratedDB(t)
	seedCredentialForFields(t, db, "cred-split")

	cases := []struct {
		name            string
		isSecret        int
		value, encValue any
	}{
		{"secret field storing cleartext", 1, "us-east-1", nil},
		{"secret field storing both", 1, "us-east-1", "enc"},
		{"secret field storing neither", 1, nil, nil},
		{"non-secret field storing ciphertext", 0, nil, "enc"},
		{"non-secret field storing both", 0, "us-east-1", "enc"},
		{"non-secret field storing neither", 0, nil, nil},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := db.Exec(`
				INSERT INTO credential_fields (credential_id, key, value, encrypted_value, is_secret, ordinal)
				VALUES ('cred-split', ?, ?, ?, ?, ?)`,
				"k", tc.value, tc.encValue, tc.isSecret, i)
			if err == nil {
				t.Fatalf("insert was accepted; the CHECK must reject it — a handler bug that "+
					"writes a secret in cleartext has to fail at the engine, not pass silently (case %q)", tc.name)
			}
		})
	}
}

func TestMigrateCredentialFields_KeyIsUniquePerCredential(t *testing.T) {
	db := openMigratedDB(t)
	seedCredentialForFields(t, db, "cred-uniq")
	seedCredentialForFields(t, db, "cred-uniq2")

	ins := func(credID, key string) error {
		_, err := db.Exec(`
			INSERT INTO credential_fields (credential_id, key, encrypted_value, is_secret, ordinal)
			VALUES (?, ?, 'enc', 1, 0)`, credID, key)
		return err
	}
	if err := ins("cred-uniq", "region"); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if err := ins("cred-uniq", "region"); err == nil {
		t.Fatal("duplicate (credential_id, key) accepted — the application check alone cannot " +
			"stop two concurrent POSTs, so the constraint has to")
	}
	// The uniqueness is scoped to the credential, not global: two credentials
	// both having a "region" is the normal case.
	if err := ins("cred-uniq2", "region"); err != nil {
		t.Fatalf("same key on a different credential must be allowed: %v", err)
	}
}

// A hard delete of the credential must take its parts with it: an orphaned
// encrypted_value is a secret nothing owns and no read path can ever show,
// which is how a vault accumulates material it cannot account for.
func TestMigrateCredentialFields_CascadeOnCredentialDelete(t *testing.T) {
	db := openMigratedDB(t)
	seedCredentialForFields(t, db, "cred-casc")
	if _, err := db.Exec(`
		INSERT INTO credential_fields (credential_id, key, encrypted_value, is_secret, ordinal)
		VALUES ('cred-casc', 'region', 'enc', 1, 0)`); err != nil {
		t.Fatalf("insert field: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM credentials WHERE id = 'cred-casc'`); err != nil {
		t.Fatalf("delete credential: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM credential_fields WHERE credential_id = 'cred-casc'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("%d field(s) survived the credential's hard delete; ON DELETE CASCADE is not wired", n)
	}
}
