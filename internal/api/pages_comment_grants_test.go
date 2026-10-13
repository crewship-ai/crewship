package api

import (
	"context"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestPageCommentGrantDiscussionWithoutWriteOrDispatch(t *testing.T) {
	h, _, ws, owner, pageID := discussionFixture(t)
	pagesSeedUser(t, h, ws, "commenter", "commenter@example.com", "MEMBER")
	pagesGrant(t, h, ws, owner, "report", `{"subject_type":"user","subject":"commenter@example.com","level":"comment"}`)
	rr := artifactDiscussionCall(t, h, ws, "commenter", "MEMBER", "list", "", nil)
	artifactExpect(t, rr, 200)
	if !strings.Contains(rr.Body.String(), `"can_comment":true`) || !strings.Contains(rr.Body.String(), `"can_resolve":false`) {
		t.Fatal(rr.Body.String())
	}
	payload := map[string]any{"revision": 1, "anchor": artifactCommentAnchor{SchemaVersion: 1, Type: "document"}, "body": "@agent Please consider this; no dispatch", "mutation_id": "commenter-create"}
	rr = artifactDiscussionCall(t, h, ws, "commenter", "MEMBER", "create", "", payload)
	artifactExpect(t, rr, 201)
	thread := discussionThread(t, rr)
	artifactExpect(t, artifactDiscussionCall(t, h, ws, "commenter", "MEMBER", "create", "", payload), 200)
	artifactExpect(t, artifactDiscussionCall(t, h, ws, "commenter", "MEMBER", "reply", thread.ID, map[string]any{"body": "Reply", "base_version": 1, "mutation_id": "commenter-reply"}), 201)
	artifactExpect(t, artifactDiscussionCall(t, h, ws, "commenter", "MEMBER", "resolve", thread.ID, map[string]any{"resolved": true, "base_version": 2, "mutation_id": "commenter-resolve"}), 403)
	artifactExpect(t, artifactCall(t, h, ws, "commenter", "MEMBER", "POST", "/revisions", `{"html":"<p>unauthorized</p>","base_rev":1,"mutation_id":"write"}`), 403)
	patch := pagesGrantRequest(t, "PATCH", "/", ws, "commenter", "MEMBER", `{"name":"Hijacked"}`)
	patch.SetPathValue("slug", "report")
	rr = httptest.NewRecorder()
	h.Update(rr, patch)
	artifactExpect(t, rr, 403)
	rec, err := h.loadPage(context.Background(), ws, "report")
	if err != nil {
		t.Fatal(err)
	}
	if h.mayEditSpec(pagesGrantRequest(t, "GET", "/", ws, "commenter", "MEMBER", "").Context(), ws, "commenter", "MEMBER", rec) {
		t.Fatal("comment implied write")
	}
	if allowed, _ := h.mayProduce(context.Background(), ws, "commenter", "MEMBER", rec, &panelRecord{ProducerKind: "script", PanelID: "p"}); allowed {
		t.Fatal("comment implied produce")
	}
	var assignments, requests int
	if err = h.db.QueryRow(`SELECT (SELECT COUNT(*) FROM assignments),(SELECT COUNT(*) FROM page_artifact_requests WHERE page_id=?)`, pageID).Scan(&assignments, &requests); err != nil || assignments != 0 || requests != 0 {
		t.Fatalf("comment dispatched: %d %d %v", assignments, requests, err)
	}
	// Revocation applies even to exact retries of successful comment mutations.
	if _, err = h.db.Exec(`DELETE FROM page_grants WHERE page_id=? AND subject_id='commenter'`, pageID); err != nil {
		t.Fatal(err)
	}
	artifactExpect(t, artifactDiscussionCall(t, h, ws, "commenter", "MEMBER", "create", "", payload), 404)
}
func TestPageCommentGrantCrewWorkspaceAndAgentBoundary(t *testing.T) {
	for _, subject := range []string{"crew", "workspace"} {
		t.Run(subject, func(t *testing.T) {
			h, _, ws, owner, pageID := discussionFixture(t)
			pagesSeedUser(t, h, ws, "commenter", "commenter@example.com", "MEMBER")
			body := `{"subject_type":"workspace","level":"comment"}`
			if subject == "crew" {
				if _, err := h.db.Exec(`INSERT INTO crew_members(id,crew_id,user_id) VALUES('comment-member','crew-lookout','commenter')`); err != nil {
					t.Fatal(err)
				}
				body = `{"subject_type":"crew","subject":"lookout","level":"comment"}`
			}
			pagesGrant(t, h, ws, owner, "report", body)
			rr := artifactDiscussionCall(t, h, ws, "commenter", "MEMBER", "list", "", nil)
			artifactExpect(t, rr, 200)
			if !strings.Contains(rr.Body.String(), `"can_comment":true`) {
				t.Fatal(rr.Body.String())
			}
			pagesSeedAgent(t, h, ws, "comment-agent", "comment-agent", "crew-lookout")
			rec, err := h.loadPage(context.Background(), ws, "report")
			if err != nil {
				t.Fatal(err)
			}
			grants, err := h.agentGrants(context.Background(), ws, rec, "comment-agent")
			if err != nil || len(grants) != 0 {
				t.Fatalf("humancomment grant reached agent: %v %v", grants, err)
			}
			artifactExpect(t, pagesGrantCall(t, h, "PUT", "/", ws, owner, "OWNER", "report", `{"subject_type":"agent","subject":"comment-agent","level":"comment"}`), 400)
			if _, err = h.db.Exec(`INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES(?,'agent','comment-agent','comment',?)`, pageID, owner); err == nil {
				t.Fatal("DB permits agentcomment")
			}
			if _, err = h.db.Exec(`DELETE FROM workspace_members WHERE workspace_id=? AND user_id=?`, ws, owner); err != nil {
				t.Fatal(err)
			}
			artifactExpect(t, artifactDiscussionCall(t, h, ws, "commenter", "MEMBER", "list", "", nil), 404)
		})
	}
}
func TestPageCommentGrantMigrationPreservesExistingRows(t *testing.T) {
	h, _, ws, owner, pageID := discussionFixture(t)
	pagesSeedUser(t, h, ws, "commenter", "commenter@example.com", "MEMBER")
	pagesGrant(t, h, ws, owner, "report", `{"subject_type":"user","subject":"commenter@example.com","level":"read"}`)
	pagesGrant(t, h, ws, owner, "report", `{"subject_type":"user","subject":"commenter@example.com","level":"write"}`)
	var before string
	if err := h.db.QueryRow(`SELECT group_concat(level||':'||granted_by_user_id||':'||granted_at) FROM (SELECT * FROM page_grants WHERE page_id=? ORDER BY level)`, pageID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../database/migrations/20261013183111_page_comment_grants.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.db.Exec(string(raw)); err != nil {
		t.Fatal(err)
	}
	var after string
	if err = h.db.QueryRow(`SELECT group_concat(level||':'||granted_by_user_id||':'||granted_at) FROM (SELECT * FROM page_grants WHERE page_id=? ORDER BY level)`, pageID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("existing grants changed %q -> %q", before, after)
	}
}
