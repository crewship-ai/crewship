package documents

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

func TestInitRequestTxKeepsCallerCommitAndRollback(t *testing.T) {
	for _, commit := range []bool{false, true} {
		name := "rollback"
		if commit {
			name = "commit"
		}
		t.Run(name, func(t *testing.T) {
			store, db, _ := fixture(t, Options{})
			db.SetMaxOpenConns(1)
			tx, err := db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if _, err = tx.ExecContext(t.Context(), `UPDATE document_writer_fence SET seq=seq+1 WHERE id=1`); err != nil {
				t.Fatal(err)
			}
			target := Key{WorkspaceID: key.WorkspaceID, PageID: "page-c"}
			if _, err = store.InitRequestTx(t.Context(), tx, target, "atomic-init", "original", 3, []byte("prepared"), projection, "server-actor"); err != nil {
				t.Fatal(err)
			}
			var inside int
			if err = tx.QueryRow(`SELECT COUNT(*) FROM document_receipts WHERE page_id=? AND id=?`, target.PageID, "atomic-init").Scan(&inside); err != nil || inside != 1 {
				t.Fatalf("transaction receipt: %d %v", inside, err)
			}
			if commit {
				err = tx.Commit()
			} else {
				err = tx.Rollback()
			}
			if err != nil {
				t.Fatal(err)
			}
			want := 0
			if commit {
				want = 1
			}
			for _, table := range []string{"documents", "document_receipts", "document_checkpoints"} {
				var count int
				if err = db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE page_id=?`, target.PageID).Scan(&count); err != nil || count != want {
					t.Fatalf("%s count=%d want=%d err=%v", table, count, want, err)
				}
			}
		})
	}
}

func TestInitRequestTxRejectsIncompleteAuthority(t *testing.T) {
	for _, tc := range []struct {
		name  string
		noTx  bool
		actor string
		k     Key
	}{
		{name: "missing transaction", noTx: true, actor: "actor", k: key},
		{name: "missing actor", k: key},
		{name: "missing workspace", actor: "actor", k: Key{PageID: key.PageID}},
		{name: "missing page", actor: "actor", k: Key{WorkspaceID: key.WorkspaceID}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, db, _ := fixture(t, Options{})
			var tx *sql.Tx
			if !tc.noTx {
				var err error
				tx, err = db.BeginTx(t.Context(), nil)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
			}
			if _, err := store.InitRequestTx(t.Context(), tx, tc.k, "atomic-init", "original", 3, []byte("prepared"), projection, tc.actor); err == nil {
				t.Fatal("incomplete transaction authority accepted")
			}
		})
	}
}

func TestInitialCheckpointSurvivesEditAndExactRetry(t *testing.T) {
	s, db, _ := fixture(t, Options{})
	target := Key{key.WorkspaceID, "page-c"}
	id := strings.Repeat("x", 256)
	original := []byte("confirmed initial content")
	if _, err := s.InitRequest(ctx, target, id, "original", 3, original, projection, allow); err != nil {
		t.Fatal(err)
	}
	pins, err := s.Checkpoints(ctx, target, allow)
	if err != nil || len(pins) != 1 {
		t.Fatalf("initial history: %v %v", pins, err)
	}
	pinID := pins[0]["id"].(string)
	if len(pinID) > 128 || !strings.HasPrefix(pinID, "initial-") {
		t.Fatalf("public pin identity: %q", pinID)
	}
	lease, err := s.Acquire(ctx, target, "writer", allow)
	if err != nil {
		t.Fatal(err)
	}
	change := commit(lease, "human-edit", 0)
	change.Schema = 3
	change.Kind = "human"
	if _, err = s.Commit(ctx, target, change, allow); err != nil {
		t.Fatal(err)
	}
	if _, err = s.InitRequest(ctx, target, id, "original", 3, original, projection, allow); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		snap  []byte
		actor string
	}{{[]byte("changed"), "server-actor"}, {original, "other-actor"}} {
		auth := func(context.Context, *sql.Tx) (string, error) { return tc.actor, nil }
		if _, err = s.InitRequest(ctx, target, id, "original", 3, tc.snap, projection, auth); !errors.Is(err, ErrDuplicate) {
			t.Fatalf("changed retry: %v", err)
		}
	}
	pin, err := s.CheckpointRead(ctx, target, pinID, allow)
	if err != nil || pin.Rev != 0 || pin.Schema != 3 || pin.Actor != "server-actor" || !bytes.Equal(pin.Snapshot, original) || !bytes.Equal(pin.Projection, projection) {
		t.Fatalf("initial pin changed: %+v %v", pin, err)
	}
	pins, err = s.Checkpoints(ctx, target, allow)
	if err != nil || len(pins) != 1 {
		t.Fatalf("retry duplicated pin: %v %v", pins, err)
	}
	var receipts int
	if err = db.QueryRow("SELECT count(*) FROM document_receipts WHERE page_id=?", target.PageID).Scan(&receipts); err != nil || receipts != 2 {
		t.Fatalf("receipt count: %d %v", receipts, err)
	}
}
func TestInitialCheckpointFailureRollsBackInitialization(t *testing.T) {
	for _, mode := range []string{"history-quota", "checkpoint-insert"} {
		t.Run(mode, func(t *testing.T) {
			s, db, _ := fixture(t, Options{})
			if mode == "history-quota" {
				s = New(db.DB, Options{MaxHistoryBytes: 1})
			} else {
				if _, err := db.Exec("CREATE TRIGGER fail_initial_checkpoint BEFORE INSERT ON document_checkpoints BEGIN SELECT RAISE(ABORT, 'injected initial history failure'); END"); err != nil {
					t.Fatal(err)
				}
			}
			target := Key{key.WorkspaceID, "page-c"}
			_, err := s.InitRequest(ctx, target, "initial", "fp", 3, []byte("prepared"), projection, allow)
			if err == nil || mode == "history-quota" && !errors.Is(err, ErrQuota) {
				t.Fatalf("expected atomic failure: %v", err)
			}
			for _, table := range []string{"documents", "document_receipts", "document_checkpoints"} {
				var count int
				if err := db.QueryRow("SELECT count(*) FROM "+table+" WHERE page_id=?", target.PageID).Scan(&count); err != nil || count != 0 {
					t.Fatalf("%s leaked %d: %v", table, count, err)
				}
			}
		})
	}
}
