package presence

// Atomicity test for Upsert's read-modify-write (issue #1892, task F3).
//
// Upsert reads the prior status to decide whether the write is a transition
// worth journalling. Read and write must observe the same database state: if
// another writer lands in between, Upsert decides against a status that is
// already gone and emits an agent.status_change for a transition that never
// happened — the journal is supposed to be a transition log, not a log of
// what each racing goroutine happened to see.
//
// The race is made deterministic instead of hammered with goroutines: an
// external transaction holds SQLite's single writer lock, the Upsert under
// test is started while that lock is held, and the holder then changes the
// status to the value the Upsert is about to write before committing. A
// non-transactional Upsert has already read the stale "online" by then and
// emits a bogus online → busy entry; a transactional one does its read after
// it owns the writer lock and correctly emits nothing.

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// presenceAtomicityDB opens a file-backed database with the same pragmas
// production uses (internal/database/database.go): WAL so a reader is never
// blocked by the writer, a real busy_timeout, and _txlock=immediate so
// BeginTx issues BEGIN IMMEDIATE and takes the writer lock up front.
func presenceAtomicityDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := t.TempDir() + "/presence.db" +
		"?_pragma=busy_timeout(10000)" +
		"&_pragma=journal_mode(WAL)" +
		"&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(schemaSQL); err != nil {
		t.Fatalf("schema: %v", err)
	}
	return db
}

func TestUpsertReadAndWriteSeeTheSameState(t *testing.T) {
	tests := []struct {
		name string
		// what a concurrent writer commits while our Upsert is in flight
		concurrent Status
		// what our Upsert writes
		want Status
		// journal entries our Upsert must emit
		wantEmits int
	}{
		{
			name:       "concurrent writer already made the transition",
			concurrent: StatusBusy,
			want:       StatusBusy,
			wantEmits:  0,
		},
		{
			name:       "concurrent writer moved somewhere else",
			concurrent: StatusBlocked,
			want:       StatusBusy,
			wantEmits:  1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			db := presenceAtomicityDB(t)

			// Prior state both writers start from.
			if err := Upsert(ctx, db, nil, Snapshot{
				AgentID: "a1", WorkspaceID: "ws_test", CrewID: "crew_a", Status: StatusOnline,
			}); err != nil {
				t.Fatalf("seed: %v", err)
			}

			// Take the writer lock and keep it.
			holder, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatalf("begin holder tx: %v", err)
			}

			j := &recordingEmitter{}
			done := make(chan error, 1)
			go func() {
				done <- Upsert(ctx, db, j, Snapshot{
					AgentID: "a1", WorkspaceID: "ws_test", CrewID: "crew_a", Status: tc.want,
				})
			}()

			// Give the racing Upsert time to reach its read. With the read
			// inside the transaction it is still parked on BEGIN IMMEDIATE;
			// without it, it has already scanned the stale "online".
			time.Sleep(200 * time.Millisecond)

			if _, err := holder.ExecContext(ctx,
				`UPDATE agent_status SET status = ?, since = ? WHERE agent_id = 'a1'`,
				string(tc.concurrent), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
				t.Fatalf("holder write: %v", err)
			}
			if err := holder.Commit(); err != nil {
				t.Fatalf("holder commit: %v", err)
			}

			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("racing Upsert: %v", err)
				}
			case <-time.After(15 * time.Second):
				t.Fatal("racing Upsert never returned — writer lock not released?")
			}

			got, err := Get(ctx, db, "a1")
			if err != nil || got == nil {
				t.Fatalf("get: %v %+v", err, got)
			}
			if got.Status != tc.want {
				t.Errorf("final status = %q, want %q", got.Status, tc.want)
			}
			if len(j.entries) != tc.wantEmits {
				t.Fatalf("emitted %d journal entries, want %d — the prior status read must "+
					"see the same state the write lands on", len(j.entries), tc.wantEmits)
			}
			if tc.wantEmits == 1 && j.entries[0].Payload["prev"] != string(tc.concurrent) {
				t.Errorf("emitted prev = %v, want %q", j.entries[0].Payload["prev"], tc.concurrent)
			}
		})
	}
}
