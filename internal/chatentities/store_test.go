package chatentities

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	_ "modernc.org/sqlite"
)

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "store.db")+"?_pragma=busy_timeout(10000)&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if _, err = db.Exec(`CREATE TABLE workspaces(id TEXT PRIMARY KEY); INSERT INTO workspaces VALUES('ws'),('other-ws'); CREATE TABLE chats(id TEXT PRIMARY KEY,workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE);INSERT INTO chats VALUES('chat','ws'),('other','other-ws');CREATE TABLE mutations(id TEXT PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	applyMigration(t, db, "20261013174955_chat_entity_operations.sql")
	return db
}
func applyMigration(t *testing.T, db *sql.DB, name string) {
	t.Helper()
	b, e := os.ReadFile(filepath.Join("..", "database", "migrations", name))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec(string(b)); e != nil {
		t.Fatal(e)
	}
}
func spec(id string) OperationSpec {
	return OperationSpec{ID: id, WorkspaceID: "ws", Key: "key", RequestHash: "hash", ActorType: "agent", ActorID: "agent", RunID: "run", Provenance: "verified", Kind: "issue", Verb: "created"}
}
func finish(t *testing.T, tx *sql.Tx, id string) {
	t.Helper()
	if e := CompleteOperation(context.Background(), tx, id, ResultSpec{EntityID: "issue", Revision: 1, AggregateRevision: 1, Result: []byte(`{"id":"issue"}`)}); e != nil {
		t.Fatal(e)
	}
}
func TestOperationRollbackAndReplay(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	tx, _ := db.BeginTx(ctx, nil)
	o, replay, e := BeginOperation(ctx, tx, spec("op"))
	if e != nil || replay {
		t.Fatalf("begin %v %v", replay, e)
	}
	tx.Exec(`INSERT INTO mutations VALUES('mutation')`)
	finish(t, tx, o.ID)
	_, e = AppendRef(ctx, tx, RefSpec{EventID: "event", ChatID: "chat", OperationID: o.ID, RunID: "run", TriggerMessageID: "msg"})
	if e != nil {
		t.Fatal(e)
	}
	tx.Rollback()
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM mutations`).Scan(&n)
	if n != 0 {
		t.Fatal("mutation survived rollback")
	}
	db.QueryRow(`SELECT COUNT(*) FROM agent_operations`).Scan(&n)
	if n != 0 {
		t.Fatal("operation survived rollback")
	}
	db.QueryRow(`SELECT COUNT(*) FROM chat_entity_refs`).Scan(&n)
	if n != 0 {
		t.Fatal("ref survived rollback")
	}
	tx, _ = db.BeginTx(ctx, nil)
	o, _, e = BeginOperation(ctx, tx, spec("op"))
	if e != nil {
		t.Fatal(e)
	}
	finish(t, tx, o.ID)
	r, e := AppendRef(ctx, tx, RefSpec{EventID: "event", ChatID: "chat", OperationID: o.ID, RunID: "run", TriggerMessageID: "msg"})
	if e != nil {
		t.Fatal(e)
	}
	r2, e := AppendRef(ctx, tx, RefSpec{EventID: "duplicate", ChatID: "chat", OperationID: o.ID, RunID: "run", TriggerMessageID: "msg"})
	if e != nil || r.EventID != r2.EventID || r.Seq != r2.Seq {
		t.Fatalf("duplicate %+v %v", r2, e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	tx, _ = db.BeginTx(ctx, nil)
	defer tx.Rollback()
	s := spec("second")
	o, replay, e = BeginOperation(ctx, tx, s)
	if e != nil || !replay || o.ID != "op" {
		t.Fatalf("replay %+v %v %v", o, replay, e)
	}
	s.RequestHash = "other"
	if _, _, e = BeginOperation(ctx, tx, s); !errors.Is(e, ErrKeyConflict) {
		t.Fatal(e)
	}
	s = spec("second")
	s.RunID = "different-run"
	if _, _, e = BeginOperation(ctx, tx, s); !errors.Is(e, ErrKeyConflict) {
		t.Fatal(e)
	}
}
func TestScopeAndReferenceFences(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	tx, _ := db.BeginTx(ctx, nil)
	defer tx.Rollback()
	s := spec("first")
	o, _, e := BeginOperation(ctx, tx, s)
	if e != nil {
		t.Fatal(e)
	}
	finish(t, tx, o.ID)
	s.ID = "second"
	s.ActorID = "other-agent"
	if _, r, e := BeginOperation(ctx, tx, s); e != nil || r {
		t.Fatalf("actor scope %v %v", r, e)
	}
	for _, r := range []RefSpec{{"event", "other", "first", "run", "msg"}, {"event", "chat", "first", "other-run", "msg"}, {"event", "chat", "second", "run", "msg"}} {
		if _, e := AppendRef(ctx, tx, r); !errors.Is(e, ErrInvalid) {
			t.Fatalf("ref fence %v", e)
		}
	}
	if e := CompleteOperation(ctx, tx, "first", ResultSpec{EntityID: "overwrite"}); !errors.Is(e, ErrOperationIncomplete) {
		t.Fatalf("overwrite %v", e)
	}
}
func TestConcurrentOperationDeduplicates(t *testing.T) {
	db := testDB(t)
	db.SetMaxOpenConns(8)
	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tx, e := db.BeginTx(ctx, nil)
			if e != nil {
				errs <- e
				return
			}
			defer tx.Rollback()
			o, replay, e := BeginOperation(ctx, tx, spec(fmt.Sprint("op", i)))
			if e != nil {
				errs <- e
				return
			}
			if !replay {
				if _, e = tx.Exec(`INSERT INTO mutations VALUES('once')`); e != nil {
					errs <- e
					return
				}
				if e = CompleteOperation(ctx, tx, o.ID, ResultSpec{EntityID: "issue"}); e != nil {
					errs <- e
					return
				}
			}
			errs <- tx.Commit()
		}(i)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM mutations`).Scan(&n)
	if n != 1 {
		t.Fatalf("mutations %d", n)
	}
}
func TestBoundedScan(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	tx, _ := db.BeginTx(ctx, nil)
	for i := 0; i < 3; i++ {
		s := spec(fmt.Sprint("op", i))
		s.Key = s.ID
		o, _, e := BeginOperation(ctx, tx, s)
		if e != nil {
			t.Fatal(e)
		}
		finish(t, tx, o.ID)
		if _, e = AppendRef(ctx, tx, RefSpec{fmt.Sprint("event", i), "chat", o.ID, "run", "msg"}); e != nil {
			t.Fatal(e)
		}
	}
	if e := tx.Commit(); e != nil {
		t.Fatal(e)
	}
	first, pos, e := scanRefs(ctx, db, "chat", 0, 2)
	if e != nil || len(first) != 2 || pos != first[1].Seq {
		t.Fatalf("first %+v %d %v", first, pos, e)
	}
	second, pos2, e := scanRefs(ctx, db, "chat", pos, 2)
	if e != nil || len(second) != 1 || pos2 <= pos || second[0].Operation.EntityID != "issue" {
		t.Fatalf("second %+v %v", second, e)
	}
	if _, _, e = scanRefs(ctx, db, "chat", -1, 2); !errors.Is(e, ErrInvalid) {
		t.Fatal(e)
	}
}

func TestWorkspaceDeletionRemovesOperationResults(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	tx, e := db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	o, _, e := BeginOperation(ctx, tx, spec("op"))
	if e != nil {
		t.Fatal(e)
	}
	finish(t, tx, o.ID)
	if _, e = AppendRef(ctx, tx, RefSpec{"event", "chat", o.ID, "run", "msg"}); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec(`DELETE FROM workspaces WHERE id='ws'`); e != nil {
		t.Fatal(e)
	}
	for _, table := range []string{"agent_operations", "chat_entity_refs"} {
		var n int
		if e = db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); e != nil {
			t.Fatal(e)
		}
		if n != 0 {
			t.Fatalf("workspace deletion retained %s", table)
		}
	}
}
