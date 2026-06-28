package kinds

// Coverage-focused tests for agent.go: the list helpers' error
// branches, binding loops, decode helpers, diffPatch field matrix, and
// the Plan Exec failure paths. Uses the shared covClient defined in
// routine_cov_test.go.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/manifest/internalapi"
)

func agentCovBoolPtr(b bool) *bool    { return &b }
func agentCovStrPtr(s string) *string { return &s }

// minimal valid document used by the Plan tests.
func agentCovSampleDoc() AgentDocument {
	return AgentDocument{
		APIVersion: agentAPIVersion,
		Kind:       agentKind,
		Metadata:   internalapi.Metadata{Name: "Viktor", Slug: "viktor"},
		Spec: AgentSpec{
			CrewSlug:    "alpha",
			AgentRole:   "AGENT",
			CLIAdapter:  "CLAUDE_CODE",
			ToolProfile: "CODING",
			Prompt:      "be helpful",
		},
	}
}

// ── list helpers: shared error matrix ───────────────────────────────

func TestAgentCov_ListHelpers(t *testing.T) {
	t.Parallel()

	type lister struct {
		name string
		path string
		call func(internalapi.Client) (int, error)
		ok   string // body that decodes to exactly one row
	}
	listers := []lister{
		{
			name: "agentListAll", path: "/api/v1/agents",
			call: func(c internalapi.Client) (int, error) {
				rows, err := agentListAll(context.Background(), c)
				return len(rows), err
			},
			ok: `[{"id":"a1","slug":"viktor","name":"Viktor"}]`,
		},
		{
			name: "agentListCrews", path: "/api/v1/crews",
			call: func(c internalapi.Client) (int, error) {
				rows, err := agentListCrews(context.Background(), c)
				return len(rows), err
			},
			ok: `[{"id":"c1","slug":"alpha"}]`,
		},
		{
			name: "agentListSkills", path: "/api/v1/skills",
			call: func(c internalapi.Client) (int, error) {
				rows, err := agentListSkills(context.Background(), c)
				return len(rows), err
			},
			ok: `[{"id":"s1","slug":"research"}]`,
		},
		{
			name: "agentListCredentials", path: "/api/v1/credentials",
			call: func(c internalapi.Client) (int, error) {
				rows, err := agentListCredentials(context.Background(), c)
				return len(rows), err
			},
			ok: `[{"id":"cr1","name":"API_KEY"}]`,
		},
	}

	for _, l := range listers {
		t.Run(l.name, func(t *testing.T) {
			t.Parallel()

			// transport error
			c := newCovClient(map[string]covRoute{"GET " + l.path: {err: errors.New("down")}})
			if _, err := l.call(c); err == nil || !strings.Contains(err.Error(), l.path) {
				t.Errorf("transport: got %v", err)
			}
			// non-2xx
			c = newCovClient(map[string]covRoute{"GET " + l.path: {status: 500, body: "boom"}})
			if _, err := l.call(c); err == nil || !strings.Contains(err.Error(), "unexpected status 500") {
				t.Errorf("500: got %v", err)
			}
			// body read failure
			c = newCovClient(map[string]covRoute{"GET " + l.path: {badBody: true}})
			if _, err := l.call(c); err == nil || !strings.Contains(err.Error(), "read") {
				t.Errorf("bad body: got %v", err)
			}
			// empty body → zero rows, nil error
			c = newCovClient(map[string]covRoute{"GET " + l.path: {body: ""}})
			if n, err := l.call(c); err != nil || n != 0 {
				t.Errorf("empty: n=%d err=%v", n, err)
			}
			// decode error
			c = newCovClient(map[string]covRoute{"GET " + l.path: {body: "not json"}})
			if _, err := l.call(c); err == nil || !strings.Contains(err.Error(), "decode") {
				t.Errorf("decode: got %v", err)
			}
			// success
			c = newCovClient(map[string]covRoute{"GET " + l.path: {body: l.ok}})
			if n, err := l.call(c); err != nil || n != 1 {
				t.Errorf("ok: n=%d err=%v", n, err)
			}
		})
	}
}

