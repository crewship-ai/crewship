package api

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
)

func commentGrantFixture(t *testing.T, level string) (*PageHandler, string, string, string, func(string, string, string, string) *httptest.ResponseRecorder) {
	t.Helper()
	h, ws, owner, call := artifactProjectFixture(t)
	created := call(owner, "POST", "artifacts", `{"slug":"three-screens","name":"Comments",`+artifactProjectBody(0, "Shared one")[1:])
	if created.Code != 201 {
		t.Fatalf("create %d %s", created.Code, created.Body)
	}
	if shared := call(owner, "POST", "three-screens/artifact/share", `{"base_version":1,"version":1}`); shared.Code != 200 {
		t.Fatalf("share %d %s", shared.Code, shared.Body)
	}
	if private := call(owner, "POST", "three-screens/artifact/versions", artifactProjectBody(1, "Private two")); private.Code != 200 {
		t.Fatalf("private %d %s", private.Code, private.Body)
	}
	var page string
	if err := h.db.QueryRow(`SELECT id FROM pages WHERE workspace_id=? AND slug='three-screens'`, ws).Scan(&page); err != nil {
		t.Fatal(err)
	}
	user := "comment-grantee"
	mustExec(t, h.db, `INSERT INTO users(id,email,full_name) VALUES(?, 'comment-grantee@example.test','Grantee')`, user)
	mustExec(t, h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,?,'MEMBER')`, generateCUID(), ws, user)
	mustExec(t, h.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES(?,'user',?,?,?)`, page, user, level, owner)
	return h, page, owner, user, call
}

const sharedCommentBody = `{"version":1,"path":"index.html","selector":"#hero","quote":"Shared one","text":"Feedback only","send_to_agent":true}`

func TestArtifactCommentGrantSharedPinAndNoContentAuthority(t *testing.T) {
	h, page, owner, user, call := commentGrantFixture(t, "comment")
	read := call(user, "GET", "three-screens/artifact", "")
	var snapshot map[string]any
	if read.Code != 200 || json.Unmarshal(read.Body.Bytes(), &snapshot) != nil || snapshot["can_comment"] != true || snapshot["can_write"] != false || snapshot["origin"] != nil {
		t.Fatalf("commenter projection %d %s", read.Code, read.Body)
	}
	if got := call(user, "POST", "three-screens/artifact/comments", sharedCommentBody); got.Code != 201 {
		t.Fatalf("shared feedback %d %s", got.Code, got.Body)
	}
	var author string
	if err := h.db.QueryRow(`SELECT author FROM artifact_project_comments WHERE page_id=?`, page).Scan(&author); err != nil || author != user {
		t.Fatalf("feedback actor %q %v", author, err)
	}
	var taskCount, intents int
	if err := h.db.QueryRow(`SELECT count(*) FROM artifact_project_comment_tasks WHERE page_id=?`, page).Scan(&taskCount); err != nil || taskCount != 1 {
		t.Fatalf("tasks %d %v", taskCount, err)
	}
	if err := h.db.QueryRow(`SELECT count(*) FROM artifact_project_message_intents WHERE page_id=?`, page).Scan(&intents); err != nil || intents != 0 {
		t.Fatalf("commenter silently started owner intent: %d %v", intents, err)
	}
	for suffix, body := range map[string]string{"versions": artifactProjectBody(2, "Forbidden"), "restore": `{"base_version":2,"version":1}`, "share": `{"base_version":2,"version":2}`} {
		if got := call(user, "POST", "three-screens/artifact/"+suffix, body); got.Code != 403 {
			t.Errorf("commenter %s %d %s", suffix, got.Code, got.Body)
		}
	}
	if got := call(user, "POST", "three-screens/artifact/comments", `{"version":2,"path":"index.html","selector":"#hero","quote":"Private two","text":"Forbidden"}`); got.Code != 409 {
		t.Fatalf("private latest anchor %d %s", got.Code, got.Body)
	}
	if got := call(owner, "POST", "three-screens/artifact/comments", `{"version":2,"path":"index.html","selector":"#hero","quote":"Private two","text":"Owner remains writer"}`); got.Code != 201 {
		t.Fatalf("owner feedback %d %s", got.Code, got.Body)
	}
	var latest, shared int
	if err := h.db.QueryRow(`SELECT latest_version,shared_version FROM artifact_version_heads WHERE page_id=?`, page).Scan(&latest, &shared); err != nil || latest != 2 || shared != 1 {
		t.Fatalf("feedback changed content/pin: %d/%d %v", latest, shared, err)
	}
}

func TestArtifactReaderAndProducerCannotComment(t *testing.T) {
	for _, level := range []string{"read", "produce"} {
		t.Run(level, func(t *testing.T) {
			h, page, _, user, call := commentGrantFixture(t, level)
			if read := call(user, "GET", "three-screens/artifact", ""); read.Code != 200 {
				t.Fatalf("reader calibration %d", read.Code)
			}
			if got := call(user, "POST", "three-screens/artifact/comments", sharedCommentBody); got.Code != 403 {
				t.Fatalf("%s feedback allowed %d %s", level, got.Code, got.Body)
			}
			var count int
			if err := h.db.QueryRow(`SELECT count(*) FROM artifact_project_comments WHERE page_id=?`, page).Scan(&count); err != nil || count != 0 {
				t.Fatalf("denial inserted comment %d %v", count, err)
			}
		})
	}
}

