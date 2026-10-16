package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestArtifactCrewGrantCannotReplaceIndependentAgentGrant(t *testing.T) {
	h, workspace, owner, page, token := artifactRunFixture(t)
	const published = `{"html":"<h1>Private source sentinel</h1>","base_rev":0,"mutation_id":"explicit-agent-publication"}`
	artifactExpect(t, artifactAgentSave(t, h, workspace, token, published), 201)
	created := artifactDiscussionCreate(t, h, workspace, owner, "Private discussion sentinel", "crew-grant-thread", 1)
	artifactExpect(t, created, 201)
	thread := discussionThread(t, created).ID
	const replied = `{"body":"Explicit agent reply sentinel","base_version":1,"mutation_id":"explicit-agent-reply"}`
	artifactExpect(t, agentCommentCall(t, h, workspace, token, thread, replied, false), 201)
	readSource := func() *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest("GET", "/api/v1/internal/artifacts/report?revision=1", nil)
		r.SetPathValue("slug", "report")
		r.Header.Set("Authorization", "Bearer "+token)
		ctx := context.WithValue(r.Context(), ctxInternalTokenWS, workspace)
		ctx = context.WithValue(ctx, ctxInternalTokenCrew, "artifact-crew")
		response := httptest.NewRecorder()
		h.GetArtifactAgent(response, r.WithContext(ctx))
		return response
	}
	artifactExpect(t, readSource(), 200)
	// The crew grant is real and remains usable by human crew members. It must
	// not transfer to the crew's agent, even with a verified human/run delegation.
	pagesSeedUser(t, h, workspace, "crew-human", "crew-human@example.test", "MEMBER")
	execOrFatal(t, h.db, `INSERT INTO crew_members(id,crew_id,user_id) VALUES('crew-human-member','artifact-crew','crew-human')`)
	pagesGrant(t, h, workspace, owner, "report", `{"subject_type":"crew","subject":"artifact-crew","level":"write"}`)
	execOrFatal(t, h.db, `DELETE FROM page_grants WHERE page_id=? AND subject_type='agent' AND subject_id='artifact-agent'`, page)
	artifactExpect(t, artifactCall(t, h, workspace, "crew-human", "MEMBER", "GET", "?revision=1", ""), 200)
	for _, operation := range []struct {
		name string
		call func() *httptest.ResponseRecorder
	}{
		{"source read", readSource},
		{"new publication", func() *httptest.ResponseRecorder {
			return artifactAgentSave(t, h, workspace, token, `{"html":"<h1>Denied</h1>","base_rev":1,"mutation_id":"crew-only-publication"}`)
		}},
		{"committed publication retry", func() *httptest.ResponseRecorder {
			return artifactAgentSave(t, h, workspace, token, published)
		}},
		{"discussion read", func() *httptest.ResponseRecorder {
			return agentCommentCall(t, h, workspace, token, thread, "", true)
		}},
		{"new reply", func() *httptest.ResponseRecorder {
			return agentCommentCall(t, h, workspace, token, thread, `{"body":"Denied","base_version":2,"mutation_id":"crew-only-reply"}`, false)
		}},
		{"committed reply retry", func() *httptest.ResponseRecorder {
			return agentCommentCall(t, h, workspace, token, thread, replied, false)
		}},
	} {
		t.Run(operation.name, func(t *testing.T) {
			response := operation.call()
			artifactExpect(t, response, 403)
			for _, private := range []string{"Private source sentinel", "Private discussion sentinel", "Explicit agent reply sentinel"} {
				if strings.Contains(response.Body.String(), private) {
					t.Fatalf("crew-only agent received private content: %s", response.Body.String())
				}
			}
		})
	}
	listed := artifactAgentToolRead(t, h, workspace, token, "/", true)
	artifactExpect(t, listed, 200)
	var result struct {
		Artifacts []json.RawMessage `json:"artifacts"`
	}
	if err := json.Unmarshal(listed.Body.Bytes(), &result); err != nil || result.Artifacts == nil || len(result.Artifacts) != 0 {
		t.Fatalf("crew-only grant exposed an artifact listing: %s %v", listed.Body.String(), err)
	}
	var head, revisions, receipts, rate, version, comments, mutations int
	if err := h.db.QueryRow(`SELECT head_rev,(SELECT COUNT(*) FROM page_artifact_revisions WHERE page_id=?),(SELECT COUNT(*) FROM page_artifact_mutation_receipts WHERE page_id=?),(SELECT COUNT(*) FROM artifact_workspace_writes WHERE workspace_id=?),(SELECT version FROM page_artifact_threads WHERE id=?),(SELECT COUNT(*) FROM page_artifact_comments WHERE thread_id=?),(SELECT COUNT(*) FROM page_artifact_comment_mutations WHERE page_id=?) FROM page_artifacts WHERE page_id=?`, page, page, workspace, thread, thread, page, page).Scan(&head, &revisions, &receipts, &rate, &version, &comments, &mutations); err != nil {
		t.Fatal(err)
	}
	if head != 1 || revisions != 1 || receipts != 1 || rate != 1 || version != 2 || comments != 2 || mutations != 2 {
		t.Fatalf("crew-only operations mutated protected state: head=%d sources=%d receipts=%d rate=%d thread=%d comments=%d comment_receipts=%d", head, revisions, receipts, rate, version, comments, mutations)
	}
	// Restore only the independent agent grant. The same bearer, human principal
	// and delegation must work again, proving their authority was not revoked.
	pagesGrant(t, h, workspace, owner, "report", `{"subject_type":"agent","subject":"artifact-agent","level":"write"}`)
	artifactExpect(t, readSource(), 200)
	artifactExpect(t, artifactAgentSave(t, h, workspace, token, published), 200)
	artifactExpect(t, agentCommentCall(t, h, workspace, token, thread, replied, false), 200)
}
