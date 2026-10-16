package api

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
)

func TestArtifactCopiesBackupForkPreservesDetachedReceiptsAndIndependentBytes(t *testing.T) {
	h, _, _, ws, user, source := artifactFixture(t)
	artifactExpect(t, artifactSave(t, h, ws, user, "<h1>Independent source</h1>", "copy-storage-source", 0), 201)
	copyPage := func(sourceSlug, targetSlug string) string {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"source_revision": 1, "slug": targetSlug, "name": "Independent copy", "mutation_id": targetSlug})
		req := pagesGrantRequest(t, "POST", "/api/v1/pages/"+sourceSlug+"/artifact/copies", ws, user, "OWNER", string(body))
		req.SetPathValue("slug", sourceSlug)
		rr := httptest.NewRecorder()
		h.CopyArtifact(rr, req)
		artifactExpect(t, rr, 201)
		var id string
		if err := h.db.QueryRow(`SELECT id FROM pages WHERE slug=?`, targetSlug).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	live := copyPage("report", "copied-live")
	deleted := copyPage("report", "copied-deleted")
	descendant := copyPage("copied-live", "copied-descendant")
	// Durable receipts survive both deleted Page references and historical source
	// or target revisions that are no longer retained. The copy's own blob is
	// authoritative; the receipt is not an extra blob/revision pin.
	execOrFatal(t, h.db, `DELETE FROM pages WHERE id=?`, deleted)
	execOrFatal(t, h.db, `DELETE FROM pages WHERE id=?`, source)
	execOrFatal(t, h.db, `INSERT INTO page_artifact_revisions(id,page_id,revision,sha256,author_user_id,mutation_id,request_digest,created_at) SELECT 'copy-storage-next-revision',page_id,2,sha256,author_user_id,'copy-storage-next','copy-storage-next',created_at FROM page_artifact_revisions WHERE page_id=? AND revision=1`, live)
	execOrFatal(t, h.db, `UPDATE page_artifacts SET head_rev=2 WHERE page_id=?`, live)
	execOrFatal(t, h.db, `DELETE FROM page_artifact_revisions WHERE page_id=? AND revision=1`, live)
	const pass = "artifact-copy-storage-roundtrip-password"
	made, err := backup.CreateBackup(t.Context(), h.db, backup.CreateOptions{Scope: backup.ScopeWorkspace, WorkspaceID: ws, OutputDir: t.TempDir(), Passphrase: pass, Actor: backup.Actor{UserID: user, Role: "ADMIN"}, AttachmentRoot: h.artifactStoragePath})
	if err != nil {
		t.Fatal(err)
	}
	for _, fork := range []string{"", "copy-storage-fork"} {
		t.Run(fork, func(t *testing.T) {
			db := setupTestDB(t)
			root := storageDir(t)
			restored, err := backup.RestoreBackup(t.Context(), db, backup.RestoreOptions{Path: made.Path, Passphrase: pass, AsWorkspace: fork, Actor: backup.Actor{UserID: user, Role: "ADMIN"}, AttachmentRoot: root})
			if err != nil {
				t.Fatal(err)
			}
			var n int
			if err := db.QueryRow(`SELECT COUNT(*) FROM page_artifact_copies WHERE workspace_id=?`, restored.RestoredWorkspaceID).Scan(&n); err != nil || n != 3 {
				t.Fatalf("durable receipts=%d %v", n, err)
			}
			if err := db.QueryRow(`SELECT COUNT(*) FROM page_artifact_copies WHERE workspace_id=? AND source_page_id IS NULL AND target_page_id IS NULL AND mutation_id='copied-deleted'`, restored.RestoredWorkspaceID).Scan(&n); err != nil || n != 1 {
				t.Fatalf("deleted source/target receipt=%d %v", n, err)
			}
			var target, sha string
			if err := db.QueryRow(`SELECT c.target_page_id,v.sha256 FROM page_artifact_copies c JOIN pages p ON p.id=c.target_page_id JOIN page_artifact_revisions v ON v.page_id=p.id AND v.revision=1 WHERE c.workspace_id=? AND c.mutation_id='copied-descendant' AND c.source_page_id IN(SELECT id FROM pages WHERE slug='copied-live')`, restored.RestoredWorkspaceID).Scan(&target, &sha); err != nil {
				t.Fatal(err)
			}
			if fork != "" && target == descendant {
				t.Fatal("fork target was not remapped")
			}
			data, err := readAttachmentBlob(root, restored.RestoredWorkspaceID, sha)
			if err != nil || string(data) != "<h1>Independent source</h1>" {
				t.Fatalf("independent bytes=%q %v", data, err)
			}
			if _, err := db.Exec(`UPDATE page_artifact_copies SET source_revision=99 WHERE workspace_id=?`, restored.RestoredWorkspaceID); err == nil {
				t.Fatal("restored provenance became mutable")
			}
			if err := db.QueryRow(`SELECT COUNT(*) FROM artifact_run_principals`).Scan(&n); err != nil || n != 0 {
				t.Fatalf("copy restored live authority=%d %v", n, err)
			}
		})
	}
}
