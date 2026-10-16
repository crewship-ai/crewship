package backup_test

import (
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
)

func TestBoardCommentHistoryEncryptedRoundtrip(t *testing.T) {
	source := openMigratedDB(t)
	ws := seedWorkspace(t, source)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := source.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO users(id,email) VALUES('board-historian','board-history@example.test')`)
	exec(`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('history-board',?,'history-board','History','u_admin','{}')`, ws)
	exec(`INSERT INTO page_board_threads(id,page_id,anchor_json,version,created_by_user_id,created_at,updated_at) VALUES('history-thread','history-board','{"type":"board","board_seq":1,"schema_version":1}',3,'board-historian','2026-10-13T00:00:00Z','2026-10-13T00:01:00Z')`)
	exec(`INSERT INTO page_board_comments(id,thread_id,thread_version,body,author_user_id,created_at,version,edited_at,edited_by_user_id) VALUES('history-comment','history-thread',1,'Revised text','board-historian','2026-10-13T00:00:00Z',2,'2026-10-13T00:01:00Z','board-historian')`)
	exec(`INSERT INTO page_board_comment_history(id,page_id,thread_id,comment_id,version,body,action,actor_user_id,created_at) VALUES('history-original','history-board','history-thread','history-comment',1,'Original text','create','board-historian','2026-10-13T00:00:00Z'),('history-edit','history-board','history-thread','history-comment',2,'Revised text','edit','board-historian','2026-10-13T00:01:00Z')`)
	exec(`INSERT INTO page_board_comment_reactions(comment_id,user_id,emoji,created_at) VALUES('history-comment','board-historian','👍','2026-10-13T00:02:00Z')`)
	exec(`INSERT INTO page_board_comment_mutations(id,page_id,thread_id,comment_id,mutation_id,request_digest,actor_user_id,operation,result_version,created_at,reaction_emoji,reaction_active) VALUES('history-receipt','history-board','history-thread','history-comment','react-once','digest','board-historian','react',3,'2026-10-13T00:02:00Z','👍',1)`)
	// Include a foreign workspace with the same human: scope follows the Page,
	// never the author or reactor identity.
	exec(`INSERT INTO workspaces(id,name,slug) VALUES('foreign-history','Foreign','foreign-history')`)
	exec(`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('foreign-board','foreign-history','foreign-board','Foreign','u_admin','{}')`)
	exec(`INSERT INTO page_board_threads(id,page_id,anchor_json,created_by_user_id,created_at,updated_at) VALUES('foreign-thread','foreign-board','{}','board-historian','2026-10-13T00:00:00Z','2026-10-13T00:00:00Z')`)
	exec(`INSERT INTO page_board_comments(id,thread_id,thread_version,body,author_user_id,created_at) VALUES('foreign-comment','foreign-thread',1,'Foreign secret','board-historian','2026-10-13T00:00:00Z')`)
	exec(`INSERT INTO page_board_comment_history(id,page_id,thread_id,comment_id,version,body,action,actor_user_id,created_at) VALUES('foreign-audit','foreign-board','foreign-thread','foreign-comment',1,'Foreign secret','create','board-historian','2026-10-13T00:00:00Z')`)
	exec(`INSERT INTO page_board_comment_reactions(comment_id,user_id,emoji,created_at) VALUES('foreign-comment','board-historian','👍','2026-10-13T00:00:00Z')`)

	actor := backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"}
	created, err := backup.CreateBackup(t.Context(), source, backup.CreateOptions{Scope: backup.ScopeWorkspace, WorkspaceID: ws, OutputDir: t.TempDir(), Passphrase: "board-comment-encrypted-history", Actor: actor})
	if err != nil {
		t.Fatal(err)
	}
	if !created.Manifest.Encryption.Enabled {
		t.Fatal("backup is not encrypted")
	}
	for _, fork := range []bool{false, true} {
		t.Run(map[bool]string{false: "same", true: "fork"}[fork], func(t *testing.T) {
			target := openMigratedDB(t)
			options := backup.RestoreOptions{Path: created.Path, Passphrase: "board-comment-encrypted-history", Actor: actor}
			if fork {
				options.AsWorkspace = "board-history-fork"
			}
			if _, err := backup.RestoreBackup(t.Context(), target, options); err != nil {
				t.Fatal(err)
			}
			var count int
			if err := target.QueryRow(`SELECT COUNT(*) FROM page_board_comment_history`).Scan(&count); err != nil || count != 2 {
				t.Fatalf("history scope/count=%d err=%v", count, err)
			}
			if err := target.QueryRow(`SELECT COUNT(*) FROM page_board_comment_history h JOIN page_board_comments c ON c.id=h.comment_id JOIN page_board_threads t ON t.id=h.thread_id JOIN pages p ON p.id=h.page_id WHERE c.thread_id=t.id AND t.page_id=p.id AND h.actor_user_id=c.author_user_id AND c.body='Revised text' AND ((h.version=1 AND h.body='Original text') OR (h.version=2 AND h.body='Revised text'))`).Scan(&count); err != nil || count != 2 {
				t.Fatalf("history linkage=%d err=%v", count, err)
			}
			if err := target.QueryRow(`SELECT COUNT(*) FROM page_board_comment_reactions r JOIN page_board_comment_mutations m ON m.comment_id=r.comment_id AND m.actor_user_id=r.user_id WHERE r.emoji='👍' AND m.reaction_emoji=r.emoji AND m.reaction_active=1 AND m.result_version=3`).Scan(&count); err != nil || count != 1 {
				t.Fatalf("reaction receipt linkage=%d err=%v", count, err)
			}
			var id string
			if err := target.QueryRow(`SELECT id FROM page_board_comments`).Scan(&id); err != nil {
				t.Fatal(err)
			}
			if fork == (id == "history-comment") {
				t.Fatalf("unexpected restored identity %q fork=%v", id, fork)
			}
			if _, err := target.Exec(`UPDATE page_board_comment_history SET body='rewritten'`); err == nil {
				t.Fatal("history became mutable")
			}
			if _, err := target.Exec(`DELETE FROM users WHERE id=(SELECT author_user_id FROM page_board_comments LIMIT 1)`); err != nil {
				t.Fatalf("historical author deletion: %v", err)
			}
			if err := target.QueryRow(`SELECT COUNT(*) FROM page_board_comment_history WHERE actor_user_id IS NULL`).Scan(&count); err != nil || count != 2 {
				t.Fatalf("nullable attribution=%d err=%v", count, err)
			}
			if err := target.QueryRow(`SELECT COUNT(*) FROM page_board_comment_reactions`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("reactor cascade=%d err=%v", count, err)
			}
			if _, err := target.Exec(`DELETE FROM pages WHERE id=(SELECT page_id FROM page_board_threads LIMIT 1)`); err != nil {
				t.Fatal(err)
			}
			if err := target.QueryRow(`SELECT COUNT(*) FROM page_board_comment_history`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("page cascade=%d err=%v", count, err)
			}
		})
	}
}
