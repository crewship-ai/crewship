package api

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func commentChangeCall(t *testing.T, h *PageHandler, ws, user, op, thread, comment, emoji string, payload any) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(payload)
	method := "PATCH"
	if op == "delete" {
		method = "DELETE"
	}
	if op == "react" {
		method = "PUT"
	}
	if op == "history" {
		method = "GET"
	}
	r := pagesGrantRequest(t, method, "/api/v1/pages/report/artifact/threads", ws, user, "OWNER", string(raw))
	r.SetPathValue("slug", "report")
	r.SetPathValue("threadId", thread)
	r.SetPathValue("commentId", comment)
	r.SetPathValue("emoji", emoji)
	w := httptest.NewRecorder()
	switch op {
	case "edit":
		h.EditArtifactComment(w, r)
	case "delete":
		h.DeleteArtifactComment(w, r)
	case "react":
		h.ReactArtifactComment(w, r)
	case "history":
		h.GetArtifactCommentHistory(w, r)
	}
	return w
}

func TestArtifactCommentChanges_CASRetryTombstoneAndAudit(t *testing.T) {
	h, _, ws, user, _ := discussionFixture(t)
	created := artifactDiscussionCreate(t, h, ws, user, "original", "create", 1)
	artifactExpect(t, created, 201)
	th := discussionThread(t, created)
	id := th.Comments[0].ID
	edit := map[string]any{"base_version": 1, "body": "revised", "mutation_id": "edit-one"}
	w := commentChangeCall(t, h, ws, user, "edit", th.ID, id, "", edit)
	artifactExpect(t, w, 200)
	th = discussionThread(t, w)
	if th.Version != 2 || th.Comments[0].Version != 2 || th.Comments[0].Body != "revised" || !th.Comments[0].CanEdit {
		t.Fatalf("edit: %+v", th)
	}
	artifactExpect(t, commentChangeCall(t, h, ws, user, "edit", th.ID, id, "", edit), 200)
	artifactExpect(t, commentChangeCall(t, h, ws, user, "edit", th.ID, id, "", map[string]any{"base_version": 1, "body": "changed retry", "mutation_id": "edit-one"}), 409)
	artifactExpect(t, commentChangeCall(t, h, ws, user, "edit", th.ID, id, "", map[string]any{"base_version": 1, "body": "stale", "mutation_id": "stale"}), 409)
	react := map[string]any{"base_version": 2, "active": true, "mutation_id": "react-one"}
	w = commentChangeCall(t, h, ws, user, "react", th.ID, id, "👀", react)
	artifactExpect(t, w, 200)
	th = discussionThread(t, w)
	if th.Version != 3 || len(th.Comments[0].Reactions) != 1 || !th.Comments[0].Reactions[0].ReactedByMe {
		t.Fatalf("reaction: %+v", th)
	}
	artifactExpect(t, commentChangeCall(t, h, ws, user, "react", th.ID, id, "👀", react), 200)
	del := map[string]any{"base_version": 3, "mutation_id": "delete-one"}
	w = commentChangeCall(t, h, ws, user, "delete", th.ID, id, "", del)
	artifactExpect(t, w, 200)
	th = discussionThread(t, w)
	c := th.Comments[0]
	if th.Version != 4 || c.Version != 3 || c.Body != "" || c.DeletedAt == "" || c.CanEdit || c.CanDelete || c.CanReact || !c.CanViewHistory || len(c.Reactions) != 0 {
		t.Fatalf("tombstone: %+v", th)
	}
	artifactExpect(t, commentChangeCall(t, h, ws, user, "delete", th.ID, id, "", del), 200)
	w = commentChangeCall(t, h, ws, user, "history", th.ID, id, "", nil)
	artifactExpect(t, w, 200)
	var result struct {
		History []struct {
			Version      int64
			Body, Action string
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.History) != 3 || result.History[0].Action != "delete" || result.History[1].Body != "revised" || result.History[2].Body != "original" {
		t.Fatalf("history: %s", w.Body.String())
	}
	if _, err := h.db.Exec(`UPDATE page_artifact_comment_history SET body='tampered' WHERE comment_id=?`, id); err == nil {
		t.Fatal("audit must be immutable")
	}
}

func TestArtifactCommentChanges_OtherHumanCannotRewriteAuthor(t *testing.T) {
	h, _, ws, user, page := discussionFixture(t)
	pagesSeedUser(t, h, ws, "comment-other", "other@example.test", "MEMBER")
	if _, err := h.db.Exec(`INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES (?,'user','comment-other','comment',?)`, page, user); err != nil {
		t.Fatal(err)
	}
	w := artifactDiscussionCreate(t, h, ws, user, "owned", "create", 1)
	artifactExpect(t, w, 201)
	th := discussionThread(t, w)
	id := th.Comments[0].ID
	artifactExpect(t, commentChangeCall(t, h, ws, "comment-other", "edit", th.ID, id, "", map[string]any{"base_version": 1, "body": "forged", "mutation_id": "foreign-edit"}), 403)
	artifactExpect(t, commentChangeCall(t, h, ws, "comment-other", "delete", th.ID, id, "", map[string]any{"base_version": 1, "mutation_id": "foreign-delete"}), 403)
	artifactExpect(t, commentChangeCall(t, h, ws, "comment-other", "history", th.ID, id, "", nil), 403)
	artifactExpect(t, commentChangeCall(t, h, ws, "comment-other", "react", th.ID, id, "👍", map[string]any{"base_version": 1, "active": true, "mutation_id": "foreign-react"}), 200)
	if _, err := h.db.Exec(`DELETE FROM page_grants WHERE page_id=? AND subject_id='comment-other'`, page); err != nil {
		t.Fatal(err)
	}
	artifactExpect(t, commentChangeCall(t, h, ws, "comment-other", "react", th.ID, id, "👍", map[string]any{"base_version": 2, "active": false, "mutation_id": "revoked-react"}), 404)
}

func TestArtifactCommentChanges_OnBehalfHumanIsNotAgentAuthor(t *testing.T) {
	h, ws, user, _, token, thread := agentCommentFixture(t)
	w := agentCommentCall(t, h, ws, token, thread, `{"body":"agent source","base_version":1,"mutation_id":"agent-body"}`, false)
	artifactExpect(t, w, 201)
	th := discussionThread(t, w)
	id := th.Comments[len(th.Comments)-1].ID
	for _, op := range []string{"edit", "delete"} {
		payload := map[string]any{"base_version": 2, "mutation_id": "human-" + op}
		if op == "edit" {
			payload["body"] = "not human authored"
		}
		artifactExpect(t, commentChangeCall(t, h, ws, user, op, thread, id, "", payload), 403)
	}
	artifactExpect(t, commentChangeCall(t, h, ws, user, "history", thread, id, "", nil), 403)
	artifactExpect(t, commentChangeCall(t, h, ws, user, "react", thread, id, "✅", map[string]any{"base_version": 2, "active": true, "mutation_id": "human-react-agent"}), 200)
}