func TestAgentCov_ListSkillSlugsAndCredentialEnvNames(t *testing.T) {
	t.Parallel()
	skillsPath := "/api/v1/agents/a1/skills"
	credsPath := "/api/v1/agents/a1/credentials"

	t.Run("skill slugs success sorted", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + skillsPath: {body: `[
			{"skill":{"slug":"zeta"}},
			{"skill":{"slug":"alpha"}},
			{"skill":{"slug":""}}
		]`}})
		out, err := agentListSkillSlugs(context.Background(), c, "a1")
		if err != nil || len(out) != 2 || out[0] != "alpha" || out[1] != "zeta" {
			t.Fatalf("out=%v err=%v", out, err)
		}
	})
	t.Run("skill slugs error branches", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + skillsPath: {err: errors.New("down")}})
		if _, err := agentListSkillSlugs(context.Background(), c, "a1"); err == nil {
			t.Error("transport: want error")
		}
		c = newCovClient(map[string]covRoute{"GET " + skillsPath: {status: 500, body: "x"}})
		if _, err := agentListSkillSlugs(context.Background(), c, "a1"); err == nil {
			t.Error("500: want error")
		}
		c = newCovClient(map[string]covRoute{"GET " + skillsPath: {badBody: true}})
		if _, err := agentListSkillSlugs(context.Background(), c, "a1"); err == nil {
			t.Error("bad body: want error")
		}
		c = newCovClient(map[string]covRoute{"GET " + skillsPath: {body: ""}})
		if out, err := agentListSkillSlugs(context.Background(), c, "a1"); err != nil || out != nil {
			t.Errorf("empty: out=%v err=%v", out, err)
		}
		c = newCovClient(map[string]covRoute{"GET " + skillsPath: {body: "not json"}})
		if _, err := agentListSkillSlugs(context.Background(), c, "a1"); err == nil {
			t.Error("decode: want error")
		}
	})

	t.Run("credential env names success sorted", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + credsPath: {body: `[
			{"env_var_name":"Z_KEY"},
			{"env_var_name":"A_KEY"},
			{"env_var_name":""}
		]`}})
		out, err := agentListCredentialEnvNames(context.Background(), c, "a1")
		if err != nil || len(out) != 2 || out[0] != "A_KEY" || out[1] != "Z_KEY" {
			t.Fatalf("out=%v err=%v", out, err)
		}
	})
	t.Run("credential env names error branches", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + credsPath: {err: errors.New("down")}})
		if _, err := agentListCredentialEnvNames(context.Background(), c, "a1"); err == nil {
			t.Error("transport: want error")
		}
		c = newCovClient(map[string]covRoute{"GET " + credsPath: {status: 403, body: "no"}})
		if _, err := agentListCredentialEnvNames(context.Background(), c, "a1"); err == nil {
			t.Error("403: want error")
		}
		c = newCovClient(map[string]covRoute{"GET " + credsPath: {badBody: true}})
		if _, err := agentListCredentialEnvNames(context.Background(), c, "a1"); err == nil {
			t.Error("bad body: want error")
		}
		c = newCovClient(map[string]covRoute{"GET " + credsPath: {body: ""}})
		if out, err := agentListCredentialEnvNames(context.Background(), c, "a1"); err != nil || out != nil {
			t.Errorf("empty: out=%v err=%v", out, err)
		}
		c = newCovClient(map[string]covRoute{"GET " + credsPath: {body: "not json"}})
		if _, err := agentListCredentialEnvNames(context.Background(), c, "a1"); err == nil {
			t.Error("decode: want error")
		}
	})
}

// ── decode / status helpers ─────────────────────────────────────────

func TestAgentCov_DecodeCreateResponse(t *testing.T) {
	t.Parallel()
	out, err := agentDecodeCreateResponse(nil)
	if err != nil || out == nil || out.ID != "" {
		t.Errorf("nil reader: out=%+v err=%v", out, err)
	}
	out, err = agentDecodeCreateResponse(strings.NewReader(""))
	if err != nil || out.ID != "" {
		t.Errorf("empty: out=%+v err=%v", out, err)
	}
	if _, err = agentDecodeCreateResponse(covErrReader{}); err == nil {
		t.Error("bad reader: want error")
	}
	if _, err = agentDecodeCreateResponse(strings.NewReader("not json")); err == nil {
		t.Error("invalid JSON: want error")
	}
	out, err = agentDecodeCreateResponse(strings.NewReader(`{"id":"a1"}`))
	if err != nil || out.ID != "a1" {
		t.Errorf("valid: out=%+v err=%v", out, err)
	}
}

