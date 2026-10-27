package api

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestArtifactProjectReaderDoesNotSeePrivateAnswerMetadata(t *testing.T) {
	h, ws, owner, call := artifactProjectFixture(t)
	body := strings.TrimPrefix(artifactProjectBody(0, "Shared title"), "{")
	if w := call(owner, "POST", "artifacts", `{"slug":"three-screens","name":"Project",`+body); w.Code != 201 {
		t.Fatal(w.Code, w.Body)
	}
	w := call(owner, "POST", "three-screens/artifact/comments", `{"version":1,"path":"index.html","selector":"#hero","quote":"Shared title","text":"Change the title","send_to_agent":true}`)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body)
	}
	var comment artifactProjectComment
	if err := json.Unmarshal(w.Body.Bytes(), &comment); err != nil {
		t.Fatal(err)
	}
	if w = call(owner, "POST", "three-screens/artifact/share", `{"base_version":1,"version":1}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if w = call(owner, "POST", "three-screens/artifact/versions", artifactProjectBody(1, "Private title")); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	mustExec(t, h.db, `INSERT INTO artifact_project_comment_replies(id,comment_id,version,text,author,created_at) VALUES('private-answer',?,2,'Private change explained',?,datetime('now'))`, comment.ID, owner)
	mustExec(t, h.db, `UPDATE artifact_project_comment_tasks SET status='answered' WHERE comment_id=?`, comment.ID)
	const reader = "artifact-answer-reader"
	mustExec(t, h.db, `INSERT INTO users(id,email,full_name) VALUES(?,'artifact-answer-reader@example.test','Reader')`, reader)
	mustExec(t, h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('artifact-answer-member',?,?,'MEMBER')`, ws, reader)
	mustExec(t, h.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id,granted_at) SELECT id,'user',?,'read',?,datetime('now') FROM pages WHERE slug='three-screens'`, reader, owner)
	w = call(reader, "GET", "three-screens/artifact", "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	var snapshot struct {
		Comments []artifactProjectComment `json:"comments"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Comments) != 1 {
		t.Fatal("shared comment missing")
	}
	c := snapshot.Comments[0]
	if c.AnsweredVersion != 0 || c.Status == "answered" || c.Status == "working" || len(c.Replies) != 0 || strings.Contains(w.Body.String(), "Private change explained") {
		t.Fatalf("reader learned unpublished answer metadata: %s", w.Body)
	}
}
