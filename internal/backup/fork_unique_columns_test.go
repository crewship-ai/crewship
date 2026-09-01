package backup_test

// Does a forked restore (`--as-workspace` / `--as-crew`) survive a bundle that
// carries a live mission?
//
// RemapIDs regenerates every primary key so the fork can land beside the source
// on the SAME instance. That is only half the job: a column can be UNIQUE
// without being the row's PK, and `missions.trace_id TEXT NOT NULL UNIQUE`
// (migrate_consts_v02_v15.go) is exactly that. The source row still holds the
// bundle's trace_id, so the forked mission's INSERT OR IGNORE collides on the
// UNIQUE index and is silently dropped — while `mission_activity`, whose
// mission_id pass 2 DID rewrite to the fork's new id, lands and dangles. The
// deferred foreign_key_check then blames mission_activity for a parent that
// never arrived (#2260).
//
// Same shape as #1190 (workspace slug) and #2226 (journal chain): a fork has to
// regenerate identity, not just the primary key.

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
)

// seedLiveMission plants one live, non-deleted mission in workspaceID with a
// UNIQUE trace_id and a workspace-scoped identifier, plus one mission_activity
// row hanging off it by a DECLARED foreign key. This is the minimum shape the
// #2260 report reproduces on.
func seedLiveMission(t *testing.T, db *sql.DB, workspaceID string) (missionID, traceID, activityID string) {
	t.Helper()
	ctx := context.Background()

	missionID = "mis_fork_2260"
	traceID = "mission-c2260fixture0001"
	activityID = "mact_fork_2260"

	if _, err := db.ExecContext(ctx, `INSERT INTO missions
		(id, workspace_id, crew_id, lead_agent_id, trace_id, title, description, status, identifier)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'PLANNING', ?)`,
		missionID, workspaceID, "c_alpha", "a_alice", traceID,
		"Fork me", "A live mission", "ALP-1"); err != nil {
		t.Fatalf("seed mission: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO mission_activity
		(id, mission_id, actor_type, actor_id, action, details)
		VALUES (?, ?, 'user', ?, 'created', ?)`,
		activityID, missionID, "u_admin", `{}`); err != nil {
		t.Fatalf("seed mission_activity: %v", err)
	}
	return missionID, traceID, activityID
}

// assertNoFKViolations fails the test with every orphan row the target holds.
// Post-restore the database must be clean, not merely "clean enough that
// restore did not notice".
func assertNoFKViolations(t *testing.T, db *sql.DB, what string) {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatalf("%s: foreign_key_check: %v", what, err)
	}
	defer func() { _ = rows.Close() }()
	var seen []string
	for rows.Next() {
		var child, parent sql.NullString
		var rowID, fkID sql.NullInt64
		if err := rows.Scan(&child, &rowID, &parent, &fkID); err != nil {
			t.Fatalf("%s: scan foreign_key_check: %v", what, err)
		}
		seen = append(seen, fmt.Sprintf("%s.rowid=%d → %s", child.String, rowID.Int64, parent.String))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("%s: foreign_key_check iter: %v", what, err)
	}
	if len(seen) > 0 {
		t.Errorf("%s: database holds %d FK violation(s) after restore: %v", what, len(seen), seen)
	}
}

// TestForkedRestore_MissionWithActivity is the #2260 reproduction: fork a
// bundle that carries one live mission and its activity row back into the same
// instance.
func TestForkedRestore_MissionWithActivity(t *testing.T) {
	ctx := context.Background()

	source := openMigratedDB(t)
	workspaceID := seedWorkspace(t, source)
	srcMissionID, srcTraceID, srcActivityID := seedLiveMission(t, source, workspaceID)

	const passphrase = "fork-mission-trace-pass-123"
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

	// Fork beside the original, on the SAME instance — the documented
	// --as-workspace use case, and the one that makes a non-PK UNIQUE
	// column collide.
	res, err := backup.RestoreBackup(ctx, source, backup.RestoreOptions{
		Path:        created.Path,
		Passphrase:  passphrase,
		Actor:       actor,
		AsWorkspace: "e2e-ws-mission-fork",
	})
	if err != nil {
		t.Fatalf("RestoreBackup --as-workspace: %v", err)
	}
	if res.RestoredWorkspaceID == "" || res.RestoredWorkspaceID == workspaceID {
		t.Fatalf("--as-workspace did not fork the workspace (got %q, source %q)",
			res.RestoredWorkspaceID, workspaceID)
	}

	// The forked mission must exist, under a NEW id, in the NEW workspace.
	var forkMissionID, forkTraceID string
	if err := source.QueryRowContext(ctx,
		`SELECT id, trace_id FROM missions WHERE workspace_id = ?`,
		res.RestoredWorkspaceID).Scan(&forkMissionID, &forkTraceID); err != nil {
		t.Fatalf("forked workspace has no missions row (the INSERT OR IGNORE swallowed it): %v", err)
	}
	if forkMissionID == srcMissionID {
		t.Errorf("forked mission kept the source id %q — pass 1 did not remap it", srcMissionID)
	}
	if forkTraceID == srcTraceID {
		t.Errorf("forked mission kept the source trace_id %q — a UNIQUE, instance-wide column "+
			"cannot survive a fork verbatim", srcTraceID)
	}

	// Its activity row must have followed it, under a new id.
	var activityCount int
	if err := source.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM mission_activity WHERE mission_id = ?`, forkMissionID).Scan(&activityCount); err != nil {
		t.Fatalf("count forked mission_activity: %v", err)
	}
	if activityCount != 1 {
		t.Errorf("forked mission has %d mission_activity rows, want 1", activityCount)
	}

	// The source must be untouched: same mission, same trace, same activity.
	var srcStillThere int
	if err := source.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM missions WHERE id = ? AND workspace_id = ? AND trace_id = ?`,
		srcMissionID, workspaceID, srcTraceID).Scan(&srcStillThere); err != nil {
		t.Fatalf("count source mission: %v", err)
	}
	if srcStillThere != 1 {
		t.Errorf("source mission %s no longer intact after the fork (count=%d)", srcMissionID, srcStillThere)
	}
	var srcActivityStillThere int
	if err := source.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM mission_activity WHERE id = ? AND mission_id = ?`,
		srcActivityID, srcMissionID).Scan(&srcActivityStillThere); err != nil {
		t.Fatalf("count source mission_activity: %v", err)
	}
	if srcActivityStillThere != 1 {
		t.Errorf("source mission_activity %s no longer intact after the fork (count=%d)",
			srcActivityID, srcActivityStillThere)
	}

	assertNoFKViolations(t, source, "after --as-workspace fork")
}
