package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

func TestLegacyBudgetAdmissionHostScope(t *testing.T) {
	r, _ := costRouter(t)
	db := r.db
	user := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, user)
	crew := seedCrewRow(t, db, "budget-crew", ws, "Budget", "budget")
	agent := seedAgentRow(t, db, "budget-agent", ws, crew, "A", "a", "AGENT")
	other := seedCrewRow(t, db, "budget-other-crew", ws, "Other", "other")
	sibling := seedAgentRow(t, db, "budget-sibling", ws, other, "B", "b", "AGENT")
	_, err := db.Exec(`INSERT INTO credentials(id,workspace_id,name,encrypted_value,type,provider,status,scope,created_by) VALUES('budget-key',?,'budget-key','synthetic','API_KEY','OPENAI','ACTIVE','CREW',?)`, ws, user)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO agent_credentials(id,agent_id,credential_id,env_var_name) VALUES('budget-grant',?,'budget-key','OPENAI_API_KEY')`, agent)
	if err != nil {
		t.Fatal(err)
	}
	h := NewInternalHandler(db, bindTestMaster, testLogger())
	call := func(token, actor, extra string) int {
		req := httptest.NewRequest("POST", "/api/v1/internal/cost/admit", strings.NewReader(`{"agent_id":"`+actor+`","credential_id":"budget-key","provider":"OPENAI"`+extra+`}`))
		req.RemoteAddr = "127.0.0.1:1234"
		req.Header.Set("X-Internal-Token", token)
		rr := httptest.NewRecorder()
		h.requireInternal(http.HandlerFunc(r.handleSidecarCostAdmit)).ServeHTTP(rr, req)
		return rr.Code
	}
	token := internaltoken.DeriveCrewToken(bindTestMaster, ws, crew)
	if got := call(token, agent, ""); got != 200 {
		t.Fatalf("uncapped %d", got)
	}

	_, err = db.Exec(`UPDATE agent_credentials SET expires_at='2000-01-01T00:00:00Z' WHERE id='budget-grant'`)
	if err != nil {
		t.Fatal(err)
	}
	if got := call(token, agent, ""); got != 403 {
		t.Fatalf("expired grant %d", got)
	}
	_, err = db.Exec(`UPDATE agent_credentials SET expires_at=NULL WHERE id='budget-grant'`)
	if err != nil {
		t.Fatal(err)
	}
	if got := call(token, sibling, ""); got != 403 {
		t.Fatalf("foreign crew %d", got)
	}
	if got := call(bindTestMaster, agent, ""); got != 403 {
		t.Fatalf("unbound %d", got)
	}
	if got := call(token, agent, `,"billing_mode":"flat_rate"`); got != 400 {
		t.Fatalf("spoof %d", got)
	}
	_, err = db.Exec(`INSERT INTO budget_limits(id,workspace_id,scope_kind,scope_id,window,limit_usd,mode) VALUES('legacy-cap',?,'mission','unknown-mission','day',100,'hard')`, ws)
	if err != nil {
		t.Fatal(err)
	}
	if got := call(token, agent, ""); got != 403 {
		t.Fatalf("mission cap %d", got)
	}
	_, err = db.Exec(`UPDATE budget_limits SET mode='soft' WHERE id='legacy-cap'`)
	if err != nil {
		t.Fatal(err)
	}
	if got := call(token, agent, ""); got != 200 {
		t.Fatalf("soft %d", got)
	}

	_, err = db.Exec(`UPDATE budget_limits SET mode='hard',scope_kind='crew',scope_id=?`, other)
	if err != nil {
		t.Fatal(err)
	}
	if got := call(token, agent, ""); got != 200 {
		t.Fatalf("unrelated crew meter cap %d", got)
	}
	opaque := httptest.NewRequest("POST", "/api/v1/internal/cost/admit", strings.NewReader(`{"agent_id":"`+agent+`","credential_id":"","provider":"OPAQUE_TUNNEL"}`))
	opaque.RemoteAddr = "127.0.0.1:1234"
	opaque.Header.Set("X-Internal-Token", token)
	rr := httptest.NewRecorder()
	h.requireInternal(http.HandlerFunc(r.handleSidecarCostAdmit)).ServeHTTP(rr, opaque)
	if rr.Code != 403 {
		t.Fatalf("unattributed tunnel %d", rr.Code)
	}
	_, err = db.Exec(`UPDATE budget_limits SET mode='hard',scope_kind='workspace',scope_id=workspace_id; UPDATE credentials SET type='PROVIDER_LOGIN' WHERE id='budget-key'`)
	if err != nil {
		t.Fatal(err)
	}
	if got := call(token, agent, ""); got != 403 {
		t.Fatalf("cached key cannot become exempt via type change %d", got)
	}
	_, err = db.Exec(`UPDATE budget_limits SET enabled=0`)
	if err != nil {
		t.Fatal(err)
	}
	if got := call(token, agent, ""); got != 200 {
		t.Fatalf("uncapped subscription %d", got)
	}
	_, err = db.Exec(`DELETE FROM agent_credentials WHERE id='budget-grant'`)
	if err != nil {
		t.Fatal(err)
	}
	if got := call(token, agent, ""); got != 403 {
		t.Fatalf("revoked grant %d", got)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM cost_ledger`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("gate debited %d: %v", count, err)
	}
}
