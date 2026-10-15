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
	source := artifactCall(t, h, ws, "commenter", "MEMBER", "GET", "", "")
	artifactExpect(t, source, 200)
	if !strings.Contains(source.Body.String(), `"can_comment":true`) || !strings.Contains(source.Body.String(), `"can_write":false`) {
		t.Fatal("commenter artifact capabilities incorrect: " + source.Body.String())
	}
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
			h, _, ws, owner, _ := discussionFixture(t)
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
			artifactExpect(t, pagesGrantCall(t, h, "PUT", "/", ws, owner, "OWNER", "report", `{"subject_type":"agent","subject":"comment-agent","level":"comment"}`), 200)
			grants, err = h.agentGrants(context.Background(), ws, rec, "comment-agent")
			if err != nil || len(grants) != 1 || grants[0].Level != pageGrantComment {
				t.Fatalf("explicit named agent grant missing: %v %v", grants, err)
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
	raw, err := os.ReadFile("../database/migrations/20261014114745_page_comment_grants.sql")
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

func TestPageCommentGrantArtifactReadCapabilities(t *testing.T) {
	h, _, ws, user, _ := discussionFixture(t)
	pagesSeedUser(t, h, ws, "plain-reader", "plain-reader@example.com", "MEMBER")
	pagesGrant(t, h, ws, user, "report", `{"subject_type":"user","subject":"plain-reader@example.com","level":"read"}`)
	rr := artifactCall(t, h, ws, "plain-reader", "MEMBER", "GET", "", "")
	artifactExpect(t, rr, 200)
	if !strings.Contains(rr.Body.String(), `"can_comment":false`) {
		t.Fatal("read implied comment")
	}
	rr = artifactCall(t, h, ws, user, "OWNER", "GET", "", "")
	artifactExpect(t, rr, 200)
	if !strings.Contains(rr.Body.String(), `"can_comment":true`) {
		t.Fatal("owner cannot comment")
	}
}

func TestPageCommentGrantAgentReadNeverClaimsCommentAuthority(t *testing.T) {
	h, workspace, _, _, token := artifactRunFixture(t)
	artifactExpect(t, artifactAgentSave(t, h, workspace, token, `{"html":"<p>agent</p>","base_rev":0,"mutation_id":"first"}`), 201)
	r := httptest.NewRequest("GET", "/", nil)
	r.SetPathValue("slug", "report")
	r.Header.Set("Authorization", "Bearer "+token)
	ctx := context.WithValue(r.Context(), ctxInternalTokenWS, workspace)
	ctx = context.WithValue(ctx, ctxInternalTokenCrew, "artifact-crew")
	rr := httptest.NewRecorder()
	h.GetArtifactAgent(rr, r.WithContext(ctx))
	artifactExpect(t, rr, 200)
	if !strings.Contains(rr.Body.String(), `"can_comment":false`) {
		t.Fatal("agent on behalf of human received comment capability")
	}
}

func TestPageGrantsInactiveIssuerDoesNotDelegateAuthority(t *testing.T) {
	for _, column := range []string{"suspended_at"} {
		t.Run(column, func(t *testing.T) {
			h, _, ws, owner, _ := discussionFixture(t)
			pagesSeedUser(t, h, ws, "grant-admin", "grant-admin@example.com", "ADMIN")
			pagesSeedUser(t, h, ws, "commenter", "commenter@example.com", "MEMBER")
			pagesGrant(t, h, ws, "grant-admin", "report", `{"subject_type":"user","subject":"commenter@example.com","level":"comment"}`)
			rr := artifactDiscussionCall(t, h, ws, "commenter", "MEMBER", "list", "", nil)
			artifactExpect(t, rr, 200)
			execOrFatal(t, h.db, `UPDATE users SET `+column+`='2026-10-13T00:00:00Z' WHERE id='grant-admin'`)
			artifactExpect(t, artifactDiscussionCall(t, h, ws, "commenter", "MEMBER", "list", "", nil), 404)
			// Another active owner may issue a fresh grant; no role is widened.
			pagesGrant(t, h, ws, owner, "report", `{"subject_type":"user","subject":"commenter@example.com","level":"comment"}`)
			artifactExpect(t, artifactDiscussionCall(t, h, ws, "commenter", "MEMBER", "list", "", nil), 200)
		})
	}
}
