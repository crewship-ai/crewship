package backup_test

// End-to-end disaster-recovery test. Reproduces the canonical user
// scenario that the previous DumpWorkspace + bundleBelongsToWorkspace
// + RestoreDumpTx combination silently broke:
//
//   1. Workspace exists with real data (workspace, crew, agents,
//      chats, journal entries).
//   2. Backup is taken.
//   3. `dev.sh nuke` wipes everything (DB + Docker state).
//   4. `crewship start` boots and bootstraps a NEW workspace under
//      the SAME slug (fresh bootstrap regenerates the CUID — the
//      slug stays "demo" or whatever the user typed at first-run).
//   5. Admin runs `crewship backup restore bundle.tar.zst --replace`.
//   6. Expectation: the target's workspace row gets WIPED and the
//      bundle's rows land verbatim, original IDs intact.
//
// Pre-fix, step 6 failed in three ways depending on whether the user
// passed --as-workspace, --replace, or neither:
//
//   - No flag: API returned 404 because bundleBelongsToWorkspace
//     rejected the bundle (workspace_id didn't match the
//     fresh-bootstrap CUID).
//   - --as-workspace: created a SECOND workspace beside the
//     fresh-bootstrap one; the original slug-with-data scenario was
//     impossible because RemapIDs minted new IDs and the bundle's
//     IDs were lost.
//   - --replace: didn't exist as a flag at all.
//
// This test asserts the new contract end-to-end.

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/crewship-ai/crewship/internal/backup"
)

// TestE2E_DisasterRecovery_ReplaceModePreservesIDs is the test that
// would have caught every restore bug from the recent user DR cycle.
// It exercises the post-nuke "fresh bootstrap took the slug" path
// without spinning up a real container — pure DB layer.
func TestE2E_DisasterRecovery_ReplaceModePreservesIDs(t *testing.T) {
	ctx := context.Background()

	// === 1. Source: seed a workspace with real-ish data.
	source := openMigratedDB(t)
	bundleWorkspaceID := seedWorkspace(t, source)

	const passphrase = "dr-e2e-replace-mode-test"
	createResult, err := backup.CreateBackup(ctx, source, backup.CreateOptions{
		Scope:       backup.ScopeWorkspace,
		WorkspaceID: bundleWorkspaceID,
		OutputDir:   t.TempDir(),
		Actor:       backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"},
		Passphrase:  passphrase,
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	// === 2. Target: simulate post-nuke fresh bootstrap. The user
	// re-creates a workspace under the SAME slug ("e2e-ws") but the
	// platform mints a new CUID. This is what `crewship start`
	// produces after `dev.sh nuke` — bootstrap creates a default
	// workspace from config, no relation to whatever the prior
	// lifecycle had.
	target := openMigratedDB(t)
	const freshBootstrapID = "ws_fresh_bootstrap_abc123"
	if _, err := target.ExecContext(ctx,
		`INSERT INTO workspaces (id, name, slug) VALUES (?, ?, ?)`,
		freshBootstrapID, "Fresh Bootstrap WS", "e2e-ws"); err != nil {
		t.Fatalf("seed fresh-bootstrap workspace: %v", err)
	}
	// Also seed a different user the bootstrap might have created.
	if _, err := target.ExecContext(ctx,
		`INSERT INTO users (id, email, full_name) VALUES (?, ?, ?)`,
		"u_fresh_admin", "fresh@bootstrap.test", "Fresh Admin"); err != nil {
		t.Fatalf("seed fresh-bootstrap user: %v", err)
	}

	// === 3. Restore with --replace.
	restoreResult, err := backup.RestoreBackup(ctx, target, backup.RestoreOptions{
		Path:       createResult.Path,
		Passphrase: passphrase,
		Replace:    true,
		Actor:      backup.Actor{UserID: "u_fresh_admin", Email: "fresh@bootstrap.test", Role: "ADMIN"},
	})
	if err != nil {
		t.Fatalf("RestoreBackup with --replace: %v", err)
	}
	if restoreResult.RowsInserted <= 0 {
		t.Fatalf("expected RowsInserted > 0 after --replace restore; got %d", restoreResult.RowsInserted)
	}

	// === 4. Assertions: the bundle's WORKSPACE ID is what survived,
	// not the fresh-bootstrap one. This proves --replace wiped the
	// target and the original IDs landed.
	var restoredID string
	if err := target.QueryRowContext(ctx,
		`SELECT id FROM workspaces WHERE slug = ?`, "e2e-ws").Scan(&restoredID); err != nil {
		t.Fatalf("query restored workspace: %v", err)
	}
	if restoredID != bundleWorkspaceID {
		t.Errorf("--replace restored under wrong id:\n  bundle workspace id  = %s\n  fresh-bootstrap id   = %s\n  actual id after restore = %s",
			bundleWorkspaceID, freshBootstrapID, restoredID)
	}

	// Fresh-bootstrap row must be gone (replaced).
	var freshCount int
	if err := target.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM workspaces WHERE id = ?`, freshBootstrapID).Scan(&freshCount); err != nil {
		t.Fatalf("count fresh-bootstrap row: %v", err)
	}
	if freshCount != 0 {
		t.Errorf("--replace should have wiped fresh-bootstrap workspace row; %d still present", freshCount)
	}

	// Crew rows from the bundle landed under the bundle's workspace.
	var crewCount int
	if err := target.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM crews WHERE workspace_id = ?`, bundleWorkspaceID).Scan(&crewCount); err != nil {
		t.Fatalf("count crews: %v", err)
	}
	if crewCount != 2 {
		t.Errorf("expected 2 crews under restored workspace, got %d", crewCount)
	}

	// Agents landed.
	var agentCount int
	if err := target.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM agents WHERE workspace_id = ?`, bundleWorkspaceID).Scan(&agentCount); err != nil {
		t.Fatalf("count agents: %v", err)
	}
	if agentCount != 4 {
		t.Errorf("expected 4 agents under restored workspace, got %d", agentCount)
	}

	// Journal entries landed (test catches the previously-missing
	// journal_entries roundtrip).
	var journalCount int
	if err := target.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM journal_entries WHERE workspace_id = ?`, bundleWorkspaceID).Scan(&journalCount); err != nil {
		t.Fatalf("count journal_entries: %v", err)
	}
	if journalCount != 2 {
		t.Errorf("expected 2 journal_entries restored, got %d", journalCount)
	}
}

