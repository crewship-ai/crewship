package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/ws"
)

func artifactRequestEditorFixture(t *testing.T) (*PageHandler, string, string, string) {
	t.Helper()
	h, workspace, user, page := requestFixture(t)
	pagesSeedUser(t, h, workspace, "artifact-owner", "artifact-owner@example.test", "MEMBER")
	execOrFatal(t, h.db, `UPDATE pages SET owner_user_id='artifact-owner' WHERE id=?`, page)
	execOrFatal(t, h.db, `UPDATE workspace_members SET role='MEMBER' WHERE workspace_id=? AND user_id=?`, workspace, user)
	execOrFatal(t, h.db, `UPDATE page_grants SET granted_by_user_id='artifact-owner' WHERE page_id=?`, page)
	execOrFatal(t, h.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES(?,'user',?,'write','artifact-owner')`, page, user)
	return h, workspace, user, page
}
func TestArtifactRequestEditorTrustedScopedReadPublishAndRevoke(t *testing.T) {
	h, workspace, user, page := artifactRequestEditorFixture(t)
	id := createArtifactRequestForTest(t, h, workspace, user, requestBody, 202)
	called := false
	h.artifactDispatch = func(ctx context.Context, actualUser, chat, prompt string, opts ws.ChatMessageOption) error {
		called = true
		if actualUser != user || opts.ArtifactRequestID != id || !strings.Contains(prompt, "/artifact-requests/"+id) {
			t.Fatal("missing scoped request contract")
		}
		r := httptest.NewRequest("POST", "/", nil)
		if err := captureArtifactChatPrincipal(r, h.db, "editor-request-run", workspace, "artifact-agent", "artifact-crew", "artifact-chat", "artifact-message", opts.HumanAuthority); err != nil {
			t.Fatal(err)
		}
		execOrFatal(t, h.db, `INSERT INTO journal_entries(id,workspace_id,crew_id,agent_id,entry_type,actor_type,summary,trace_id) VALUES('editor-start',?,'artifact-crew','artifact-agent','run.started','system','test','editor-request-run')`, workspace)
		if err := bindArtifactRequestRun(r, h.db, id, "editor-request-run", opts.HumanAuthority); err != nil {
			t.Fatal(err)
		}
		token := internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(h.artifactMasterToken, workspace, "artifact-crew"), workspace, "artifact-agent", "editor-request-run")
		artifactExpect(t, boardJobAgentCall(t, h, workspace, token, id, "", h.GetArtifactRequestAgent), 200)
		publish := fmt.Sprintf(`{"html":"<h1>Editor authorized result</h1>","base_rev":1,"mutation_id":"editor-result","request_id":%q}`, id)
		artifactExpect(t, artifactAgentSave(t, h, workspace, token, publish), 201)
		execOrFatal(t, h.db, `UPDATE page_grants SET level='read' WHERE page_id=? AND subject_type='user' AND subject_id=?`, page, user)
		artifactExpect(t, boardJobAgentCall(t, h, workspace, token, id, "", h.GetArtifactRequestAgent), 403)
		artifactExpect(t, artifactAgentSave(t, h, workspace, token, publish), 403)
		return nil
	}
	h.dispatchNextArtifactRequest(context.Background())
	if !called {
		t.Fatal("editor request not dispatched")
	}
	createArtifactRequestForTest(t, h, workspace, user, strings.Replace(requestBody, "first-request", "revoked", 1), 403)
}
func TestArtifactRequestDiscussionNotCopiedBeforeAdmission(t *testing.T) {
	h, workspace, user, page := artifactRequestEditorFixture(t)
	threadResponse := artifactDiscussionCreate(t, h, workspace, user, "PRIVATE_DISCUSSION_SENTINEL", "secret-thread", 1)
	artifactExpect(t, threadResponse, 201)
	thread := discussionThread(t, threadResponse)
	var body map[string]any
	_ = json.Unmarshal([]byte(requestBody), &body)
	body["thread_id"] = thread.ID
	raw, _ := json.Marshal(body)
	id := createArtifactRequestForTest(t, h, workspace, user, string(raw), 202)
	called := false
	h.artifactDispatch = func(_ context.Context, _ string, _ string, prompt string, opts ws.ChatMessageOption) error {
		called = true
		if strings.Contains(prompt, "PRIVATE_DISCUSSION_SENTINEL") {
			t.Fatal("discussion entered chat before admission")
		}
		execOrFatal(t, h.db, `UPDATE page_grants SET level='read' WHERE page_id=? AND subject_type='user' AND subject_id=?`, page, user)
		if bindArtifactRequestRun(httptest.NewRequest("POST", "/", nil), h.db, id, "artifact-run", opts.HumanAuthority) == nil {
			t.Fatal("revoked editor admitted")
		}
		return fmt.Errorf("admission refused")
	}
	h.dispatchNextArtifactRequest(context.Background())
	if !called {
		t.Fatal("request not dispatched")
	}
}
