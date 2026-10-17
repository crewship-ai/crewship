package api

import (
	"database/sql"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/crewship-ai/crewship/internal/database"
)

// Intermediate unreleased schemas no longer exist. Leave Board pending in the
// real main+Artifacts registry, then apply its final schema through Migrate.
// The foundation regression separately upgrades seeded main through all three
// consolidated migrations and compares the complete original final schema.
func TestBoardCommentFinalSchemaPreservesActorReceiptsAndImmutableHistory(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "board-upgrade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err = database.MigrateSkipping(t.Context(), db.DB, logger, "page_boards"); err != nil {
		t.Fatal("main+Artifacts baseline", err)
	}
	var tables int
	if err = db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='page_board_comments'`).Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("Board was not actually pending: tables=%d err=%v", tables, err)
	}
	exec := func(statement string, args ...any) {
		t.Helper()
		if _, err := db.Exec(statement, args...); err != nil {
			t.Fatal(statement, err)
		}
	}
	exec(`INSERT INTO users(id,email) VALUES('u','owner@example.invalid'),('other','other@example.invalid');
INSERT INTO workspaces(id,name,slug) VALUES('ws','Workspace','workspace');
INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('p','ws','board','Board','u','{}'),('p2','ws','other-board','Other','u','{}');
INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id,granted_at) VALUES('p','user','other','read','u','2026-10-01T00:00:00Z');`)
	if err = database.Migrate(t.Context(), db.DB, logger); err != nil {
		t.Fatal("apply final Board schema", err)
	}
	var grant string
	if err = db.QueryRow(`SELECT level||':'||granted_by_user_id||':'||granted_at FROM page_grants WHERE page_id='p' AND subject_id='other'`).Scan(&grant); err != nil || grant != "read:u:2026-10-01T00:00:00Z" {
		t.Fatalf("Board upgrade changed the existing Page grant: %q %v", grant, err)
	}
	exec(`INSERT INTO page_boards(page_id,seq,format) VALUES('p',0,'crewship-board-excalidraw-0.18.1-v1'),('p2',0,'crewship-board-excalidraw-0.18.1-v1');
INSERT INTO page_board_threads(id,page_id,anchor_json,created_by_user_id,created_at,updated_at) VALUES('t','p','{}','u','2000','2000'),('other-thread','p2','{}','u','2000','2000');
INSERT INTO page_board_comments(id,thread_id,thread_version,body,author_user_id,created_at) VALUES('c','t',1,'Original body','u','2000');`)
	var version int
	var edited, deleted, editor, deleter sql.NullString
	if err = db.QueryRow(`SELECT version,edited_at,deleted_at,edited_by_user_id,deleted_by_user_id FROM page_board_comments WHERE id='c'`).Scan(&version, &edited, &deleted, &editor, &deleter); err != nil || version != 1 || edited.Valid || deleted.Valid || editor.Valid || deleter.Valid {
		t.Fatalf("fresh lifecycle defaults changed: version=%d edited=%v deleted=%v editor=%v deleter=%v err=%v", version, edited, deleted, editor, deleter, err)
	}
	exec(`INSERT INTO page_board_comment_history(id,page_id,thread_id,comment_id,version,body,action,actor_user_id,created_at) VALUES('original','p','t','c',1,'Original body','create','u','2000'),('edit','p','t','c',2,'Edited body','edit','other','2001');
UPDATE page_board_comments SET body='Edited body',version=2,edited_at='2001',edited_by_user_id='other' WHERE id='c';
INSERT INTO page_board_comment_mutations(id,page_id,thread_id,comment_id,mutation_id,request_digest,actor_user_id,operation,result_version,created_at) VALUES('receipt','p','t','c','stable','digest','u','create',1,'2000');`)
	insert := `INSERT INTO page_board_comment_mutations(id,page_id,thread_id,comment_id,mutation_id,request_digest,actor_user_id,operation,result_version,created_at,reaction_emoji,reaction_active) VALUES(?,'p','t','c','stable','digest',?,'react',2,'2001','👍',1)`
	if _, err = db.Exec(insert, "duplicate", "u"); err == nil {
		t.Fatal("duplicate same-actor receipt accepted")
	}
	exec(insert, "other-receipt", "other")
	var actor, operation, digest, emoji string
	var active int
	if err = db.QueryRow(`SELECT actor_user_id,operation,request_digest,result_version,reaction_emoji,reaction_active FROM page_board_comment_mutations WHERE id='other-receipt'`).Scan(&actor, &operation, &digest, &version, &emoji, &active); err != nil || actor != "other" || operation != "react" || digest != "digest" || version != 2 || emoji != "👍" || active != 1 {
		t.Fatalf("actor-scoped receipt changed: actor=%s operation=%s digest=%s version=%d emoji=%s active=%d err=%v", actor, operation, digest, version, emoji, active, err)
	}
	for _, rejected := range []string{
		`UPDATE page_board_comment_mutations SET request_digest='forged' WHERE id='receipt'`,
		`UPDATE page_board_comment_mutations SET actor_user_id='other' WHERE id='receipt'`,
		`UPDATE page_board_comment_mutations SET reaction_active=0 WHERE id='other-receipt'`,
		`UPDATE page_board_comment_history SET body='tampered' WHERE id='original'`,
		`INSERT INTO page_board_comment_history(id,page_id,thread_id,comment_id,version,body,action,created_at) VALUES('bad','p2','other-thread','c',3,'bad','edit','2002')`,
		`INSERT INTO page_board_comment_mutations(id,page_id,thread_id,comment_id,mutation_id,request_digest,actor_user_id,operation,result_version,created_at) VALUES('bad-receipt','p2','other-thread','c','cross-source','digest','u','react',3,'2002')`,
		`INSERT INTO page_board_comment_reactions(comment_id,user_id,emoji,created_at) VALUES('c','u','unsupported','2002')`,
	} {
		if _, err = db.Exec(rejected); err == nil {
			t.Fatal("final comment constraint accepted tampering", rejected)
		}
	}
	// FK deletion may clear historical attribution without rewriting content.
	exec(`DELETE FROM page_grants WHERE subject_id='other'; DELETE FROM users WHERE id='other';`)
	var history, receipts int
	if err = db.QueryRow(`SELECT (SELECT COUNT(*) FROM page_board_comment_history WHERE id='edit' AND body='Edited body' AND actor_user_id IS NULL),(SELECT COUNT(*) FROM page_board_comment_mutations WHERE id='other-receipt' AND actor_user_id IS NULL AND request_digest='digest' AND reaction_emoji='👍' AND reaction_active=1)`).Scan(&history, &receipts); err != nil || history != 1 || receipts != 1 {
		t.Fatalf("actor deletion changed durable content: history=%d receipts=%d err=%v", history, receipts, err)
	}
	if err = database.Migrate(t.Context(), db.DB, logger); err != nil {
		t.Fatal("migration replay", err)
	}
	if err = db.QueryRow(`SELECT (SELECT COUNT(*) FROM page_board_comment_history),(SELECT COUNT(*) FROM page_board_comment_mutations)`).Scan(&history, &receipts); err != nil || history != 2 || receipts != 2 {
		t.Fatalf("migration replay changed history: history=%d receipts=%d err=%v", history, receipts, err)
	}
}
