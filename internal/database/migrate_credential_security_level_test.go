package database

import (
	"strings"
	"testing"
)

// credentials.security_level is read in five places as
// `COALESCE(security_level, 1)` — a fail-OPEN default, L1 being the only tier
// with AutoAllow. Everywhere else in the codebase an unknown tier resolves to
// L4 (keeper.SecurityLevel.Tier, deliberately fail-closed).
//
// That disagreement is harmless only for as long as the NULL branch is
// unreachable, and exactly one thing makes it unreachable: the column is
// NOT NULL, with a DEFAULT, on the single ALTER TABLE that ever created it.
// No migration has rebuilt `credentials`, every reader selects from
// `credentials` directly or through an INNER JOIN (so no outer join can
// synthesise a NULL), and every writer either supplies a value or omits the
// column and takes the default.
//
// The migration that will break that premise is a foreseeable one: adding the
// CHECK constraint the column never had (#1603) means a 12-step table rebuild,
// and a rebuild is precisely where a NOT NULL gets dropped by accident. This
// test is the tripwire for that day — if it goes red, the COALESCE default has
// become live code and must flip to the strictest tier before the rebuild
// merges.
func TestCredentialSecurityLevelIsNotNullable(t *testing.T) {
	db := openMigratedDB(t)

	rows, err := db.Query(`PRAGMA table_info(credentials)`)
	if err != nil {
		t.Fatalf("table_info: %v", err)
	}
	defer func() { _ = rows.Close() }()

	found := false
	for rows.Next() {
		var (
			cid         int
			name, ctype string
			notnull, pk int
			dflt        any
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan table_info: %v", err)
		}
		if name != "security_level" {
			continue
		}
		found = true
		if notnull != 1 {
			t.Errorf("credentials.security_level is nullable — the COALESCE(security_level, 1) " +
				"reads in internal/api are now live fail-OPEN defaults to the one tier with " +
				"AutoAllow. Flip them to the strictest tier (keeper.SecurityLevel.Tier's own " +
				"fallback) before shipping the schema change that caused this.")
		}
		if dflt == nil {
			t.Errorf("credentials.security_level lost its DEFAULT — a writer that omits the " +
				"column now fails instead of taking a tier")
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate table_info: %v", err)
	}
	if !found {
		t.Fatal("credentials.security_level is missing from the migrated schema")
	}

	// The constraint, not just its declaration: prove the engine rejects a NULL.
	if _, err := db.Exec(
		`INSERT INTO workspaces (id, name, slug) VALUES ('ws-sl', 'SL', 'sl')`); err != nil &&
		!strings.Contains(err.Error(), "UNIQUE") {
		t.Fatalf("seed workspace: %v", err)
	}
	if _, err := db.Exec(
		`INSERT OR IGNORE INTO users (id, email, full_name) VALUES ('u-sl', 'sl@example.com', 'SL')`); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	_, err = db.Exec(`
		INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, provider, scope, status, created_by, security_level)
		VALUES ('cred-sl-null', 'ws-sl', 'sl-null', 'enc', 'SECRET', 'NONE', 'WORKSPACE', 'ACTIVE', 'u-sl', NULL)`)
	if err == nil {
		t.Fatal("an explicit NULL security_level was accepted — the premise that makes " +
			"COALESCE(security_level, 1) unreachable no longer holds")
	}
}
