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

func agentRestoreCall(t *testing.T, h *PageHandler, ws, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("POST", "/", strings.NewReader(body))
	r.SetPathValue("slug", "report")
	r.Header.Set("Authorization", "Bearer "+token)
	ctx := context.WithValue(r.Context(), ctxInternalTokenWS, ws)
	ctx = context.WithValue(ctx, ctxInternalTokenCrew, "artifact-crew")
	w := httptest.NewRecorder()
	h.RestoreArtifactAgent(w, r.WithContext(ctx))
	return w
}

func TestArtifactAgentRestoreRetainedSourceCASProvenanceAndLiveRevocation(t *testing.T) {
	h, ws, user, page, token := artifactRunFixture(t)
	artifactExpect(t, artifactSave(t, h, ws, user, "<h1>Original</h1>", "human-first", 0), 201)
	artifactExpect(t, artifactSave(t, h, ws, user, "<h1>Latest human</h1>", "human-second", 1), 201)
	body := `{"revision":1,"base_rev":2,"mutation_id":"agent-restore"}`
	artifactExpect(t, agentRestoreCall(t, h, ws, token, body), 201)
	artifactExpect(t, agentRestoreCall(t, h, ws, token, body), 200)
	var revision, restored int
	var human, agent, run string
	if err := h.db.QueryRow(`SELECT revision,restored_from,author_user_id,author_agent_id,author_run_id FROM page_artifact_revisions WHERE page_id=? AND revision=3`, page).Scan(&revision, &restored, &human, &agent, &run); err != nil {
		t.Fatal(err)
	}
	if revision != 3 || restored != 1 || human != user || agent != "artifact-agent" || run != "artifact-run" {
		t.Fatalf("restore provenance %d %d %s %s %s", revision, restored, human, agent, run)
	}
	artifactExpect(t, agentRestoreCall(t, h, ws, token, `{"revision":2,"base_rev":2,"mutation_id":"agent-restore"}`), 409)
	artifactExpect(t, agentRestoreCall(t, h, ws, token, `{"revision":1,"base_rev":2,"mutation_id":"stale-restore"}`), 409)
	execOrFatal(t, h.db, `DELETE FROM page_grants WHERE page_id=? AND subject_type='agent'`, page)
	artifactExpect(t, agentRestoreCall(t, h, ws, token, body), 403)
	var head int
	if err := h.db.QueryRow(`SELECT head_rev FROM page_artifacts WHERE page_id=?`, page).Scan(&head); err != nil || head != 3 {
		t.Fatalf("head=%d %v", head, err)
	}
}

func TestArtifactAgentRestorePolicyDenialBeforeBlobRead(t *testing.T) {
	h, ws, user, _, token := artifactRunFixture(t)
	artifactExpect(t, artifactSave(t, h, ws, user, "<h1>Original</h1>", "human-first", 0), 201)
	// An absent source would produce404 if accessed. Policy must deny first.
	execOrFatal(t, h.db, `UPDATE workspace_artifact_creation_policy SET enabled=0 WHERE workspace_id=?`, ws)
	artifactExpect(t, agentRestoreCall(t, h, ws, token, `{"revision":999,"base_rev":1,"mutation_id":"policy-denied"}`), 403)
}

