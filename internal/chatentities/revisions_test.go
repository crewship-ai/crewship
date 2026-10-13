package chatentities

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestIssueRevisionTriggers(t *testing.T) {
	db, e := sql.Open("sqlite", filepath.Join(t.TempDir(), "revisions.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	_, e = db.Exec(`PRAGMA recursive_triggers=ON;
 CREATE TABLE missions(id TEXT PRIMARY KEY,parent_issue_id TEXT,title TEXT,updated_at TEXT);
 CREATE TABLE mission_comments(id TEXT PRIMARY KEY,mission_id TEXT,body TEXT);
 CREATE TABLE mission_labels(mission_id TEXT,label_id TEXT);
 CREATE TABLE mission_code_links(id TEXT PRIMARY KEY,mission_id TEXT,state TEXT);
 CREATE TABLE mission_relations(id TEXT PRIMARY KEY,source_id TEXT,target_id TEXT,relation_type TEXT);
 CREATE TABLE attachments(id TEXT PRIMARY KEY,mission_id TEXT,comment_id TEXT,filename TEXT);`)
	if e != nil {
		t.Fatal(e)
	}
	applyMigration(t, db, "20261013120100_issue_revisions.sql")
	exec := func(query string) {
		t.Helper()
		if _, e := db.Exec(query); e != nil {
			t.Fatal(e)
		}
	}
	rev := func(id string) (int64, int64) {
		t.Helper()
		var r, a int64
		if e := db.QueryRow(`SELECT revision,aggregate_revision FROM missions WHERE id=?`, id).Scan(&r, &a); e != nil {
			t.Fatal(e)
		}
		return r, a
	}
	exec(`INSERT INTO missions(id,title) VALUES('parent','p'),('other','o')`)
	exec(`INSERT INTO missions(id,parent_issue_id,title) VALUES('child','parent','c')`)
	before, agg := rev("child")
	_, pa := rev("parent")
	exec(`UPDATE missions SET title='changed' WHERE id='child'`)
	after, aggAfter := rev("child")
	_, paAfter := rev("parent")
	if after != before+1 || aggAfter != agg+1 || paAfter != pa+1 {
		t.Fatalf("one update: field %d->%d aggregate %d->%d parent %d->%d", before, after, agg, aggAfter, pa, paAfter)
	}
	// Moving subissues refreshes both old and new parent; no trigger recursion.
	_, oa := rev("other")
	exec(`UPDATE missions SET parent_issue_id='other' WHERE id='child'`)
	_, pn := rev("parent")
	_, on := rev("other")
	if pn <= paAfter || on <= oa {
		t.Fatal("parent move not tracked")
	}
	changes := []string{
		`INSERT INTO mission_comments VALUES('comment','child','body')`,
		`UPDATE mission_comments SET body='changed' WHERE id='comment'`,
		`INSERT INTO attachments VALUES('attachment',NULL,'comment','a.txt')`,
		`UPDATE attachments SET filename='b.txt' WHERE id='attachment'`,
		`DELETE FROM attachments WHERE id='attachment'`,
		`DELETE FROM mission_comments WHERE id='comment'`,
		`INSERT INTO mission_labels VALUES('child','label')`,
		`DELETE FROM mission_labels WHERE mission_id='child'`,
		`INSERT INTO mission_code_links VALUES('code','child','open')`,
		`UPDATE mission_code_links SET state='closed' WHERE id='code'`,
		`DELETE FROM mission_code_links WHERE id='code'`,
		`INSERT INTO mission_relations VALUES('relation','child','parent','blocks')`,
		`UPDATE mission_relations SET relation_type='relates_to' WHERE id='relation'`,
		`DELETE FROM mission_relations WHERE id='relation'`,
	}
	for _, query := range changes {
		r, a := rev("child")
		exec(query)
		r2, a2 := rev("child")
		if r2 != r || a2 != a+1 {
			t.Fatalf("%s: revision %d->%d aggregate %d->%d", query, r, r2, a, a2)
		}
	}
}
