package backup

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// Exercise actual workspace filters against both pre-history discussion schemas
// and the current attribution columns. No membership/crew/chat can incidentally
// pull these users in, so the assertions isolate discussion scope.
func TestDiscussionUsersScopeLegacyAndCurrentParity(t *testing.T) {
	for _, current := range []bool{false, true} {
		for _, family := range []string{"page_artifact_", "page_board_"} {
			t.Run(family+map[bool]string{false: "legacy", true: "current"}[current], func(t *testing.T) {
				db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "scope.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				exec := func(query string) {
					t.Helper()
					if _, err := db.Exec(query); err != nil {
						t.Fatal(err)
					}
				}
				exec(`CREATE TABLE users(id TEXT PRIMARY KEY); CREATE TABLE pages(id TEXT PRIMARY KEY, workspace_id TEXT); INSERT INTO pages VALUES('own-page','own'),('foreign-page','foreign'); INSERT INTO users VALUES('base'),('thread-author'),('comment-author'),('receipt-actor'),('history-actor'),('reactor'),('editor'),('deleter'),('foreign-only'),('unreferenced')`)
				exec(`CREATE TABLE ` + family + `threads(id TEXT PRIMARY KEY,page_id TEXT,created_by_user_id TEXT); INSERT INTO ` + family + `threads VALUES('own-thread','own-page','thread-author'),('foreign-thread','foreign-page','foreign-only')`)
				exec(`CREATE TABLE ` + family + `comments(id TEXT PRIMARY KEY,thread_id TEXT,author_user_id TEXT); INSERT INTO ` + family + `comments VALUES('own-comment','own-thread','comment-author'),('foreign-comment','foreign-thread','foreign-only')`)
				exec(`CREATE TABLE ` + family + `comment_mutations(id TEXT PRIMARY KEY,page_id TEXT,actor_user_id TEXT); INSERT INTO ` + family + `comment_mutations VALUES('own-receipt','own-page','receipt-actor'),('foreign-receipt','foreign-page','foreign-only')`)
				if current {
					exec(`ALTER TABLE ` + family + `comments ADD COLUMN edited_by_user_id TEXT; ALTER TABLE ` + family + `comments ADD COLUMN deleted_by_user_id TEXT; UPDATE ` + family + `comments SET edited_by_user_id='editor',deleted_by_user_id='deleter' WHERE id='own-comment'`)
					exec(`CREATE TABLE ` + family + `comment_history(id TEXT PRIMARY KEY,page_id TEXT,actor_user_id TEXT); INSERT INTO ` + family + `comment_history VALUES('own-history','own-page','history-actor'),('foreign-history','foreign-page','foreign-only')`)
					exec(`CREATE TABLE ` + family + `comment_reactions(comment_id TEXT,user_id TEXT); INSERT INTO ` + family + `comment_reactions VALUES('own-comment','reactor'),('foreign-comment','foreign-only')`)
				}
				tx, err := db.BeginTx(t.Context(), nil)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				where, args, err := includeDiscussionUsers(t.Context(), tx, "own", "id = ?", []any{"base"})
				if err != nil {
					t.Fatal(err)
				}
				rows, err := tx.QueryContext(t.Context(), `SELECT id FROM users WHERE `+where, args...)
				if err != nil {
					t.Fatal(err)
				}
				defer rows.Close()
				got := map[string]bool{}
				for rows.Next() {
					var id string
					if err := rows.Scan(&id); err != nil {
						t.Fatal(err)
					}
					got[id] = true
				}
				if err := rows.Err(); err != nil {
					t.Fatal(err)
				}
				expected := []string{"base", "thread-author", "comment-author", "receipt-actor"}
				if current {
					expected = append(expected, "history-actor", "reactor", "editor", "deleter")
				}
				if len(got) != len(expected) {
					t.Fatalf("unexpected exported identities: %v", got)
				}
				for _, id := range expected {
					if !got[id] {
						t.Fatalf("missing historical user %q: %v", id, got)
					}
				}
				if got["foreign-only"] || got["unreferenced"] {
					t.Fatalf("unscoped user export: %v", got)
				}
			})
		}
	}
}
