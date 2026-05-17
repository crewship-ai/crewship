package backup_test

// New gap-filling tests for the bundle-builder pipeline. The handler
// layer is exercised by tests in internal/api/backup_*; the catalog +
// manifest + lock pieces have unit coverage under internal/backup/*_
// test.go; the BackupTables round-trip is covered by
// e2e_roundtrip_test.go. This file gap-fills the contracts that none of
// those touch end-to-end:
//
//   - Create -> Verify produces a valid bundle whose recorded SHA-256
//     matches the on-disk payload bytes (the smoke test the runner
//     relies on but never asserts directly outside of an e2e suite).
//   - The manifest stamped by the runner carries the helper's
//     FormatVersion constant — a future bump that forgets to thread
//     through CreateBackup would silently produce unverifiable
//     bundles.
//   - Tamper detection on a real Create-produced bundle: a single byte
//     flip in the payload region yields Verify.Valid=false AND the
//     Err field wraps ErrInvalidChecksum (callers depend on errors.Is
//     for HTTP status mapping).
//   - ReconcileCatalog round-trip: a catalog row pointing at a file
//     that was rm'd out-of-band is pruned AND no longer surfaces
//     through ListCatalog. Existing unit tests assert each leg
//     separately; this exercises the full sequence via the public
//     surface.
//   - Restore is workspace-scoped: bundle made from workspace A
//     restored into a target that already holds workspace A's rows
//     surfaces ErrNoOpRestore (every PK collided), proving that
//     restoring into a "different workspace identity" without
//     --as-workspace is rejected loudly rather than silently
//     overwriting data.
//   - Lock acquire/release/IsLockHeld lifecycle on the public
//     SQLLockManager API: acquire -> IsLockHeld(true) -> release ->
//     IsLockHeld(false), plus the double-acquire-without-release
//     ErrLockHeld contract that gates concurrent backups.

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/crewship-ai/crewship/internal/backup"
)

