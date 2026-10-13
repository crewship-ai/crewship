package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/pages"
)

func boardRunFixture(t *testing.T) (*PageHandler, string, string, string, string) {
	t.Helper()
	h, workspace, user, page, token := artifactRunFixture(t)
	artifactExpect(t, boardCall(t, h, workspace, user, "OWNER", "report", "init", `{"mutation_id":"init"}`), 200)
	grantBoardRunForTest(t, h, workspace, user, "artifact-run", 201)
	return h, workspace, user, page, token
}
func grantBoardRunForTest(t *testing.T, h *PageHandler, workspace, user, run string, want int) {
	t.Helper()
	r := pagesGrantRequest(t, "POST", "/", workspace, user, "OWNER", fmt.Sprintf(`{"run_id":%q}`, run))
	r.SetPathValue("slug", "report")
	rr := httptest.NewRecorder()
	h.GrantBoardRun(rr, r)
	artifactExpect(t, rr, want)
}
func boardAgentCall(t *testing.T, h *PageHandler, workspace, token, method, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, "/", strings.NewReader(body))
	r.SetPathValue("slug", "report")
	r.Header.Set("Authorization", "Bearer "+token)
	ctx := context.WithValue(r.Context(), ctxInternalTokenWS, workspace)
	ctx = context.WithValue(ctx, ctxInternalTokenCrew, "artifact-crew")
	rr := httptest.NewRecorder()
	if method == "GET" {
		h.GetBoardAgent(rr, r.WithContext(ctx))
	} else {
		h.PublishBoardAgent(rr, r.WithContext(ctx))
	}
	return rr
}
func boardAgentElement(t *testing.T, id string, changes map[string]any) json.RawMessage {
	t.Helper()
	raw := boardAPIElement(t)
	var fields map[string]any
	_ = json.Unmarshal(raw, &fields)
	fields["id"] = id
	for key, value := range changes {
		fields[key] = value
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func boardAgentBatch(t *testing.T, id string, seq, rev int64, raw json.RawMessage) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{"mutation_id": id, "base_seq": seq, "upserts": []map[string]any{{"element": raw, "expected_rev": rev}}, "deletes": []any{}})
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}
func TestBoardAgentOwnElementsHumanOwnershipAndRetryIdentity(t *testing.T) {
	h, workspace, user, page, token := boardRunFixture(t)
	body := boardAgentBatch(t, "agent-first", 0, 0, boardAgentElement(t, "own", nil))
	artifactExpect(t, boardAgentCall(t, h, workspace, token, "POST", body), 201)
	artifactExpect(t, boardAgentCall(t, h, workspace, token, "POST", body), 200)
	var human, agent, run string
	if err := h.db.QueryRow(`SELECT author_user_id,author_agent_id,author_run_id FROM page_board_elements WHERE board_page_id=? AND element_id='own'`, page).Scan(&human, &agent, &run); err != nil || human != user || agent != "artifact-agent" || run != "artifact-run" {
		t.Fatalf("provenance %q %q %q %v", human, agent, run, err)
	}
	rr := boardAgentCall(t, h, workspace, token, "GET", "")
	artifactExpect(t, rr, 200)
	if !strings.Contains(rr.Body.String(), `"write_scope":"own-elements"`) || !strings.Contains(rr.Body.String(), `"author_agent_id":"artifact-agent"`) {
		t.Fatal(rr.Body.String())
	}
	// The same human on whose behalf an agent published cannot replay its receipt
	// as if it were a human mutation, even with an identical payload.
	artifactExpect(t, boardCall(t, h, workspace, user, "OWNER", "report", "batch", body), 409)
	humanBody := boardAgentBatch(t, "human-edit", 1, 1, boardAgentElement(t, "own", map[string]any{"x": 300}))
	artifactExpect(t, boardCall(t, h, workspace, user, "OWNER", "report", "batch", humanBody), 201)
	var count int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM page_board_elements WHERE board_page_id=? AND author_user_id=? AND author_agent_id IS NULL AND author_run_id IS NULL`, page, user).Scan(&count); err != nil || count != 1 {
		t.Fatalf("human ownership not retained %d %v", count, err)
	}
	artifactExpect(t, boardAgentCall(t, h, workspace, token, "POST", boardAgentBatch(t, "cannot-overwrite", 2, 2, boardAgentElement(t, "own", map[string]any{"x": 999, "version": 999999999}))), 403)
	var seq int64
	if err := h.db.QueryRow(`SELECT seq FROM page_boards WHERE page_id=?`, page).Scan(&seq); err != nil || seq != 2 {
		t.Fatalf("denied batch advanced %d %v", seq, err)
	}
}
func TestBoardAgentAuthorityRevocationAndCurrentAccount(t *testing.T) {
	cases := map[string]string{
		"suspended human":    `UPDATE users SET suspended_at='2026-10-13T00:00:00Z' WHERE id=(SELECT owner_user_id FROM pages WHERE slug='report')`,
		"membership":         `DELETE FROM workspace_members`,
		"agent grant":        `DELETE FROM page_grants WHERE subject_type='agent'`,
		"revoked delegation": `UPDATE page_board_run_delegations SET revoked_at='2026-10-13T00:00:00Z'`,
		"expired delegation": `UPDATE page_board_run_delegations SET expires_at='2026-01-01T00:00:00Z'`,
		"generation":         `UPDATE chats SET authority_revision=authority_revision+1`,
		"ended run":          `INSERT INTO journal_entries(id,workspace_id,agent_id,entry_type,actor_type,summary,trace_id) SELECT 'end',workspace_id,'artifact-agent','run.completed','system','ended','artifact-run' FROM pages WHERE slug='report'`,
	}
	for name, statement := range cases {
		t.Run(name, func(t *testing.T) {
			h, workspace, _, _, token := boardRunFixture(t)
			execOrFatal(t, h.db, statement)
			artifactExpect(t, boardAgentCall(t, h, workspace, token, "POST", boardAgentBatch(t, "denied", 0, 0, boardAgentElement(t, "new", nil))), 403)
			artifactExpect(t, boardAgentCall(t, h, workspace, token, "GET", ""), 403)
		})
	}
}
func TestBoardAgentBatchAtomicProtectionAndNoClientProvenance(t *testing.T) {
	h, workspace, user, page, token := boardRunFixture(t)
	artifactExpect(t, boardCall(t, h, workspace, user, "OWNER", "report", "batch", boardAgentBatch(t, "human-first", 0, 0, boardAgentElement(t, "human", nil))), 201)
	raw, _ := json.Marshal(map[string]any{"mutation_id": "mixed", "base_seq": 1, "upserts": []map[string]any{{"element": boardAgentElement(t, "new", nil), "expected_rev": 0}, {"element": boardAgentElement(t, "human", map[string]any{"x": 500}), "expected_rev": 1}}})
	artifactExpect(t, boardAgentCall(t, h, workspace, token, "POST", string(raw)), 403)
	var count int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM page_board_elements WHERE board_page_id=? AND element_id='new'`, page).Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial batch committed %d %v", count, err)
	}
	body := boardAgentBatch(t, "forged", 1, 0, boardAgentElement(t, "new", map[string]any{"author_agent_id": "artifact-agent"}))
	artifactExpect(t, boardAgentCall(t, h, workspace, token, "POST", body), 400)
	artifactExpect(t, boardAgentCall(t, h, workspace, token, "POST", `{"mutation_id":"placement","base_seq":1,"placement_updates":[{"id":"pin","x":0,"y":0,"width":100,"height":100}]}`), 403)
	for _, bad := range []string{"", token + "bad", internaltoken.DeriveAgentToken(h.artifactMasterToken, workspace, "artifact-agent")} {
		artifactExpect(t, boardAgentCall(t, h, workspace, bad, "GET", ""), 403)
	}
}
func TestBoardAgentDelegationAuditAndCrossRunReceipt(t *testing.T) {
	h, workspace, user, page, token := boardRunFixture(t)
	var granted string
	if err := h.db.QueryRow(`SELECT granted_at FROM page_board_run_delegations WHERE board_page_id=?`, page).Scan(&granted); err != nil || granted == "" {
		t.Fatal(err)
	}
	grantBoardRunForTest(t, h, workspace, user, "artifact-run", 201)
	body := boardAgentBatch(t, "agent-first", 0, 0, boardAgentElement(t, "own", nil))
	artifactExpect(t, boardAgentCall(t, h, workspace, token, "POST", body), 201)
	execOrFatal(t, h.db, `INSERT INTO artifact_run_principals SELECT 'second-run',workspace_id,agent_id,crew_id,human_user_id,chat_id,message_id,member_id,member_revision,chat_generation,chat_revision,expires_at FROM artifact_run_principals WHERE run_id='artifact-run'`)
	execOrFatal(t, h.db, `INSERT INTO journal_entries(id,workspace_id,agent_id,entry_type,actor_type,summary,trace_id) VALUES('second-start',?,'artifact-agent','run.started','system','second','second-run')`, workspace)
	grantBoardRunForTest(t, h, workspace, user, "second-run", 201)
	second := internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(h.artifactMasterToken, workspace, "artifact-crew"), workspace, "artifact-agent", "second-run")
	artifactExpect(t, boardAgentCall(t, h, workspace, second, "POST", body), 409)
	// A new explicit run may continue the same agent's elements with current CAS.
	artifactExpect(t, boardAgentCall(t, h, workspace, second, "POST", boardAgentBatch(t, "second-edit", 1, 1, boardAgentElement(t, "own", map[string]any{"x": 50}))), 201)
	r := pagesGrantRequest(t, "DELETE", "/", workspace, user, "OWNER", "")
	r.SetPathValue("slug", "report")
	r.SetPathValue("runId", "second-run")
	for i := 0; i < 2; i++ {
		rr := httptest.NewRecorder()
		h.RevokeBoardRun(rr, r)
		artifactExpect(t, rr, 204)
	}
	artifactExpect(t, boardAgentCall(t, h, workspace, second, "GET", ""), 403)
	var by, when string
	if err := h.db.QueryRow(`SELECT revoked_by_user_id,revoked_at FROM page_board_run_delegations WHERE board_page_id=? AND run_id='second-run'`, page).Scan(&by, &when); err != nil || by != user || when == "" {
		t.Fatalf("revocation audit %q %q %v", by, when, err)
	}
	grantBoardRunForTest(t, h, workspace, user, "second-run", 403)
}
func TestBoardAgentProtectionGraph(t *testing.T) {
	cases := map[string][]json.RawMessage{
		"shared group":         {boardAgentElement(t, "human", map[string]any{"groupIds": []string{"shared"}}), boardAgentElement(t, "own", map[string]any{"groupIds": []string{"shared"}})},
		"protected parent":     {boardAgentElement(t, "human", map[string]any{"type": "frame", "name": "Human"}), boardAgentElement(t, "own", map[string]any{"frameId": "human"})},
		"protected descendant": {boardAgentElement(t, "own", map[string]any{"type": "frame", "name": "Agent"}), boardAgentElement(t, "nested", map[string]any{"type": "frame", "frameId": "own"}), boardAgentElement(t, "human", map[string]any{"frameId": "nested"})},
	}
	for name, elements := range cases {
		t.Run(name, func(t *testing.T) {
			current := pages.BoardScene{Records: map[string]pages.BoardRecord{}}
			authors := map[string]boardElementAuthor{}
			for _, raw := range elements {
				e, err := pages.ValidateBoardElement(raw)
				if err != nil {
					t.Fatal(err)
				}
				current.Records[e.ID] = pages.BoardRecord{Element: raw, ElementRev: 1}
				author := boardElementAuthor{AgentID: "artifact-agent"}
				if e.ID == "human" {
					author.AgentID = ""
				}
				authors[e.ID] = author
			}
			var fields map[string]any
			_ = json.Unmarshal(current.Records["own"].Element, &fields)
			fields["x"] = 100
			changed, _ := json.Marshal(fields)
			batch := pages.BoardBatch{Upserts: []pages.BoardUpsert{{Element: changed, ExpectedRev: 1}}}
			next, err := pages.ApplyBoardBatch(current, batch)
			if err != nil {
				t.Fatal(err)
			}
			if err := protectBoardHumanElements(current, next, batch, authors, "artifact-agent"); err == nil {
				t.Fatal("indirect protectedelement edit allowed")
			}
		})
	}
}
