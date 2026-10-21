package documents

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
)

func TestOperationalLifetimeReceiptsDoNotPermanentlyFreezeContent(t *testing.T) {
	s, db, _ := fixture(t, Options{})
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 9998; i++ {
		_, err = tx.ExecContext(ctx, `INSERT INTO document_receipts(workspace_id,page_id,id,digest,fingerprint,rev,epoch,actor,kind,created_at) VALUES(?,?,?,?,?,0,0,?,'edit','2000-01-01T00:00:00Z')`, key.WorkspaceID, key.PageID, fmt.Sprintf("historical-%d", i), "historical-digest", "historical-fingerprint", "server-actor")
		if err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	l := acquire(t, s)
	if _, err = s.Commit(ctx, key, commit(l, "actual-10000th", 0), allow); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM document_receipts WHERE workspace_id=? AND page_id=?`, key.WorkspaceID, key.PageID).Scan(&count); err != nil || count != 10000 {
		t.Fatalf("receipt count %d: %v", count, err)
	}
	if _, err = s.Commit(ctx, key, commit(l, "actual-10001st", 1), allow); err != nil {
		t.Fatalf("historical receipts permanently deny a valid new edit after 10000 durable receipts: %v", err)
	}
}

func TestOperationalDocumentCapacityIsWorkspaceLocal(t *testing.T) {
	s, db, _ := fixture(t, Options{})
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 126; i++ {
		id := fmt.Sprintf("historical-page-%d", i)
		_, err = tx.ExecContext(ctx, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES(?,'workspace-a',?,?,'document-owner','{}')`, id, id, id)
		if err == nil {
			_, err = tx.ExecContext(ctx, `INSERT INTO documents(workspace_id,page_id,schema_version,snapshot,projection) VALUES('workspace-a',?,1,?,?)`, id, []byte("baseline"), []byte(projection))
		}
		if err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Init(ctx, Key{"workspace-a", "page-c"}, 1, []byte("baseline"), projection, allow); err != nil {
		t.Fatalf("128th document: %v", err)
	}
	if _, err = s.Init(ctx, Key{"workspace-b", "page-b"}, 1, []byte("baseline"), projection, allow); err != nil {
		t.Fatalf("workspace A's 128 documents deny workspace B's first document: %v", err)
	}
}

func TestOperationalRateWindowExpiresWithoutExpiringRetryEvidence(t *testing.T) {
	s, db, path := fixture(t, Options{MaxCommits: 3})
	l := acquire(t, s)
	original := commit(l, "original", 0)
	first, err := s.Commit(ctx, key, original, allow)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Checkpoint(ctx, key, l, "retained-checkpoint", []byte("candidate"), 1, allow); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Commit(ctx, key, commit(l, "burst-denied", 1), allow); !errors.Is(err, ErrRateLimit) {
		t.Fatalf("full window: %v", err)
	}
	var before string
	if err = db.QueryRow(`SELECT created_at FROM document_receipts WHERE id='original'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	replay, err := s.Commit(ctx, key, original, allow)
	if err != nil || replay != first {
		t.Fatalf("retry during full window: %+v %v", replay, err)
	}
	var after string
	if err = db.QueryRow(`SELECT created_at FROM document_receipts WHERE id='original'`).Scan(&after); err != nil || before != after {
		t.Fatalf("retry refreshed timestamp: %v", err)
	}
	// Age the server-owned timestamps, not the receipt identity or payload. This
	// exercises the actual SQL window predicate without a ten-minute test sleep.
	if _, err = db.Exec(`UPDATE document_receipts SET created_at='2000-01-01T00:00:00Z'`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Commit(ctx, key, commit(l, "after-window", 1), allow); err != nil {
		t.Fatalf("expired admission window: %v", err)
	}
	// A restored clock-skewed timestamp is still retry evidence, but is not a
	// new operation inside the current server-time window.
	if _, err = db.Exec(`UPDATE document_receipts SET created_at='2099-01-01T00:00:00.000Z' WHERE id IN ('original','retained-checkpoint')`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RestoreRequest(ctx, key, l, "retained-checkpoint", "restore", 2, allow); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	restarted := openTest(t, path)
	defer restarted.Close()
	s = New(restarted.DB, Options{MaxCommits: 3})
	original.Lease = acquire(t, s)
	replay, err = s.Commit(ctx, key, original, allow)
	if err != nil || replay != first {
		t.Fatalf("restart/restore/window expiry reexecuted original: %+v %v", replay, err)
	}
	state, err := s.Read(ctx, key, allow)
	if err != nil || state.HeadRev != 3 {
		t.Fatalf("retry added revision: %+v %v", state, err)
	}
	original.Update = []byte("different")
	if _, err = s.Commit(ctx, key, original, allow); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("old ID accepted changed payload: %v", err)
	}
	denied := func(context.Context, *sql.Tx) (string, error) { return "", ErrNotFound }
	if _, err = s.Commit(ctx, key, original, denied); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked retry exposed receipt: %v", err)
	}
}
