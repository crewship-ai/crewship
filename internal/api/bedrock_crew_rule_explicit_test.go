package api

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/bedrock"
)

// #5446: a crew's Bedrock account rule is a default for agents that are on
// Bedrock or have no provider. It never rewrites an agent configured for
// another provider, never strands a leaving agent without a model, and does
// not outlive the account being unshared from the crew.
func TestBedrockCrewRuleRespectsExplicitProvider(t *testing.T) {
	h, db := newCredHandler(t)
	user := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, user)
	ruled := seedCrewRow(t, db, "explicit-ruled", ws, "Ruled", "ruled")
	plain := seedCrewRow(t, db, "explicit-plain", ws, "Plain", "plain")
	models := []string{"eu.anthropic.claude-opus-5-5", "eu.anthropic.claude-sonnet-5-5"}
	document, _ := json.Marshal(bedrock.Account{AuthMethod: "bedrock_api_key", APIKey: "explicit-synthetic-key", Region: "eu-central-1", Models: models, EUOnly: true})
	code, out := plCreate(t, h, user, ws, map[string]any{"name": "Rule account", "provider": bedrock.Provider, "type": "PROVIDER_LOGIN", "mode": "api_key", "value": string(document), "crew_ids": []string{ruled, plain}})
	if code != 201 {
		t.Fatal(code, out)
	}
	account := out["id"].(string)
	raw, _ := json.Marshal(map[string]any{"agent_ids": []string{}, "crew_ids": []string{ruled, plain}, "model": models[0]})
	r := plRequest(t, "POST", "/assign", string(raw), user, ws, "OWNER")
	r.SetPathValue("credentialId", account)
	w := httptest.NewRecorder()
	h.AssignBedrock(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	agents := NewAgentHandler(db, newTestLogger())
	config := func(agent string) (provider, runner, model, payer string) {
		t.Helper()
		var p, m *string
		if err := db.QueryRow(`SELECT llm_provider,cli_adapter,llm_model FROM agents WHERE id=?`, agent).Scan(&p, &runner, &m); err != nil {
			t.Fatal(err)
		}
		if p != nil {
			provider = *p
		}
		if m != nil {
			model = *m
		}
		payer, err := selectedBedrockAccount(t.Context(), db, agent)
		if err != nil {
			t.Fatal(err)
		}
		return provider, runner, model, payer
	}
	create := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		agents.Create(w, plRequest(t, "POST", "/api/v1/agents", body, user, ws, "OWNER"))
		return w
	}

	t.Run("an agent created on another provider keeps it", func(t *testing.T) {
		w := create(`{"name":"Claude","slug":"explicit-claude","crew_id":"` + ruled + `","cli_adapter":"CLAUDE_CODE","llm_provider":"ANTHROPIC","llm_model":"claude-sonnet-5-5"}`)
		if w.Code != 201 {
			t.Fatal(w.Code, w.Body.String())
		}
		var created agentResponse
		_ = json.Unmarshal(w.Body.Bytes(), &created)
		if p, r, m, payer := config(created.ID); p != "ANTHROPIC" || r != "CLAUDE_CODE" || m != "claude-sonnet-5-5" || payer != "" {
			t.Fatalf("explicit provider rewritten: %s/%s/%s payer=%q", p, r, m, payer)
		}
	})

	t.Run("an agent without a provider still inherits the rule", func(t *testing.T) {
		agent := seedAgentRow(t, db, "explicit-default", ws, ruled, "Default", "explicit-default", "AGENT")
		if p, r, m, payer := config(agent); p != bedrock.Provider || r != "OPENCODE" || m != bedrock.NativeProvider+"/"+models[0] || payer != account {
			t.Fatalf("default agent did not inherit: %s/%s/%s payer=%q", p, r, m, payer)
		}
	})

	t.Run("moving an agent on another provider into a rule crew leaves it alone", func(t *testing.T) {
		execOrFatal(t, db, `INSERT INTO agents(id,workspace_id,name,slug,cli_adapter,llm_provider,llm_model) VALUES('explicit-codex',?,'Codex','explicit-codex','CODEX_CLI','OPENAI','gpt-6')`, ws)
		if w := covAUPatch(t, agents, user, ws, "OWNER", "explicit-codex", `{"crew_id":"`+ruled+`"}`); w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		if p, r, m, payer := config("explicit-codex"); p != "OPENAI" || r != "CODEX_CLI" || m != "gpt-6" || payer != "" {
			t.Fatalf("moved agent rewritten: %s/%s/%s payer=%q", p, r, m, payer)
		}
	})

	t.Run("leaving for a crew without a rule keeps a usable model", func(t *testing.T) {
		agent := seedAgentRow(t, db, "explicit-leaver", ws, ruled, "Leaver", "explicit-leaver", "AGENT")
		execOrFatal(t, db, `UPDATE agents SET crew_id=NULL WHERE id=?`, agent)
		if p, r, m, payer := config(agent); p != bedrock.Provider || r != "OPENCODE" || m != bedrock.NativeProvider+"/"+models[0] || payer != "" {
			t.Fatalf("leaver stranded: %s/%s/%s payer=%q", p, r, m, payer)
		}
		// Back in a rule crew, the rule's model is still its default.
		execOrFatal(t, db, `UPDATE agents SET crew_id=? WHERE id=?`, ruled, agent)
		var overridden int
		if err := db.QueryRow(`SELECT bedrock_model_overridden FROM credential_bindings WHERE agent_id=? AND slot=?`, agent, bedrock.Slot).Scan(&overridden); err != nil || overridden != 0 {
			t.Fatal("rule model turned into an override", overridden, err)
		}
	})

	t.Run("a Bedrock model the crew account refuses is rejected on create and on a move", func(t *testing.T) {
		bad := bedrock.NativeProvider + "/global.anthropic.claude-fable-5-2"
		if w := create(`{"name":"Bad","slug":"explicit-bad","crew_id":"` + ruled + `","cli_adapter":"OPENCODE","llm_provider":"AWS_BEDROCK","llm_model":"` + bad + `"}`); w.Code != 400 {
			t.Fatalf("refused model accepted on create: %d %s", w.Code, w.Body.String())
		}
		execOrFatal(t, db, `INSERT INTO agents(id,workspace_id,name,slug,cli_adapter,llm_provider,llm_model) VALUES('explicit-mover',?,'Mover','explicit-mover','OPENCODE','AWS_BEDROCK',?)`, ws, bad)
		if w := covAUPatch(t, agents, user, ws, "OWNER", "explicit-mover", `{"crew_id":"`+ruled+`"}`); w.Code != 400 {
			t.Fatalf("refused model accepted on move: %d %s", w.Code, w.Body.String())
		}
		var crew *string
		if err := db.QueryRow(`SELECT crew_id FROM agents WHERE id='explicit-mover'`).Scan(&crew); err != nil || crew != nil {
			t.Fatal("refused move was not rolled back", crew, err)
		}
		// Created on Bedrock outside a rule crew: the account follows in a
		// separate assignment, so no account is not a refusal.
		if w := create(`{"name":"Later","slug":"explicit-later","crew_id":"` + plain + `","cli_adapter":"OPENCODE","llm_provider":"AWS_BEDROCK","llm_model":"` + bedrock.NativeProvider + "/" + models[1] + `"}`); w.Code != 201 {
			t.Fatalf("Bedrock agent with a permitted model refused: %d %s", w.Code, w.Body.String())
		}
	})

	t.Run("unsharing the account from a crew removes that crew's rule", func(t *testing.T) {
		before := seedAgentRow(t, db, "explicit-plain-member", ws, plain, "Member", "explicit-plain-member", "AGENT")
		if _, _, _, payer := config(before); payer != account {
			t.Fatal("precondition: plain crew rule not applied")
		}
		r := plRequest(t, "PATCH", "/api/v1/credentials/"+account, `{"crew_ids":["`+ruled+`"]}`, user, ws, "OWNER")
		r.SetPathValue("credentialId", account)
		w := httptest.NewRecorder()
		h.Update(w, r)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var rules int
		if err := db.QueryRow(`SELECT COUNT(*) FROM credential_bindings WHERE credential_id=? AND scope='CREW' AND crew_id=?`, account, plain).Scan(&rules); err != nil || rules != 0 {
			t.Fatal("rule survived unsharing", rules, err)
		}
		if _, _, _, payer := config(before); payer != "" {
			t.Fatal("inherited payer survived unsharing")
		}
		hire := seedAgentRow(t, db, "explicit-plain-hire", ws, plain, "Hire", "explicit-plain-hire", "AGENT")
		if _, _, _, payer := config(hire); payer != "" {
			t.Fatal("next hire inherited an unshared account")
		}
		if _, _, _, payer := config("explicit-default"); payer != account {
			t.Fatal("rule of a crew still shared was removed")
		}
	})
}

