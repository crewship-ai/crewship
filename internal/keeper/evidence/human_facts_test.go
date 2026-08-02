package evidence

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// newHumanFactsDB carries only the two tables these facts read. Separate from
// newDB so adding a column here cannot quietly change what the other tests
// exercise.
func newHumanFactsDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1) // same reason as newDB: :memory: is per-connection
	if _, err := db.Exec(`
		CREATE TABLE backup_catalog (
			id TEXT PRIMARY KEY,
			file_path TEXT NOT NULL UNIQUE,
			scope TEXT NOT NULL,
			slug TEXT,
			workspace_id TEXT,
			created_at TEXT NOT NULL,
			created_by TEXT,
			size INTEGER NOT NULL,
			sha256 TEXT NOT NULL,
			encrypted INTEGER NOT NULL,
			format_version INTEGER NOT NULL
		);
		CREATE TABLE credentials (
			id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL,
			name TEXT NOT NULL,
			encrypted_value TEXT NOT NULL,
			type TEXT NOT NULL,
			provider TEXT,
			security_level INTEGER NOT NULL DEFAULT 1,
			status TEXT NOT NULL DEFAULT 'ACTIVE',
			created_by TEXT,
			deleted_at TEXT
		);`); err != nil {
		t.Fatalf("schema: %v", err)
	}
	return db
}

func mustExec(t *testing.T, db *sql.DB, q string) {
	t.Helper()
	if _, err := db.Exec(q); err != nil {
		t.Fatalf("exec %.60q: %v", q, err)
	}
}

// Two facts an operator asked for by name while ruling on a corpus of
// escalations: "is there a backup?" and "would a narrower credential do?".
//
// They are here rather than on the card because the card must not compute
// anything — the whole value of this package is that every line is a query
// against a real table, and a line that cannot be one is left out.
//
// A third was asked for and is NOT here: "is this reversible?". An access
// request carries only a free-text intent, so deciding that DROP TABLE is
// irreversible means parsing agent prose — the confident wrong answer this
// package exists to refuse. The /execute path carries a real command and could
// answer it structurally; /access cannot, and a heuristic wearing a fact's
// clothes is worse than a missing line, because it is repeated back as
// justification and acted on.

func TestLastBackup_ReportsTheWorkspacesMostRecent(t *testing.T) {
	db := newHumanFactsDB(t)
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)

	mustExec(t, db, `INSERT INTO backup_catalog
		(id, file_path, scope, slug, workspace_id, created_at, size, sha256, encrypted, format_version)
		VALUES ('b1', '/b1.tar', 'workspace', 'demo', 'ws1', '2026-07-30T04:00:00Z', 10, 'x', 1, 3),
		       ('b2', '/b2.tar', 'workspace', 'demo', 'ws1', '2026-08-01T04:00:00Z', 10, 'y', 1, 3)`)

	got, err := queryLastBackup(context.Background(), db, "ws1", now)
	if err != nil {
		t.Fatalf("queryLastBackup: %v", err)
	}
	if got == nil {
		t.Fatal("no fact for a workspace that has backups")
	}
	if got.AgeHours != 32 {
		t.Errorf("age = %dh, want 32h — the NEWEST backup is what bears on approving now", got.AgeHours)
	}
}

// No backup at all is the answer that matters most, and it must be
// distinguishable from "the query failed". A nil fact means we do not know; a
// present fact with Exists=false means we looked and there is none.
func TestLastBackup_SaysSoWhenThereIsNone(t *testing.T) {
	db := newHumanFactsDB(t)
	got, err := queryLastBackup(context.Background(), db, "ws1", time.Now().UTC())
	if err != nil {
		t.Fatalf("queryLastBackup: %v", err)
	}
	if got == nil || got.Exists {
		t.Fatalf("got %+v, want a present fact reporting no backup — "+
			"'we looked and found none' is not the same claim as 'we do not know'", got)
	}
}

// Another workspace's backup must never answer this one's question. The whole
// line reads "there is a recent backup", and borrowing one from a neighbour
// would be an argument for approving, manufactured by a missing predicate.
func TestLastBackup_DoesNotBorrowAnotherWorkspacesBackup(t *testing.T) {
	db := newHumanFactsDB(t)
	mustExec(t, db, `INSERT INTO backup_catalog
		(id, file_path, scope, slug, workspace_id, created_at, size, sha256, encrypted, format_version)
		VALUES ('b1', '/b1.tar', 'workspace', 'other', 'ws-other', '2026-08-01T04:00:00Z', 10, 'x', 1, 3)`)

	got, err := queryLastBackup(context.Background(), db, "ws1", time.Now().UTC())
	if err != nil {
		t.Fatalf("queryLastBackup: %v", err)
	}
	if got == nil || got.Exists {
		t.Error("a neighbouring workspace's backup was reported as this one's")
	}
}

