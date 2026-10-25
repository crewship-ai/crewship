package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

// The submit validator must receive the current definition of the chat's
// agent through the real authority-gated resolver, not a client-selected form.
func TestAskSubmissionResolveHumanChatUsesCurrentAgentForms(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	owner := seedTestUser(t, db)
	workspace := seedTestWorkspace(t, db, owner)
	execOrFatal(t, db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES('ask-resolve-crew',?,'Ask crew','ask-resolve-crew')`, workspace)
	for _, id := range []string{"ask-resolve-a", "ask-resolve-b"} {
		seedAgentRow(t, db, id, workspace, "ask-resolve-crew", id, id, "AGENT")
		execOrFatal(t, db, `INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES(?,?,?,?, 'private')`, id+"-chat", workspace, id, owner)
	}
	form := func(pattern string) string {
		return `[{"id":"same-form","label":"Format","version":2,"attachment":"none","template":"Answer: {{answer}}","fields":[{"name":"answer","label":"Answer","type":"text","required":true,"pattern":"` + pattern + `"}]}]`
	}
	formsA, formsB := form("ask-pattern-alpha"), form("ask-pattern-beta")
	execOrFatal(t, db, `UPDATE agents SET ask_forms=? WHERE id='ask-resolve-a'`, formsA)
	execOrFatal(t, db, `UPDATE agents SET ask_forms=? WHERE id='ask-resolve-b'`, formsB)
	execOrFatal(t, db, `INSERT INTO users(id,email) VALUES('ask-resolve-peer','ask-peer@resolve.test'),('ask-resolve-outsider','ask-outsider@resolve.test')`)
	execOrFatal(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('ask-resolve-peer-member',?,'ask-resolve-peer','MEMBER')`, workspace)
	execOrFatal(t, db, `INSERT INTO workspaces(id,name,slug) VALUES('ask-resolve-foreign','Foreign','ask-resolve-foreign')`)
	execOrFatal(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('ask-resolve-foreign-member','ask-resolve-foreign','ask-resolve-outsider','OWNER')`)
	const hostToken = "synthetic-ask-resolve-host-token"
	router, err := NewRouter(db, "synthetic-ask-resolve-jwt-secret", newTestLogger(), WithInternalToken(hostToken))
	if err != nil {
		t.Fatal(err)
	}
	resolve := func(actor, agent, token string, wantStatus int, wantForms string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/chats/"+agent+"-chat/resolve-human?user_id="+url.QueryEscape(actor), nil)
		req.RemoteAddr = "127.0.0.1:1234"
		req.Header.Set("X-Internal-Token", token)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		if rr.Code != wantStatus {
			t.Fatalf("actor=%q agent=%q status=%d want=%d body=%s", actor, agent, rr.Code, wantStatus, rr.Body.String())
		}
		var response map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if wantStatus != http.StatusOK {
			if _, exists := response["ask_forms"]; exists || strings.Contains(rr.Body.String(), "ask-pattern-") || strings.Contains(rr.Body.String(), "same-form") {
				t.Fatalf("denied response disclosed form definitions: %s", rr.Body.String())
			}
			return
		}
		if response["agent_id"] != agent || response["workspace_id"] != workspace || response["ask_forms"] != wantForms {
			t.Fatalf("wrong agent's forms projected: agent=%v workspace=%v forms=%v want=%s", response["agent_id"], response["workspace_id"], response["ask_forms"], wantForms)
		}
		receipt, ok := response["human_authority"].(map[string]any)
		if !ok || receipt["user_id"] != actor || receipt["agent_id"] != agent || receipt["workspace_id"] != workspace {
			t.Fatalf("form projection lacks matching human authority: %+v", response["human_authority"])
		}
	}
	t.Run("same form id resolves independently for each chat agent", func(t *testing.T) {
		resolve(owner, "ask-resolve-a", hostToken, http.StatusOK, formsA)
		resolve(owner, "ask-resolve-b", hostToken, http.StatusOK, formsB)
		// The ordinary detail read and trusted chat resolve agree on stored bytes.
		h := NewAgentHandler(db, newTestLogger())
		for agent, expected := range map[string]string{"ask-resolve-a": formsA, "ask-resolve-b": formsB} {
			if detail := agentDetailJSON(t, h, owner, workspace, agent); detail["ask_forms"] != expected {
				t.Fatalf("GET agent %s forms=%v want=%s", agent, detail["ask_forms"], expected)
			}
		}
	})
	t.Run("same-workspace user cannot resolve private chat", func(t *testing.T) {
		resolve("ask-resolve-peer", "ask-resolve-a", hostToken, http.StatusNotFound, "")
	})
	t.Run("foreign workspace user cannot resolve chat", func(t *testing.T) {
		resolve("ask-resolve-outsider", "ask-resolve-a", hostToken, http.StatusNotFound, "")
	})
	t.Run("missing actor cannot resolve chat", func(t *testing.T) {
		resolve("", "ask-resolve-a", hostToken, http.StatusNotFound, "")
	})
	t.Run("workspace and crew tokens cannot impersonate human", func(t *testing.T) {
		resolve(owner, "ask-resolve-a", internaltoken.DeriveWorkspaceToken(hostToken, workspace), http.StatusForbidden, "")
		resolve(owner, "ask-resolve-a", internaltoken.DeriveCrewToken(hostToken, workspace, "ask-resolve-crew"), http.StatusForbidden, "")
	})
	t.Run("foreign workspace detail read does not expose forms", func(t *testing.T) {
		h := NewAgentHandler(db, newTestLogger())
		req := httptest.NewRequest(http.MethodGet, "/api/v1/agents/ask-resolve-a", nil)
		req.SetPathValue("agentId", "ask-resolve-a")
		req = withWorkspaceUser(req, "ask-resolve-outsider", "ask-resolve-foreign", "OWNER")
		rr := httptest.NewRecorder()
		h.Get(rr, req)
		if rr.Code != http.StatusNotFound || strings.Contains(rr.Body.String(), "ask-pattern-") || strings.Contains(rr.Body.String(), "same-form") {
			t.Fatalf("foreign detail read status=%d body=%s", rr.Code, rr.Body.String())
		}
	})
	t.Run("definition changed after earlier resolve is read fresh", func(t *testing.T) {
		updated := form("ask-pattern-current")
		execOrFatal(t, db, `UPDATE agents SET ask_forms=? WHERE id='ask-resolve-a'`, updated)
		resolve(owner, "ask-resolve-a", hostToken, http.StatusOK, updated)
		resolve(owner, "ask-resolve-b", hostToken, http.StatusOK, formsB)
	})
	t.Run("deleted membership prevents later projection", func(t *testing.T) {
		execOrFatal(t, db, `DELETE FROM workspace_members WHERE workspace_id=? AND user_id=?`, workspace, owner)
		resolve(owner, "ask-resolve-a", hostToken, http.StatusNotFound, "")
	})
}