func TestAgentCov_CheckStatusAndReadAll(t *testing.T) {
	t.Parallel()
	if err := checkStatus(nil, "op"); err == nil || !strings.Contains(err.Error(), "response is nil") {
		t.Errorf("nil resp: got %v", err)
	}
	err := checkStatus(&internalapi.Response{StatusCode: 500, Body: strings.NewReader("why")}, "op")
	if err == nil || !strings.Contains(err.Error(), "unexpected status 500") || !strings.Contains(err.Error(), "why") {
		t.Errorf("500: got %v", err)
	}
	if err := checkStatus(&internalapi.Response{StatusCode: 204}, "op"); err != nil {
		t.Errorf("204: got %v", err)
	}
	if b, err := readAll(nil); b != nil || err != nil {
		t.Errorf("readAll(nil) = %v, %v", b, err)
	}
}

// ── binding loops ───────────────────────────────────────────────────

func TestAgentCov_BindSkills(t *testing.T) {
	t.Parallel()
	skillsList := covRoute{body: `[{"id":"s1","slug":"research"}]`}

	t.Run("no skills → no calls", func(t *testing.T) {
		c := newCovClient(nil)
		if err := agentBindSkills(context.Background(), c, "a1", "viktor", nil); err != nil {
			t.Fatalf("got %v", err)
		}
		if len(c.calls) != 0 {
			t.Errorf("unexpected calls %v", c.calls)
		}
	})
	t.Run("catalog list error", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET /api/v1/skills": {err: errors.New("down")}})
		err := agentBindSkills(context.Background(), c, "a1", "viktor", []string{"research"})
		if err == nil || !strings.Contains(err.Error(), "list skills for binding") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("unknown slug", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET /api/v1/skills": skillsList})
		err := agentBindSkills(context.Background(), c, "a1", "viktor", []string{"ghost"})
		if err == nil || !strings.Contains(err.Error(), `skill "ghost" not found`) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("bind POST transport error", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{
			"GET /api/v1/skills":            skillsList,
			"POST /api/v1/agents/a1/skills": {err: errors.New("down")},
		})
		err := agentBindSkills(context.Background(), c, "a1", "viktor", []string{"research"})
		if err == nil || !strings.Contains(err.Error(), `bind skill "research"`) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("409 tolerated", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{
			"GET /api/v1/skills":            skillsList,
			"POST /api/v1/agents/a1/skills": {status: 409, body: `{"already_assigned":true}`},
		})
		if err := agentBindSkills(context.Background(), c, "a1", "viktor", []string{"research"}); err != nil {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("bind POST 500", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{
			"GET /api/v1/skills":            skillsList,
			"POST /api/v1/agents/a1/skills": {status: 500, body: "boom"},
		})
		err := agentBindSkills(context.Background(), c, "a1", "viktor", []string{"research"})
		if err == nil || !strings.Contains(err.Error(), "unexpected status 500") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("success", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{
			"GET /api/v1/skills":            skillsList,
			"POST /api/v1/agents/a1/skills": {status: 200, body: `{}`},
		})
		if err := agentBindSkills(context.Background(), c, "a1", "viktor", []string{"research"}); err != nil {
			t.Fatalf("got %v", err)
		}
		if !c.sawCall("POST /api/v1/agents/a1/skills") {
			t.Errorf("expected POST, got %v", c.calls)
		}
	})
}

func TestAgentCov_BindEnvRefs(t *testing.T) {
	t.Parallel()
	credsList := covRoute{body: `[{"id":"cr1","name":"API_KEY"}]`}

	t.Run("no refs → no calls", func(t *testing.T) {
		c := newCovClient(nil)
		if err := agentBindEnvRefs(context.Background(), c, "a1", "viktor", nil); err != nil {
			t.Fatalf("got %v", err)
		}
		if len(c.calls) != 0 {
			t.Errorf("unexpected calls %v", c.calls)
		}
	})
	t.Run("catalog list error", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET /api/v1/credentials": {err: errors.New("down")}})
		err := agentBindEnvRefs(context.Background(), c, "a1", "viktor", []string{"API_KEY"})
		if err == nil || !strings.Contains(err.Error(), "list credentials for binding") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("unknown env ref", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET /api/v1/credentials": credsList})
		err := agentBindEnvRefs(context.Background(), c, "a1", "viktor", []string{"GHOST_KEY"})
		if err == nil || !strings.Contains(err.Error(), `env_ref "GHOST_KEY" has no matching workspace credential`) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("bind POST transport error", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{
			"GET /api/v1/credentials":            credsList,
			"POST /api/v1/agents/a1/credentials": {err: errors.New("down")},
		})
		err := agentBindEnvRefs(context.Background(), c, "a1", "viktor", []string{"API_KEY"})
		if err == nil || !strings.Contains(err.Error(), `bind env_ref "API_KEY"`) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("409 tolerated then 500 fails", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{
			"GET /api/v1/credentials":            credsList,
			"POST /api/v1/agents/a1/credentials": {status: 409, body: `{}`},
		})
		if err := agentBindEnvRefs(context.Background(), c, "a1", "viktor", []string{"API_KEY"}); err != nil {
			t.Fatalf("409: got %v", err)
		}
		c = newCovClient(map[string]covRoute{
			"GET /api/v1/credentials":            credsList,
			"POST /api/v1/agents/a1/credentials": {status: 500, body: "boom"},
		})
		err := agentBindEnvRefs(context.Background(), c, "a1", "viktor", []string{"API_KEY"})
		if err == nil || !strings.Contains(err.Error(), "unexpected status 500") {
			t.Fatalf("500: got %v", err)
		}
	})
}

// ── diffPatch matrix ────────────────────────────────────────────────

func TestAgentCov_DiffPatch(t *testing.T) {
	t.Parallel()

	remote := &AgentRemote{
		ID:             "a1",
		Slug:           "viktor",
		Name:           "Old Name",
		Description:    agentCovStrPtr("old desc"),
		RoleTitle:      agentCovStrPtr("Old Title"),
		AgentRole:      "AGENT",
		CLIAdapter:     "OPENCODE",
		LLMProvider:    agentCovStrPtr("OPENAI"),
		LLMModel:       agentCovStrPtr("old-model"),
		SystemPrompt:   agentCovStrPtr("old prompt"),
		TimeoutSeconds: 600,
		ToolProfile:    "MINIMAL",
		MemoryEnabled:  false,
		CrewID:         agentCovStrPtr("crew_old"),
	}

	d := agentCovSampleDoc()
	d.Metadata.Description = "new desc"
	d.Spec.RoleTitle = "New Title"
	d.Spec.AgentRole = "LEAD"
	d.Spec.LLM = LLMSpec{Provider: "ANTHROPIC", Model: "new-model"}
	d.Spec.TimeoutSeconds = 1200
	d.Spec.MemoryEnabled = agentCovBoolPtr(true)

	patch := d.diffPatch(remote, "crew_new")
	wantKeys := []string{
		"name", "description", "role_title", "agent_role", "cli_adapter",
		"llm_provider", "llm_model", "tool_profile", "timeout_seconds",
		"system_prompt", "memory_enabled", "crew_id",
	}
	for _, k := range wantKeys {
		if _, ok := patch[k]; !ok {
			t.Errorf("patch missing %q: %v", k, patch)
		}
	}
	if patch["crew_id"] != "crew_new" || patch["agent_role"] != "LEAD" {
		t.Errorf("patch values wrong: %v", patch)
	}

	// Fully matching remote → empty patch.
	same := &AgentRemote{
		ID:             "a1",
		Name:           d.Metadata.Name,
		Description:    agentCovStrPtr("new desc"),
		RoleTitle:      agentCovStrPtr("New Title"),
		AgentRole:      "LEAD",
		CLIAdapter:     d.Spec.CLIAdapter,
		LLMProvider:    agentCovStrPtr("ANTHROPIC"),
		LLMModel:       agentCovStrPtr("new-model"),
		SystemPrompt:   agentCovStrPtr(d.Spec.Prompt),
		TimeoutSeconds: 1200,
		ToolProfile:    d.Spec.ToolProfile,
		MemoryEnabled:  true,
		CrewID:         agentCovStrPtr("crew_new"),
	}
	if patch := d.diffPatch(same, "crew_new"); len(patch) != 0 {
		t.Errorf("matching remote should yield empty patch, got %v", patch)
	}
}

// ── lookups ─────────────────────────────────────────────────────────

func TestAgentCov_LookupAgentRemoteBySlug(t *testing.T) {
	t.Parallel()
	body := `[{"id":"a1","slug":"viktor","name":"Viktor"}]`

	c := newCovClient(map[string]covRoute{"GET /api/v1/agents": {body: body}})
	got, err := LookupAgentRemoteBySlug(context.Background(), c, "viktor")
	if err != nil || got == nil || got.ID != "a1" {
		t.Fatalf("found: got=%+v err=%v", got, err)
	}
	got, err = LookupAgentRemoteBySlug(context.Background(), c, "ghost")
	if err != nil || got != nil {
		t.Fatalf("missing: got=%+v err=%v", got, err)
	}
	c = newCovClient(map[string]covRoute{"GET /api/v1/agents": {status: 500, body: "x"}})
	if _, err := LookupAgentRemoteBySlug(context.Background(), c, "viktor"); err == nil {
		t.Fatal("list error: want error")
	}
}

func TestAgentCov_LookupCrewIDBySlug_NotFound(t *testing.T) {
	t.Parallel()
	c := newCovClient(map[string]covRoute{"GET /api/v1/crews": {body: `[{"id":"c1","slug":"alpha"}]`}})
	if _, err := LookupCrewIDBySlug(context.Background(), c, "ghost"); err == nil || !strings.Contains(err.Error(), `crew with slug "ghost" not found`) {
		t.Fatalf("got %v", err)
	}
}

// ── Export ──────────────────────────────────────────────────────────

func TestAgentCov_ExportAgents(t *testing.T) {
	t.Parallel()

	t.Run("list agents error", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET /api/v1/agents": {status: 500, body: "x"}})
		if _, err := ExportAgents(context.Background(), c); err == nil || !strings.Contains(err.Error(), "export agents") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("list crews error", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{
			"GET /api/v1/agents": {body: `[]`},
			"GET /api/v1/crews":  {err: errors.New("down")},
		})
		if _, err := ExportAgents(context.Background(), c); err == nil || !strings.Contains(err.Error(), "list crews") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("full round trip", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{
			"GET /api/v1/agents": {body: `[{
				"id":"a1","slug":"viktor","name":"Viktor",
				"description":"the architect","role_title":"Architect",
				"agent_role":"LEAD","cli_adapter":"CLAUDE_CODE",
				"llm_provider":"ANTHROPIC","llm_model":"claude-x",
				"system_prompt":"be precise","timeout_seconds":900,
				"tool_profile":"CODING","memory_enabled":true,
				"crew_id":"c1"
			}]`},
			"GET /api/v1/crews":                 {body: `[{"id":"c1","slug":"alpha"}]`},
			"GET /api/v1/agents/a1/skills":      {body: `[{"skill":{"slug":"research"}}]`},
			"GET /api/v1/agents/a1/credentials": {body: `[{"env_var_name":"API_KEY"}]`},
		})
		docs, err := ExportAgents(context.Background(), c)
		if err != nil || len(docs) != 1 {
			t.Fatalf("docs=%v err=%v", docs, err)
		}
		got := docs[0]
		if got.Metadata.Slug != "viktor" || got.Metadata.Description != "the architect" {
			t.Errorf("metadata: %+v", got.Metadata)
		}
		if got.Spec.CrewSlug != "alpha" || got.Spec.RoleTitle != "Architect" {
			t.Errorf("crew/title: %+v", got.Spec)
		}
		if got.Spec.LLM.Provider != "ANTHROPIC" || got.Spec.LLM.Model != "claude-x" {
			t.Errorf("llm: %+v", got.Spec.LLM)
		}
		if got.Spec.Prompt != "be precise" || got.Spec.TimeoutSeconds != 900 {
			t.Errorf("prompt/timeout: %+v", got.Spec)
		}
		if got.Spec.MemoryEnabled == nil || !*got.Spec.MemoryEnabled {
			t.Errorf("memory_enabled: %+v", got.Spec.MemoryEnabled)
		}
		if len(got.Spec.Skills) != 1 || got.Spec.Skills[0] != "research" {
			t.Errorf("skills: %v", got.Spec.Skills)
		}
		if len(got.Spec.EnvRefs) != 1 || got.Spec.EnvRefs[0] != "API_KEY" {
			t.Errorf("env_refs: %v", got.Spec.EnvRefs)
		}
	})
}