// "Would a narrower credential do?" answered as a FACT: this workspace holds
// another active credential from the same provider at a lower tier. Naming it
// lets the operator check; recommending it would be the package deciding.
func TestNarrowerCredential_FindsALowerTierFromTheSameProvider(t *testing.T) {
	db := newHumanFactsDB(t)
	mustExec(t, db, `INSERT INTO credentials
		(id, workspace_id, name, encrypted_value, type, provider, security_level, status, created_by)
		VALUES ('c-hi', 'ws1', 'GITHUB_ADMIN', 'v1:x', 'SECRET', 'GITHUB', 4, 'ACTIVE', 'u1'),
		       ('c-lo', 'ws1', 'GITHUB_READONLY', 'v1:x', 'SECRET', 'GITHUB', 1, 'ACTIVE', 'u1')`)

	got, err := queryNarrowerCredential(context.Background(), db, "c-hi")
	if err != nil {
		t.Fatalf("queryNarrowerCredential: %v", err)
	}
	if got == nil {
		t.Fatal("no fact at all")
	}
	if !got.Exists || got.Name != "GITHUB_READONLY" || got.SecurityLevel != 1 {
		t.Errorf("got %+v, want GITHUB_READONLY at L1", got)
	}
}

// A different provider is not a narrower version of this credential — it is a
// different key for a different system, and offering it would send the operator
// to something that cannot do the job.
func TestNarrowerCredential_IgnoresOtherProvidersAndHigherTiers(t *testing.T) {
	db := newHumanFactsDB(t)
	mustExec(t, db, `INSERT INTO credentials
		(id, workspace_id, name, encrypted_value, type, provider, security_level, status, created_by)
		VALUES ('c-hi',    'ws1', 'GITHUB_ADMIN', 'v1:x', 'SECRET', 'GITHUB', 3, 'ACTIVE', 'u1'),
		       ('c-other', 'ws1', 'AWS_READONLY', 'v1:x', 'SECRET', 'AWS',    1, 'ACTIVE', 'u1'),
		       ('c-up',    'ws1', 'GITHUB_OWNER', 'v1:x', 'SECRET', 'GITHUB', 4, 'ACTIVE', 'u1')`)

	got, err := queryNarrowerCredential(context.Background(), db, "c-hi")
	if err != nil {
		t.Fatalf("queryNarrowerCredential: %v", err)
	}
	if got == nil || got.Exists {
		t.Errorf("got %+v, want no narrower credential — AWS cannot do a GITHUB job "+
			"and L4 is not narrower than L3", got)
	}
}

// A revoked or expired credential is not an available alternative. Naming one
// would send the operator to a key that cannot be used, and the next thing they
// do is grant the wide one anyway, having lost the time.
func TestNarrowerCredential_IgnoresCredentialsThatAreNotUsable(t *testing.T) {
	db := newHumanFactsDB(t)
	mustExec(t, db, `INSERT INTO credentials
		(id, workspace_id, name, encrypted_value, type, provider, security_level, status, created_by)
		VALUES ('c-hi', 'ws1', 'GITHUB_ADMIN', 'v1:x', 'SECRET', 'GITHUB', 4, 'ACTIVE',  'u1'),
		       ('c-rv', 'ws1', 'GITHUB_OLD',   'v1:x', 'SECRET', 'GITHUB', 1, 'REVOKED', 'u1')`)

	got, err := queryNarrowerCredential(context.Background(), db, "c-hi")
	if err != nil {
		t.Fatalf("queryNarrowerCredential: %v", err)
	}
	if got == nil || got.Exists {
		t.Errorf("got %+v, want none — a revoked credential is not an alternative", got)
	}
}

// Soft-deleted credentials are gone as far as anyone using the product is
// concerned, and the deleted_at convention is repo-wide.
func TestNarrowerCredential_IgnoresSoftDeleted(t *testing.T) {
	db := newHumanFactsDB(t)
	mustExec(t, db, `INSERT INTO credentials
		(id, workspace_id, name, encrypted_value, type, provider, security_level, status, created_by, deleted_at)
		VALUES ('c-hi', 'ws1', 'GITHUB_ADMIN', 'v1:x', 'SECRET', 'GITHUB', 4, 'ACTIVE', 'u1', NULL),
		       ('c-del','ws1', 'GITHUB_GONE',  'v1:x', 'SECRET', 'GITHUB', 1, 'ACTIVE', 'u1', '2026-07-01T00:00:00Z')`)

	got, err := queryNarrowerCredential(context.Background(), db, "c-hi")
	if err != nil {
		t.Fatalf("queryNarrowerCredential: %v", err)
	}
	if got == nil || got.Exists {
		t.Errorf("got %+v, want none — a deleted credential is not an alternative", got)
	}
}