// TestBackup_CreateThenVerify_RoundTripValid asserts the production
// pipeline produces a bundle whose recorded SHA-256 matches the actual
// payload bytes: a Verify round-trip returns Valid=true, a non-nil
// Manifest, and a non-zero Size. This is the single smoke test that
// gates every restore — if it ever flips, every downstream contract
// (e2e round-trip, catalog refresh, /admin/backups list) becomes
// unreliable in the same direction.
func TestBackup_CreateThenVerify_RoundTripValid(t *testing.T) {
	ctx := context.Background()
	db := openMigratedDB(t)
	workspaceID := seedWorkspace(t, db)

	const passphrase = "create-verify-roundtrip-pass-123"
	res, err := backup.CreateBackup(ctx, db, backup.CreateOptions{
		Scope:       backup.ScopeWorkspace,
		WorkspaceID: workspaceID,
		OutputDir:   t.TempDir(),
		Actor: backup.Actor{
			UserID: "u_admin",
			Email:  "admin@e2e.test",
			Role:   "ADMIN",
		},
		Passphrase: passphrase,
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}
	if res == nil || res.Path == "" {
		t.Fatalf("CreateBackup returned nil/empty path: %+v", res)
	}
	if res.Size <= 0 {
		t.Errorf("CreateResult.Size = %d, want > 0", res.Size)
	}
	if res.SHA256 == "" {
		t.Errorf("CreateResult.SHA256 = \"\", want non-empty")
	}

	v, err := backup.Verify(ctx, res.Path)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !v.Valid {
		t.Fatalf("Verify reported Valid=false on a freshly-created bundle (err=%v)", v.Err)
	}
	if v.Manifest == nil {
		t.Fatal("Verify returned a valid bundle but nil Manifest")
	}
	// SHA in the verify result's manifest must match what Create
	// stamped — otherwise a future regression in WriteBundle that
	// re-hashes the wrong bytes would slip past Verify=true.
	if v.Manifest.Checksums.PayloadSHA256 != res.SHA256 {
		t.Errorf("checksum drift: create=%q verify-manifest=%q",
			res.SHA256, v.Manifest.Checksums.PayloadSHA256)
	}
}

// TestBackup_CreatedBundle_StampsFormatVersion locks in that the
// runner threads the package-level FormatVersion constant into every
// produced manifest. A future migration of the constant that forgets
// to repopulate manifest.FormatVersion in CreateBackup would yield
// bundles that Validate() rejects (format_version must be positive) —
// pinning the relationship at the source of truth catches the drift.
func TestBackup_CreatedBundle_StampsFormatVersion(t *testing.T) {
	ctx := context.Background()
	db := openMigratedDB(t)
	workspaceID := seedWorkspace(t, db)

	res, err := backup.CreateBackup(ctx, db, backup.CreateOptions{
		Scope:       backup.ScopeWorkspace,
		WorkspaceID: workspaceID,
		OutputDir:   t.TempDir(),
		Actor: backup.Actor{
			UserID: "u_admin",
			Email:  "admin@e2e.test",
			Role:   "ADMIN",
		},
		Passphrase: "format-version-stamp-pass-123",
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}
	if res.Manifest == nil {
		t.Fatal("CreateResult.Manifest is nil")
	}
	// The runner-produced manifest must carry the helper's
	// FormatVersion verbatim.
	if res.Manifest.FormatVersion != backup.FormatVersion {
		t.Errorf("manifest.FormatVersion = %d, want %d (the helper const)",
			res.Manifest.FormatVersion, backup.FormatVersion)
	}

	// Re-inspect the bundle from disk to assert the value survived
	// the on-disk write. The runner could in principle stamp the
	// CreateResult.Manifest correctly but write a different value
	// into MANIFEST.json; Inspect closes that gap.
	m, err := backup.Inspect(ctx, res.Path)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if m.FormatVersion != backup.FormatVersion {
		t.Errorf("on-disk manifest.FormatVersion = %d, want %d",
			m.FormatVersion, backup.FormatVersion)
	}
}

// TestBackup_TamperedPayload_VerifyReportsChecksumMismatch makes sure
// that flipping a byte inside the payload region of a Create-produced
// bundle causes Verify to return Valid=false with an Err that wraps
// ErrInvalidChecksum. Callers in cmd_backup_verify and the REST
// handler use errors.Is(err, ErrInvalidChecksum) to map to the
// "corrupted bundle" exit/HTTP code — a wrap that doesn't preserve
// the typed error sentinel would silently demote every tamper to a
// generic failure.
func TestBackup_TamperedPayload_VerifyReportsChecksumMismatch(t *testing.T) {
	ctx := context.Background()
	db := openMigratedDB(t)
	workspaceID := seedWorkspace(t, db)

	res, err := backup.CreateBackup(ctx, db, backup.CreateOptions{
		Scope:       backup.ScopeWorkspace,
		WorkspaceID: workspaceID,
		OutputDir:   t.TempDir(),
		Actor: backup.Actor{
			UserID: "u_admin",
			Email:  "admin@e2e.test",
			Role:   "ADMIN",
		},
		Passphrase: "tamper-detection-pass-123",
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	// Sanity: untampered bundle verifies.
	clean, err := backup.Verify(ctx, res.Path)
	if err != nil {
		t.Fatalf("baseline Verify: %v", err)
	}
	if !clean.Valid {
		t.Fatalf("baseline bundle Valid=false (err=%v); test cannot distinguish tamper from a broken create", clean.Err)
	}

	// Flip a byte ~70% through the file. The manifest + RESTORE.md
	// live near the start; 70% lands well inside the payload region
	// so the tar headers stay intact and Verify reaches the
	// checksum step rather than failing earlier with a tar/zstd
	// decode error.
	data, err := os.ReadFile(res.Path)
	if err != nil {
		t.Fatalf("read bundle: %v", err)
	}
	if len(data) < 100 {
		t.Fatalf("bundle suspiciously small (%d bytes); cannot tamper safely", len(data))
	}
	pos := len(data) * 7 / 10
	data[pos] ^= 0xFF
	if err := os.WriteFile(res.Path, data, 0o600); err != nil {
		t.Fatalf("write tampered bundle: %v", err)
	}

	got, err := backup.Verify(ctx, res.Path)
	if err != nil {
		t.Fatalf("Verify (tampered) returned a Go error (should surface mismatch via VerifyResult.Err): %v", err)
	}
	if got == nil {
		t.Fatal("Verify (tampered) returned a nil result")
	}
	if got.Valid {
		t.Error("Verify (tampered) reported Valid=true; expected Valid=false on byte-flipped payload")
	}
	if got.Err == nil {
		t.Fatal("Verify (tampered) returned Valid=false but a nil Err — callers cannot distinguish corruption from format errors without it")
	}
	if !errors.Is(got.Err, backup.ErrInvalidChecksum) {
		t.Errorf("Verify (tampered).Err = %v; want errors.Is(..., ErrInvalidChecksum)", got.Err)
	}
}

// TestBackup_ReconcileCatalog_PrunesAndListSurfacesNothing exercises
// the end-to-end contract the admin UI's `List` view depends on: a
// catalog row whose backing file was deleted out-of-band (manual rm,
// crashed rotate, NFS hiccup) is pruned by ReconcileCatalog AND a
// subsequent ListCatalog does not surface it. The individual halves
// have unit coverage in catalog_test.go; this test pins the sequence
// through the public surface so a refactor that flips one half but
// not the other can't slip past.
func TestBackup_ReconcileCatalog_PrunesAndListSurfacesNothing(t *testing.T) {
	ctx := context.Background()
	db := openMigratedDB(t)

	tmp := t.TempDir()
	// Build a real on-disk bundle so the catalog row points at a
	// file that ListCatalog (which doesn't re-parse the bundle)
	// will accept. We don't actually need the manifest fidelity —
	// just a path that exists, then a row pointing at a sibling
	// path that does NOT exist.
	presentPath := tmp + "/present.tar.zst"
	if err := os.WriteFile(presentPath, []byte("placeholder"), 0o600); err != nil {
		t.Fatalf("write placeholder: %v", err)
	}
	missingPath := tmp + "/missing-bundle.tar.zst"

	now := time.Now().UTC().Truncate(time.Second)
	const workspaceID = "ws_reconcile_e2e"
	if err := backup.UpsertCatalogEntry(ctx, db, backup.CatalogEntry{
		FilePath:    presentPath,
		Scope:       string(backup.ScopeWorkspace),
		WorkspaceID: workspaceID,
		CreatedAt:   now,
		CreatedBy:   "admin@e2e.test",
		Size:        11,
		SHA256:      "sha256:present",
	}); err != nil {
		t.Fatalf("seed present row: %v", err)
	}
	if err := backup.UpsertCatalogEntry(ctx, db, backup.CatalogEntry{
		FilePath:    missingPath,
		Scope:       string(backup.ScopeWorkspace),
		WorkspaceID: workspaceID,
		CreatedAt:   now,
		CreatedBy:   "admin@e2e.test",
		Size:        99,
		SHA256:      "sha256:missing",
	}); err != nil {
		t.Fatalf("seed missing row: %v", err)
	}

	// Pre-reconcile: ListCatalog must see BOTH rows. If it doesn't,
	// the test cannot distinguish "reconcile pruned" from "seed
	// never landed", and the assertion below would be vacuous.
	pre, err := backup.ListCatalog(ctx, db, workspaceID)
	if err != nil {
		t.Fatalf("ListCatalog (pre): %v", err)
	}
	if len(pre) != 2 {
		t.Fatalf("pre-reconcile rows = %d, want 2 (seed didn't land both)", len(pre))
	}

	pruned, err := backup.ReconcileCatalog(ctx, db, workspaceID)
	if err != nil {
		t.Fatalf("ReconcileCatalog: %v", err)
	}
	if len(pruned) != 1 || pruned[0] != missingPath {
		t.Errorf("ReconcileCatalog returned %v, want [%s]", pruned, missingPath)
	}

	// Post-reconcile: ListCatalog returns only the present row.
	// The pruned row must NOT appear — that's the user-visible
	// guarantee the admin UI is built on.
	post, err := backup.ListCatalog(ctx, db, workspaceID)
	if err != nil {
		t.Fatalf("ListCatalog (post): %v", err)
	}
	if len(post) != 1 {
		t.Fatalf("post-reconcile rows = %d, want 1", len(post))
	}
	if post[0].FilePath != presentPath {
		t.Errorf("post-reconcile row.FilePath = %q, want %q", post[0].FilePath, presentPath)
	}
}

// TestBackup_RestoreSameWorkspaceTwice_RejectsAsNoOp captures the
// workspace-scoping rule for restore: a bundle made from workspace A
// carries A's IDs verbatim, so restoring it into a target that
// already holds A's rows yields ErrNoOpRestore (every PK collides).
// The caller is supposed to either restore onto a clean target, or
// supply --as-workspace to remap. This test pins the "rejected
// loudly" half — silent overwrite would be data loss.
func TestBackup_RestoreSameWorkspaceTwice_RejectsAsNoOp(t *testing.T) {
	ctx := context.Background()
	source := openMigratedDB(t)
	workspaceID := seedWorkspace(t, source)

	const passphrase = "workspace-scope-restore-pass-123"
	res, err := backup.CreateBackup(ctx, source, backup.CreateOptions{
		Scope:       backup.ScopeWorkspace,
		WorkspaceID: workspaceID,
		OutputDir:   t.TempDir(),
		Actor: backup.Actor{
			UserID: "u_admin",
			Email:  "admin@e2e.test",
			Role:   "ADMIN",
		},
		Passphrase: passphrase,
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	// Restore back into the SAME DB. Every workspace/crew/agent row
	// already exists, so RestoreDumpTx's INSERT OR IGNORE drops the
	// lot. The runner detects the collision and returns
	// ErrNoOpRestore — that is the loud-rejection contract.
	_, err = backup.RestoreBackup(ctx, source, backup.RestoreOptions{
		Path:       res.Path,
		Passphrase: passphrase,
		Actor: backup.Actor{
			UserID: "u_admin",
			Email:  "admin@e2e.test",
			Role:   "ADMIN",
		},
	})
	if err == nil {
		t.Fatal("RestoreBackup into self returned nil error; expected ErrNoOpRestore (silent overwrite would be data loss)")
	}
	if !errors.Is(err, backup.ErrNoOpRestore) {
		t.Errorf("RestoreBackup into self err = %v; want errors.Is(..., ErrNoOpRestore)", err)
	}

	// Sanity: the same bundle restored with --as-workspace into the
	// same target SHOULD succeed (lands alongside under a new slug).
	// We don't deeply assert the new rows — that contract is owned
	// by TestE2E_AsWorkspace_SurfacesDroppedFilesystems — we only
	// pin that the rewrite flag is the canonical escape hatch.
	rewrite, err := backup.RestoreBackup(ctx, source, backup.RestoreOptions{
		Path:        res.Path,
		Passphrase:  passphrase,
		AsWorkspace: "rescoped-target-ws",
		Actor: backup.Actor{
			UserID: "u_admin",
			Email:  "admin@e2e.test",
			Role:   "ADMIN",
		},
	})
	if err != nil {
		t.Fatalf("RestoreBackup with --as-workspace into self: %v", err)
	}
	if rewrite == nil || rewrite.RowsInserted <= 0 {
		t.Errorf("--as-workspace restore inserted %d rows, want > 0", rewrite.RowsInserted)
	}
}

// TestBackup_LockLifecycle_AcquireIsHeldRelease pins the public
// SQLLockManager API the orchestrator depends on to refuse new agent
// runs while a backup is in flight. The lifecycle:
//
//  1. AcquireWorkspaceLock returns a non-nil ReleaseFunc + nil error
//     on a clean workspace.
//  2. IsLockHeld returns (true, nil) while the lock is held.
//  3. Calling ReleaseFunc returns nil and IsLockHeld flips to
//     (false, nil).
//  4. A second AcquireWorkspaceLock without releasing the first
//     returns ErrLockHeld (not a generic DB error) so callers can
//     map to HTTP 409 via errors.Is.
//
// Existing lock_test.go covers each leg in isolation; this test
// pins the full sequence in one TestBackup_* function so a
// refactor cannot quietly skip one of the four invariants the
// orchestrator's mission-start path relies on.
func TestBackup_LockLifecycle_AcquireIsHeldRelease(t *testing.T) {
	ctx := context.Background()
	db := openMigratedDB(t)

	// Pin a workspace row so the FK in backup_locks resolves.
	const workspaceID = "ws_lock_lifecycle"
	if _, err := db.ExecContext(ctx,
		`INSERT INTO workspaces (id, name, slug) VALUES (?, ?, ?)`,
		workspaceID, "Lock LC", "lock-lc"); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}

	mgr := backup.NewSQLLockManager(db)
	now := time.Now().UTC()

	// (1) Acquire returns a release function.
	release, err := mgr.AcquireWorkspaceLock(ctx, workspaceID, "u_admin", time.Hour)
	if err != nil {
		t.Fatalf("AcquireWorkspaceLock: %v", err)
	}
	if release == nil {
		t.Fatal("AcquireWorkspaceLock returned nil ReleaseFunc")
	}

	// (2) IsLockHeld is true while held.
	held, err := backup.IsLockHeld(ctx, db, workspaceID, now)
	if err != nil {
		t.Fatalf("IsLockHeld (held): %v", err)
	}
	if !held {
		t.Error("IsLockHeld returned false immediately after Acquire; expected true")
	}

	// (4) A second acquire without releasing must surface
	// ErrLockHeld so callers can map cleanly. Done BEFORE the
	// release so we can see the lock-collision path.
	_, err = mgr.AcquireWorkspaceLock(ctx, workspaceID, "u_other", time.Hour)
	if !errors.Is(err, backup.ErrLockHeld) {
		t.Errorf("second AcquireWorkspaceLock err = %v; want errors.Is(..., ErrLockHeld)", err)
	}

	// (3) Release succeeds, then IsLockHeld flips to false.
	if err := release(ctx); err != nil {
		t.Fatalf("ReleaseFunc: %v", err)
	}
	held, err = backup.IsLockHeld(ctx, db, workspaceID, now)
	if err != nil {
		t.Fatalf("IsLockHeld (after release): %v", err)
	}
	if held {
		t.Error("IsLockHeld returned true after release; expected false")
	}

	// A fresh acquire on the now-released workspace must succeed —
	// proving the second-acquire-failure above was a TRUE collision
	// (lock still held by us) rather than a permanent DB corruption.
	release2, err := mgr.AcquireWorkspaceLock(ctx, workspaceID, "u_admin", time.Hour)
	if err != nil {
		t.Fatalf("AcquireWorkspaceLock after release: %v", err)
	}
	if err := release2(ctx); err != nil {
		t.Errorf("ReleaseFunc (#2): %v", err)
	}
}
