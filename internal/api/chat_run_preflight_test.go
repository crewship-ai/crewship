package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/paymaster"
)

func TestChatRunPreflightWholeRunOptInMatchesRuntimeRules(t *testing.T) {
	db, user, ws, crew, agent := acrSeed(t, "CLAUDE_CODE", "ANTHROPIC", "claude-sonnet-4-6")
	execOrFatal(t, db, `INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES('optin-chat',?,?,?,'private')`, ws, agent, user)
	r := &Router{db: db, logger: newTestLogger()}
	for _, tc := range []struct {
		enabled           bool
		level             string
		matches, blocking bool
	}{
		{false, "guided", false, false}, {true, "guided", true, true}, {true, "trusted", true, false}, {true, "full", true, false}, {false, "guided", false, false},
	} {
		execOrFatal(t, db, `UPDATE crews SET whole_run_approval=?,autonomy_level=? WHERE id=?`, tc.enabled, tc.level, crew)
		r.PolicyResolver().Invalidate(crew)
		req := withWorkspaceUser(httptest.NewRequest("GET", "/", nil), user, ws, "OWNER")
		req.SetPathValue("chatId", "optin-chat")
		rr := httptest.NewRecorder()
		r.chatRunPreflight(rr, req)
		var got chatRunPreflightResponse
		if rr.Code != 200 || json.Unmarshal(rr.Body.Bytes(), &got) != nil {
			t.Fatalf("preflight %d %s", rr.Code, rr.Body.String())
		}
		if got.Gate.RuleMatches != tc.matches || got.Gate.Blocking != tc.blocking {
			t.Fatalf("enabled=%v level=%s gate=%+v", tc.enabled, tc.level, got.Gate)
		}
	}
	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM approvals_queue`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("preflight wrote approval: %d %v", rows, err)
	}
}

func TestChatRunPreflightReadsPayerBudgetWithoutEffectsAndRevokes(t *testing.T) {
	db, user, ws, crew, agent := acrSeed(t, "CLAUDE_CODE", "ANTHROPIC", "claude-sonnet-4-6")
	acrCredential(t, db, ws, user, "preflight-key", "ANTHROPIC_API_KEY", "API_KEY", "ANTHROPIC")
	execOrFatal(t, db, `INSERT INTO agent_credentials(id,agent_id,credential_id,env_var_name) VALUES('preflight-grant',?,'preflight-key','ANTHROPIC_API_KEY')`, agent)
	execOrFatal(t, db, `INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES('preflight-chat',?,?,?,'private')`, ws, agent, user)
	execOrFatal(t, db, `INSERT INTO budget_limits(id,workspace_id,scope_kind,scope_id,window,limit_usd,mode) VALUES('preflight-cap',?,'workspace',?,'month',100,'soft')`, ws, ws)
	execOrFatal(t, db, `INSERT INTO workspaces(id,name,slug) VALUES('foreign-budget-workspace','Foreign','foreign-budget-workspace')`)
	execOrFatal(t, db, `INSERT INTO budget_limits(id,workspace_id,scope_kind,scope_id,window,limit_usd,mode) VALUES('foreign-private-cap','foreign-budget-workspace','workspace','foreign-budget-workspace','month',9999,'hard')`)
	if _, err := paymaster.Record(t.Context(), db, nil, paymaster.Call{Scope: paymaster.Scope{WorkspaceID: ws, CrewID: crew, AgentID: agent}, CredentialID: "preflight-key", Provider: "anthropic", Model: "claude-sonnet-4-6", CostUSD: 10}); err != nil {
		t.Fatal(err)
	}
	r := &Router{db: db, logger: newTestLogger()}
	actor, role := user, "OWNER"
	read := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/api/v1/chats/preflight-chat/run-preflight", nil)
		req.SetPathValue("chatId", "preflight-chat")
		req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: actor}), ws, role))
		rec := httptest.NewRecorder()
		r.chatRunPreflight(rec, req)
		return rec
	}
	rec := read()
	if strings.Contains(rec.Body.String(), "preflight-key") || strings.Contains(rec.Body.String(), "credential_id") || strings.Contains(rec.Body.String(), "agent_grant") || strings.Contains(rec.Body.String(), "foreign-private-cap") || strings.Contains(rec.Body.String(), "9999") {
		t.Fatalf("vault identity leaked: %s", rec.Body.String())
	}
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	var out chatRunPreflightResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Payer.Category != "connected_provider_account" || out.Payer.Label != "ANTHROPIC_API_KEY" || out.EstimateUSD != nil || out.EstimateState != "unknown" || len(out.Budgets) != 1 || out.Budgets[0].RemainingUSD != 90 {
		t.Fatalf("incorrect current projection: %+v", out)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("cacheable private snapshot")
	}
	// A chat participant has no implied vault metadata authority.
	execOrFatal(t, db, `INSERT INTO users(id,email) VALUES('preflight-member','preflight-member@test.local')`)
	execOrFatal(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('preflight-member-row',?,'preflight-member','MEMBER')`, ws)
	execOrFatal(t, db, `UPDATE chats SET visibility='group' WHERE id='preflight-chat'`)
	execOrFatal(t, db, `INSERT INTO chat_participants(chat_id,user_id) VALUES('preflight-chat','preflight-member')`)
	actor, role = "preflight-member", "MEMBER"
	member := read()
	if member.Code != 200 {
		t.Fatalf("member preflight: %d %s", member.Code, member.Body.String())
	}
	var memberOut chatRunPreflightResponse
	if err := json.Unmarshal(member.Body.Bytes(), &memberOut); err != nil {
		t.Fatal(err)
	}
	if memberOut.Payer.Label != "Connected provider account" || strings.Contains(member.Body.String(), "ANTHROPIC_API_KEY") || strings.Contains(member.Body.String(), "preflight-key") || strings.Contains(member.Body.String(), "credential_id") {
		t.Fatalf("member vault metadata leaked: %s", member.Body.String())
	}
	execOrFatal(t, db, `UPDATE workspace_members SET access_mode='restricted' WHERE id='preflight-member-row'`)
	denied := read()
	if denied.Code != 404 || strings.Contains(denied.Body.String(), "payer") {
		t.Fatalf("restricted hint leaked: %d %s", denied.Code, denied.Body.String())
	}
	execOrFatal(t, db, `DELETE FROM workspace_members WHERE id='preflight-member-row'`)
	denied = read()
	if denied.Code != 404 {
		t.Fatalf("removed member: %d", denied.Code)
	}
	actor, role = user, "OWNER"
	// A lower budget is read afresh, never pinned to the first preflight.
	execOrFatal(t, db, `UPDATE budget_limits SET limit_usd=1,mode='hard' WHERE id='preflight-cap'`)
	rec = read()
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Budgets[0].RemainingUSD != 0 || out.Budgets[0].Mode != "hard" || out.Budgets[0].State != string(paymaster.StateExceeded) {
		t.Fatal(out.Budgets)
	}
	// Existing provider admission remains the authority before a paid effect.
	req := httptest.NewRequest("POST", "/api/v1/internal/cost/admit", strings.NewReader(`{"agent_id":"`+agent+`","credential_id":"preflight-key","provider":"ANTHROPIC"}`))
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("X-Internal-Token", internaltoken.DeriveCrewToken(bindTestMaster, ws, crew))
	h := NewInternalHandler(db, bindTestMaster, testLogger())
	paid := httptest.NewRecorder()
	h.requireInternal(http.HandlerFunc(r.handleSidecarCostAdmit)).ServeHTTP(paid, req)
	if paid.Code != 403 {
		t.Fatalf("lowered budget admitted paid request: %d %s", paid.Code, paid.Body.String())
	}
	for table, want := range map[string]int{"cost_ledger": 1, "approvals_queue": 0} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != want {
			t.Fatalf("preflight created %s effects: %d %v", table, count, err)
		}
	}
	execOrFatal(t, db, `UPDATE users SET suspended_at=datetime('now') WHERE id=?`, user)
	rec = read()
	if rec.Code != 404 || strings.Contains(rec.Body.String(), "preflight-key") {
		t.Fatalf("revoked read leaked: %d %s", rec.Code, rec.Body.String())
	}
}
