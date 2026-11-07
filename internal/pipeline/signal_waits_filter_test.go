package pipeline

import (
	"context"
	"testing"
	"time"
)

func armFiltered(t *testing.T, s *SQLSignalWaitStore, ws, run, event, filter, key string) {
	t.Helper()
	if _, err := s.ArmMatching(context.Background(), ws, run, "gate", event, filter, key, time.Hour); err != nil {
		t.Fatalf("arm %s: %v", run, err)
	}
}

// A non-matching signal must leave the wait pending and still reach a later
// matching wait; a matching one completes only the wait whose filter accepts it.
func TestSignalWaitStore_EventFilter(t *testing.T) {
	ctx := context.Background()
	db := openSignalWaitTestDB(t)
	s := NewSQLSignalWaitStore(db)
	armFiltered(t, s, "ws", "run_a", "ev", "kind == build", "")
	armFiltered(t, s, "ws", "run_b", "ev", "kind == deploy && env.name == prod", "")
	armFiltered(t, s, "ws", "run_c", "ev", "", "")

	// Run-keyed: wrong payload does not consume run_a's wait.
	armed, err := s.Deliver(ctx, "run_a", "ev", `{"kind":"deploy"}`)
	if err != nil || armed {
		t.Fatalf("non-matching Deliver armed=%v err=%v, want false/nil", armed, err)
	}
	if st, _ := waitStatus(t, db, "run_a"); st != "pending" {
		t.Fatalf("run_a status %q, want pending", st)
	}
	// Topic: only b (matches) and c (no filter) are claimed; a stays pending.
	runs, err := s.DeliverTopic(ctx, "ws", "ev", `{"kind":"deploy","env":{"name":"prod"}}`)
	if err != nil || len(runs) != 2 {
		t.Fatalf("DeliverTopic runs=%v err=%v, want b and c", runs, err)
	}
	if st, _ := waitStatus(t, db, "run_a"); st != "pending" {
		t.Fatalf("run_a status %q after non-matching topic, want pending", st)
	}
	// The later matching signal still reaches a.
	armed, err = s.Deliver(ctx, "run_a", "ev", `{"kind":"build"}`)
	if err != nil || !armed {
		t.Fatalf("matching Deliver armed=%v err=%v", armed, err)
	}
	// Non-JSON payload never satisfies a filter.
	armFiltered(t, s, "ws", "run_d", "ev", "kind == build", "")
	if armed, _ := s.Deliver(ctx, "run_d", "ev", "kind == build"); armed {
		t.Fatal("non-JSON payload satisfied a filter")
	}
}

func TestSignalWaitStore_DeliverCorrelated_Scope(t *testing.T) {
	ctx := context.Background()
	db := openSignalWaitTestDB(t)
	s := NewSQLSignalWaitStore(db)
	db.SetMaxOpenConns(1) // PRAGMA is per-connection
	if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatal(err)
	}
	for _, r := range [][3]string{{"r1", "ws", "pipe1"}, {"r2", "ws", "pipe2"}, {"r3", "other", "pipe1"}} {
		if _, err := db.Exec(`INSERT INTO pipeline_runs (id, workspace_id, pipeline_id, pipeline_slug, status, started_at) VALUES (?, ?, ?, 's', 'waiting', '2026-01-01T00:00:00Z')`, r[0], r[1], r[2]); err != nil {
			t.Fatal(err)
		}
		armFiltered(t, s, r[1], r[0], "ev", "", "order-7")
	}
	if got, _ := s.DeliverCorrelated(ctx, "ws", "pipe1", "ev", "order-8", "{}"); len(got) != 0 {
		t.Fatalf("wrong key claimed %v", got)
	}
	if got, _ := s.DeliverCorrelated(ctx, "ws", "pipe1", "ev", "", "{}"); len(got) != 0 {
		t.Fatalf("empty key claimed %v", got)
	}
	got, err := s.DeliverCorrelated(ctx, "ws", "pipe1", "ev", "order-7", `{"x":1}`)
	if err != nil || len(got) != 1 || got[0] != "r1" {
		t.Fatalf("claimed %v err=%v, want only r1 (other pipeline/workspace fenced)", got, err)
	}
	if got, _ := s.DeliverCorrelated(ctx, "ws", "pipe1", "ev", "order-7", "{}"); len(got) != 0 {
		t.Fatalf("second delivery re-claimed %v", got)
	}
}
