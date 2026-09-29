package api

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

func TestHumanChatResolutionAuthorizesBeforeContextMaterialization(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	owner := seedTestUser(t, db)
	workspace := seedTestWorkspace(t, db, owner)
	execOrFatal(t, db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES('human-crew',?,'Crew','human-crew')`, workspace)
	seedAgentRow(t, db, "human-agent", workspace, "human-crew", "Agent", "human-agent", "AGENT")
	execOrFatal(t, db, `UPDATE agents SET system_prompt_legacy='protected-human-prompt',memory_enabled=1 WHERE id='human-agent'`)
	for _, user := range []string{"human-one", "human-two"} {
		execOrFatal(t, db, `INSERT INTO users(id,email) VALUES(?,?)`, user, user+"@access.test")
		execOrFatal(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,?,'MEMBER')`, user, workspace, user)
		execOrFatal(t, db, `INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES(?,?,'human-agent',?,'private')`, user+"-chat", workspace, user)
	}
	const master = "synthetic-human-resolution-host-token"
	router, err := NewRouter(db, "synthetic-human-resolution-jwt-secret", newTestLogger(), WithInternalToken(master))
	if err != nil {
		t.Fatal(err)
	}
	request := func(user, chat, token string, want int) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/chats/"+chat+"/resolve-human?user_id="+url.QueryEscape(user), nil)
		req.RemoteAddr = "127.0.0.1:1234"
		req.Header.Set("X-Internal-Token", token)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		if rr.Code != want {
			t.Fatalf("actor=%s chat=%s status=%d want=%d body=%s", user, chat, rr.Code, want, rr.Body.String())
		}
		hasPrompt := strings.Contains(rr.Body.String(), "protected-human-prompt")
		if (want == 200) != hasPrompt {
			t.Fatalf("context projection: status=%d prompt=%v", rr.Code, hasPrompt)
		}
	}
	request("human-one", "human-one-chat", master, 200)
	request("human-two", "human-one-chat", master, 404)
	request("", "human-one-chat", master, 404)
	request("human-one", "human-one-chat", internaltoken.DeriveWorkspaceToken(master, workspace), 403)
	request("human-one", "human-one-chat", internaltoken.DeriveCrewToken(master, workspace, "human-crew"), 403)
	request("human-one", "human-one-chat", "invalid-host-token", 403)
	// The sender, not the creator, controls group admission.
	execOrFatal(t, db, `UPDATE chats SET visibility='group' WHERE id='human-one-chat'`)
	execOrFatal(t, db, `INSERT INTO chat_participants(chat_id,user_id,role) VALUES('human-one-chat','human-two','member')`)
	request("human-two", "human-one-chat", master, 200)
	execOrFatal(t, db, `DELETE FROM chat_participants WHERE chat_id='human-one-chat' AND user_id='human-two'`)
	request("human-two", "human-one-chat", master, 404)
	// A read grant is not authority to materialize the shared runtime's context.
	store := access.Store{DB: db}
	member, err := store.Membership(t.Context(), "human-one", workspace)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Replace(t.Context(), owner, "human-one", workspace, "restricted", member, []access.Right{{Kind: "agent", ID: "human-agent", Operation: "chat"}}); err != nil {
		t.Fatal(err)
	}
	request("human-one", "human-one-chat", master, 404)
	request("human-two", "human-two-chat", master, 200)
	execOrFatal(t, db, `DELETE FROM workspace_members WHERE user_id='human-two' AND workspace_id=?`, workspace)
	request("human-two", "human-two-chat", master, 404)
}
