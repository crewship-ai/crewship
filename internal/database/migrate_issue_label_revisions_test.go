package database

import (
	"io"
	"log/slog"
	"path/filepath"
	"testing"
)

func TestIssueLabelMembershipAdvancesFieldRevisionForCAS(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "label-revisions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err = Migrate(t.Context(), db.DB, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	exec := func(q string) {
		t.Helper()
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO workspaces(id,name,slug) VALUES('label-w','Labels','labels')`)
	exec(`INSERT INTO crews(id,workspace_id,name,slug) VALUES('label-c','label-w','Crew','crew')`)
	exec(`INSERT INTO agents(id,workspace_id,crew_id,name,slug) VALUES('label-a','label-w','label-c','Agent','agent')`)
	exec(`INSERT INTO missions(id,workspace_id,crew_id,lead_agent_id,trace_id,title,mission_type) VALUES('label-first','label-w','label-c','label-a','label-first','First','issue'),('label-second','label-w','label-c','label-a','label-second','Second','issue')`)
	exec(`INSERT INTO labels(id,workspace_id,name,color) VALUES('label-one','label-w','One','#ffffff'),('label-two','label-w','Two','#000000')`)
	read := func(id string) (int64, int64) {
		t.Helper()
		var r, a int64
		if err := db.QueryRow(`SELECT revision,aggregate_revision FROM missions WHERE id=?`, id).Scan(&r, &a); err != nil {
			t.Fatal(err)
		}
		return r, a
	}
	revision, aggregate := read("label-first")
	exec(`INSERT INTO mission_labels(mission_id,label_id) VALUES('label-first','label-one')`)
	next, nextAggregate := read("label-first")
	if next != revision+1 || nextAggregate != aggregate+1 {
		t.Fatalf("membership insert not tracked: %d/%d -> %d/%d", revision, aggregate, next, nextAggregate)
	}
	// The first labels-only editor committed. A second editor's original CAS
	// token can no longer authorize replacement of that label set.
	result, err := db.Exec(`UPDATE missions SET title='Stale editor' WHERE id='label-first' AND revision=?`, revision)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := result.RowsAffected(); err != nil || n != 0 {
		t.Fatalf("stale pre-label CAS accepted: %d %v", n, err)
	}
	exec(`UPDATE mission_labels SET label_id=label_id WHERE mission_id='label-first'`)
	unchanged, unchangedAggregate := read("label-first")
	if unchanged != next || unchangedAggregate != nextAggregate {
		t.Fatal("no-op membership update advanced revisions")
	}
	exec(`UPDATE mission_labels SET label_id='label-two' WHERE mission_id='label-first'`)
	updated, updatedAggregate := read("label-first")
	if updated != next+1 || updatedAggregate != nextAggregate+1 {
		t.Fatal("membership replacement not tracked")
	}
	second, secondAggregate := read("label-second")
	exec(`UPDATE mission_labels SET mission_id='label-second' WHERE mission_id='label-first'`)
	moved, movedAggregate := read("label-first")
	newSecond, newSecondAggregate := read("label-second")
	if moved != updated+1 || movedAggregate != updatedAggregate+1 || newSecond != second+1 || newSecondAggregate != secondAggregate+1 {
		t.Fatal("membership move did not advance both issues")
	}
	exec(`DELETE FROM mission_labels WHERE mission_id='label-second'`)
	deleted, deletedAggregate := read("label-second")
	if deleted != newSecond+1 || deletedAggregate != newSecondAggregate+1 {
		t.Fatal("membership deletion not tracked")
	}
}