func TestArtifactCommentGrantFreshRevocationAndABARollback(t *testing.T) {
	for _, change := range []string{"downgrade", "issuer-suspended", "aba", "pin"} {
		t.Run(change, func(t *testing.T) {
			h, page, owner, user, call := commentGrantFixture(t, "comment")
			var sql string
			switch change {
			case "downgrade":
				sql = fmt.Sprintf(`UPDATE page_grants SET level='read' WHERE page_id='%s' AND subject_id='%s';`, page, user)
			case "issuer-suspended":
				sql = fmt.Sprintf(`UPDATE users SET suspended_at=datetime('now') WHERE id='%s';`, owner)
			case "aba":
				sql = fmt.Sprintf(`DELETE FROM page_grants WHERE page_id='%s' AND subject_id='%s'; INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('%s','user','%s','comment','%s');`, page, user, page, user, owner)
			case "pin":
				sql = fmt.Sprintf(`UPDATE artifact_version_heads SET shared_version=2 WHERE page_id='%s';`, page)
			}
			mustExec(t, h.db, `CREATE TRIGGER comment_revoke_during_save AFTER INSERT ON artifact_project_comments BEGIN `+sql+` END`)
			got := call(user, "POST", "three-screens/artifact/comments", sharedCommentBody)
			if got.Code != 403 && got.Code != 404 {
				t.Fatalf("late %s not refused: %d %s", change, got.Code, got.Body)
			}
			var count int
			if err := h.db.QueryRow(`SELECT count(*) FROM artifact_project_comments WHERE page_id=?`, page).Scan(&count); err != nil || count != 0 {
				t.Fatalf("late denial committed comment %d %v", count, err)
			}
			mustExec(t, h.db, `DROP TRIGGER comment_revoke_during_save`)
			mustExec(t, h.db, `DELETE FROM page_grants WHERE page_id=? AND subject_id=?`, page, user)
			if again := call(user, "POST", "three-screens/artifact/comments", sharedCommentBody); again.Code != 403 && again.Code != 404 {
				t.Fatalf("revoked cache admitted %d %s", again.Code, again.Body)
			}
		})
	}
}

func TestPageCommentGrantSubjectIDAliasAndClosedBody(t *testing.T) {
	h, _, ws, owner, _ := pagesGrantFixture(t, "")
	for _, tc := range []struct {
		body   string
		status int
	}{
		{`{"subject_type":"user","subject_id":"` + owner + `","level":"comment"}`, 200},
		{`{"subject_type":"user","subject":"` + owner + `","subject_id":"different","level":"comment"}`, 400},
		{`{"subject_type":"user","subject_id":"` + owner + `","level":"comment","unexpected":true}`, 400},
		{`{"subject_type":"agent","subject":"arbitrary","level":"comment"}`, 400},
	} {
		got := pagesGrantCall(t, h, "PUT", "/api/v1/pages/fleet-201/grants", ws, owner, "OWNER", "fleet-201", tc.body)
		if got.Code != tc.status {
			t.Fatalf("grant %s: %d %s", tc.body, got.Code, got.Body)
		}
	}
}

func TestArtifactCommentGrantFolderMoveKeepsSharedAndOwnerAuthority(t *testing.T) {
	h, page, owner, user, call := commentGrantFixture(t, "comment")
	var ws, crew string
	if err := h.db.QueryRow(`SELECT workspace_id FROM pages WHERE id=?`, page).Scan(&ws); err != nil {
		t.Fatal(err)
	}
	if err := h.db.QueryRow(`SELECT id FROM crews WHERE workspace_id=? LIMIT 1`, ws).Scan(&crew); err != nil {
		t.Fatal(err)
	}
	mustExec(t, h.db, `INSERT INTO page_folders(id,workspace_id,slug,name,owner_crew_id,created_at,updated_at) VALUES('comment-folder',?,'comment-folder','Folder',?,datetime('now'),datetime('now'))`, ws, crew)
	mustExec(t, h.db, `UPDATE pages SET folder_id='comment-folder' WHERE id=?`, page)
	for _, actor := range []string{owner, user} {
		got := call(actor, "GET", "three-screens/artifact", "")
		if got.Code != 200 {
			t.Fatalf("folder artifact %s: %d %s", actor, got.Code, got.Body)
		}
	}
	if got := call(user, "POST", "three-screens/artifact/comments", sharedCommentBody); got.Code != 201 {
		t.Fatalf("folder comment %d %s", got.Code, got.Body)
	}
	mustExec(t, h.db, `DELETE FROM page_grants WHERE page_id=? AND subject_id=?`, page, user)
	mustExec(t, h.db, `INSERT INTO page_folder_acl(folder_id,subject_type,subject_id,can_read,can_write,set_at) VALUES('comment-folder','user',?,1,1,datetime('now'))`, user)
	// Explicit folder Edit grants the latest project content, even without a shared pin.
	mustExec(t, h.db, `UPDATE artifact_version_heads SET shared_version=0 WHERE page_id=?`, page)
	if got := call(user, "GET", "three-screens/artifact", ""); got.Code != 200 {
		t.Fatalf("folder editor latest %d %s", got.Code, got.Body)
	}
}
