package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func boardPermissionCall(t *testing.T, h *PageHandler, ws, user, operation, id string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(body)
	r := pagesGrantRequest(t, "POST", "/", ws, user, "OWNER", string(raw))
	r.SetPathValue("slug", "report")
	r.SetPathValue("permissionId", id)
	w := httptest.NewRecorder()
	switch operation {
	case "grant":
		h.GrantBoardElementPermission(w, r)
	case "list":
		h.ListBoardElementPermissions(w, r)
	case "revoke":
		h.RevokeBoardElementPermission(w, r)
	}
	return w
}
func grantBoardElementForTest(t *testing.T, h *PageHandler, ws, user string, methods []string) boardElementPermission {
	t.Helper()
	w := boardPermissionCall(t, h, ws, user, "grant", "", map[string]any{"mutation_id": "permission-intent", "run_id": "artifact-run", "elements": []boardPermissionElement{{"human", 1}}, "methods": methods, "purpose": "Update the selected drawing", "expires_in_seconds": 300})
	if w.Code != 200 && w.Code != 201 {
		t.Fatalf("grant status %d %s", w.Code, w.Body.String())
	}
	var p boardElementPermission
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	return p
}
func boardPermissionBatch(t *testing.T, body, id string) string {
	t.Helper()
	var req map[string]any
	json.Unmarshal([]byte(body), &req)
	req["element_permission_id"] = id
	raw, _ := json.Marshal(req)
	return string(raw)
}
func TestBoardElementPermissionOneBatchPreservesHumanOwnership(t *testing.T) {
	h, ws, user, page, token := boardRunFixture(t)
	artifactExpect(t, boardCall(t, h, ws, user, "OWNER", "report", "batch", boardAgentBatch(t, "human", 0, 0, boardAgentElement(t, "human", nil))), 201)
	p := grantBoardElementForTest(t, h, ws, user, []string{"update"})
	w := boardPermissionCall(t, h, ws, user, "list", "", nil)
	artifactExpect(t, w, 200)
	if !strings.Contains(w.Body.String(), `"eligible_runs":[{"agent_id":"artifact-agent"`) {
		t.Fatal(w.Body.String())
	}
	body := boardPermissionBatch(t, boardAgentBatch(t, "authorized", 1, 1, boardAgentElement(t, "human", map[string]any{"x": 321})), p.ID)
	artifactExpect(t, boardAgentCall(t, h, ws, token, "POST", body), 201)
	artifactExpect(t, boardAgentCall(t, h, ws, token, "POST", body), 200)
	retry := grantBoardElementForTest(t, h, ws, user, []string{"update"})
	if retry.ID != p.ID || !retry.Used {
		t.Fatalf("grant retry widened/reset authority: %+v", retry)
	}

	var human string
	var agent, run *string
	if err := h.db.QueryRow(`SELECT author_user_id,author_agent_id,author_run_id FROM page_board_elements WHERE board_page_id=? AND element_id='human'`, page).Scan(&human, &agent, &run); err != nil || human != user || agent != nil || run != nil {
		t.Fatalf("human protection lost %s %v %v %v", human, agent, run, err)
	}
	var actorAgent, actorRun string
	if err := h.db.QueryRow(`SELECT author_agent_id,author_run_id FROM page_board_mutations WHERE board_page_id=? AND mutation_id='authorized'`, page).Scan(&actorAgent, &actorRun); err != nil || actorAgent != "artifact-agent" || actorRun != "artifact-run" {
		t.Fatalf("agent audit lost %s %s %v", actorAgent, actorRun, err)
	}
	next := boardPermissionBatch(t, boardAgentBatch(t, "reuse", 2, 2, boardAgentElement(t, "human", map[string]any{"x": 999})), p.ID)
	artifactExpect(t, boardAgentCall(t, h, ws, token, "POST", next), 403)
	artifactExpect(t, boardAgentCall(t, h, ws, token, "POST", boardAgentBatch(t, "no-permission", 2, 2, boardAgentElement(t, "human", nil))), 403)
	h.clock.(*pagesFakeClock).now = h.clock.Now().Add(25 * time.Hour)
	artifactExpect(t, boardCall(t, h, ws, user, "OWNER", "report", "batch", boardAgentBatch(t, "after-audit-window", 2, 0, boardAgentElement(t, "new-human", nil))), 201)
	if err := h.db.QueryRow(`SELECT actor_agent_id,actor_run_id FROM page_board_write_audit WHERE board_page_id=? AND mutation_id='authorized'`, page).Scan(&actorAgent, &actorRun); err != nil || actorAgent != "artifact-agent" || actorRun != "artifact-run" {
		t.Fatalf("compaction erased agent audit %s %s %v", actorAgent, actorRun, err)
	}
	var receipts int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM page_board_mutations WHERE board_page_id=? AND mutation_id='authorized'`, page).Scan(&receipts); err != nil || receipts != 0 {
		t.Fatalf("expired receipt remains %d %v", receipts, err)
	}

}
func TestBoardElementPermissionMethodRevocationAndAtomicFailure(t *testing.T) {
	for _, kind := range []string{"method", "revoked", "expired", "grantor-revoked", "terminal", "outside"} {
		t.Run(kind, func(t *testing.T) {
			h, ws, user, page, token := boardRunFixture(t)
			artifactExpect(t, boardCall(t, h, ws, user, "OWNER", "report", "batch", boardAgentBatch(t, "human", 0, 0, boardAgentElement(t, "human", nil))), 201)
			methods := []string{"update"}
			if kind == "method" {
				methods = []string{"delete"}
			}
			p := grantBoardElementForTest(t, h, ws, user, methods)
			switch kind {
			case "revoked":
				artifactExpect(t, boardPermissionCall(t, h, ws, user, "revoke", p.ID, nil), 204)
			case "expired":
				execOrFatal(t, h.db, `UPDATE page_board_element_permissions SET expires_at='2000-01-01T00:00:00.000Z' WHERE id=?`, p.ID)
			case "grantor-revoked":
				execOrFatal(t, h.db, `INSERT INTO users(id,email) VALUES('former-editor','former@example.invalid')`)
				execOrFatal(t, h.db, `UPDATE page_board_element_permissions SET granted_by_user_id='former-editor' WHERE id=?`, p.ID)
			case "terminal":
				execOrFatal(t, h.db, `INSERT INTO journal_entries(id,workspace_id,crew_id,agent_id,entry_type,actor_type,summary,trace_id) VALUES('terminal',?,'artifact-crew','artifact-agent','run.completed','system','done','artifact-run')`, ws)
			case "outside": // A new agent shape cannot join a protected outside frame/group through this token.
				execOrFatal(t, h.db, `UPDATE page_board_elements SET element_json=json_set(element_json,'$.groupIds',json('["protected-group"]')) WHERE board_page_id=? AND element_id='human'`, page)
				raw := boardAgentElement(t, "outside", map[string]any{"groupIds": []string{"protected-group"}})
				execOrFatal(t, h.db, `INSERT INTO page_board_elements(board_page_id,element_id,element_rev,element_json,author_user_id,updated_seq) VALUES(?,'outside',1,?,?,1)`, page, string(raw), user)
			}
			changes := map[string]any{"x": 200}
			if kind == "outside" {
				changes["groupIds"] = []string{"protected-group"}
			}
			body := boardPermissionBatch(t, boardAgentBatch(t, "denied", 1, 1, boardAgentElement(t, "human", changes)), p.ID)
			artifactExpect(t, boardAgentCall(t, h, ws, token, "POST", body), 403)
			var used, seq int
			if err := h.db.QueryRow(`SELECT used_mutation_id IS NOT NULL FROM page_board_element_permissions WHERE id=?`, p.ID).Scan(&used); err != nil || used != 0 {
				t.Fatalf("failed batch consumed permission %d %v", used, err)
			}
			if err := h.db.QueryRow(`SELECT seq FROM page_boards WHERE page_id=?`, page).Scan(&seq); err != nil || seq != 1 {
				t.Fatalf("failed batch changed board %d %v", seq, err)
			}
		})
	}
}
func TestBoardElementPermissionScopeValidation(t *testing.T) {
	if validBoardPermissionScope([]boardPermissionElement{{"a", 1}, {"a", 1}}, []string{"update"}, "purpose", 60) {
		t.Fatal("duplicate targets accepted")
	}
	for _, methods := range [][]string{{"update", "update"}, {"produce"}, {}} {
		if validBoardPermissionScope([]boardPermissionElement{{"a", 1}}, methods, "purpose", 60) {
			t.Fatalf("accepted %v", methods)
		}
	}
	h, ws, user, _, _ := boardRunFixture(t)
	artifactExpect(t, boardPermissionCall(t, h, ws, user, "grant", "", map[string]any{"mutation_id": "permission-intent", "run_id": "artifact-run", "elements": []boardPermissionElement{{"missing", 1}}, "methods": []string{"update"}, "purpose": "purpose", "expires_in_seconds": 60}), 409)
}

func TestBoardElementPermissionEligibleRunRequiresExplicitBaseGrant(t *testing.T) {
	h, ws, user, page, _ := boardRunFixture(t)
	execOrFatal(t, h.db, `DELETE FROM page_board_run_delegations WHERE board_page_id=?`, page)
	w := boardPermissionCall(t, h, ws, user, "list", "", nil)
	artifactExpect(t, w, 200)
	if !strings.Contains(w.Body.String(), `"needs_board_delegation":true`) {
		t.Fatal(w.Body.String())
	}
	body := map[string]any{"mutation_id": "grant", "run_id": "artifact-run", "elements": []boardPermissionElement{{"human", 1}}, "methods": []string{"update"}, "purpose": "purpose", "expires_in_seconds": 60}
	artifactExpect(t, boardPermissionCall(t, h, ws, user, "grant", "", body), 403)
	grantBoardRunForTest(t, h, ws, user, "artifact-run", 201)
	w = boardPermissionCall(t, h, ws, user, "list", "", nil)
	artifactExpect(t, w, 200)
	if !strings.Contains(w.Body.String(), `"needs_board_delegation":false`) {
		t.Fatal(w.Body.String())
	}
}
func TestBoardElementPermissionDeleteAndRevokeRetry(t *testing.T) {
	h, ws, user, page, token := boardRunFixture(t)
	artifactExpect(t, boardCall(t, h, ws, user, "OWNER", "report", "batch", boardAgentBatch(t, "human", 0, 0, boardAgentElement(t, "human", nil))), 201)
	p := grantBoardElementForTest(t, h, ws, user, []string{"delete"})
	raw, _ := json.Marshal(map[string]any{"mutation_id": "delete", "base_seq": 1, "upserts": []any{}, "deletes": []map[string]any{{"id": "human", "expected_rev": 1}}, "element_permission_id": p.ID})
	artifactExpect(t, boardAgentCall(t, h, ws, token, "POST", string(raw)), 201)
	var n int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM page_board_elements WHERE board_page_id=? AND element_id='human' AND element_json IS NULL AND author_agent_id IS NULL`, page).Scan(&n); err != nil || n != 1 {
		t.Fatalf("human tombstone ownership lost %d %v", n, err)
	}
	artifactExpect(t, boardPermissionCall(t, h, ws, user, "revoke", p.ID, nil), 204)
	artifactExpect(t, boardPermissionCall(t, h, ws, user, "revoke", p.ID, nil), 204)
	p2 := grantBoardElementForTest(t, h, ws, user, []string{"delete"})
	if p2.ID != p.ID || !p2.Revoked || !p2.Used {
		t.Fatal(p2)
	}
}

