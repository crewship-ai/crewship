package api

import (
	"database/sql"
	"os"
	"testing"
)

func TestBoardCommentLifecycleMigrationPreservesReceiptsAndBackfillsAudit(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`PRAGMA foreign_keys=ON;CREATE TABLE pages(id TEXT PRIMARY KEY);CREATE TABLE users(id TEXT PRIMARY KEY);INSERT INTO pages VALUES('p');INSERT INTO users VALUES('u'),('other');`); err != nil {
		t.Fatal(err)
	}
	old, err := os.ReadFile("../database/migrations/20261013193200_board_comments.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(old)); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO page_board_threads(id,page_id,anchor_json,created_by_user_id,created_at,updated_at) VALUES('t','p','{}','u','2000','2000');INSERT INTO page_board_comments(id,thread_id,thread_version,body,author_user_id,created_at) VALUES('c','t',1,'old body','u','2000');INSERT INTO page_board_comment_mutations(id,page_id,thread_id,comment_id,mutation_id,request_digest,actor_user_id,operation,result_version,created_at) VALUES('receipt','p','t','c','stable','digest','u','create',1,'2000');`); err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("../database/migrations/20261013231523_board_comment_lifecycle.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(migration)); err != nil {
		t.Fatal(err)
	}
	var body, actor, operation, digest string
	var version int
	if err = db.QueryRow(`SELECT body,version,actor_user_id FROM page_board_comment_history WHERE comment_id='c'`).Scan(&body, &version, &actor); err != nil || body != "old body" || version != 1 || actor != "u" {
		t.Fatalf("backfill %q %d %q %v", body, version, actor, err)
	}
	if err = db.QueryRow(`SELECT operation,request_digest,result_version FROM page_board_comment_mutations WHERE id='receipt'`).Scan(&operation, &digest, &version); err != nil || operation != "create" || digest != "digest" || version != 1 {
		t.Fatalf("receipt %s %s %d %v", operation, digest, version, err)
	}
	insert := `INSERT INTO page_board_comment_mutations(id,page_id,thread_id,comment_id,mutation_id,request_digest,actor_user_id,operation,result_version,created_at) VALUES(?,'p','t','c','stable','digest',?,'react',2,'2000')`
	if _, err = db.Exec(insert, "duplicate", "u"); err == nil {
		t.Fatal("duplicate sameactor receipt accepted")
	}
	if _, err = db.Exec(insert, "other-receipt", "other"); err != nil {
		t.Fatalf("actor-scoped identity changed: %v", err)
	}
	if _, err = db.Exec(`UPDATE page_board_comment_mutations SET request_digest='forged' WHERE id='receipt'`); err == nil {
		t.Fatal("mutation audit mutable")
	}
	if _, err = db.Exec(`UPDATE page_board_comment_history SET body='tampered' WHERE comment_id='c'`); err == nil {
		t.Fatal("backfilled audit mutable")
	}
	if _, err = db.Exec(`INSERT INTO page_board_comment_history(id,page_id,thread_id,comment_id,version,body,action,created_at) VALUES('bad','p','not-thread','c',2,'bad','edit','2000')`); err == nil {
		t.Fatal("cross-source history accepted")
	}
}
