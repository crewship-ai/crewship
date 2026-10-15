package chatentities

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
)

func deletionOperation(t *testing.T, tx *sql.Tx, snapshot string) *Operation {
	t.Helper()
	s := spec("delete")
	s.Key = "delete-key"
	s.ActorType = "human"
	s.ActorID = "user"
	s.RunID = ""
	s.Provenance = "human"
	s.Verb = "deleted"
	o, _, e := BeginOperation(context.Background(), tx, s)
	if e != nil {
		t.Fatal(e)
	}
	if e = CompleteOperation(context.Background(), tx, o.ID, ResultSpec{EntityID: "issue", AccessSnapshot: json.RawMessage(snapshot), Revision: 2, AggregateRevision: 3}); e != nil {
		t.Fatal(e)
	}
	return o
}
func originalRef(t *testing.T, tx *sql.Tx, id, chat, run, message, entity string) Ref {
	t.Helper()
	s := spec(id)
	s.Key = id
	s.RunID = run
	o, _, e := BeginOperation(context.Background(), tx, s)
	if e != nil {
		t.Fatal(e)
	}
	if e = CompleteOperation(context.Background(), tx, o.ID, ResultSpec{EntityID: entity, Revision: 1, AggregateRevision: 1}); e != nil {
		t.Fatal(e)
	}
	r, e := AppendRef(context.Background(), tx, RefSpec{EventID: "event-" + id, ChatID: chat, OperationID: o.ID, RunID: run, TriggerMessageID: message})
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func TestDeletionRefsPreserveHumanActorAndOriginalAnchors(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	tx, e := db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(`INSERT INTO chats VALUES('second','ws')`); e != nil {
		t.Fatal(e)
	}
	originalRef(t, tx, "original", "chat", "run-one", "msg-one", "issue")
	originalRef(t, tx, "again", "chat", "run-later", "msg-later", "issue")
	originalRef(t, tx, "second", "second", "run-two", "msg-two", "issue")
	originalRef(t, tx, "unrelated", "chat", "run-unrelated", "msg-unrelated", "other-issue")
	o := deletionOperation(t, tx, `{"version":1,"workspace_id":"ws","visibility":"trusted_workspace"}`)
	refs, e := AppendDeletionRefs(ctx, tx, o.ID, "ws", "issue")
	if e != nil {
		t.Fatal(e)
	}
	if len(refs) != 2 {
		t.Fatalf("got %d refs", len(refs))
	}
	for _, r := range refs {
		if r.Operation.ActorType != "human" || r.Operation.ActorID != "user" || r.Operation.RunID != "" || r.Operation.Verb != "deleted" {
			t.Fatalf("wrong attribution %+v", r)
		}
		if r.ChatID == "chat" && (r.RunID != "run-one" || r.TriggerMessageID != "msg-one") {
			t.Fatalf("wrong anchor %+v", r)
		}
	}
	repeated, e := AppendDeletionRefs(ctx, tx, o.ID, "ws", "issue")
	if e != nil {
		t.Fatal(e)
	}
	for i := range refs {
		if refs[i].EventID != repeated[i].EventID || refs[i].Seq != repeated[i].Seq {
			t.Fatal("retry duplicated event")
		}
	}
	// The normal API may not claim this human action was performed by a run.
	if _, e = AppendRef(ctx, tx, RefSpec{"bad", "chat", o.ID, "run-one", "msg-one"}); !errors.Is(e, ErrInvalid) {
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	scanned, _, e := scanRefs(ctx, db, "chat", 0, 10)
	if e != nil {
		t.Fatal(e)
	}
	if len(scanned) != 4 || scanned[3].Operation.Verb != "deleted" {
		t.Fatalf("scan %+v", scanned)
	}
}
func TestDeletionRefsRejectUnsafeSnapshots(t *testing.T) {
	cases := []string{`{}`, `{"version":1,"workspace_id":"other-ws","visibility":"trusted_workspace"}`, `{"version":2,"workspace_id":"ws","visibility":"trusted_workspace"}`, `{"version":1,"workspace_id":"ws","visibility":"private_issue"}`, `{"version":1,"workspace_id":"ws","visibility":"trusted_workspace","project_id":"private"}`}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			db := testDB(t)
			ctx := context.Background()
			tx, e := db.BeginTx(ctx, nil)
			if e != nil {
				t.Fatal(e)
			}
			defer tx.Rollback()
			o := deletionOperation(t, tx, raw)
			if _, e = AppendDeletionRefs(ctx, tx, o.ID, "ws", "issue"); !errors.Is(e, ErrInvalid) {
				t.Fatalf("snapshot accepted: %v", e)
			}
		})
	}
}
func TestDeletionRefsFenceCorruptCrossWorkspaceHistory(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	tx, e := db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	r := originalRef(t, tx, "original", "chat", "run", "msg", "issue")
	// Simulate an invalid historical link written outside the guarded API.
	if _, e = tx.Exec(`INSERT INTO chat_entity_refs(event_id,chat_id,operation_id,run_id,trigger_message_id,created_at) VALUES('corrupt','other',?,'run','msg','now')`, r.OperationID); e != nil {
		t.Fatal(e)
	}
	o := deletionOperation(t, tx, `{"version":1,"workspace_id":"ws","visibility":"trusted_workspace"}`)
	refs, e := AppendDeletionRefs(ctx, tx, o.ID, "ws", "issue")
	if e != nil {
		t.Fatal(e)
	}
	if len(refs) != 1 || refs[0].ChatID != "chat" {
		t.Fatalf("cross-workspace refs %+v", refs)
	}
	if _, e = AppendDeletionRefs(ctx, tx, o.ID, "ws", "other-issue"); !errors.Is(e, ErrInvalid) {
		t.Fatal(e)
	}
	if _, e = AppendDeletionRefs(ctx, tx, o.ID, "other-ws", "issue"); !errors.Is(e, sql.ErrNoRows) {
		t.Fatal(e)
	}
}
func TestDeletionRefsRollback(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	tx, e := db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	originalRef(t, tx, "original", "chat", "run", "msg", "issue")
	o := deletionOperation(t, tx, `{"version":1,"workspace_id":"ws","visibility":"trusted_workspace"}`)
	if _, e = AppendDeletionRefs(ctx, tx, o.ID, "ws", "issue"); e != nil {
		t.Fatal(e)
	}
	if e = tx.Rollback(); e != nil {
		t.Fatal(e)
	}
	refs, _, e := scanRefs(ctx, db, "chat", 0, 10)
	if e != nil || len(refs) != 0 {
		t.Fatalf("rollback %+v %v", refs, e)
	}
}