func TestBoardElementPermissionAgentManifestScopedAndRevocable(t *testing.T) {
	h, ws, user, page, token := boardRunFixture(t)
	artifactExpect(t, boardCall(t, h, ws, user, "OWNER", "report", "batch", boardAgentBatch(t, "human", 0, 0, boardAgentElement(t, "human", nil))), 201)
	p := grantBoardElementForTest(t, h, ws, user, []string{"update"})
	w := boardAgentCall(t, h, ws, token, "GET", "")
	artifactExpect(t, w, 200)
	if !strings.Contains(w.Body.String(), `"element_permissions":[{"id":"`+p.ID) {
		t.Fatal(w.Body.String())
	}
	human := boardCall(t, h, ws, user, "OWNER", "report", "get", "")
	artifactExpect(t, human, 200)
	if strings.Contains(human.Body.String(), "element_permissions") {
		t.Fatal("normal Board read exposed permission authority")
	}
	execOrFatal(t, h.db, `UPDATE page_board_element_permissions SET revoked_at='2026-01-01T00:00:00.000Z' WHERE board_page_id=?`, page)
	w = boardAgentCall(t, h, ws, token, "GET", "")
	artifactExpect(t, w, 200)
	if !strings.Contains(w.Body.String(), `"element_permissions":[]`) {
		t.Fatal(w.Body.String())
	}
}
