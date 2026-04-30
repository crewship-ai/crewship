package cartographer

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/crewship-ai/crewship/internal/journal"
)

// schemaSQL is a minimal mirror of the production schema at migration
// 52 — enough tables and columns that cartographer can build, read,
// and fork. Kept in one string so the test doesn't pull in the whole
// migrate package.
const schemaSQL = `
CREATE TABLE workspaces (id TEXT PRIMARY KEY);
CREATE TABLE crews (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL);
CREATE TABLE agents (id TEXT PRIMARY KEY, crew_id TEXT);

CREATE TABLE missions (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    crew_id TEXT NOT NULL,
    lead_agent_id TEXT NOT NULL,
    trace_id TEXT NOT NULL UNIQUE,
    title TEXT NOT NULL,
    description TEXT,
    status TEXT NOT NULL DEFAULT 'PLANNING',
    plan TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE assignments (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    chat_id TEXT NOT NULL,
    assigned_by_id TEXT NOT NULL,
    assigned_to_id TEXT NOT NULL,
    task TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'PENDING'
);

CREATE TABLE mission_tasks (
    id TEXT PRIMARY KEY,
    mission_id TEXT NOT NULL REFERENCES missions(id) ON DELETE CASCADE,
    assigned_agent_id TEXT,
    title TEXT NOT NULL,
    description TEXT,
    status TEXT NOT NULL DEFAULT 'PENDING',
    task_order INTEGER NOT NULL DEFAULT 0,
    depends_on TEXT DEFAULT '[]',
    assignment_id TEXT
);

CREATE TABLE journal_entries (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    crew_id TEXT,
    agent_id TEXT,
    mission_id TEXT,
    ts TEXT NOT NULL,
    entry_type TEXT NOT NULL,
    severity TEXT NOT NULL DEFAULT 'info',
    priority TEXT NOT NULL DEFAULT 'normal',
    actor_type TEXT NOT NULL,
    actor_id TEXT,
    summary TEXT NOT NULL,
    payload TEXT NOT NULL DEFAULT '{}',
    refs TEXT NOT NULL DEFAULT '{}',
    trace_id TEXT,
    span_id TEXT,
    expires_at TEXT
);

CREATE TABLE checkpoints (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    crew_id TEXT,
    mission_id TEXT REFERENCES missions(id) ON DELETE CASCADE,
    label TEXT,
    journal_cursor TEXT NOT NULL,
    state_snapshot TEXT NOT NULL DEFAULT '{}',
    fork_of TEXT REFERENCES checkpoints(id) ON DELETE SET NULL,
    created_by TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
`

// openTestDB spins up an in-memory sqlite DB, applies the minimal schema,
// and seeds one workspace/crew/mission so the cartographer APIs have
// something to bind to. Individual tests layer their own fixtures on
// top (journal entries, tasks, assignments).
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// :memory: gives each connection a *fresh* database in modernc.org's
	// driver. Pin the pool to one connection so schema/data stay visible
	// across the journal writer goroutine, Fork's transactions, and the
	// test's own queries. Production uses a file-backed DB, so this is a
	// test-only workaround.
	db.SetMaxOpenConns(1)
	// Enable foreign key enforcement so ON DELETE SET NULL actually
	// fires — sqlite defaults it off per-connection.
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		t.Fatalf("pragma: %v", err)
	}
	if _, err := db.Exec(schemaSQL); err != nil {
		t.Fatalf("schema: %v", err)
	}
	seed := []string{
		`INSERT INTO workspaces (id) VALUES ('ws_test')`,
		`INSERT INTO workspaces (id) VALUES ('ws_other')`,
		`INSERT INTO crews (id, workspace_id) VALUES ('crew_a', 'ws_test')`,
		`INSERT INTO agents (id, crew_id) VALUES ('agent_lead', 'crew_a')`,
		`INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title)
			VALUES ('mis_1', 'ws_test', 'crew_a', 'agent_lead', 'tr_1', 'Demo mission')`,
	}
	for _, stmt := range seed {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}
	return db
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newJournal returns a flushed journal writer bound to db. Tests that
// want to inspect emitted entries should Flush before querying.
func newJournal(t *testing.T, db *sql.DB) *journal.Writer {
	t.Helper()
	return journal.NewWriter(db, quietLogger(), journal.WriterOptions{FlushSize: 1})
}

