package backup_test

// Regression coverage for the #2255 code-review fixes on top of #2009's
// restore-side row-count reporting. All three defects fixed here fire on
// perfectly healthy, ordinary restores while printing text that asserts
// the bundle is corrupt or that rows were dropped by a collision — see
// runner_restore.go and completeness.go for the fixes.
//
// TestRestoreBackup_ForkedRestoreDoesNotFalselyReportRowCountMismatch pins
// defect (1): a --as-workspace fork's own bookkeeping (a re-signed journal
// chain notice, a restoring-admin membership row) must not read as the
// bundle disagreeing with its own manifest.
//
// TestRestoreBackup_DesignedNoOpsNotReportedAsShortfalls pins defect (2):
// a table whose shortfall is the DESIGNED outcome of INSERT OR IGNORE
// (bundled skills reseeded on every boot, a user ReconcileUsersByEmail
// deliberately aligned onto a matching target row) must not be reported
// as a completeness problem.
//
// Both use the real CreateBackup -> RestoreBackup pipeline specifically
// because the gap that let the original defects through was that the
// branch's own tests built manifests with newValidManifest(), which
// carries no TableRowCounts at all — the comparison short-circuits on an
// empty map and never runs. CreateBackup always populates TableRowCounts
// from the real dump, so these tests cannot repeat that gap.

import (
	"context"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
)