// ── Plan failure paths ──────────────────────────────────────────────

func TestAgentCov_Plan_ResolveCrewError(t *testing.T) {
	t.Parallel()
	d := agentCovSampleDoc()
	c := newCovClient(map[string]covRoute{"GET /api/v1/crews": {err: errors.New("down")}})
	if _, err := d.Plan(context.Background(), c, nil); err == nil || !strings.Contains(err.Error(), "resolve crew_slug") {
		t.Fatalf("got %v", err)
	}
}

func TestAgentCov_Plan_CreateExecFailures(t *testing.T) {
	t.Parallel()
	crews := covRoute{body: `[{"id":"c1","slug":"alpha"}]`}

	planCreate := func(t *testing.T, c *covClient) internalapi.PlanItem {
		t.Helper()
		d := agentCovSampleDoc()
		items, err := d.Plan(context.Background(), c, nil)
		if err != nil || len(items) != 1 || items[0].Action != internalapi.ActionCreate {
			t.Fatalf("plan: items=%+v err=%v", items, err)
		}
		return items[0]
	}

	t.Run("POST transport error", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{
			"GET /api/v1/crews":   crews,
			"POST /api/v1/agents": {err: errors.New("down")},
		})
		item := planCreate(t, c)
		if err := item.Exec(context.Background(), c); err == nil || !strings.Contains(err.Error(), "POST /api/v1/agents") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("POST 500", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{
			"GET /api/v1/crews":   crews,
			"POST /api/v1/agents": {status: 500, body: "boom"},
		})
		item := planCreate(t, c)
		if err := item.Exec(context.Background(), c); err == nil || !strings.Contains(err.Error(), "unexpected status 500") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("undecodable create response", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{
			"GET /api/v1/crews":   crews,
			"POST /api/v1/agents": {status: 201, body: "not json"},
		})
		item := planCreate(t, c)
		if err := item.Exec(context.Background(), c); err == nil || !strings.Contains(err.Error(), "decode create response") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("create response missing id", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{
			"GET /api/v1/crews":   crews,
			"POST /api/v1/agents": {status: 201, body: `{}`},
		})
		item := planCreate(t, c)
		if err := item.Exec(context.Background(), c); err == nil || !strings.Contains(err.Error(), "missing id") {
			t.Fatalf("got %v", err)
		}
	})
}