// emitJournalEntry inserts a journal row directly via SQL. Bypasses the
// batched writer so tests can control the exact (ts, id) ordering.
func emitJournalEntry(t *testing.T, db *sql.DB, id, missionID string, ts time.Time) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO journal_entries
		(id, workspace_id, mission_id, ts, entry_type, actor_type, summary)
		VALUES (?, 'ws_test', ?, ?, 'peer.conversation', 'agent', 's')`,
		id, missionID, ts.UTC().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatalf("emit entry %s: %v", id, err)
	}
}

// TestCreateRoundtrip asserts Create stores and Get reads back a
// checkpoint with every snapshot field intact — this is the core
// durability guarantee of the package.
func TestCreateRoundtrip(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	ctx := context.Background()
	state := StateSnapshot{
		AgentMemory: map[string]string{
			"agent_lead":  "sha256:abc",
			"agent_scout": "sha256:def",
		},
		PendingTasks:    []string{"mt_1", "mt_2"},
		OpenAssignments: []string{"as_1"},
		CrewContainerID: "crewship-team-demo",
		Meta:            map[string]any{"note": "first checkpoint", "score": float64(3)},
	}
	cp := Checkpoint{
		WorkspaceID:   "ws_test",
		CrewID:        "crew_a",
		MissionID:     "mis_1",
		Label:         "before migration",
		JournalCursor: "j_abc",
		State:         state,
		CreatedBy:     "user_owner",
	}

	id, err := Create(ctx, db, nil, cp)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if id == "" || id[:3] != "cp_" {
		t.Errorf("unexpected id: %q", id)
	}

	got, err := Get(ctx, db, "ws_test", id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil {
		t.Fatal("get: nil result")
	}
	if got.MissionID != "mis_1" || got.Label != "before migration" {
		t.Errorf("scalar fields: %+v", got)
	}
	if got.State.CrewContainerID != "crewship-team-demo" {
		t.Errorf("container id not preserved: %q", got.State.CrewContainerID)
	}
	if got.State.AgentMemory["agent_lead"] != "sha256:abc" {
		t.Errorf("memory map lost: %+v", got.State.AgentMemory)
	}
	if len(got.State.PendingTasks) != 2 || got.State.PendingTasks[1] != "mt_2" {
		t.Errorf("pending tasks lost: %+v", got.State.PendingTasks)
	}
	if got.State.OpenAssignments[0] != "as_1" {
		t.Errorf("open assignments lost: %+v", got.State.OpenAssignments)
	}
	if got.State.Meta["note"] != "first checkpoint" {
		t.Errorf("meta lost: %+v", got.State.Meta)
	}
}

// TestCreateEmitsJournalEntry proves the journal side-effect fires with
// the correct refs populated — the UI uses these refs to jump from a
// journal entry to its checkpoint row and back.
func TestCreateEmitsJournalEntry(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	j := newJournal(t, db)
	defer j.Close()

	ctx := context.Background()
	id, err := Create(ctx, db, j, Checkpoint{
		WorkspaceID:   "ws_test",
		CrewID:        "crew_a",
		MissionID:     "mis_1",
		Label:         "cp",
		JournalCursor: "j_seed",
		CreatedBy:     "user_x",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_ = j.Flush(ctx)
	time.Sleep(30 * time.Millisecond)

	entries, _, err := journal.List(ctx, db, journal.Query{
		WorkspaceID: "ws_test",
		Types:       []journal.EntryType{journal.EntryCheckpointCreated},
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 journal entry, got %d", len(entries))
	}
	refs := entries[0].Refs
	if refs["checkpoint_id"] != id {
		t.Errorf("ref checkpoint_id: got %v want %s", refs["checkpoint_id"], id)
	}
	if refs["mission_id"] != "mis_1" {
		t.Errorf("ref mission_id: got %v", refs["mission_id"])
	}
}

// TestListNewestFirst asserts List orders checkpoints by created_at DESC.
// Pausing between inserts is the simplest way to guarantee distinct
// timestamps — sqlite's datetime('now') only has second precision.
func TestListNewestFirst(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	ids := make([]string, 0, 3)
	for i := 0; i < 3; i++ {
		id, err := Create(ctx, db, nil, Checkpoint{
			WorkspaceID:   "ws_test",
			MissionID:     "mis_1",
			JournalCursor: "cursor",
			Label:         "cp",
		})
		if err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
		ids = append(ids, id)
		time.Sleep(15 * time.Millisecond)
	}

	list, err := List(ctx, db, "ws_test", "mis_1", 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("want 3, got %d", len(list))
	}
	// Newest first = ids[2], ids[1], ids[0].
	if list[0].ID != ids[2] || list[2].ID != ids[0] {
		t.Errorf("order wrong: got %v want [%s %s %s]",
			[]string{list[0].ID, list[1].ID, list[2].ID}, ids[2], ids[1], ids[0])
	}
}

// TestRestoreDivergence proves Restore reports newer journal entries
// as divergence without touching anything. We emit an entry, snapshot,
// then emit two more entries and check they're flagged.
func TestRestoreDivergence(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	base := time.Now().UTC()
	emitJournalEntry(t, db, "j_1", "mis_1", base)
	emitJournalEntry(t, db, "j_2", "mis_1", base.Add(time.Second))

	id, err := Create(ctx, db, nil, Checkpoint{
		WorkspaceID:   "ws_test",
		CrewID:        "crew_a",
		MissionID:     "mis_1",
		JournalCursor: "j_2",
		Label:         "stable",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Two more entries happen AFTER the checkpoint.
	emitJournalEntry(t, db, "j_3", "mis_1", base.Add(2*time.Second))
	emitJournalEntry(t, db, "j_4", "mis_1", base.Add(3*time.Second))

	res, err := Restore(ctx, db, nil, "ws_test", id)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if res.Checkpoint == nil || res.Checkpoint.ID != id {
		t.Fatal("restore returned wrong checkpoint")
	}
	if len(res.WarnDivergence) != 2 {
		t.Fatalf("divergence: got %d want 2 (%v)", len(res.WarnDivergence), res.WarnDivergence)
	}
	if res.WarnDivergence[0] != "j_3" || res.WarnDivergence[1] != "j_4" {
		t.Errorf("divergence order: %v", res.WarnDivergence)
	}

	// Restore must not mutate the DB. Re-running yields the same result.
	res2, err := Restore(ctx, db, nil, "ws_test", id)
	if err != nil {
		t.Fatalf("restore 2: %v", err)
	}
	if len(res2.WarnDivergence) != 2 {
		t.Errorf("restore mutated state: now %d", len(res2.WarnDivergence))
	}
}

// TestRestoreMissingCursor returns an error when the cursor points at a
// journal entry that's been purged. Defensive check only — production
// never purges — but a corrupt DB should fail loudly.
func TestRestoreMissingCursor(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	id, err := Create(ctx, db, nil, Checkpoint{
		WorkspaceID:   "ws_test",
		MissionID:     "mis_1",
		JournalCursor: "j_missing",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	_, err = Restore(ctx, db, nil, "ws_test", id)
	if err == nil {
		t.Fatal("expected error for missing cursor")
	}
}

// TestForkRemapsTaskDependencies verifies that depends_on references on
// copied tasks are rewritten to point at the FORK's task IDs rather than
// the parent's. Without remapping, blocked tasks on the fork wait
// forever for parent task IDs that don't exist there.
func TestForkRemapsTaskDependencies(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	// Parent: mt_p1 (root) and mt_p2 which depends on mt_p1.
	if _, err := db.Exec(`INSERT INTO mission_tasks
		(id, mission_id, title, status, task_order, depends_on)
		VALUES ('mt_p1', 'mis_1', 'Plan', 'COMPLETED', 0, '[]')`); err != nil {
		t.Fatalf("seed mt_p1: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO mission_tasks
		(id, mission_id, title, status, task_order, depends_on)
		VALUES ('mt_p2', 'mis_1', 'Execute', 'PENDING', 1, '["mt_p1"]')`); err != nil {
		t.Fatalf("seed mt_p2: %v", err)
	}
	emitJournalEntry(t, db, "j_seed", "mis_1", time.Now().UTC())

	srcID, err := Create(ctx, db, nil, Checkpoint{
		WorkspaceID:   "ws_test",
		CrewID:        "crew_a",
		MissionID:     "mis_1",
		JournalCursor: "j_seed",
		Label:         "pre-fork",
		CreatedBy:     "user_x",
	})
	if err != nil {
		t.Fatalf("create src: %v", err)
	}

	newMission, _, err := Fork(ctx, db, nil, "ws_test", srcID, "branch", "user_x")
	if err != nil {
		t.Fatalf("fork: %v", err)
	}

	// Find the two fork tasks by title and read their depends_on.
	type forkTask struct {
		id, title, dependsOn string
	}
	rows, err := db.Query(`SELECT id, title, depends_on FROM mission_tasks WHERE mission_id = ? ORDER BY task_order`, newMission)
	if err != nil {
		t.Fatalf("query fork tasks: %v", err)
	}
	defer rows.Close()
	var forkTasks []forkTask
	for rows.Next() {
		var ft forkTask
		if err := rows.Scan(&ft.id, &ft.title, &ft.dependsOn); err != nil {
			t.Fatalf("scan: %v", err)
		}
		forkTasks = append(forkTasks, ft)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate fork tasks: %v", err)
	}
	if len(forkTasks) != 2 {
		t.Fatalf("want 2 fork tasks, got %d", len(forkTasks))
	}

	planForkID := forkTasks[0].id // fork-side mt_p1 (Plan)
	execForkID := forkTasks[1].id // fork-side mt_p2 (Execute)
	execDeps := forkTasks[1].dependsOn

	// Sanity: the fork's task IDs are NEW (not the parent's).
	if planForkID == "mt_p1" || execForkID == "mt_p2" {
		t.Fatalf("fork tasks reused parent ids: %+v", forkTasks)
	}

	// Bug repro: the Execute task's depends_on must reference the fork's
	// own Plan task ID — not the parent's "mt_p1". Stale references mean
	// blocked-task gating on the fork waits for an ID that doesn't exist
	// on this mission.
	var got []string
	if err := json.Unmarshal([]byte(execDeps), &got); err != nil {
		t.Fatalf("unmarshal depends_on %q: %v", execDeps, err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 dependency, got %v", got)
	}
	if got[0] == "mt_p1" {
		t.Errorf("fork retained parent task id in depends_on: %v (must remap to fork's %q)", got, planForkID)
	}
	if got[0] != planForkID {
		t.Errorf("depends_on must point at fork's plan task %q, got %q", planForkID, got[0])
	}
}

