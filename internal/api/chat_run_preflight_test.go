package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

func TestChatRunPreflightReadsPayerBudgetWithoutEffectsAndRevokes(t *testing.T) {
	db, user, ws, crew, agent := acrSeed(t, "CLAUDE_CODE", "ANTHROPIC", "claude-sonnet-4-6")
	acrCredential(t, db, ws, user, "preflight-key", "ANTHROPIC_API_KEY", "API_KEY", "ANTHROPIC")
	execOrFatal(t, db, `INSERT INTO agent_credentials(id,agent_id,credential_id,env_var_name) VALUES('preflight-grant',?,'preflight-key','ANTHROPIC_API_KEY')`, agent)
	execOrFatal(t, db, `INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES('preflight-chat',?,?,?,'private')`, ws, agent, user)
	execOrFatal(t, db, `INSERT INTO budget_limits(id,workspace_id,scope_kind,scope_id,window,limit_usd,mode) VALUES('preflight-cap',?,'workspace',?,'month',100,'soft')`, ws, ws)
	r := &Router{db: db, logger: newTestLogger()}
	read := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/api/v1/chats/preflight-chat/run-preflight", nil)
		req.SetPathValue("chatId", "preflight-chat")
		req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: user}), ws, "OWNER"))
		rec := httptest.NewRecorder()
		r.chatRunPreflight(rec, req)
		return rec
	}
	rec := read()
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	var out chatRunPreflightResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Payer.CredentialID != "preflight-key" || out.EstimateUSD != nil || out.EstimateState != "unknown" || len(out.Budgets) != 1 || out.Budgets[0].RemainingUSD != 100 {
		t.Fatalf("incorrect current projection: %+v", out)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("cacheable private snapshot")
	}
	// A lower budget is read afresh, never pinned to the first preflight.
	execOrFatal(t, db, `UPDATE budget_limits SET limit_usd=1,mode='hard' WHERE id='preflight-cap'`)
	rec = read()
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Budgets[0].RemainingUSD != 1 || out.Budgets[0].Mode != "hard" {
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
	for _, table := range []string{"cost_ledger", "approvals_queue"} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("preflight created %s effects: %d %v", table, count, err)
		}
	}
	execOrFatal(t, db, `UPDATE users SET suspended_at=datetime('now') WHERE id=?`, user)
	rec = read()
	if rec.Code != 404 || strings.Contains(rec.Body.String(), "preflight-key") {
		t.Fatalf("revoked read leaked: %d %s", rec.Code, rec.Body.String())
	}
}