func TestArtifactAgentRestoreCompletesOnlyBoundRequestAtomically(t *testing.T) {
	h, workspace, user, page := requestFixture(t)
	id := createArtifactRequestForTest(t, h, workspace, user, requestBody, 202)
	h.artifactDispatch = func(ctx context.Context, _, _, _ string, opts ws.ChatMessageOption) error {
		execOrFatal(t, h.db, `INSERT INTO artifact_run_principals(run_id,workspace_id,agent_id,crew_id,human_user_id,chat_id,message_id,member_id,member_revision,chat_generation,chat_revision,expires_at) SELECT 'restore-request-run',workspace_id,agent_id,crew_id,human_user_id,chat_id,message_id,member_id,member_revision,chat_generation,chat_revision,expires_at FROM artifact_run_principals WHERE run_id='artifact-run'`)
		if err := bindArtifactRequestRun(httptest.NewRequest("POST", "/", nil), h.db, id, "restore-request-run", opts.HumanAuthority); err != nil {
			t.Fatal(err)
		}
		execOrFatal(t, h.db, `INSERT INTO journal_entries(id,workspace_id,crew_id,agent_id,entry_type,actor_type,summary,trace_id) VALUES('restore-request-start',?,'artifact-crew','artifact-agent','run.started','system','test','restore-request-run')`, workspace)
		token := internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(h.artifactMasterToken, workspace, "artifact-crew"), workspace, "artifact-agent", "restore-request-run")
		artifactExpect(t, agentRestoreCall(t, h, workspace, token, `{"revision":1,"base_rev":1,"mutation_id":"unbound-restore"}`), 403)
		body := fmt.Sprintf(`{"revision":1,"base_rev":1,"mutation_id":"bound-restore","request_id":%q}`, id)
		artifactExpect(t, agentRestoreCall(t, h, workspace, token, body), 201)
		artifactExpect(t, agentRestoreCall(t, h, workspace, token, body), 200)
		return nil
	}
	h.dispatchNextArtifactRequest(context.Background())
	var status string
	var result, head int
	if err := h.db.QueryRow(`SELECT r.status,r.result_revision,a.head_rev FROM page_artifact_requests r JOIN page_artifacts a ON a.page_id=r.page_id WHERE r.id=? AND r.page_id=?`, id, page).Scan(&status, &result, &head); err != nil || status != "succeeded" || result != 2 || head != 2 {
		t.Fatalf("result=%s/%d head=%d err=%v", status, result, head, err)
	}
}

func artifactAgentToolRead(t *testing.T, h *PageHandler, ws, token, path string, list bool) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("GET", path, nil)
	r.SetPathValue("slug", "report")
	r.Header.Set("Authorization", "Bearer "+token)
	ctx := context.WithValue(r.Context(), ctxInternalTokenWS, ws)
	ctx = context.WithValue(ctx, ctxInternalTokenCrew, "artifact-crew")
	w := httptest.NewRecorder()
	if list {
		h.ListArtifactsAgent(w, r.WithContext(ctx))
	} else {
		h.DiffArtifactAgent(w, r.WithContext(ctx))
	}
	return w
}

func TestArtifactAgentListExplicitScopeRevocationAndBoundedScan(t *testing.T) {
	h, ws, user, page, token := artifactRunFixture(t)
	artifactExpect(t, artifactSave(t, h, ws, user, "<h1>Original</h1>", "human-first", 0), 201)
	for i := 0; i < 101; i++ {
		id := fmt.Sprintf("listed-%03d", i)
		name := "Visible delegated"
		if i == 0 {
			name = "PRIVATE-REVOKED"
		}
		execOrFatal(t, h.db, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) SELECT ?,workspace_id,?,?,owner_user_id,spec_json FROM pages WHERE id=?`, id, id, name, page)
		execOrFatal(t, h.db, `INSERT INTO page_artifact_revisions(id,page_id,revision,sha256,author_user_id,mutation_id,request_digest,created_at) SELECT ?,?,1,sha256,author_user_id,'seed','seed',created_at FROM page_artifact_revisions WHERE page_id=? AND revision=1`, id+"-revision", id, page)
		execOrFatal(t, h.db, `INSERT INTO page_artifacts(page_id,head_rev) VALUES(?,1)`, id)
		execOrFatal(t, h.db, `INSERT INTO page_artifact_run_delegations(page_id,run_id,granted_by_user_id,expires_at,granted_at) SELECT ?,run_id,granted_by_user_id,expires_at,granted_at FROM page_artifact_run_delegations WHERE page_id=?`, id, page)
		execOrFatal(t, h.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES(?,'agent','artifact-agent','write',?)`, id, user)
	}
	execOrFatal(t, h.db, `DELETE FROM page_grants WHERE page_id='listed-000'`)
	execOrFatal(t, h.db, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) SELECT 'undelegated',workspace_id,'undelegated','HIDDEN-UNDELEGATED',owner_user_id,spec_json FROM pages WHERE id=?`, page)
	execOrFatal(t, h.db, `INSERT INTO page_artifacts(page_id,head_rev) VALUES('undelegated',1)`)
	var count int
	cursor := ""
	for scan := 0; scan < 3; scan++ {
		w := artifactAgentToolRead(t, h, ws, token, "/?workspace_id="+ws+"&cursor="+cursor, true)
		artifactExpect(t, w, 200)
		if strings.Contains(w.Body.String(), "PRIVATE-REVOKED") || strings.Contains(w.Body.String(), "HIDDEN-UNDELEGATED") {
			t.Fatal("catalog leaked denied metadata")
		}
		var result struct {
			Artifacts []map[string]any `json:"artifacts"`
			HasMore   bool             `json:"has_more"`
			Next      string           `json:"next_cursor"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if len(result.Artifacts) > 100 {
			t.Fatal("unbounded catalog")
		}
		count += len(result.Artifacts)
		if !result.HasMore {
			break
		}
		if result.Next == "" || result.Next == cursor {
			t.Fatal("catalog silently truncated")
		}
		cursor = result.Next
	}
	if count != 101 {
		t.Fatalf("visible delegated count=%d want101", count)
	}
	execOrFatal(t, h.db, `INSERT INTO journal_entries(id,workspace_id,crew_id,agent_id,entry_type,actor_type,summary,trace_id) VALUES('list-ended',?,'artifact-crew','artifact-agent','run.completed','system','test','artifact-run')`, ws)
	artifactExpect(t, artifactAgentToolRead(t, h, ws, token, "/", true), 403)
}