// TestForkCreatesMissionAndCheckpoint verifies the happy-path fork: a
// new mission row, copied tasks, a fork_of-linked checkpoint, and a
// journal entry witnessing the fork.
func TestForkCreatesMissionAndCheckpoint(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	// Parent mission has two tasks.
	for i, spec := range []struct{ id, title, status string }{
		{"mt_p1", "Plan", "COMPLETED"},
		{"mt_p2", "Execute", "PENDING"},
	} {
		_, err := db.Exec(`INSERT INTO mission_tasks (id, mission_id, title, status, task_order)
			VALUES (?, 'mis_1', ?, ?, ?)`,
			spec.id, spec.title, spec.status, i)
		if err != nil {
			t.Fatalf("seed task %s: %v", spec.id, err)
		}
	}
	emitJournalEntry(t, db, "j_seed", "mis_1", time.Now().UTC())

	srcID, err := Create(ctx, db, nil, Checkpoint{
		WorkspaceID:   "ws_test",
		CrewID:        "crew_a",
		MissionID:     "mis_1",
		JournalCursor: "j_seed",
		Label:         "pre-fork",
		CreatedBy:     "user_x",
	})
	if err != nil {
		t.Fatalf("create src: %v", err)
	}

	j := newJournal(t, db)
	defer j.Close()

	newMission, newCP, err := Fork(ctx, db, j, "ws_test", srcID, "experimental branch", "user_x")
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	if newMission == "" || newCP == "" {
		t.Fatalf("empty ids: mission=%q cp=%q", newMission, newCP)
	}

	// The new mission exists with a prefixed title and PLANNING status.
	var title, status string
	err = db.QueryRow(`SELECT title, status FROM missions WHERE id = ?`, newMission).Scan(&title, &status)
	if err != nil {
		t.Fatalf("query new mission: %v", err)
	}
	if title != "Fork: Demo mission" {
		t.Errorf("new title: %q", title)
	}
	if status != "PLANNING" {
		t.Errorf("new status: %q", status)
	}

	// Tasks copied — 2 rows on the new mission.
	var taskCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM mission_tasks WHERE mission_id = ?`, newMission).Scan(&taskCount); err != nil {
		t.Fatalf("count tasks: %v", err)
	}
	if taskCount != 2 {
		t.Errorf("want 2 copied tasks, got %d", taskCount)
	}

	// New checkpoint has fork_of = src and mission_id = new mission.
	cp, err := Get(ctx, db, "ws_test", newCP)
	if err != nil {
		t.Fatalf("get new cp: %v", err)
	}
	if cp.ForkOf != srcID {
		t.Errorf("fork_of: got %q want %q", cp.ForkOf, srcID)
	}
	if cp.MissionID != newMission {
		t.Errorf("mission_id on fork cp: got %q want %q", cp.MissionID, newMission)
	}
	if cp.Label != "experimental branch" {
		t.Errorf("label: %q", cp.Label)
	}

	// Journal entry witnessed.
	_ = j.Flush(ctx)
	time.Sleep(30 * time.Millisecond)
	entries, _, err := journal.List(ctx, db, journal.Query{
		WorkspaceID: "ws_test",
		Types:       []journal.EntryType{journal.EntryForkCreated},
	})
	if err != nil {
		t.Fatalf("list journal: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 fork entry, got %d", len(entries))
	}
	if entries[0].Refs["new_mission"] != newMission {
		t.Errorf("refs new_mission: %v", entries[0].Refs["new_mission"])
	}
}

// TestDeleteOrphansForks proves the migration's ON DELETE SET NULL
// behaviour: deleting the parent checkpoint drops fork_of to NULL on
// its children rather than removing them. The forked mission row and
// its checkpoint must survive deletion of the origin.
func TestDeleteOrphansForks(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	emitJournalEntry(t, db, "j_seed", "mis_1", time.Now().UTC())
	srcID, err := Create(ctx, db, nil, Checkpoint{
		WorkspaceID:   "ws_test",
		CrewID:        "crew_a",
		MissionID:     "mis_1",
		JournalCursor: "j_seed",
		Label:         "root",
	})
	if err != nil {
		t.Fatalf("create src: %v", err)
	}

	_, forkCP, err := Fork(ctx, db, nil, "ws_test", srcID, "branch", "user_x")
	if err != nil {
		t.Fatalf("fork: %v", err)
	}

	if err := Delete(ctx, db, "ws_test", srcID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	// Src gone.
	if _, err := Get(ctx, db, "ws_test", srcID); err == nil {
		t.Errorf("expected src to be gone")
	}

	// Fork checkpoint still exists but its fork_of is NULL.
	cp, err := Get(ctx, db, "ws_test", forkCP)
	if err != nil {
		t.Fatalf("fork cp lookup: %v", err)
	}
	if cp.ForkOf != "" {
		t.Errorf("fork_of should be cleared, got %q", cp.ForkOf)
	}
}

// TestWorkspaceIsolation proves Get refuses to return a checkpoint
// belonging to a different workspace — the core multi-tenancy
// guarantee.
func TestWorkspaceIsolation(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	id, err := Create(ctx, db, nil, Checkpoint{
		WorkspaceID:   "ws_test",
		MissionID:     "mis_1",
		JournalCursor: "j_seed",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := Get(ctx, db, "ws_other", id)
	if err == nil {
		t.Errorf("expected error for cross-workspace Get, got cp=%+v", got)
	}
}

// TestCaptureBuildsSnapshot runs Capture end-to-end on a mission with
// pending tasks, a running assignment, and one journal entry. Every
// field in StateSnapshot gets exercised.
func TestCaptureBuildsSnapshot(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	// A completed task (excluded) and a pending one (included).
	_, err := db.Exec(`INSERT INTO mission_tasks (id, mission_id, title, status) VALUES
		('mt_done', 'mis_1', 'Done', 'COMPLETED'),
		('mt_open', 'mis_1', 'Open', 'IN_PROGRESS')`)
	if err != nil {
		t.Fatalf("seed tasks: %v", err)
	}

	// Two assignments: one RUNNING attached to mt_open, one COMPLETED attached to mt_done.
	_, err = db.Exec(`INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status) VALUES
		('as_run', 'ws_test', 'ch_1', 'agent_lead', 'agent_lead', 't', 'RUNNING'),
		('as_done', 'ws_test', 'ch_1', 'agent_lead', 'agent_lead', 't', 'COMPLETED')`)
	if err != nil {
		t.Fatalf("seed assignments: %v", err)
	}
	if _, err = db.Exec(`UPDATE mission_tasks SET assignment_id = 'as_run' WHERE id = 'mt_open'`); err != nil {
		t.Fatalf("wire task: %v", err)
	}
	if _, err = db.Exec(`UPDATE mission_tasks SET assignment_id = 'as_done' WHERE id = 'mt_done'`); err != nil {
		t.Fatalf("wire task: %v", err)
	}

	emitJournalEntry(t, db, "j_1", "mis_1", time.Now().UTC())

	snap, cursor, err := Capture(ctx, db, "mis_1")
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if cursor != "j_1" {
		t.Errorf("cursor: got %q want j_1", cursor)
	}
	if len(snap.PendingTasks) != 1 || snap.PendingTasks[0] != "mt_open" {
		t.Errorf("pending: %v", snap.PendingTasks)
	}
	if len(snap.OpenAssignments) != 1 || snap.OpenAssignments[0] != "as_run" {
		t.Errorf("open assignments: %v", snap.OpenAssignments)
	}
}

// TestCaptureMemoryDirHashesContents proves two identical directories
// produce the same digest and that mutating a file changes it. We use
// t.TempDir() so each run is isolated on disk.
func TestCaptureMemoryDirHashesContents(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.md"), []byte("world"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	snap := &StateSnapshot{}
	if err := CaptureMemoryDir(snap, "agent_lead", dir); err != nil {
		t.Fatalf("capture: %v", err)
	}
	h1 := snap.AgentMemory["agent_lead"]
	if h1 == "" {
		t.Fatal("empty digest for non-empty dir")
	}

	// Same content → same hash.
	snap2 := &StateSnapshot{}
	if err := CaptureMemoryDir(snap2, "agent_lead", dir); err != nil {
		t.Fatalf("capture 2: %v", err)
	}
	if snap2.AgentMemory["agent_lead"] != h1 {
		t.Errorf("deterministic hash broken: %q vs %q", h1, snap2.AgentMemory["agent_lead"])
	}

	// Mutate → hash changes.
	if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte("HELLO"), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	snap3 := &StateSnapshot{}
	if err := CaptureMemoryDir(snap3, "agent_lead", dir); err != nil {
		t.Fatalf("capture 3: %v", err)
	}
	if snap3.AgentMemory["agent_lead"] == h1 {
		t.Errorf("hash should have changed after edit")
	}

	// Missing directory → empty digest, no error.
	snap4 := &StateSnapshot{}
	if err := CaptureMemoryDir(snap4, "agent_lead", filepath.Join(dir, "nope")); err != nil {
		t.Fatalf("missing dir should not error: %v", err)
	}
	if snap4.AgentMemory["agent_lead"] != "" {
		t.Errorf("missing dir should hash to empty, got %q", snap4.AgentMemory["agent_lead"])
	}
}
