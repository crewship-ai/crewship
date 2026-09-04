package missionactivity

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/testutil"
)

// seedMission puts one workspace/crew/agent/mission row in db, the minimum
// FK chain Emit's `SELECT workspace_id, crew_id FROM missions` needs to
// resolve. Real schema (testutil.MigratedSQLDB), not a hand-rolled fixture —
// this package's whole job is to be trusted with the real table's
// constraints (the CHECK on action, UNIQUE(mission_id, seq)).
func seedMission(t *testing.T, db *sql.DB, wsID, crewID, agentID, missionID string) {
	t.Helper()
	// OR IGNORE on the shared workspace/crew/agent rows: a test that seeds
	// two missions under the same (ws, crew, agent) — as
	// TestEmit_AllocatesPerMissionIndependently does — calls this twice.
	testutil.MustExec(t, db, `INSERT OR IGNORE INTO workspaces (id, name, slug) VALUES (?, 'WS', ?)`, wsID, wsID)
	testutil.MustExec(t, db, `INSERT OR IGNORE INTO crews (id, workspace_id, name, slug) VALUES (?, ?, 'Crew', ?)`, crewID, wsID, crewID)
	testutil.MustExec(t, db, `INSERT OR IGNORE INTO agents (id, workspace_id, crew_id, name, slug) VALUES (?, ?, ?, 'Agent', ?)`,
		agentID, wsID, crewID, agentID)
	testutil.MustExec(t, db, `
		INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status)
		VALUES (?, ?, ?, ?, ?, 'Mission', 'IN_PROGRESS')`,
		missionID, wsID, crewID, agentID, missionID+"-trace")
}

// TestEmit_SeqIsMonotonicUnderConcurrentWriters is the B1 accept-line proof
// for "seq is monotonic under concurrent writers" (§9.1,
// PRD-ISSUES-AND-ROUTINES-2026, #2332).
//
// Real goroutines, run with `-race`, all hammering Emit for the SAME
// mission_id at once — the exact scenario the UNIQUE(mission_id, seq) index
// and the _txlock=immediate locking Emit's doc comment describes exist for.
// A version of this function that read MAX(seq) outside a transaction (or
// with a DEFERRED one) would flake here under -race with a UNIQUE constraint
// violation; this test is red against that version and green against the
// BEGIN-IMMEDIATE one.
func TestEmit_SeqIsMonotonicUnderConcurrentWriters(t *testing.T) {
	db := testutil.MigratedSQLDB(t)
	const wsID, crewID, agentID, missionID = "ws1", "crew1", "agent1", "mission1"
	seedMission(t, db, wsID, crewID, agentID, missionID)

	const n = 20
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := Emit(context.Background(), db, Entry{
				ID:        fmt.Sprintf("act-%d", i),
				MissionID: missionID,
				ActorType: "agent",
				ActorID:   agentID,
				Action:    "status_changed",
				Details:   fmt.Sprintf("concurrent write %d", i),
			})
			errs[i] = err
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("Emit(%d): %v", i, err)
		}
	}

	// Every row got a seq, no two rows share one, and the set is exactly
	// {1..n} — not just "no duplicates" (a gap would also be a bug: it
	// means a write was silently lost, or seq skipped ahead of what the
	// UNIQUE(mission_id, seq) index would ever let two callers agree on).
	rows, err := db.Query(`SELECT seq FROM mission_activity WHERE mission_id = ? ORDER BY seq`, missionID)
	if err != nil {
		t.Fatalf("query seqs: %v", err)
	}
	defer rows.Close()
	seen := map[int]bool{}
	count := 0
	for rows.Next() {
		var seq sql.NullInt64
		if err := rows.Scan(&seq); err != nil {
			t.Fatalf("scan seq: %v", err)
		}
		if !seq.Valid {
			t.Fatalf("row with NULL seq — every Emit call must allocate one")
		}
		if seen[int(seq.Int64)] {
			t.Fatalf("seq %d allocated twice — UNIQUE(mission_id, seq) should have made this impossible", seq.Int64)
		}
		seen[int(seq.Int64)] = true
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if count != n {
		t.Fatalf("rows written = %d, want %d", count, n)
	}
	for i := 1; i <= n; i++ {
		if !seen[i] {
			t.Errorf("seq %d missing — allocation left a gap", i)
		}
	}
}

