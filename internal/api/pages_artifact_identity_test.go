package api

import (
	"database/sql"
	"net/http/httptest"
	"testing"
)

// Seed the data model directly so this privacy foundation stays independent of
// HTTP handlers. Erasure itself goes through the production GDPR transaction.
func artifactIdentityDataFixture(t *testing.T) (*sql.DB, string, string) {
	t.Helper()
	db := setupTestDB(t)
	user := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, user)
	mustExec(t, db, `INSERT INTO workspaces(id,name,slug) VALUES('identity-other-ws','Other','identity-other-ws')`)
	for _, scope := range []struct{ ws, page string }{{ws, "identity-page"}, {"identity-other-ws", "identity-other-page"}} {
		mustExec(t, db, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json,private_audience) VALUES(?,?,?,?,?,'{}',1)`, scope.page, scope.ws, scope.page, "Retained native content", user)
		mustExec(t, db, `INSERT INTO artifact_version_heads(workspace_id,page_id,latest_version,content_type) VALUES(?,?,2,'wordcraft')`, scope.ws, scope.page)
		for i, actor := range []string{user, "user:" + user} {
			mustExec(t, db, `INSERT INTO artifact_versions(workspace_id,page_id,version,manifest_json,actor,created_at) VALUES(?,?,?,'{"entry_point":"index.html","files":{}}',?,datetime('now'))`, scope.ws, scope.page, i+1, actor)
		}
		mustExec(t, db, `INSERT INTO artifact_version_blobs(workspace_id,page_id,hash,content) VALUES(?,?,'retained-binary',x'00ff80')`, scope.ws, scope.page)
	}
	mustExec(t, db, `INSERT INTO artifact_project_comments(id,workspace_id,page_id,version,path,selector,quote,text,author,created_at) VALUES('identity-comment',?,'identity-page',1,'index.html','#hero','Retained','Keep history',?,datetime('now'))`, ws, user)
	mustExec(t, db, `INSERT INTO artifact_project_comment_replies(id,comment_id,version,text,author,created_at) VALUES('identity-reply','identity-comment',2,'Updated',?,datetime('now'))`, user)
	mustExec(t, db, `INSERT INTO artifact_project_origins(page_id,workspace_id,human_user_id,message_id,created_at) VALUES('identity-page',?,?,'synthetic-original-message',datetime('now'))`, ws, user)
	return db, ws, user
}

func TestArtifactProjectIdentityErasurePreservesHistoryAndWorkspaceScope(t *testing.T) {
	db, ws, user := artifactIdentityDataFixture(t)
	eraser := NewAdminGDPRHandler(db, newTestLogger(), t.TempDir())
	if _, err := eraser.eraseSubjectIdentity(t.Context(), "artifact-erasure-action", user, ws, user); err != nil {
		t.Fatal(err)
	}
	for _, version := range []int{1, 2} {
		var actor, manifest, other string
		if err := db.QueryRow(`SELECT actor,manifest_json FROM artifact_versions WHERE workspace_id=? AND version=?`, ws, version).Scan(&actor, &manifest); err != nil || actor != "erased" || manifest != `{"entry_point":"index.html","files":{}}` {
			t.Errorf("version%d identity/content: actor=%q manifest=%q: %v", version, actor, manifest, err)
		}
		want := user
		if version == 2 {
			want = "user:" + user
		}
		if err := db.QueryRow(`SELECT actor FROM artifact_versions WHERE workspace_id='identity-other-ws' AND version=?`, version).Scan(&other); err != nil || other != want {
			t.Errorf("foreign workspace version%d changed: %q: %v", version, other, err)
		}
	}
	for _, query := range []string{
		`SELECT count(*) FROM artifact_project_comments WHERE workspace_id=? AND author=?`,
		`SELECT count(*) FROM artifact_project_comment_replies WHERE comment_id IN(SELECT id FROM artifact_project_comments WHERE workspace_id=?) AND author=?`,
		`SELECT count(*) FROM artifact_project_origins WHERE workspace_id=? AND human_user_id=?`,
	} {
		var n int
		if err := db.QueryRow(query, ws, user).Scan(&n); err != nil || n != 0 {
			t.Errorf("identity remains: count%d: %v", n, err)
		}
	}
	var versions, heads, blobs int
	if err := db.QueryRow(`SELECT count(*) FROM artifact_versions WHERE workspace_id=?`, ws).Scan(&versions); err != nil || versions != 2 {
		t.Errorf("history lost: %d: %v", versions, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM artifact_version_heads WHERE workspace_id=? AND content_type='wordcraft' AND latest_version=2`, ws).Scan(&heads); err != nil || heads != 1 {
		t.Errorf("head changed: %d: %v", heads, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM artifact_version_blobs WHERE workspace_id=? AND content=x'00ff80'`, ws).Scan(&blobs); err != nil || blobs != 1 {
		t.Errorf("binary content changed: %d: %v", blobs, err)
	}
}

func TestArtifactProjectIdentityErasureRollsBackAsOneTransaction(t *testing.T) {
	db, ws, user := artifactIdentityDataFixture(t)
	mustExec(t, db, `CREATE TRIGGER refuse_feedback_identity BEFORE UPDATE ON artifact_project_comment_replies BEGIN SELECT RAISE(ABORT,'synthetic privacy failure'); END`)
	eraser := NewAdminGDPRHandler(db, newTestLogger(), t.TempDir())
	if _, err := eraser.eraseSubjectIdentity(t.Context(), "artifact-erasure-rollback", user, ws, user); err == nil {
		t.Fatal("failed erasure acknowledged")
	}
	var actor string
	if err := db.QueryRow(`SELECT actor FROM artifact_versions WHERE workspace_id=? AND version=1`, ws).Scan(&actor); err != nil || actor != user {
		t.Fatalf("partial attribution committed: %q: %v", actor, err)
	}
}

func TestArtifactProjectSharedPinDoesNotExposePrivateHistoryThroughBackups(t *testing.T) {
	h, _, _, ws, user := newPagesFixture(t)
	db := h.db
	// A shared snapshot leaves all other immutable versions private. This is the
	// only Page in this workspace, and its legacy private_audience bit is off.
	mustExec(t, db, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json,private_audience) VALUES('backup-project',?,'backup-project','Project',?,'{}',0)`, ws, user)
	mustExec(t, db, `INSERT INTO artifact_version_heads(workspace_id,page_id,latest_version,shared_version,content_type) VALUES(?,'backup-project',2,1,'artifact_project')`, ws)
	for _, version := range []int{1, 2} {
		mustExec(t, db, `INSERT INTO artifact_versions(workspace_id,page_id,version,manifest_json,actor,created_at) VALUES(?,'backup-project',?,'{"entry_point":"index.html","files":{}}',?,datetime('now'))`, ws, version, user)
	}
	mustExec(t, db, `INSERT INTO users(id,email,full_name) VALUES('identity-backup-admin','identity-backup@example.test','Admin')`)
	mustExec(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('identity-backup-member',?,'identity-backup-admin','ADMIN')`, ws)
	backup := &BackupHandler{db: db, logger: newTestLogger()}
	req := pagesRequest(t, "GET", "/api/v1/admin/backups/status", ws, "identity-backup-admin", "ADMIN", "")
	got := httptest.NewRecorder()
	if backup.authorizeWorkspaceBackups(got, req) || got.Code != 403 {
		t.Fatalf("shared pin exposed private history: %d %s", got.Code, got.Body)
	}
	req = pagesRequest(t, "GET", "/api/v1/admin/backups/status", ws, user, "OWNER", "")
	if !backup.authorizeWorkspaceBackups(httptest.NewRecorder(), req) {
		t.Fatal("workspace custodian denied backup")
	}
}

func TestArtifactVersionHistoryPreservesNativeCustodyClassification(t *testing.T) {
	db, ws, user := artifactIdentityDataFixture(t)
	mustExec(t, db, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json,private_audience) VALUES('identity-grid-page',?,'identity-grid','Native workbook',?,'{}',1)`, ws, user)
	mustExec(t, db, `INSERT INTO artifact_version_heads(workspace_id,page_id,latest_version,content_type) VALUES(?,'identity-grid-page',1,'gridcraft')`, ws)
	mustExec(t, db, `INSERT INTO gridcraft_heads(page_id,workspace_id,schema_version,head_rev,generation,snapshot) VALUES('identity-grid-page',?,1,1,1,x'7b7d')`, ws)
	_, kind, err := privatePageOwnershipSQL(t.Context(), db, "p")
	if err != nil {
		t.Fatal(err)
	}
	var got string
	if err = db.QueryRow("SELECT " + kind + " FROM pages p WHERE id='identity-grid-page'").Scan(&got); err != nil || got != "workbook" {
		t.Fatalf("native history changes custody: %q: %v", got, err)
	}
}