func TestAgentCov_Plan_UpdateExec(t *testing.T) {
	t.Parallel()
	crews := covRoute{body: `[{"id":"c1","slug":"alpha"}]`}
	remote := &AgentRemote{
		ID:             "a1",
		Slug:           "viktor",
		Name:           "Old Name", // drift → name patch
		AgentRole:      "AGENT",
		CLIAdapter:     "CLAUDE_CODE",
		ToolProfile:    "CODING",
		SystemPrompt:   agentCovStrPtr("be helpful"),
		TimeoutSeconds: 1800,
		MemoryEnabled:  true,
		CrewID:         agentCovStrPtr("c1"),
	}

	t.Run("PATCH 500", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{
			"GET /api/v1/crews":       crews,
			"PATCH /api/v1/agents/a1": {status: 500, body: "nope"},
		})
		d := agentCovSampleDoc()
		items, err := d.Plan(context.Background(), c, remote)
		if err != nil || len(items) != 1 || items[0].Action != internalapi.ActionUpdate {
			t.Fatalf("plan: items=%+v err=%v", items, err)
		}
		if err := items[0].Exec(context.Background(), c); err == nil || !strings.Contains(err.Error(), "unexpected status 500") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("PATCH transport error", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{
			"GET /api/v1/crews":       crews,
			"PATCH /api/v1/agents/a1": {err: errors.New("down")},
		})
		d := agentCovSampleDoc()
		items, err := d.Plan(context.Background(), c, remote)
		if err != nil {
			t.Fatalf("plan: %v", err)
		}
		if err := items[0].Exec(context.Background(), c); err == nil || !strings.Contains(err.Error(), "PATCH /api/v1/agents/a1") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("unchanged when remote matches and no bindings", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET /api/v1/crews": crews})
		d := agentCovSampleDoc()
		same := *remote
		same.Name = d.Metadata.Name
		items, err := d.Plan(context.Background(), c, &same)
		if err != nil || len(items) != 1 || items[0].Action != internalapi.ActionUnchanged {
			t.Fatalf("items=%+v err=%v", items, err)
		}
	})
}