// TestEmit_AllocatesPerMissionIndependently proves seq is keyed on
// mission_id, not global: two missions written interleaved each get their
// own 1..n run, so one issue's history never steals numbers from another's.
func TestEmit_AllocatesPerMissionIndependently(t *testing.T) {
	db := testutil.MigratedSQLDB(t)
	const wsID, crewID, agentID = "ws1", "crew1", "agent1"
	seedMission(t, db, wsID, crewID, agentID, "missionA")
	seedMission(t, db, wsID, crewID, agentID, "missionB")

	for i := 0; i < 3; i++ {
		for _, mid := range []string{"missionA", "missionB"} {
			if _, err := Emit(context.Background(), db, Entry{
				ID:        fmt.Sprintf("act-%s-%d", mid, i),
				MissionID: mid,
				ActorType: "agent",
				ActorID:   agentID,
				Action:    "status_changed",
			}); err != nil {
				t.Fatalf("Emit(%s, %d): %v", mid, i, err)
			}
		}
	}

	for _, mid := range []string{"missionA", "missionB"} {
		var maxSeq int
		if err := db.QueryRow(`SELECT MAX(seq) FROM mission_activity WHERE mission_id = ?`, mid).Scan(&maxSeq); err != nil {
			t.Fatalf("max seq for %s: %v", mid, err)
		}
		if maxSeq != 3 {
			t.Errorf("%s: max seq = %d, want 3", mid, maxSeq)
		}
	}
}

// TestEmit_RejectsUnknownAction proves the CHECK constraint added in
// 20260904095700_mission_activity_widen.sql actually constrains — a typo'd
// action must fail loudly, not land silently in an "unroutable" row.
func TestEmit_RejectsUnknownAction(t *testing.T) {
	db := testutil.MigratedSQLDB(t)
	const wsID, crewID, agentID, missionID = "ws1", "crew1", "agent1", "mission1"
	seedMission(t, db, wsID, crewID, agentID, missionID)

	_, err := Emit(context.Background(), db, Entry{
		ID:        "act-1",
		MissionID: missionID,
		ActorType: "agent",
		ActorID:   agentID,
		Action:    "not_a_real_action",
	})
	if err == nil {
		t.Fatal("Emit with an unknown action must fail the CHECK constraint, got nil error")
	}
}

// TestEmit_ResolvesWorkspaceAndCrewFromMission proves the workspace/crew
// lookup Written carries actually comes back populated for a real mission —
// the value issueEvents.log depends on to decide whether to emit a journal
// entry at all.
func TestEmit_ResolvesWorkspaceAndCrewFromMission(t *testing.T) {
	db := testutil.MigratedSQLDB(t)
	const wsID, crewID, agentID, missionID = "ws1", "crew1", "agent1", "mission1"
	seedMission(t, db, wsID, crewID, agentID, missionID)

	written, err := Emit(context.Background(), db, Entry{
		ID:        "act-1",
		MissionID: missionID,
		ActorType: "agent",
		ActorID:   agentID,
		Action:    "status_changed",
	})
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if written.WorkspaceID != wsID {
		t.Errorf("WorkspaceID = %q, want %q", written.WorkspaceID, wsID)
	}
	if written.CrewID != crewID {
		t.Errorf("CrewID = %q, want %q", written.CrewID, crewID)
	}
	if written.Seq != 1 {
		t.Errorf("Seq = %d, want 1", written.Seq)
	}
}