// #5446 review: the cases a regression in either trigger would slip through.
func TestBedrockCrewRuleExplicitChoicesAndDefaults(t *testing.T) {
	h, db := newCredHandler(t)
	user := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, user)
	crewA := seedCrewRow(t, db, "defaults-a", ws, "A", "defaults-a")
	crewB := seedCrewRow(t, db, "defaults-b", ws, "B", "defaults-b")
	models := []string{"eu.anthropic.claude-opus-5-5", "eu.anthropic.claude-sonnet-5-5", "eu.anthropic.claude-haiku-5-5"}
	document, _ := json.Marshal(bedrock.Account{AuthMethod: "bedrock_api_key", APIKey: "defaults-synthetic-key", Region: "eu-central-1", Models: models, EUOnly: true})
	code, out := plCreate(t, h, user, ws, map[string]any{"name": "Defaults account", "provider": bedrock.Provider, "type": "PROVIDER_LOGIN", "mode": "api_key", "value": string(document)})
	if code != 201 {
		t.Fatal(code, out)
	}
	account := out["id"].(string)
	rule := func(crew, model string) {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"agent_ids": []string{}, "crew_ids": []string{crew}, "model": model})
		r := plRequest(t, "POST", "/assign", string(raw), user, ws, "OWNER")
		r.SetPathValue("credentialId", account)
		w := httptest.NewRecorder()
		h.AssignBedrock(w, r)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	rule(crewA, models[0])
	rule(crewB, models[1])
	agents := NewAgentHandler(db, newTestLogger())
	create := func(body string) string {
		t.Helper()
		w := httptest.NewRecorder()
		agents.Create(w, plRequest(t, "POST", "/api/v1/agents", body, user, ws, "OWNER"))
		if w.Code != 201 {
			t.Fatal(w.Code, w.Body.String())
		}
		var created agentResponse
		_ = json.Unmarshal(w.Body.Bytes(), &created)
		return created.ID
	}
	state := func(agent string) (runner, provider, model string, overridden int, payer string) {
		t.Helper()
		var p, m *string
		if err := db.QueryRow(`SELECT cli_adapter,llm_provider,llm_model FROM agents WHERE id=?`, agent).Scan(&runner, &p, &m); err != nil {
			t.Fatal(err)
		}
		if p != nil {
			provider = *p
		}
		if m != nil {
			model = *m
		}
		overridden = -1
		_ = db.QueryRow(`SELECT bedrock_model_overridden FROM credential_bindings WHERE agent_id=? AND slot=? AND inherited_from_binding_id IS NOT NULL`, agent, bedrock.Slot).Scan(&overridden)
		payer, _ = selectedBedrockAccount(t.Context(), db, agent)
		return
	}
	native := func(id string) string { return bedrock.NativeProvider + "/" + id }

	t.Run("an explicit runner and model without a provider are kept", func(t *testing.T) {
		agent := create(`{"name":"Codex","slug":"defaults-codex","crew_id":"` + crewA + `","cli_adapter":"CODEX_CLI","llm_model":"gpt-6"}`)
		if r, p, m, _, payer := state(agent); r != "CODEX_CLI" || p != "" || m != "gpt-6" || payer != "" {
			t.Fatalf("explicit runner/model rewritten: %s/%s/%s payer=%q", r, p, m, payer)
		}
	})

	t.Run("the rule's own model is a default, another permitted model an override", func(t *testing.T) {
		same := create(`{"name":"Same","slug":"defaults-same","crew_id":"` + crewA + `","cli_adapter":"OPENCODE","llm_provider":"AWS_BEDROCK","llm_model":"` + native(models[0]) + `"}`)
		if _, _, m, o, payer := state(same); m != native(models[0]) || o != 0 || payer != account {
			t.Fatalf("rule model: %s overridden=%d payer=%q", m, o, payer)
		}
		own := create(`{"name":"Own","slug":"defaults-own","crew_id":"` + crewA + `","cli_adapter":"OPENCODE","llm_provider":"AWS_BEDROCK","llm_model":"` + native(models[2]) + `"}`)
		if _, _, m, o, payer := state(own); m != native(models[2]) || o != 1 || payer != account {
			t.Fatalf("own model: %s overridden=%d payer=%q", m, o, payer)
		}
	})

	t.Run("a crew default follows the agent to another rule crew's default", func(t *testing.T) {
		agent := seedAgentRow(t, db, "defaults-mover", ws, crewA, "Mover", "defaults-mover", "AGENT")
		if _, _, m, o, _ := state(agent); m != native(models[0]) || o != 0 {
			t.Fatalf("precondition: %s overridden=%d", m, o)
		}
		if w := covAUPatch(t, agents, user, ws, "OWNER", agent, `{"crew_id":"`+crewB+`"}`); w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		if _, p, m, o, payer := state(agent); p != bedrock.Provider || m != native(models[1]) || o != 0 || payer != account {
			t.Fatalf("crew A default kept under crew B: %s/%s overridden=%d payer=%q", p, m, o, payer)
		}
	})

	t.Run("moving in while switching to another provider keeps that provider", func(t *testing.T) {
		execOrFatal(t, db, `INSERT INTO agents(id,workspace_id,name,slug,cli_adapter) VALUES('defaults-switch',?,'Switch','defaults-switch','CLAUDE_CODE')`, ws)
		body := `{"crew_id":"` + crewA + `","cli_adapter":"CLAUDE_CODE","llm_provider":"ANTHROPIC","llm_model":"claude-sonnet-5-5"}`
		if w := covAUPatch(t, agents, user, ws, "OWNER", "defaults-switch", body); w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		if r, p, m, _, payer := state("defaults-switch"); r != "CLAUDE_CODE" || p != "ANTHROPIC" || m != "claude-sonnet-5-5" || payer != "" {
			t.Fatalf("explicit switch rewritten: %s/%s/%s payer=%q", r, p, m, payer)
		}
	})
}