func TestArtifactAgentDiffExactHunkLimitsAndRevocation(t *testing.T) {
	h, ws, user, page, token := artifactRunFixture(t)
	artifactExpect(t, artifactSave(t, h, ws, user, "head\nold\ntail\n", "human-first", 0), 201)
	artifactExpect(t, artifactSave(t, h, ws, user, "head\nnew\ntail\n", "human-second", 1), 201)
	artifactExpect(t, artifactAgentToolRead(t, h, ws, token, "/?workspace_id=forged&from_revision=1&to_revision=2", false), 400)
	w := artifactAgentToolRead(t, h, ws, token, "/?workspace_id="+ws+"&from_revision=1&to_revision=2", false)
	artifactExpect(t, w, 200)
	var diff struct {
		Prefix, Suffix int
		Removed, Added []string
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(w.Body.Bytes(), &fields)
	_ = json.Unmarshal(fields["prefix_lines"], &diff.Prefix)
	_ = json.Unmarshal(fields["suffix_lines"], &diff.Suffix)
	_ = json.Unmarshal(fields["removed_lines"], &diff.Removed)
	_ = json.Unmarshal(fields["added_lines"], &diff.Added)
	if diff.Prefix != 1 || diff.Suffix != 2 || len(diff.Removed) != 1 || diff.Removed[0] != "old" || len(diff.Added) != 1 || diff.Added[0] != "new" {
		t.Fatalf("diff=%s", w.Body.String())
	}
	artifactExpect(t, artifactAgentToolRead(t, h, ws, token, "/?from_revision=1&to_revision=999", false), 404)
	artifactExpect(t, artifactAgentToolRead(t, h, ws, token, "/?from_revision=1&from_revision=2&to_revision=2", false), 400)
	artifactExpect(t, artifactSave(t, h, ws, user, strings.Repeat("x", maxArtifactDiffSourceBytes+1), "large", 2), 201)
	w = artifactAgentToolRead(t, h, ws, token, "/?from_revision=1&to_revision=3", false)
	artifactExpect(t, w, 413)
	if !strings.Contains(w.Body.String(), "No partial diff") || strings.Contains(w.Body.String(), "removed_lines") {
		t.Fatal("oversized diff lacked whole-result rejection")
	}
	execOrFatal(t, h.db, `DELETE FROM page_grants WHERE page_id=? AND subject_type='agent'`, page)
	artifactExpect(t, artifactAgentToolRead(t, h, ws, token, "/?from_revision=1&to_revision=2", false), 403)
}
