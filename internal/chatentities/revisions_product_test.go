package chatentities

import (
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/crewship-ai/crewship/internal/database"
)

func TestIssueRevisionsOnProductDatabaseDefaultPragmas(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "issue-revisions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err = database.Migrate(t.Context(), db.DB, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	var recursive int
	if err = db.QueryRow(`PRAGMA recursive_triggers`).Scan(&recursive); err != nil || recursive != 0 {
		t.Fatalf("production default recursive_triggers=%d: %v", recursive, err)
	}
	exec := func(q string) {
		t.Helper()
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO workspaces(id,name,slug) VALUES('rev-w','Revisions','revisions')`)
	exec(`INSERT INTO crews(id,workspace_id,name,slug) VALUES('rev-c','rev-w','Crew','crew')`)
	exec(`INSERT INTO agents(id,workspace_id,crew_id,name,slug) VALUES('rev-a','rev-w','rev-c','Agent','agent')`)
	exec(`INSERT INTO missions(id,workspace_id,crew_id,lead_agent_id,trace_id,title,mission_type) VALUES('rev-parent','rev-w','rev-c','rev-a','rev-parent','Parent','issue')`)
	exec(`INSERT INTO missions(id,workspace_id,crew_id,lead_agent_id,trace_id,title,mission_type,parent_issue_id) VALUES('rev-child','rev-w','rev-c','rev-a','rev-child','Child','issue','rev-parent')`)
	read := func(id string) (int64, int64) {
		t.Helper()
		var r, a int64
		if err := db.QueryRow(`SELECT revision,aggregate_revision FROM missions WHERE id=?`, id).Scan(&r, &a); err != nil {
			t.Fatal(err)
		}
		return r, a
	}
	for _, q := range []string{
		`UPDATE issue_work SET mode='human' WHERE mission_id='rev-child'`,
		`UPDATE issue_work SET client_review_required=0 WHERE mission_id='rev-child'`,
		`UPDATE issue_work SET note='A verified work note',revision=revision+1 WHERE mission_id='rev-child'`,
		`UPDATE missions SET title='Changed child' WHERE id='rev-child'`,
	} {
		r, a := read("rev-child")
		_, p := read("rev-parent")
		exec(q)
		nr, na := read("rev-child")
		_, np := read("rev-parent")
		if nr <= r || na <= a || np <= p {
			t.Fatalf("visible change did not propagate under default pragmas: %s child %d/%d -> %d/%d parent %d -> %d", q, r, a, nr, na, p, np)
		}
	}
	for _, q := range []string{
		`UPDATE missions SET title=title WHERE id='rev-child'`,
		`UPDATE missions SET chat_origin_id=NULL WHERE id='rev-child'`,
		`UPDATE missions SET updated_at='2026-10-13T00:00:00Z' WHERE id='rev-child'`,
		`UPDATE issue_work SET note=note,revision=revision WHERE mission_id='rev-child'`,
	} {
		r, a := read("rev-child")
		_, p := read("rev-parent")
		exec(q)
		nr, na := read("rev-child")
		_, np := read("rev-parent")
		if nr != r || na != a || np != p {
			t.Fatalf("non-visible update bumped revision: %s child %d/%d -> %d/%d parent %d -> %d", q, r, a, nr, na, p, np)
		}
	}
}