// TestE2E_DisasterRecovery_NoReplaceInSameInstance asserts the
// "restore into the same instance you backed up from" path returns
// ErrNoOpRestore — every PK collides with itself. That's the loud
// signal that --replace is required for a re-restore.
func TestE2E_DisasterRecovery_NoReplaceInSameInstance(t *testing.T) {
	ctx := context.Background()

	source := openMigratedDB(t)
	workspaceID := seedWorkspace(t, source)

	const passphrase = "dr-e2e-no-replace-same-instance"
	createResult, err := backup.CreateBackup(ctx, source, backup.CreateOptions{
		Scope:       backup.ScopeWorkspace,
		WorkspaceID: workspaceID,
		OutputDir:   t.TempDir(),
		Actor:       backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"},
		Passphrase:  passphrase,
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	// Restore back into the SAME source DB without --replace.
	_, err = backup.RestoreBackup(ctx, source, backup.RestoreOptions{
		Path:       createResult.Path,
		Passphrase: passphrase,
		Actor:      backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"},
	})
	if !errors.Is(err, backup.ErrNoOpRestore) {
		t.Errorf("expected ErrNoOpRestore on same-instance restore without --replace, got %v", err)
	}
}

// TestE2E_DisasterRecovery_ReplaceRejectsAsWorkspace pins the
// semantic conflict: --replace and --as-workspace mean opposite
// things ("reassert this identity" vs "fork under a new slug"), so
// combining them must fail fast.
func TestE2E_DisasterRecovery_ReplaceRejectsAsWorkspace(t *testing.T) {
	ctx := context.Background()

	source := openMigratedDB(t)
	workspaceID := seedWorkspace(t, source)

	const passphrase = "dr-e2e-replace-as-ws-conflict"
	createResult, err := backup.CreateBackup(ctx, source, backup.CreateOptions{
		Scope:       backup.ScopeWorkspace,
		WorkspaceID: workspaceID,
		OutputDir:   t.TempDir(),
		Actor:       backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"},
		Passphrase:  passphrase,
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	target := openMigratedDB(t)
	_, err = backup.RestoreBackup(ctx, target, backup.RestoreOptions{
		Path:        createResult.Path,
		Passphrase:  passphrase,
		Replace:     true,
		AsWorkspace: "forked-ws",
		Actor:       backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"},
	})
	if !errors.Is(err, backup.ErrInvalidScope) {
		t.Errorf("expected ErrInvalidScope when combining --replace and --as-workspace, got %v", err)
	}
	if err == nil || !strings.Contains(err.Error(), "--replace is incompatible") {
		t.Errorf("expected helpful 'incompatible' error message, got %v", err)
	}
}

// TestE2E_DisasterRecovery_ReplaceWipesOnSlugMatchWithDifferentID is
// the post-nuke + pre-existing-data scenario: the fresh-bootstrap
// workspace has new id AND already accumulated some data (a user
// poked around before realising they needed the backup). --replace
// must wipe that too.
func TestE2E_DisasterRecovery_ReplaceWipesOnSlugMatchWithDifferentID(t *testing.T) {
	ctx := context.Background()

	source := openMigratedDB(t)
	bundleWorkspaceID := seedWorkspace(t, source)

	const passphrase = "dr-e2e-replace-slug-match"
	createResult, err := backup.CreateBackup(ctx, source, backup.CreateOptions{
		Scope:       backup.ScopeWorkspace,
		WorkspaceID: bundleWorkspaceID,
		OutputDir:   t.TempDir(),
		Actor:       backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"},
		Passphrase:  passphrase,
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	// Fresh target: same slug, different id, with some additional rows.
	target := openMigratedDB(t)
	const freshID = "ws_fresh_with_data_xyz"
	if _, err := target.ExecContext(ctx,
		`INSERT INTO workspaces (id, name, slug) VALUES (?, ?, ?)`,
		freshID, "Fresh Bootstrap Polluted", "e2e-ws"); err != nil {
		t.Fatalf("seed fresh-with-data workspace: %v", err)
	}
	if _, err := target.ExecContext(ctx,
		`INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, ?, ?)`,
		"c_pollute_1", freshID, "Polluting Crew", "pollute"); err != nil {
		t.Fatalf("seed polluting crew: %v", err)
	}

	// Restore with --replace. The polluting crew under the same slug
	// must vanish; the bundle's crews must replace it.
	_, err = backup.RestoreBackup(ctx, target, backup.RestoreOptions{
		Path:       createResult.Path,
		Passphrase: passphrase,
		Replace:    true,
		Actor:      backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"},
	})
	if err != nil {
		t.Fatalf("RestoreBackup with --replace: %v", err)
	}

	// Polluting crew must be gone.
	var polluteCount int
	if err := target.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM crews WHERE id = ?`, "c_pollute_1").Scan(&polluteCount); err != nil {
		t.Fatalf("count polluting crew: %v", err)
	}
	if polluteCount != 0 {
		t.Errorf("--replace failed to wipe polluting crew under same slug; %d rows remain", polluteCount)
	}

	// Bundle's crews landed under the bundle's workspace id.
	var bundleCrewCount int
	if err := target.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM crews WHERE workspace_id = ?`, bundleWorkspaceID).Scan(&bundleCrewCount); err != nil {
		t.Fatalf("count bundle crews: %v", err)
	}
	if bundleCrewCount != 2 {
		t.Errorf("expected 2 bundle crews after --replace, got %d", bundleCrewCount)
	}
}

// TestE2E_DisasterRecovery_ReplaceWithNoMatchingTarget covers the
// "restore into completely fresh instance" path. --replace finds no
// workspace under the bundle's id OR slug, the pre-INSERT delete pass
// is a no-op, and the bundle lands with original IDs.
func TestE2E_DisasterRecovery_ReplaceWithNoMatchingTarget(t *testing.T) {
	ctx := context.Background()

	source := openMigratedDB(t)
	bundleWorkspaceID := seedWorkspace(t, source)

	const passphrase = "dr-e2e-replace-empty-target"
	createResult, err := backup.CreateBackup(ctx, source, backup.CreateOptions{
		Scope:       backup.ScopeWorkspace,
		WorkspaceID: bundleWorkspaceID,
		OutputDir:   t.TempDir(),
		Actor:       backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"},
		Passphrase:  passphrase,
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	// Completely empty target.
	target := openMigratedDB(t)
	res, err := backup.RestoreBackup(ctx, target, backup.RestoreOptions{
		Path:       createResult.Path,
		Passphrase: passphrase,
		Replace:    true,
		Actor:      backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"},
	})
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if res.RowsInserted <= 0 {
		t.Errorf("expected rows inserted on empty target, got %d", res.RowsInserted)
	}

	var restoredID string
	if err := target.QueryRowContext(ctx,
		`SELECT id FROM workspaces WHERE slug = ?`, "e2e-ws").Scan(&restoredID); err != nil {
		t.Fatalf("query restored workspace: %v", err)
	}
	if restoredID != bundleWorkspaceID {
		t.Errorf("--replace into empty target failed to preserve IDs: bundle=%s, restored=%s",
			bundleWorkspaceID, restoredID)
	}
}

// === Compile-time guards ==================================================

// _ ensures the source DB symbol is referenced so unused-import vet
// stays quiet when test selection skips both real bodies above.
var _ *sql.DB
