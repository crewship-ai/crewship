package mooring

import (
	"encoding/json"
	"testing"
)

// #4835: a legacy Mooring JSON command below the free-disk reserve writes no
// revision or receipt and keeps the confirmed snapshot. Reads and an exact
// retry of a confirmed receipt keep answering while the reserve is low.
func TestJSONCommitBelowFreeDiskReserveWritesNothing(t *testing.T) {
	_, db, _, _ := ownerFixture(t)
	manifest, err := Application("hello")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE pages SET private_audience=1 WHERE id='p'`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO mooring_documents(workspace_id,page_id,app_id,rev,generation,snapshot) VALUES('w','p','hello',1,1,?)`, string(manifest.InitialState)); err != nil {
		t.Fatal(err)
	}
	commit := func(r Request) (Receipt, error) {
		tx, err := db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		receipt, err := (JSONStorage{}).Commit(t.Context(), tx, "p", "user:u", manifest, r)
		if err == nil {
			err = tx.Commit()
		}
		return receipt, err
	}
	read := func() State {
		tx, err := db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		s, err := (JSONStorage{}).Read(t.Context(), tx, "p")
		if err != nil {
			t.Fatalf("read below reserve: %v", err)
		}
		return s
	}
	first := Request{MutationID: "one", BaseRev: 1, Generation: 1, Command: "set", Arguments: json.RawMessage(`{"text":"confirmed"}`)}
	if _, err = commit(first); err != nil {
		t.Fatal(err)
	}
	confirmed := read()
	lowFreeDisk(t, db.DB, true)
	next := Request{MutationID: "two", BaseRev: 2, Generation: 1, Command: "set", Arguments: json.RawMessage(`{"text":"refused"}`)}
	_, err = commit(next)
	wantFreeDiskRefusal(t, "JSON command", err)
	var receipts int
	if err = db.QueryRow(`SELECT count(*) FROM mooring_receipts`).Scan(&receipts); err != nil || receipts != 1 {
		t.Fatalf("refused command receipts=%d err=%v", receipts, err)
	}
	if got := read(); got.Rev != confirmed.Rev || string(got.Snapshot) != string(confirmed.Snapshot) {
		t.Fatalf("confirmed snapshot changed: %+v want %+v", got, confirmed)
	}
	if replay, err := commit(first); err != nil || replay.Rev != 2 {
		t.Fatalf("exact retry below reserve = %+v %v", replay, err)
	}
	lowFreeDisk(t, db.DB, false)
	if r, err := commit(next); err != nil || r.Rev != 3 {
		t.Fatalf("same command after space returned = %+v %v", r, err)
	}
}
