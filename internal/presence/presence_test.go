package presence

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	_ "modernc.org/sqlite"
)

const schemaSQL = `
CREATE TABLE workspaces (id TEXT PRIMARY KEY);
INSERT INTO workspaces (id) VALUES ('ws_test');

CREATE TABLE agent_status (
    agent_id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    crew_id TEXT,
    status TEXT NOT NULL DEFAULT 'offline',
    since TEXT NOT NULL,
    details TEXT NOT NULL DEFAULT '{}'
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
`

// recordingEmitter collects every Emit call for assertions. Cheaper to
// hand-roll than reach for a mocking library.
type recordingEmitter struct {
	entries []journal.Entry
}

func (r *recordingEmitter) Emit(_ context.Context, e journal.Entry) (string, error) {
	if e.ID == "" {
		e.ID = "test_"
	}
	r.entries = append(r.entries, e)
	return e.ID, nil
}

func (r *recordingEmitter) Flush(_ context.Context) error { return nil }

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(schemaSQL); err != nil {
		t.Fatal(err)
	}
	return db
}

// TestUpsertNilEmitterDoesNotPanic verifies callers that don't care about
// the journal can pass a nil Emitter without crashing the process. The
// previous code dereferenced j.Emit unconditionally on every status
// transition, mirroring the nil-tolerant contract paymaster.Enforce
// already follows.
func TestUpsertNilEmitterDoesNotPanic(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	// First Upsert is always a transition (no prior row), which forces
	// the j.Emit code path. Without the nil-check this panics.
	err := Upsert(context.Background(), db, nil, Snapshot{
		AgentID:     "agent_1",
		WorkspaceID: "ws_test",
		Status:      StatusOnline,
	})
	if err != nil {
		t.Fatalf("upsert with nil emitter: %v", err)
	}
}

func TestUpsertEmitsOnTransitionOnly(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	j := &recordingEmitter{}

	ctx := context.Background()
	// First write — must emit (new presence row).
	if err := Upsert(ctx, db, j, Snapshot{
		AgentID: "a1", WorkspaceID: "ws_test", CrewID: "crew_a", Status: StatusOnline,
	}); err != nil {
		t.Fatal(err)
	}
	if len(j.entries) != 1 {
		t.Fatalf("expected 1 emit on initial Upsert, got %d", len(j.entries))
	}

	// Same status — must NOT emit (idempotent heartbeat).
	if err := Upsert(ctx, db, j, Snapshot{
		AgentID: "a1", WorkspaceID: "ws_test", CrewID: "crew_a", Status: StatusOnline,
	}); err != nil {
		t.Fatal(err)
	}
	if len(j.entries) != 1 {
		t.Errorf("same-status Upsert should not emit, got %d total", len(j.entries))
	}

	// Transition to busy — must emit.
	if err := Upsert(ctx, db, j, Snapshot{
		AgentID: "a1", WorkspaceID: "ws_test", CrewID: "crew_a", Status: StatusBusy,
	}); err != nil {
		t.Fatal(err)
	}
	if len(j.entries) != 2 {
		t.Fatalf("expected 2 emits after transition, got %d", len(j.entries))
	}
	if j.entries[1].Type != journal.EntryAgentStatus {
		t.Errorf("expected EntryAgentStatus, got %s", j.entries[1].Type)
	}
}

func TestValidateStatus(t *testing.T) {
	tests := map[Status]bool{
		StatusOnline:   true,
		StatusBusy:     true,
		StatusBlocked:  true,
		StatusOffline:  true,
		Status("typo"): false,
		Status(""):     false,
	}
	for s, wantOK := range tests {
		err := s.Validate()
		if (err == nil) != wantOK {
			t.Errorf("status %q: want ok=%v got err=%v", s, wantOK, err)
		}
	}
}

func TestListByCrew(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	j := &recordingEmitter{}
	ctx := context.Background()

	for _, s := range []Snapshot{
		{AgentID: "a1", WorkspaceID: "ws_test", CrewID: "crew_a", Status: StatusOnline},
		{AgentID: "a2", WorkspaceID: "ws_test", CrewID: "crew_a", Status: StatusBusy},
		{AgentID: "a3", WorkspaceID: "ws_test", CrewID: "crew_b", Status: StatusOnline},
	} {
		_ = Upsert(ctx, db, j, s)
	}

	got, err := ListByCrew(ctx, db, "crew_a")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("crew_a should have 2 agents, got %d", len(got))
	}
	for _, s := range got {
		if s.CrewID != "crew_a" {
			t.Errorf("leaked crew: %+v", s)
		}
	}
}

func TestSweepOffline(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	j := &recordingEmitter{}
	ctx := context.Background()

	// Insert one agent with a stale since timestamp (10 min ago) directly
	// — Upsert would write `now`, defeating the test.
	stale := time.Now().UTC().Add(-10 * time.Minute).Format(time.RFC3339Nano)
	_, err := db.Exec(`INSERT INTO agent_status (agent_id, workspace_id, crew_id, status, since, details)
		VALUES ('a_stale', 'ws_test', 'crew_a', 'online', ?, '{}')`, stale)
	if err != nil {
		t.Fatal(err)
	}
	// One fresh agent that must stay online.
	_ = Upsert(ctx, db, j, Snapshot{AgentID: "a_fresh", WorkspaceID: "ws_test", CrewID: "crew_a", Status: StatusOnline})

	if err := SweepOffline(ctx, db, j, 5*time.Minute); err != nil {
		t.Fatal(err)
	}
	after, _ := Get(ctx, db, "a_stale")
	if after == nil || after.Status != StatusOffline {
		t.Errorf("stale agent should be offline, got %+v", after)
	}
	fresh, _ := Get(ctx, db, "a_fresh")
	if fresh == nil || fresh.Status != StatusOnline {
		t.Errorf("fresh agent should stay online, got %+v", fresh)
	}
}
