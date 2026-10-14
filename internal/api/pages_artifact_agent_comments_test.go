package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/chataudience"
)

func agentCommentFixture(t *testing.T) (*PageHandler, string, string, string, string, string) {
	t.Helper()
	h, workspace, user, page, token := artifactRunFixture(t)
	artifactExpect(t, artifactSave(t, h, workspace, user, "<h1>Original</h1>", "original", 0), 201)
	response := artifactDiscussionCreate(t, h, workspace, user, "Original comment", "thread", 1)
	artifactExpect(t, response, 201)
	thread := discussionThread(t, response)
	execOrFatal(t, h.db, `DELETE FROM page_grants WHERE page_id=? AND subject_type='agent'`, page)
	pagesGrant(t, h, workspace, user, "report", `{"subject_type":"agent","subject":"artifact-agent","level":"comment"}`)
	return h, workspace, user, page, token, thread.ID
}
func agentCommentCall(t *testing.T, h *PageHandler, workspace, token, thread, body string, read bool) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("POST", "/", strings.NewReader(body))
	r.SetPathValue("slug", "report")
	r.SetPathValue("threadId", thread)
	r.Header.Set("Authorization", "Bearer "+token)
	ctx := context.WithValue(r.Context(), ctxInternalTokenWS, workspace)
	ctx = context.WithValue(ctx, ctxInternalTokenCrew, "artifact-crew")
	w := httptest.NewRecorder()
	if read {
		h.ListArtifactThreadsAgent(w, r.WithContext(ctx))
	} else {
		h.ReplyArtifactThreadAgent(w, r.WithContext(ctx))
	}
	return w
}
func TestArtifactAgentCommentsExplicitGrantProvenanceAndRetryIdentity(t *testing.T) {
	h, workspace, user, page, token, thread := agentCommentFixture(t)
	response := agentCommentCall(t, h, workspace, token, thread, "", true)
	artifactExpect(t, response, 200)
	if !strings.Contains(response.Body.String(), `"can_resolve":false`) {
		t.Fatal("agent may resolve")
	}
	body := `{"body":"Agent reply @someone is data","base_version":1,"mutation_id":"agent-reply"}`
	response = agentCommentCall(t, h, workspace, token, thread, body, false)
	artifactExpect(t, response, 201)
	artifactExpect(t, agentCommentCall(t, h, workspace, token, thread, body, false), 200)
	if !strings.Contains(response.Body.String(), `"author_agent_id":"artifact-agent"`) || !strings.Contains(response.Body.String(), `"author_run_id":"artifact-run"`) {
		t.Fatal(response.Body.String())
	}
	var author, agent, run string
	if h.db.QueryRow(`SELECT author_user_id,author_agent_id,author_run_id FROM page_artifact_comments WHERE thread_id=? AND thread_version=2`, thread).Scan(&author, &agent, &run) != nil || author != user || agent != "artifact-agent" || run != "artifact-run" {
		t.Fatal("wrong server actor")
	}
	var human map[string]any
	_ = json.Unmarshal([]byte(body), &human)
	artifactExpect(t, artifactDiscussionCall(t, h, workspace, user, "OWNER", "reply", thread, human), 409)
	execOrFatal(t, h.db, `INSERT INTO artifact_run_principals SELECT 'other-comment-run',workspace_id,agent_id,crew_id,human_user_id,chat_id,message_id,member_id,member_revision,chat_generation,chat_revision,expires_at FROM artifact_run_principals WHERE run_id='artifact-run'`)
	execOrFatal(t, h.db, `INSERT INTO journal_entries(id,workspace_id,crew_id,agent_id,entry_type,actor_type,summary,trace_id) VALUES('other-comment-start',?,'artifact-crew','artifact-agent','run.started','system','test','other-comment-run')`, workspace)
	other := internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(h.artifactMasterToken, workspace, "artifact-crew"), workspace, "artifact-agent", "other-comment-run")
	artifactExpect(t, agentCommentCall(t, h, workspace, other, thread, body, false), 403)
	artifactExpect(t, artifactAgentSave(t, h, workspace, token, `{"html":"escape","base_rev":1,"mutation_id":"escape"}`), 403)
	var version int
	if h.db.QueryRow(`SELECT version FROM page_artifact_threads WHERE id=?`, thread).Scan(&version) != nil || version != 2 {
		t.Fatal("denied actions changed discussion")
	}
	if _, err := h.db.Exec(`UPDATE page_artifact_comments SET author_run_id='forged' WHERE thread_id=? AND thread_version=2`, thread); err == nil {
		t.Fatal("agent provenance mutable")
	}
	if _, err := h.db.Exec(`UPDATE page_artifact_comment_mutations SET actor_agent_id=NULL WHERE page_id=? AND mutation_id='agent-reply'`, page); err == nil {
		t.Fatal("actor provenance mutable")
	}
}
func TestArtifactAgentCommentsFailClosedAndLiveWriterFence(t *testing.T) {
	for name, change := range map[string]string{
		"revoked grant":      `DELETE FROM page_grants WHERE subject_type='agent'`,
		"read only":          `UPDATE page_grants SET level='read' WHERE subject_type='agent'`,
		"produce only":       `UPDATE page_grants SET level='produce' WHERE subject_type='agent'`,
		"ended run":          `INSERT INTO journal_entries(id,workspace_id,agent_id,entry_type,actor_type,summary,trace_id) SELECT 'ended',workspace_id,'artifact-agent','run.completed','system','test','artifact-run' FROM pages WHERE slug='report'`,
		"suspended human":    `UPDATE users SET suspended_at='2026-10-13T00:00:00Z'`,
		"revoked membership": `DELETE FROM workspace_members`,
	} {
		t.Run(name, func(t *testing.T) {
			h, workspace, _, _, token, thread := agentCommentFixture(t)
			execOrFatal(t, h.db, change)
			artifactExpect(t, agentCommentCall(t, h, workspace, token, thread, "", true), 403)
			artifactExpect(t, agentCommentCall(t, h, workspace, token, thread, `{"body":"denied","base_version":1,"mutation_id":"denied"}`, false), 403)
		})
	}
	h, workspace, _, _, token, thread := agentCommentFixture(t)
	r := httptest.NewRequest("POST", "/", nil)
	r.SetPathValue("slug", "report")
	r.SetPathValue("threadId", thread)
	r.Header.Set("Authorization", "Bearer "+token)
	r.Body = &artifactRevokingBody{reader: strings.NewReader(`{"body":"revoked in upload","base_version":1,"mutation_id":"race"}`), revoke: func() { execOrFatal(t, h.db, `DELETE FROM page_grants WHERE subject_type='agent'`) }}
	ctx := context.WithValue(r.Context(), ctxInternalTokenWS, workspace)
	ctx = context.WithValue(ctx, ctxInternalTokenCrew, "artifact-crew")
	w := httptest.NewRecorder()
	h.ReplyArtifactThreadAgent(w, r.WithContext(ctx))
	artifactExpect(t, w, 403)
}
func TestArtifactAgentCommentsCannotForgeActorOrResolve(t *testing.T) {
	h, workspace, _, _, token, thread := agentCommentFixture(t)
	for _, extra := range []string{`"author_user_id":"forged"`, `"author_agent_id":"forged"`, `"author_run_id":"forged"`} {
		body := fmt.Sprintf(`{"body":"x","base_version":1,"mutation_id":"forged",%s}`, extra)
		artifactExpect(t, agentCommentCall(t, h, workspace, token, thread, body, false), 400)
	}
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"base_version":1,"resolved":true,"mutation_id":"resolve"}`))
	r.SetPathValue("slug", "report")
	r.SetPathValue("threadId", thread)
	r.Header.Set("Authorization", "Bearer "+token)
	ctx := context.WithValue(r.Context(), ctxInternalTokenWS, workspace)
	ctx = context.WithValue(ctx, ctxInternalTokenCrew, "artifact-crew")
	w := httptest.NewRecorder()
	checked, ok := h.agentCommentRequest(w, r.WithContext(ctx))
	if !ok {
		t.Fatal(w.Body.String())
	}
	h.ResolveArtifactThread(w, checked)
	artifactExpect(t, w, 403)
}

func TestArtifactAgentCommentReadPaginationIsBounded(t *testing.T) {
	h, workspace, user, _, token, _ := agentCommentFixture(t)
	for i := 0; i < 11; i++ {
		artifactExpect(t, artifactDiscussionCreate(t, h, workspace, user, fmt.Sprintf("thread %d", i), fmt.Sprintf("thread-%d", i), 1), 201)
	}
	read := func(cursor string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/?limit=10&cursor="+cursor, nil)
		r.SetPathValue("slug", "report")
		r.Header.Set("Authorization", "Bearer "+token)
		ctx := context.WithValue(r.Context(), ctxInternalTokenWS, workspace)
		ctx = context.WithValue(ctx, ctxInternalTokenCrew, "artifact-crew")
		w := httptest.NewRecorder()
		h.ListArtifactThreadsAgent(w, r.WithContext(ctx))
		return w
	}
	first := read("")
	artifactExpect(t, first, 200)
	var out struct {
		Threads []artifactThreadWire `json:"threads"`
		Cursor  string               `json:"next_cursor"`
	}
	if json.Unmarshal(first.Body.Bytes(), &out) != nil || len(out.Threads) != 10 || out.Cursor == "" {
		t.Fatal(first.Body.String())
	}
	seen := map[string]bool{}
	for _, thread := range out.Threads {
		seen[thread.ID] = true
	}
	second := read(out.Cursor)
	artifactExpect(t, second, 200)
	if json.Unmarshal(second.Body.Bytes(), &out) != nil || len(out.Threads) != 2 || out.Cursor != "" {
		t.Fatal(second.Body.String())
	}
	for _, thread := range out.Threads {
		if seen[thread.ID] {
			t.Fatal("pagination duplicated a thread")
		}
	}
}

func TestArtifactAgentCommentRequiresCurrentHumanAndSeparateIssuerRights(t *testing.T) {
	for _, revocation := range []string{"human comment", "issuer account"} {
		t.Run(revocation, func(t *testing.T) {
			h, workspace, user, page := artifactRequestEditorFixture(t)
			execOrFatal(t, h.db, `UPDATE page_grants SET level='comment' WHERE page_id=? AND subject_type='user' AND subject_id=?`, page, user)
			execOrFatal(t, h.db, `DELETE FROM page_grants WHERE page_id=? AND subject_type='agent'`, page)
			pagesGrant(t, h, workspace, "artifact-owner", "report", `{"subject_type":"agent","subject":"artifact-agent","level":"comment"}`)
			response := artifactDiscussionCreate(t, h, workspace, user, "Human commenter delegates reply only", "comment-only", 1)
			artifactExpect(t, response, 201)
			thread := discussionThread(t, response)
			receipt, err := chataudience.CaptureTrusted(context.Background(), h.db, "artifact-chat", user, nil)
			if err != nil || receipt == nil {
				t.Fatal(err)
			}
			if err = captureArtifactChatPrincipal(httptest.NewRequest("POST", "/", nil), h.db, "comment-only-run", workspace, "artifact-agent", "artifact-crew", "artifact-chat", "artifact-message", receipt); err != nil {
				t.Fatal(err)
			}
			execOrFatal(t, h.db, `INSERT INTO journal_entries(id,workspace_id,crew_id,agent_id,entry_type,actor_type,summary,trace_id) VALUES('comment-only-start',?,'artifact-crew','artifact-agent','run.started','system','test','comment-only-run')`, workspace)
			execOrFatal(t, h.db, `INSERT INTO page_artifact_run_delegations(page_id,run_id,granted_by_user_id,expires_at,granted_at) SELECT ?,run_id,human_user_id,expires_at,'2026-10-13T00:00:00Z' FROM artifact_run_principals WHERE run_id='comment-only-run'`, page)
			token := internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(h.artifactMasterToken, workspace, "artifact-crew"), workspace, "artifact-agent", "comment-only-run")
			artifactExpect(t, agentCommentCall(t, h, workspace, token, thread.ID, `{"body":"comment only agent reply","base_version":1,"mutation_id":"comment-only-agent"}`, false), 201)
			if revocation == "human comment" {
				execOrFatal(t, h.db, `UPDATE page_grants SET level='read' WHERE page_id=? AND subject_type='user' AND subject_id=?`, page, user)
			} else {
				execOrFatal(t, h.db, `UPDATE users SET suspended_at='2026-10-13T00:00:00Z' WHERE id='artifact-owner'`)
			}
			a := &artifactRunAuthority{WorkspaceID: workspace, AgentID: "artifact-agent", CrewID: "artifact-crew", RunID: "comment-only-run", HumanUserID: user, ChatID: "artifact-chat", MessageID: "artifact-message"}
			if err = h.checkArtifactRunPrincipal(context.Background(), a); err != nil {
				t.Fatalf("fixture principal unexpectedly revoked: %v", err)
			}
			artifactExpect(t, agentCommentCall(t, h, workspace, token, thread.ID, "", true), 403)
			artifactExpect(t, agentCommentCall(t, h, workspace, token, thread.ID, `{"body":"denied","base_version":2,"mutation_id":"revoked"}`, false), 403)
		})
	}
}

func TestArtifactAgentCommentsRequireDelegationBeforeReplyAndRetry(t *testing.T) {
	for _, change := range []string{
		`DELETE FROM page_artifact_run_delegations`,
		`UPDATE page_artifact_run_delegations SET revoked_at='2026-10-13T00:00:00Z'`,
		`UPDATE page_artifact_run_delegations SET expires_at='2000-01-01T00:00:00Z'`,
	} {
		t.Run(change, func(t *testing.T) {
			h, workspace, _, _, token, thread := agentCommentFixture(t)
			body := `{"body":"explicitly delegated reply","base_version":1,"mutation_id":"delegated-reply"}`
			artifactExpect(t, agentCommentCall(t, h, workspace, token, thread, body, false), 201)
			execOrFatal(t, h.db, change)
			artifactExpect(t, agentCommentCall(t, h, workspace, token, thread, body, false), 403)
			artifactExpect(t, agentCommentCall(t, h, workspace, token, thread, "", true), 403)
		})
	}
}
