package api

import (
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
)

func TestArtifactCommentHistoryBackupPreservesTombstonesReactionsAndProvenance(t *testing.T) {
	h, ws, user, page, token := artifactRunFixture(t)
	artifactExpect(t, artifactAgentSave(t, h, ws, token, `{"html":"<h1>Discuss</h1>","base_rev":0,"mutation_id":"history-source"}`), 201)
	rr := artifactDiscussionCreate(t, h, ws, user, "Original body", "history-thread", 1)
	artifactExpect(t, rr, 201)
	thread := discussionThread(t, rr)
	artifactExpect(t, artifactDiscussionCall(t, h, ws, user, "OWNER", "reply", thread.ID, map[string]any{"body": "React to this live reply", "base_version": 1, "mutation_id": "history-live-reply"}), 201)
	var liveComment string
	if err := h.db.QueryRow(`SELECT id FROM page_artifact_comments WHERE thread_id=? AND thread_version=2`, thread.ID).Scan(&liveComment); err != nil {
		t.Fatal(err)
	}
	var comment string
	if err := h.db.QueryRow(`SELECT id FROM page_artifact_comments WHERE thread_id=? AND thread_version=1`, thread.ID).Scan(&comment); err != nil {
		t.Fatal(err)
	}
	// Seed the durable states independently of the HTTP edit/delete implementation:
	// a deleted comment keeps its original/edit audit, while reactions still bind
	// the exact comment and human identity after a workspace fork.
	execOrFatal(t, h.db, `INSERT OR IGNORE INTO page_artifact_comment_history(id,page_id,thread_id,comment_id,version,body,action,actor_user_id,created_at) VALUES('history-initial',?,?,?,1,'Original body','create',?,'2026-10-13T00:00:00.000Z')`, page, thread.ID, comment, user)
	execOrFatal(t, h.db, `INSERT INTO page_artifact_comment_history(id,page_id,thread_id,comment_id,version,body,action,actor_user_id,actor_agent_id,actor_run_id,created_at) VALUES('history-edit',?,?,?,2,'Edited body','edit',?,'artifact-agent','artifact-run','2026-10-13T00:01:00.000Z')`, page, thread.ID, comment, user)
	execOrFatal(t, h.db, `INSERT INTO page_artifact_comment_history(id,page_id,thread_id,comment_id,version,body,action,actor_user_id,created_at) VALUES('history-delete',?,?,?,3,'','delete',?,'2026-10-13T00:02:00.000Z')`, page, thread.ID, comment, user)
	execOrFatal(t, h.db, `UPDATE page_artifact_comments SET version=3,body='[deleted]',edited_at='2026-10-13T00:01:00.000Z',deleted_at='2026-10-13T00:02:00.000Z',edited_by_user_id=?,deleted_by_user_id=? WHERE id=?`, user, user, comment)
	execOrFatal(t, h.db, `INSERT INTO page_artifact_comment_reactions(comment_id,user_id,emoji,created_at) VALUES(?,?,'👀','2026-10-13T00:01:00.000Z')`, liveComment, user)
	const pass = "comment-history-storage-password"
	made, err := backup.CreateBackup(t.Context(), h.db, backup.CreateOptions{Scope: backup.ScopeWorkspace, WorkspaceID: ws, OutputDir: t.TempDir(), Passphrase: pass, Actor: backup.Actor{UserID: user, Role: "ADMIN"}, AttachmentRoot: h.artifactStoragePath})
	if err != nil {
		t.Fatal(err)
	}
	for _, fork := range []string{"", "comment-history-fork"} {
		t.Run(fork, func(t *testing.T) {
			db := setupTestDB(t)
			restored, err := backup.RestoreBackup(t.Context(), db, backup.RestoreOptions{Path: made.Path, Passphrase: pass, AsWorkspace: fork, Actor: backup.Actor{UserID: user, Role: "ADMIN"}, AttachmentRoot: storageDir(t)})
			if err != nil {
				t.Fatal(err)
			}
			var cid, pid string
			var n int
			if err := db.QueryRow(`SELECT c.id,p.id FROM page_artifact_comments c JOIN page_artifact_threads t ON t.id=c.thread_id JOIN pages p ON p.id=t.page_id WHERE p.workspace_id=? AND c.version=3 AND c.deleted_at IS NOT NULL AND c.edited_by_user_id=? AND c.deleted_by_user_id=?`, restored.RestoredWorkspaceID, user, user).Scan(&cid, &pid); err != nil {
				t.Fatal(err)
			}
			if fork != "" && (cid == comment || pid == page) {
				t.Fatal("fork failed to remap discussion identity")
			}
			if err := db.QueryRow(`SELECT COUNT(*) FROM page_artifact_comment_history h JOIN page_artifact_threads t ON t.id=h.thread_id WHERE h.comment_id=? AND h.page_id=t.page_id AND h.page_id=?`, cid, pid).Scan(&n); err != nil || n != 3 {
				t.Fatalf("history linkage/count=%d %v", n, err)
			}
			if err := db.QueryRow(`SELECT COUNT(*) FROM page_artifact_comment_history WHERE comment_id=? AND action='edit' AND body='Edited body' AND actor_agent_id IN(SELECT id FROM agents WHERE slug='artifact-agent') AND actor_run_id='artifact-run'`, cid).Scan(&n); err != nil || n != 1 {
				t.Fatalf("agent audit lost=%d %v", n, err)
			}
			if err := db.QueryRow(`SELECT COUNT(*) FROM page_artifact_comment_reactions WHERE comment_id IN(SELECT c.id FROM page_artifact_comments c JOIN page_artifact_threads t ON t.id=c.thread_id WHERE t.page_id=? AND c.deleted_at IS NULL) AND user_id=? AND emoji='👀'`, pid, user).Scan(&n); err != nil || n != 1 {
				t.Fatalf("reaction lost=%d %v", n, err)
			}
			if _, err := db.Exec(`UPDATE page_artifact_comment_history SET body='rewritten' WHERE comment_id=?`, cid); err == nil {
				t.Fatal("restored history became mutable")
			}
			if _, err := db.Exec(`DELETE FROM pages WHERE id=?`, pid); err != nil {
				t.Fatal(err)
			}
			for _, table := range []string{"page_artifact_comment_history", "page_artifact_comment_reactions"} {
				if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil || n != 0 {
					t.Fatalf("%s cascade=%d %v", table, n, err)
				}
			}
		})
	}
}