// TestRestoreBackup_ForkedRestoreDoesNotFalselyReportRowCountMismatch is
// the reproduction for defect (1). u_admin is seeded as a user (see
// seedWorkspace) but deliberately NOT made a member of the workspace, so
// ensureRestoringUserMembership has a row to append; seedJournalChain
// gives rechainForkedJournal a chain to re-sign. Both mutations run on
// extracted.DBDump AFTER payload extraction but were, before the fix,
// folded into the very count payloadMismatches compared against the
// manifest — so every forked restore of an intact bundle reported
// workspace_members and journal_entries as not matching their own
// manifest.
func TestRestoreBackup_ForkedRestoreDoesNotFalselyReportRowCountMismatch(t *testing.T) {
	ctx := context.Background()

	source := openMigratedDB(t)
	workspaceID := seedWorkspace(t, source)
	seedJournalChain(t, source, workspaceID, 3)

	const passphrase = "row-count-fork-pass-123"
	actor := backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"}
	created, err := backup.CreateBackup(ctx, source, backup.CreateOptions{
		Scope:       backup.ScopeWorkspace,
		WorkspaceID: workspaceID,
		OutputDir:   t.TempDir(),
		Actor:       actor,
		Passphrase:  passphrase,
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}
	// Precondition: the manifest actually recorded a baseline that says
	// what this test needs it to say — u_admin absent from
	// workspace_members going in. If this drifts, the rest of the test
	// proves nothing.
	if n, ok := created.Manifest.Contents.TableRowCounts["workspace_members"]; !ok || n != 0 {
		t.Fatalf("test setup: manifest recorded workspace_members=%d (present=%v), want 0 — "+
			"u_admin must not already be a member of the seeded workspace", n, ok)
	}

	res, err := backup.RestoreBackup(ctx, source, backup.RestoreOptions{
		Path:        created.Path,
		Passphrase:  passphrase,
		Actor:       actor,
		AsWorkspace: "e2e-ws-fork-rowcount",
	})
	if err != nil {
		t.Fatalf("RestoreBackup --as-workspace: %v", err)
	}
	if res.RestoredWorkspaceID == "" || res.RestoredWorkspaceID == workspaceID {
		t.Fatalf("--as-workspace did not fork a new workspace id (got %q)", res.RestoredWorkspaceID)
	}

	if len(res.PayloadRowCountMismatches) != 0 {
		t.Errorf("PayloadRowCountMismatches = %+v, want none — a forked restore's own bookkeeping "+
			"(the restoring-admin membership row, the journal re-sign notice) must not be read back as "+
			"the bundle disagreeing with its own manifest", res.PayloadRowCountMismatches)
	}
}

// TestRestoreBackup_DesignedNoOpsNotReportedAsShortfalls is the
// reproduction for defect (2): restore into a separate, already-
// bootstrapped instance that shares the bundle admin's email — the
// canonical disaster-recovery scenario. Two tables land short of what
// the manifest recorded, both by design:
//
//   - skills: skill_coding_01 / skill_research_01 are bundled skills the
//     target already seeded (same stable IDs) before the restore ever
//     ran; INSERT OR IGNORE correctly no-ops against them.
//   - users: the target already has an account under admin@e2e.test
//     (a different id) — ReconcileUsersByEmail aligns the bundle's user
//     row onto it before the insert pass, so that insert no-ops too.
func TestRestoreBackup_DesignedNoOpsNotReportedAsShortfalls(t *testing.T) {
	ctx := context.Background()

	source := openMigratedDB(t)
	workspaceID := seedWorkspace(t, source)

	const passphrase = "designed-noop-pass-123"
	actor := backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"}
	created, err := backup.CreateBackup(ctx, source, backup.CreateOptions{
		Scope:       backup.ScopeWorkspace,
		WorkspaceID: workspaceID,
		OutputDir:   t.TempDir(),
		Actor:       actor,
		Passphrase:  passphrase,
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}
	// Precondition: the bundle actually carries both designed-no-op
	// shapes this test exists to check — 2 bundled skills + 1 custom
	// skill, and exactly the one seeded user.
	if n := created.Manifest.Contents.TableRowCounts["skills"]; n != 3 {
		t.Fatalf("test setup: expected 3 skills in the bundle (2 bundled + 1 custom), got %d", n)
	}
	if n := created.Manifest.Contents.TableRowCounts["users"]; n != 1 {
		t.Fatalf("test setup: expected exactly 1 user (u_admin) in the bundle, got %d", n)
	}

	// A separate, freshly-bootstrapped instance: bundled skills already
	// seeded (same stable IDs as the source, via openMigratedDB), and an
	// admin account that happens to share the bundle admin's email under
	// a DIFFERENT id.
	target := openMigratedDB(t)
	if _, err := target.ExecContext(ctx,
		`INSERT INTO users (id, email, full_name) VALUES (?, ?, ?)`,
		"u_target_admin", "admin@e2e.test", "Target Admin"); err != nil {
		t.Fatalf("seed target admin: %v", err)
	}

	res, err := backup.RestoreBackup(ctx, target, backup.RestoreOptions{
		Path:       created.Path,
		Passphrase: passphrase,
		Actor:      actor,
	})
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}

	for _, m := range res.RowsInsertedShortfalls {
		switch m.Table {
		case "skills":
			t.Errorf("RowsInsertedShortfalls flags %q (recorded %d, actual %d) — bundled skills are "+
				"reseeded with stable IDs on every instance boot; INSERT OR IGNORE no-opping against "+
				"them is the expected outcome of every restore, not a completeness problem",
				m.Table, m.Recorded, m.Actual)
		case "users":
			t.Errorf("RowsInsertedShortfalls flags %q (recorded %d, actual %d) — ReconcileUsersByEmail "+
				"deliberately aligned this bundle user onto the target's matching-email row before the "+
				"insert pass; the no-op is the intended outcome of that alignment, not a shortfall",
				m.Table, m.Recorded, m.Actual)
		}
	}

	// The fix must not silence a genuine reason to look — only the two
	// designed-no-op shapes above. The custom skill and the crew_members
	// row that now points at the reconciled user id must still land.
	var customSkillCount int
	if err := target.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM skills WHERE id = 'sk_custom_e2e'`).Scan(&customSkillCount); err != nil {
		t.Fatalf("count custom skill: %v", err)
	}
	if customSkillCount != 1 {
		t.Errorf("custom skill did not land, got count=%d", customSkillCount)
	}
	var crewMemberCount int
	if err := target.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM crew_members WHERE crew_id = 'c_alpha' AND user_id = 'u_target_admin'`).Scan(&crewMemberCount); err != nil {
		t.Fatalf("count crew_members: %v", err)
	}
	if crewMemberCount != 1 {
		t.Errorf("crew_members row did not land with the reconciled user id, got count=%d", crewMemberCount)
	}
}
