package manifest

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestDefaultExportOptions(t *testing.T) {
	opts := DefaultExportOptions()
	if !opts.IncludeCredentials || !opts.IncludeSkillBodies {
		t.Errorf("defaults should inline everything, got %+v", opts)
	}
}

func TestHasDevcontainerFields(t *testing.T) {
	intp := func(v int) *int { return &v }
	floatp := func(v float64) *float64 { return &v }
	strp := func(v string) *string { return &v }

	cases := []struct {
		name string
		crew *CrewResponse
		want bool
	}{
		{"nil crew", nil, false},
		{"no fields", &CrewResponse{}, false},
		{"runtime image", &CrewResponse{RuntimeImage: strp("img")}, true},
		{"network mode", &CrewResponse{NetworkMode: strp("restricted")}, true},
		{"devcontainer config", &CrewResponse{DevcontainerConfig: strp("{}")}, true},
		{"mise config", &CrewResponse{MiseConfig: strp("[tools]")}, true},
		{"memory", &CrewResponse{ContainerMemoryMB: intp(2048)}, true},
		{"cpus", &CrewResponse{ContainerCPUs: floatp(1.5)}, true},
		{"ttl", &CrewResponse{ContainerTTLHours: intp(4)}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasDevcontainerFields(tc.crew); got != tc.want {
				t.Errorf("hasDevcontainerFields = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestExportCrew_NotFoundAndLookupError(t *testing.T) {
	ctx := context.Background()
	t.Run("crew not found", func(t *testing.T) {
		stub := newCovStub()
		stub.on("GET", "/api/v1/crews", 200, `[]`)
		_, err := ExportCrew(ctx, NewClient(stub), "ghost", DefaultExportOptions())
		if err == nil || !strings.Contains(err.Error(), `crew "ghost" not found`) {
			t.Fatalf("want not-found error, got %v", err)
		}
	})
	t.Run("lookup error", func(t *testing.T) {
		stub := newCovStub()
		stub.onErr("GET", "/api/v1/crews", errors.New("down"))
		_, err := ExportCrew(ctx, NewClient(stub), "ghost", DefaultExportOptions())
		if err == nil || !strings.Contains(err.Error(), "look up crew") {
			t.Fatalf("want lookup error, got %v", err)
		}
	})
	t.Run("agents list error", func(t *testing.T) {
		stub := newCovStub()
		stub.on("GET", "/api/v1/crews", 200, `[{"id":"c1","slug":"t","name":"T"}]`)
		stub.onErr("GET", "/api/v1/agents?crew_id=c1", errors.New("down"))
		_, err := ExportCrew(ctx, NewClient(stub), "t", DefaultExportOptions())
		if err == nil || !strings.Contains(err.Error(), "list agents") {
			t.Fatalf("want list-agents error, got %v", err)
		}
	})
}

// exportStubFullCrew wires a covStubAPI with one fully-loaded crew:
// devcontainer fields, sidecar services, two agents (one with skill +
// credential bindings), and an MCP server.
func exportStubFullCrew() *covStubAPI {
	stub := newCovStub()
	stub.on("GET", "/api/v1/crews", 200, `[{
		"id":"c1","slug":"ops","name":"Ops","description":"d","color":"#fff","icon":"gear",
		"container_memory_mb":2048,"container_cpus":1.5,"container_ttl_hours":8,
		"network_mode":"restricted","allowed_domains":["example.com"],
		"runtime_image":"ghcr.io/x/runtime:1",
		"devcontainer_config":"{\"image\":\"dc-img\"}",
		"mise_config":"[tools]\nnode = \"22\"",
		"services_json":"[{\"name\":\"redis\",\"image\":\"redis:7\"}]"
	}]`)
	stub.on("GET", "/api/v1/agents?crew_id=c1", 200, `[
		{"id":"a2","slug":"zoe","name":"Zoe","agent_role":"AGENT","cli_adapter":"CLAUDE_CODE","tool_profile":"CODING","timeout_seconds":900,"memory_enabled":false,"system_prompt":"hi zoe"},
		{"id":"a1","slug":"amy","name":"Amy","agent_role":"LEAD","cli_adapter":"CLAUDE_CODE","tool_profile":"FULL","timeout_seconds":1800,"memory_enabled":true,
		 "llm_provider":"ANTHROPIC","llm_model":"claude-x","role_title":"Lead","system_prompt":"hi amy"}
	]`)
	stub.on("GET", "/api/v1/crews/c1/integrations", 200, `[
		{"id":"m1","crew_id":"c1","name":"github","display_name":"GitHub","transport":"stdio","command":"gh-mcp","enabled":true}
	]`)
	stub.on("GET", "/api/v1/agents/a1/skills", 200, `[
		{"id":"b1","agent_id":"a1","skill_id":"s1","skill":{"slug":"house-style"}}
	]`)
	stub.on("GET", "/api/v1/agents/a1/credentials", 200, `[
		{"id":"cb1","agent_id":"a1","credential_id":"cr1","credential_name":"GH_TOKEN","env_var_name":"GH_TOKEN"}
	]`)
	stub.on("GET", "/api/v1/agents/a2/skills", 200, `[]`)
	stub.on("GET", "/api/v1/agents/a2/credentials", 200, `[]`)
	stub.on("GET", "/api/v1/workspaces/ws_cov/skills", 200, `[{"id":"s1","slug":"house-style"}]`)
	stub.on("GET", "/api/v1/skills/s1", 200, `{"content":"---\nname: house-style\n---\nthe body"}`)
	stub.on("GET", "/api/v1/credentials", 200, `[
		{"id":"cr1","name":"GH_TOKEN","type":"API_KEY","provider":"GITHUB","status":"ACTIVE"}
	]`)
	return stub
}

func TestExportCrew_FullRoundTrip(t *testing.T) {
	stub := exportStubFullCrew()
	out, err := ExportCrew(context.Background(), NewClient(stub), "ops", DefaultExportOptions())
	if err != nil {
		t.Fatalf("ExportCrew: %v", err)
	}

	// The exported YAML must reload as a legacy crew bundle.
	b, err := Load([]byte(out))
	if err != nil {
		t.Fatalf("reload exported YAML: %v\n%s", err, out)
	}
	if len(b.Documents) != 1 {
		t.Fatalf("want 1 crew document, got %d", len(b.Documents))
	}
	doc := b.Documents[0]
	if doc.Metadata.Slug != "ops" || doc.Metadata.Name != "Ops" {
		t.Errorf("metadata = %+v", doc.Metadata)
	}
	spec := doc.Spec
	if spec == nil {
		t.Fatal("spec missing")
	}

	// Agents sorted by slug: amy before zoe.
	if len(spec.Agents) != 2 || spec.Agents[0].Slug != "amy" || spec.Agents[1].Slug != "zoe" {
		t.Fatalf("agents not sorted by slug: %+v", spec.Agents)
	}
	amy := spec.Agents[0]
	if amy.AgentRole != "LEAD" || amy.LLM.Provider != "ANTHROPIC" || amy.LLM.Model != "claude-x" {
		t.Errorf("amy fields lost: %+v", amy)
	}
	if len(amy.Skills) != 1 || amy.Skills[0] != "house-style" {
		t.Errorf("amy skill binding lost: %v", amy.Skills)
	}
	if len(amy.EnvRefs) != 1 || amy.EnvRefs[0] != "GH_TOKEN" {
		t.Errorf("amy credential binding lost: %v", amy.EnvRefs)
	}

	// Skill body fetched via fetchSkillContent.
	if len(spec.Skills) != 1 || spec.Skills[0].Slug != "house-style" {
		t.Fatalf("skills = %+v", spec.Skills)
	}
	if !strings.Contains(spec.Skills[0].Inline, "the body") {
		t.Errorf("skill body not inlined: %q", spec.Skills[0].Inline)
	}

	// Credential slot with type/provider hydrated from workspace list.
	if len(spec.Credentials) != 1 || spec.Credentials[0].EnvVar != "GH_TOKEN" {
		t.Fatalf("credentials = %+v", spec.Credentials)
	}
	if spec.Credentials[0].Type != "API_KEY" || spec.Credentials[0].Provider != "GITHUB" {
		t.Errorf("credential type/provider lost: %+v", spec.Credentials[0])
	}

	// Devcontainer block round-trips.
	dc := spec.Devcontainer
	if dc == nil {
		t.Fatal("devcontainer missing")
	}
	if dc.MemoryMB == nil || *dc.MemoryMB != 2048 || dc.CPUs == nil || *dc.CPUs != 1.5 {
		t.Errorf("container shape lost: %+v", dc)
	}
	if dc.NetworkMode != "restricted" || len(dc.AllowedDomains) != 1 || dc.RuntimeImage != "ghcr.io/x/runtime:1" {
		t.Errorf("network policy lost: %+v", dc)
	}
	if !strings.Contains(dc.Mise, "node") {
		t.Errorf("mise config lost: %q", dc.Mise)
	}
	if dc.Raw == nil || dc.Raw["image"] != "dc-img" {
		t.Errorf("raw devcontainer_config lost: %+v", dc.Raw)
	}

	// Services from services_json.
	if len(spec.Services) != 1 || spec.Services[0].Name != "redis" || spec.Services[0].Image != "redis:7" {
		t.Errorf("services lost: %+v", spec.Services)
	}

	// MCP server.
	if len(spec.MCPServers) != 1 || spec.MCPServers[0].Name != "github" || spec.MCPServers[0].Command != "gh-mcp" {
		t.Errorf("mcp server lost: %+v", spec.MCPServers)
	}
}

func TestExportCrew_SkillBodyUnavailableEmitsSentinel(t *testing.T) {
	stub := exportStubFullCrew()
	// Skill content endpoint fails — export must emit a sentinel
	// inline so the validator's one-of check still passes.
	stub.on("GET", "/api/v1/skills/s1", 500, `{"error":"x"}`)
	out, err := ExportCrew(context.Background(), NewClient(stub), "ops", DefaultExportOptions())
	if err != nil {
		t.Fatalf("ExportCrew: %v", err)
	}
	if !strings.Contains(out, "exported reference, supply body before re-apply") {
		t.Errorf("expected sentinel inline body, got:\n%s", out)
	}
}

func TestExportCrew_StripsCredentialsAndBodiesWhenAsked(t *testing.T) {
	stub := exportStubFullCrew()
	out, err := ExportCrew(context.Background(), NewClient(stub), "ops", ExportOptions{
		IncludeCredentials: false,
		IncludeSkillBodies: false,
	})
	if err != nil {
		t.Fatalf("ExportCrew: %v", err)
	}
	if strings.Contains(out, "GH_TOKEN") && strings.Contains(out, "credentials:") {
		t.Errorf("credentials should be omitted:\n%s", out)
	}
	if strings.Contains(out, "the body") {
		t.Errorf("skill body should be omitted:\n%s", out)
	}
}

func TestExportCrew_BindingFailuresAreNonFatal(t *testing.T) {
	stub := exportStubFullCrew()
	stub.onErr("GET", "/api/v1/agents/a1/skills", errors.New("down"))
	stub.onErr("GET", "/api/v1/agents/a1/credentials", errors.New("down"))
	out, err := ExportCrew(context.Background(), NewClient(stub), "ops", DefaultExportOptions())
	if err != nil {
		t.Fatalf("binding failures must not abort export: %v", err)
	}
	b, err := Load([]byte(out))
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	// Amy exports without bindings, the rest of the crew survives.
	spec := b.Documents[0].Spec
	if len(spec.Agents) != 2 {
		t.Fatalf("agents = %+v", spec.Agents)
	}
	if len(spec.Agents[0].Skills) != 0 || len(spec.Agents[0].EnvRefs) != 0 {
		t.Errorf("amy should have no bindings after fetch failure: %+v", spec.Agents[0])
	}
}

func TestExportWorkspace_AggregatesCrews(t *testing.T) {
	stub := exportStubFullCrew()
	stub.on("GET", "/api/v1/workspaces/ws_cov", 200, `{"name":"Acme","slug":"acme"}`)

	out, err := ExportWorkspace(context.Background(), NewClient(stub), DefaultExportOptions())
	if err != nil {
		t.Fatalf("ExportWorkspace: %v", err)
	}

	b, err := Load([]byte(out))
	if err != nil {
		t.Fatalf("reload workspace export: %v\n%s", err, out)
	}
	if len(b.Workspaces) != 1 {
		t.Fatalf("want 1 workspace doc, got %d", len(b.Workspaces))
	}
	ws := b.Workspaces[0]
	if ws.Metadata.Name != "Acme" || ws.Metadata.Slug != "acme" {
		t.Errorf("workspace meta = %+v", ws.Metadata)
	}
	if len(ws.Spec.Crews) != 1 {
		t.Fatalf("crews = %+v", ws.Spec.Crews)
	}
	crew := ws.Spec.Crews[0]
	if crew.SlugOverride != "ops" || crew.Name != "Ops" {
		t.Errorf("crew slug/name overrides lost: %+v", crew)
	}
	// Skills + credentials hoisted to workspace scope, stripped from crew.
	if len(crew.Skills) != 0 || len(crew.Credentials) != 0 {
		t.Errorf("crew-level skills/creds should be hoisted: %+v", crew)
	}
	if len(ws.Spec.Skills) != 1 || ws.Spec.Skills[0].Slug != "house-style" {
		t.Errorf("workspace skills = %+v", ws.Spec.Skills)
	}
	if !strings.Contains(ws.Spec.Skills[0].Inline, "the body") {
		t.Errorf("workspace skill body not inlined: %q", ws.Spec.Skills[0].Inline)
	}
	if len(ws.Spec.Credentials) != 1 || ws.Spec.Credentials[0].EnvVar != "GH_TOKEN" {
		t.Fatalf("workspace credentials = %+v", ws.Spec.Credentials)
	}
	if ws.Spec.Credentials[0].Provider != "GITHUB" || ws.Spec.Credentials[0].Type != "API_KEY" {
		t.Errorf("hoisted credential lost provider/type: %+v", ws.Spec.Credentials[0])
	}
}

func TestExportWorkspace_Errors(t *testing.T) {
	ctx := context.Background()
	t.Run("list crews fails", func(t *testing.T) {
		stub := newCovStub()
		stub.onErr("GET", "/api/v1/crews", errors.New("down"))
		_, err := ExportWorkspace(ctx, NewClient(stub), DefaultExportOptions())
		if err == nil || !strings.Contains(err.Error(), "list crews") {
			t.Fatalf("want list-crews error, got %v", err)
		}
	})
	t.Run("crew export fails", func(t *testing.T) {
		stub := newCovStub()
		stub.on("GET", "/api/v1/crews", 200, `[{"id":"c1","slug":"t","name":"T"}]`)
		stub.onErr("GET", "/api/v1/agents?crew_id=c1", errors.New("down"))
		_, err := ExportWorkspace(ctx, NewClient(stub), DefaultExportOptions())
		if err == nil || !strings.Contains(err.Error(), `export crew "t"`) {
			t.Fatalf("want export-crew error, got %v", err)
		}
	})
}

func TestWorkspaceMeta(t *testing.T) {
	ctx := context.Background()
	t.Run("empty workspace id", func(t *testing.T) {
		stub := newCovStub()
		stub.wsID = ""
		name, slug := workspaceMeta(ctx, NewClient(stub))
		if name != "" || slug != "" {
			t.Errorf("want empty meta, got %q/%q", name, slug)
		}
	})
	t.Run("fetch error", func(t *testing.T) {
		stub := newCovStub()
		stub.on("GET", "/api/v1/workspaces/ws_cov", 500, `{"error":"x"}`)
		name, slug := workspaceMeta(ctx, NewClient(stub))
		if name != "" || slug != "" {
			t.Errorf("want empty meta on error, got %q/%q", name, slug)
		}
	})
	t.Run("bad json", func(t *testing.T) {
		stub := newCovStub()
		stub.on("GET", "/api/v1/workspaces/ws_cov", 200, `not json`)
		name, slug := workspaceMeta(ctx, NewClient(stub))
		if name != "" || slug != "" {
			t.Errorf("want empty meta on bad json, got %q/%q", name, slug)
		}
	})
	t.Run("success", func(t *testing.T) {
		stub := newCovStub()
		stub.on("GET", "/api/v1/workspaces/ws_cov", 200, `{"name":"Acme","slug":"acme"}`)
		name, slug := workspaceMeta(ctx, NewClient(stub))
		if name != "Acme" || slug != "acme" {
			t.Errorf("got %q/%q", name, slug)
		}
	})
}
