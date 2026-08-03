package api

// allowRestore's fourth path: the disaster-recovery resume (#1716).
//
// A workspace created by `restore --as-workspace X` / `--as-crew X` has a
// brand-new id and a slug taken from the flag, so it matches neither of
// the bundle's identities (paths 1 and 2), and the instance is not empty
// either — that same restore populated it (path 3). The step the CLI
// itself instructs the operator to run next was therefore refused 100% of
// the time, which is to say the documented DR flow could not be
// completed at all.
//
// Path 4 allows it on evidence: a backup_restore_origins row this server
// wrote itself, inside the restoring transaction, naming the payload
// digest. These tests pin both halves — that the resume is allowed, and
// that nothing else is.

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// newDRAuthorizeTestDB adds the provenance table path 4 reads. Kept
// separate from newAuthorizeTestDB so the older tests keep proving the
// guard works without it: an instance that has not run the migration
// must behave exactly as it did before.
func newDRAuthorizeTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db := newAuthorizeTestDB(t)
	// allowRestore's slug probe filters on deleted_at, which the minimal
	// fixture omits. Path 4 only runs AFTER that probe, so without the
	// column these tests would fail on the fixture rather than on the
	// guard.
	if _, err := db.Exec(`ALTER TABLE workspaces ADD COLUMN deleted_at TEXT`); err != nil {
		t.Fatalf("add deleted_at: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE backup_restore_origins (
		workspace_id  TEXT PRIMARY KEY,
		bundle_sha256 TEXT NOT NULL,
		bundle_path   TEXT NOT NULL DEFAULT '',
		crew_slug_map TEXT NOT NULL DEFAULT '{}',
		restored_at   TEXT NOT NULL,
		restored_by   TEXT NOT NULL DEFAULT ''
	)`); err != nil {
		t.Fatalf("origins schema: %v", err)
	}
	return db
}

// writeDRBundle produces a real, unencrypted bundle whose manifest
// carries a workspace identity matching neither a caller's id nor their
// slug. Returns the path and the payload digest.
//
// A real bundle rather than a stub, because allowRestore reads it through
// backup.Inspect: a hand-written manifest would prove that the test's own
// fixture parses, not that the guard reads what the product writes.
func writeDRBundle(t *testing.T, dir string) (path, sha string) {
	t.Helper()
	src := testutil.MigratedSQLDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := database.SeedBundledSkills(context.Background(), src, logger); err != nil {
		t.Fatalf("seed skills: %v", err)
	}
	if _, err := src.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws_source_dr', 'Source', 'source-dr')`); err != nil {
		t.Fatalf("seed source workspace: %v", err)
	}
	res, err := backup.CreateBackup(context.Background(), src, backup.CreateOptions{
		Scope:       backup.ScopeWorkspace,
		WorkspaceID: "ws_source_dr",
		OutputDir:   dir,
		Actor:       backup.Actor{UserID: "u1", Email: "a@b.c", Role: "ADMIN"},
		NoEncrypt:   true,
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}
	return res.Path, res.Manifest.Checksums.PayloadSHA256
}

func TestAllowRestore_ForkedWorkspaceResumeIsAllowed(t *testing.T) {
	ctx := context.Background()
	db := newDRAuthorizeTestDB(t)
	bundlePath, sha := writeDRBundle(t, t.TempDir())

	// The forked workspace: different id, different slug, neither related
	// to the bundle's.
	if _, err := db.Exec(`INSERT INTO workspaces (id, slug, name) VALUES ('ws_fork_dr', 'acme-dr', 'Acme DR')`); err != nil {
		t.Fatal(err)
	}

	// Without provenance it must still be refused — the relaxation is the
	// row, not the situation.
	allowed, reason, err := allowRestore(ctx, db, bundlePath, "ws_fork_dr", true)
	if err != nil {
		t.Fatalf("allowRestore: %v", err)
	}
	if allowed {
		t.Fatalf("a workspace with no recorded provenance must not be allowed to restore an unrelated bundle")
	}
	if !strings.Contains(reason, "not bound to your current workspace") {
		t.Errorf("unexpected deny reason: %q", reason)
	}

	// With provenance naming this exact bundle, a FILES-ONLY restore is
	// allowed.
	if _, err := db.Exec(
		`INSERT INTO backup_restore_origins (workspace_id, bundle_sha256, restored_at) VALUES (?, ?, ?)`,
		"ws_fork_dr", sha, "2026-08-03T00:00:00.000000000Z"); err != nil {
		t.Fatal(err)
	}
	allowed, reason, err = allowRestore(ctx, db, bundlePath, "ws_fork_dr", true)
	if err != nil {
		t.Fatalf("allowRestore: %v", err)
	}
	if !allowed {
		t.Fatalf("a workspace forked from THIS bundle must be allowed to finish its restore; denied with %q", reason)
	}

	// The same row must NOT authorise anything else. This is the hole:
	// an un-flagged re-run has no rewrite flag, so it takes the ordinary
	// docker phase, whose crew identities come from the manifest — on
	// this instance, the SOURCE crews. Authorising it here hands their
	// live workspace and agent memory to an older backup, with the row
	// counts looking untroubled because the DB half is INSERT OR IGNORE.
	allowed, reason, err = allowRestore(ctx, db, bundlePath, "ws_fork_dr", false)
	if err != nil {
		t.Fatalf("allowRestore: %v", err)
	}
	if allowed {
		t.Fatalf("provenance authorised a NON files-only restore; that call overwrites the source crews' live data with this bundle")
	}
	if !strings.Contains(reason, "not bound to your current workspace") {
		t.Errorf("unexpected deny reason for the un-flagged re-run: %q", reason)
	}
}

// TestAllowRestore_ProvenanceForADifferentBundleStillDenies pins the
// narrowness of path 4: the row authorises the bundle it names and
// nothing else. Matching on the payload digest rather than on the stored
// path keeps that true even when a path is re-pointed at a different file
// between the two restores.
func TestAllowRestore_ProvenanceForADifferentBundleStillDenies(t *testing.T) {
	ctx := context.Background()
	db := newDRAuthorizeTestDB(t)
	bundlePath, sha := writeDRBundle(t, t.TempDir())
	if sha == "" {
		t.Fatal("bundle has no payload digest; the guard would have nothing to compare")
	}

	if _, err := db.Exec(`INSERT INTO workspaces (id, slug, name) VALUES ('ws_fork_dr', 'acme-dr', 'Acme DR')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO backup_restore_origins (workspace_id, bundle_sha256, restored_at) VALUES (?, ?, ?)`,
		"ws_fork_dr", "0000000000000000000000000000000000000000000000000000000000000000",
		"2026-08-03T00:00:00.000000000Z"); err != nil {
		t.Fatal(err)
	}

	allowed, reason, err := allowRestore(ctx, db, bundlePath, "ws_fork_dr", true)
	if err != nil {
		t.Fatalf("allowRestore: %v", err)
	}
	if allowed {
		t.Fatalf("provenance naming a DIFFERENT bundle must not authorise this one")
	}
	if !strings.Contains(reason, "not bound to your current workspace") {
		t.Errorf("unexpected deny reason: %q", reason)
	}
}
