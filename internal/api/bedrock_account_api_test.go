package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/bedrock"
)

func TestBedrockCrewRuleFutureAgentsModelEditsAndReadiness(t *testing.T) {
	h, db := newCredHandler(t)
	user := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, user)
	crew := seedCrewRow(t, db, "bedrock-rule-crew", ws, "Bedrock rule", "bedrock-rule")
	first := seedAgentRow(t, db, "bedrock-rule-first", ws, crew, "First", "first", "AGENT")
	models := []string{"eu.anthropic.claude-opus-5-5", "eu.anthropic.claude-sonnet-5-5"}
	document, _ := json.Marshal(bedrock.Account{AuthMethod: "bedrock_api_key", APIKey: "api-contract-synthetic-key", Region: "eu-central-1", Models: models, EUOnly: true})
	code, out := plCreate(t, h, user, ws, map[string]any{"name": "Crew policy account", "provider": bedrock.Provider, "type": "PROVIDER_LOGIN", "mode": "api_key", "value": string(document)})
	if code != 201 {
		t.Fatal(code, out)
	}
	account := out["id"].(string)
	assign := func(agents, crews []string, model string) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(map[string]any{"agent_ids": agents, "crew_ids": crews, "model": model})
		r := plRequest(t, "POST", "/assign", string(raw), user, ws, "OWNER")
		r.SetPathValue("credentialId", account)
		w := httptest.NewRecorder()
		h.AssignBedrock(w, r)
		return w
	}
	if w := assign(nil, []string{crew}, models[0]); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	assertModel := func(agent, model string) {
		t.Helper()
		var provider, runner, stored string
		if err := db.QueryRow(`SELECT llm_provider,cli_adapter,llm_model FROM agents WHERE id=?`, agent).Scan(&provider, &runner, &stored); err != nil {
			t.Fatal(err)
		}
		if provider != bedrock.Provider || runner != "OPENCODE" || stored != bedrock.NativeProvider+"/"+model {
			t.Fatalf("agent %s: %s/%s/%s", agent, provider, runner, stored)
		}
		selected, err := selectedBedrockAccount(t.Context(), db, agent)
		if err != nil || selected != account {
			t.Fatalf("payer=%s err=%v", selected, err)
		}
	}
	assertModel(first, models[0])
	late := seedAgentRow(t, db, "bedrock-rule-late", ws, crew, "Late", "late", "AGENT")
	assertModel(late, models[0])
	agentHandler := NewAgentHandler(db, newTestLogger())
	// Reassigning the same payer changes its model without an off/on cycle.
	if w := assign([]string{first}, nil, models[1]); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	assertModel(first, models[1])
	if w := covAUPatch(t, agentHandler, user, ws, "OWNER", late, `{"llm_model":"amazon-bedrock/`+models[1]+`"}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	assertModel(late, models[1])
	for _, body := range []string{`{"llm_model":"amazon-bedrock/global.anthropic.haiku"}`, `{"llm_model":null}`, `{"llm_provider":null}`, `{"cli_adapter":"CLAUDE_CODE"}`, `{"llm_model":"anthropic/claude-opus"}`} {
		if w := covAUPatch(t, agentHandler, user, ws, "OWNER", late, body); w.Code != 400 {
			t.Fatalf("invalid Bedrock patch accepted: %s status=%d %s", body, w.Code, w.Body.String())
		}
		assertModel(late, models[1])
	}
	get := func() agentResponse {
		r := plRequest(t, "GET", "/agents/"+late, "", user, ws, "OWNER")
		r.SetPathValue("agentId", late)
		w := httptest.NewRecorder()
		agentHandler.Get(w, r)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "api-contract-synthetic-key") {
			t.Fatal("GET disclosed account key")
		}
		var response agentResponse
		if json.Unmarshal(w.Body.Bytes(), &response) != nil || response.CredentialReadiness == nil {
			t.Fatal("GET missing readiness")
		}
		return response
	}
	if got := get().CredentialReadiness.ModelCredential.State; got != "ready" {
		t.Fatalf("valid policy not ready: %s", got)
	}
	execOrFatal(t, db, `UPDATE credentials SET status='REVOKED' WHERE id=?`, account)
	if got := get().CredentialReadiness.ModelCredential.State; got != "missing" {
		t.Fatalf("revoked account readiness=%s", got)
	}
	execOrFatal(t, db, `UPDATE credentials SET status='ACTIVE' WHERE id=?`, account)
	if w := assign(nil, []string{crew}, models[0]); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	assertModel(first, models[1])
	assertModel(late, models[1])
	// Removing the crew rule removes inherited bindings, preserving explicit
	// individual overrides. No selection falls back to another account.
	inherited := seedAgentRow(t, db, "bedrock-rule-inherited", ws, crew, "Inherited", "inherited", "AGENT")
	assertModel(inherited, models[0])
	execOrFatal(t, db, `DELETE FROM credential_bindings WHERE scope='CREW' AND crew_id=? AND slot=?`, crew, bedrock.Slot)
	if id, err := selectedBedrockAccount(t.Context(), db, inherited); err != nil || id != "" {
		t.Fatal("removed inherited payer survived", id, err)
	}
	assertModel(first, models[1])
	if id, err := selectedBedrockAccount(t.Context(), db, late); err != nil || id != "" {
		t.Fatal("individual model edit changed inherited account ownership", id, err)
	}
	// An empty crew may be configured before its first agent exists.
	empty := seedCrewRow(t, db, "bedrock-rule-empty", ws, "Empty", "empty")
	if w := assign(nil, []string{empty}, models[0]); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	newAgent := seedAgentRow(t, db, "bedrock-rule-empty-new", ws, empty, "New", "new", "AGENT")
	assertModel(newAgent, models[0])
	// Public creation returns the materialized runner/model, not request defaults.
	create := plRequest(t, "POST", "/api/v1/agents", `{"name":"Created","slug":"bedrock-rule-created","crew_id":"`+empty+`"}`, user, ws, "OWNER")
	created := httptest.NewRecorder()
	agentHandler.Create(created, create)
	if created.Code != 201 {
		t.Fatal(created.Code, created.Body.String())
	}
	var createdAgent agentResponse
	if err := json.Unmarshal(created.Body.Bytes(), &createdAgent); err != nil {
		t.Fatal(err)
	}
	if createdAgent.LLMModel == nil || *createdAgent.LLMModel != bedrock.NativeProvider+"/"+models[0] || createdAgent.CLIAdapter != "OPENCODE" {
		t.Fatal("create response reported stale model defaults")
	}
	execOrFatal(t, db, `UPDATE agents SET crew_id=NULL WHERE id=?`, newAgent)
	if id, err := selectedBedrockAccount(t.Context(), db, newAgent); err != nil || id != "" {
		t.Fatal("moved agent retained inherited payer", id, err)
	}
	execOrFatal(t, db, `UPDATE agents SET crew_id=? WHERE id=?`, empty, newAgent)
	assertModel(newAgent, models[0])
	// Combined move/model PATCH validates the requested ID after defaults.
	other := seedCrewRow(t, db, "bedrock-rule-move", ws, "Move", "move")
	moved := seedAgentRow(t, db, "bedrock-rule-moved", ws, other, "Moved", "moved", "AGENT")
	bad := `{"crew_id":"` + empty + `","llm_model":"amazon-bedrock/global.anthropic.haiku"}`
	if w := covAUPatch(t, agentHandler, user, ws, "OWNER", moved, bad); w.Code != 400 {
		t.Fatalf("invalid explicit move model accepted: %d %s", w.Code, w.Body.String())
	}
	var storedCrew string
	if err := db.QueryRow(`SELECT crew_id FROM agents WHERE id=?`, moved).Scan(&storedCrew); err != nil || storedCrew != other {
		t.Fatal("failed move was not atomic", storedCrew, err)
	}
	good := `{"crew_id":"` + empty + `","llm_model":"amazon-bedrock/` + models[1] + `"}`
	if w := covAUPatch(t, agentHandler, user, ws, "OWNER", moved, good); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	assertModel(moved, models[1])
	// Manifests can write explicit grants after the agent INSERT. Its native
	// model must survive the crew default, and the grant replaces inheritance.
	execOrFatal(t, db, `INSERT INTO agents(id,workspace_id,crew_id,name,slug,cli_adapter,llm_provider,llm_model) VALUES('bedrock-rule-manifest',?,?,'Manifest','manifest','OPENCODE','AWS_BEDROCK',?)`, ws, empty, bedrock.NativeProvider+"/"+models[1])
	assertModel("bedrock-rule-manifest", models[1])
	execOrFatal(t, db, `INSERT INTO agent_credentials(id,agent_id,credential_id,env_var_name) VALUES('bedrock-rule-manifest-grant','bedrock-rule-manifest',?,?)`, account, bedrock.Slot)
	assertModel("bedrock-rule-manifest", models[1])
	var inheritedCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM credential_bindings WHERE agent_id='bedrock-rule-manifest' AND inherited_from_binding_id IS NOT NULL`).Scan(&inheritedCount); err != nil || inheritedCount != 0 {
		t.Fatal("late explicit grant retained inherited payer", inheritedCount, err)
	}
	// Rehiring an agent also inherits a crew rule created while it was away.
	rehire := seedAgentRow(t, db, "bedrock-rule-rehire", ws, other, "Rehire", "rehire", "AGENT")
	execOrFatal(t, db, `UPDATE agents SET deleted_at='2026-11-17T00:00:00Z' WHERE id=?`, rehire)
	if w := assign(nil, []string{other}, models[0]); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	execOrFatal(t, db, `UPDATE agents SET deleted_at=NULL WHERE id=?`, rehire)
	assertModel(rehire, models[0])
	bare := seedAgentRow(t, db, "bedrock-rule-bare", ws, "", "Bare", "bare", "AGENT")
	var parent string
	if err := db.QueryRow(`SELECT id FROM credential_bindings WHERE scope='CREW' AND crew_id=? AND slot=?`, empty, bedrock.Slot).Scan(&parent); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO credential_bindings(id,workspace_id,credential_id,scope,agent_id,slot,inherited_from_binding_id) VALUES('forged-inherited',?,?,'AGENT',?,?,?)`, ws, account, bare, bedrock.Slot, parent); err == nil {
		t.Fatal("cross-crew inherited binding accepted")
	}
}
