package journal

import (
	"testing"
	"time"
)

func TestAppendTxRejectsIgnoredAuditInsert(t *testing.T) {
	for _, configured := range []bool{false, true} {
		name := "standalone"
		if configured {
			name = "configured"
		}
		t.Run(name, func(t *testing.T) {
			db := openTestDB(t)
			db.SetMaxOpenConns(1)
			defer db.Close()
			if _, err := db.Exec(`CREATE TRIGGER ignore_access_audit BEFORE INSERT ON journal_entries WHEN NEW.entry_type='member.access_changed' BEGIN SELECT RAISE(IGNORE); END`); err != nil {
				t.Fatal(err)
			}
			w := NewWriter(db, quietLogger(), WriterOptions{ChainKey: []byte("insert-acknowledgement-test-key"), FlushInterval: time.Hour})
			defer w.Close()
			observed := 0
			w.AddCommitObserver(func([]Entry) { observed++ })
			tx, err := db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			appendEntry := AppendTx
			if configured {
				appendEntry = w.AppendTx
			}
			notify, appendErr := appendEntry(t.Context(), tx, Entry{WorkspaceID: "ws_test", Type: EntryMemberAccessChanged, ActorType: ActorUser, ActorID: "operator", Summary: "Member resource preset applied"})
			var count int
			if err := tx.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE entry_type='member.access_changed'`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("ignored INSERT stored %d audit rows", count)
			}
			if appendErr == nil {
				t.Fatal("acknowledged audit success although SQLite ignored the INSERT")
			}
			if notify != nil {
				t.Fatal("failed audit append returned a success notification")
			}
			if observed != 0 {
				t.Fatal("failed audit append notified observers")
			}
		})
	}
}
