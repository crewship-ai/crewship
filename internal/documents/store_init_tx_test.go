package documents

import (
	"database/sql"
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
			for _, table := range []string{"documents", "document_receipts"} {
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
