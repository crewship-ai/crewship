package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/bedrock"
	"github.com/crewship-ai/crewship/internal/encryption"
)

func TestResolveBedrock_CurrentExactPeerAccountOnly(t *testing.T) {
	h, db := newCredHandler(t)
	user := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, user)
	crew := seedCrewRow(t, db, "resolve-crew", ws, "Resolve", "resolve")
	agent := seedAgentRow(t, db, "resolve-agent", ws, crew, "Peer", "peer", "AGENT")
	code, out := plCreate(t, h, user, ws, map[string]any{"name": "Peer Bedrock", "provider": bedrock.Provider, "type": "PROVIDER_LOGIN", "mode": "api_key", "value": bedrockAccountFixture(t)})
	if code != 201 {
		t.Fatal(code, out)
	}
	accountID := out["id"].(string)
	request := plRequest(t, "POST", "/assign", `{"agent_ids":["`+agent+`"],"model":"eu.anthropic.claude-opus-5-5"}`, user, ws, "OWNER")
	request.SetPathValue("credentialId", accountID)
	assigned := httptest.NewRecorder()
	h.AssignBedrock(assigned, request)
	if assigned.Code != 200 {
		t.Fatal(assigned.Code, assigned.Body.String())
	}
	execOrFatal(t, db, `INSERT INTO journal_entries(id,workspace_id,crew_id,agent_id,ts,entry_type,severity,priority,actor_type,summary,payload,refs,trace_id) VALUES('resolve-start',?,?,?,'2026-11-17T00:00:00Z','run.started','info','normal','orchestrator','peer','{"trigger_type":"PEER_QUERY","host_admitted":true}','{}','resolve-run')`, ws, crew, agent)
	internal := NewInternalHandler(db, "resolve-master", newTestLogger())
	call := func(token string, body bedrock.ResolveRequest) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest("POST", bedrock.ResolvePath, bytes.NewReader(raw))
		req.RemoteAddr = "172.18.0.2:4567"
		req.Header.Set("X-Internal-Token", token)
		w := httptest.NewRecorder()
		internal.requireInternal(http.HandlerFunc(internal.ResolveBedrock)).ServeHTTP(w, req)
		return w
	}
	token := internaltoken.DeriveCrewToken("resolve-master", ws, crew)
	body := bedrock.ResolveRequest{RunID: "resolve-run", AgentID: agent, AccountID: accountID, Model: "eu.anthropic.claude-opus-5-5"}
	for _, bad := range []string{"", internaltoken.DeriveAgentToken("resolve-master", ws, agent), internaltoken.DeriveWorkspaceToken("resolve-master", ws), internaltoken.DeriveCrewToken("resolve-master", ws, "other-crew")} {
		if w := call(bad, body); w.Code == 200 || strings.Contains(w.Body.String(), "bedrock-synthetic-secret") {
			t.Fatalf("unauthorized secret delivery: %d %s", w.Code, w.Body.String())
		}
	}
	for _, field := range []string{"run", "agent", "account", "model"} {
		bad := body
		switch field {
		case "run":
			bad.RunID = "unknown"
		case "agent":
			bad.AgentID = "other"
		case "account":
			bad.AccountID = "other"
		case "model":
			bad.Model = "global.anthropic.haiku"
		}
		if w := call(token, bad); w.Code == 200 || strings.Contains(w.Body.String(), "bedrock-synthetic-secret") {
			t.Fatalf("wrong %s accepted", field)
		}
	}
	if w := call(token, body); w.Code != 200 || !strings.Contains(w.Body.String(), "bedrock-synthetic-secret") || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w.Code, w.Body.String())
	}
	execOrFatal(t, db, `UPDATE credentials SET type='API_KEY' WHERE id=?`, accountID)
	if w := call(token, body); w.Code == 200 || strings.Contains(w.Body.String(), "bedrock-synthetic-secret") {
		t.Fatal("generic API_KEY row exported by Bedrock resolver")
	}
	execOrFatal(t, db, `UPDATE credentials SET type='PROVIDER_LOGIN' WHERE id=?`, accountID)
	execOrFatal(t, db, `UPDATE credentials SET handle_only=1 WHERE id=?`, accountID)
	if w := call(token, body); w.Code == 200 || strings.Contains(w.Body.String(), "bedrock-synthetic-secret") {
		t.Fatal("handle-only account exported")
	}
	execOrFatal(t, db, `UPDATE credentials SET handle_only=0 WHERE id=?`, accountID)
	// Atomic renewal is read afresh; no sidecar restart or stale-secret cache.
	var a bedrock.Account
	_ = json.Unmarshal([]byte(bedrockAccountFixture(t)), &a)
	a.APIKey = "new-resolve-key"
	raw, _ := json.Marshal(a)
	encrypted, err := encryption.Encrypt(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	execOrFatal(t, db, `UPDATE credentials SET encrypted_value=? WHERE id=?`, encrypted, accountID)
	if w := call(token, body); w.Code != 200 || !strings.Contains(w.Body.String(), "new-resolve-key") || strings.Contains(w.Body.String(), "bedrock-synthetic-secret") {
		t.Fatal(w.Code, w.Body.String())
	}
	execOrFatal(t, db, `UPDATE credentials SET status='REVOKED' WHERE id=?`, accountID)
	if w := call(token, body); w.Code == 200 || strings.Contains(w.Body.String(), "new-resolve-key") {
		t.Fatal("revoked account delivered")
	}
	execOrFatal(t, db, `UPDATE credentials SET status='ACTIVE' WHERE id=?`, accountID)
	execOrFatal(t, db, `INSERT INTO journal_entries(id,workspace_id,crew_id,agent_id,ts,entry_type,severity,priority,actor_type,summary,payload,refs,trace_id) VALUES('resolve-end',?,?,?,'2026-11-17T00:00:01Z','run.completed','info','normal','orchestrator','done','{}','{}','resolve-run')`, ws, crew, agent)
	if w := call(token, body); w.Code == 200 {
		t.Fatal("ended run received credentials")
	}
}
