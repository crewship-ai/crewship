package kinds

// Tests for kind: Agent (agent.go).
//
// The fake client below is agent-prefixed to avoid collisions with the
// per-kind fakes already in this directory (milestoneFakeClient,
// crewTemplateFakeClient, …). Each test wires only the endpoints the
// scenario exercises so a future server-side change in an unrelated
// route doesn't ripple into Agent test failures.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/manifest/internalapi"
)

// ── Fake client ────────────────────────────────────────────────────────────

type agentFakeCall struct {
	Method string
	Path   string
	Body   any
}

type agentFakeClient struct {
	wsID string

	// Fixtures the test seeds.
	crews       map[string]agentCrewStub // keyed by slug
	agents      map[string]AgentRemote   // keyed by slug
	skills      map[string]agentSkillStub
	credentials map[string]agentCredentialStub

	// agentSkillBindings / agentCredBindings index per agent id so
	// the bindings GETs we use in ExportAgents can return deterministic
	// rows.
	agentSkillBindings map[string][]string // agentID → slugs
	agentCredBindings  map[string][]string // agentID → env names

	// createResponse is what the POST /api/v1/agents fake returns.
	// Defaults to {"id":"agt_new"} via Post. Tests can override.
	createResponseID string

	// Per-route status overrides — set to non-zero to force a specific
	// code on the next matching call.
	postAgentsStatus      int
	patchAgentStatus      int
	postSkillBindStatus   int
	postCredBindStatus    int
	listAgentsStatus      int
	listCrewsStatus       int
	listSkillsStatus      int
	listCredentialsStatus int

	calls []agentFakeCall
}

func newAgentFake() *agentFakeClient {
	return &agentFakeClient{
		wsID:               "ws_test",
		crews:              map[string]agentCrewStub{},
		agents:             map[string]AgentRemote{},
		skills:             map[string]agentSkillStub{},
		credentials:        map[string]agentCredentialStub{},
		agentSkillBindings: map[string][]string{},
		agentCredBindings:  map[string][]string{},
		createResponseID:   "agt_new",
	}
}

func (f *agentFakeClient) WorkspaceID() string { return f.wsID }

func (f *agentFakeClient) record(method, path string, body any) {
	f.calls = append(f.calls, agentFakeCall{Method: method, Path: path, Body: body})
}

func agentJSONResp(status int, v any) *internalapi.Response {
	data, _ := json.Marshal(v)
	return &internalapi.Response{
		StatusCode: status,
		Body:       io.NopCloser(bytes.NewReader(data)),
	}
}

func (f *agentFakeClient) Get(_ context.Context, path string) (*internalapi.Response, error) {
	f.record("GET", path, nil)
	switch {
	case path == "/api/v1/agents":
		if f.listAgentsStatus != 0 {
			return agentJSONResp(f.listAgentsStatus, map[string]any{"error": "forced"}), nil
		}
		out := make([]AgentRemote, 0, len(f.agents))
		for _, a := range f.agents {
			out = append(out, a)
		}
		return agentJSONResp(200, out), nil
	case path == "/api/v1/crews":
		if f.listCrewsStatus != 0 {
			return agentJSONResp(f.listCrewsStatus, map[string]any{"error": "forced"}), nil
		}
		out := make([]agentCrewStub, 0, len(f.crews))
		for _, c := range f.crews {
			out = append(out, c)
		}
		return agentJSONResp(200, out), nil
	case path == "/api/v1/skills":
		if f.listSkillsStatus != 0 {
			return agentJSONResp(f.listSkillsStatus, map[string]any{"error": "forced"}), nil
		}
		out := make([]agentSkillStub, 0, len(f.skills))
		for _, s := range f.skills {
			out = append(out, s)
		}
		return agentJSONResp(200, out), nil
	case path == "/api/v1/credentials":
		if f.listCredentialsStatus != 0 {
			return agentJSONResp(f.listCredentialsStatus, map[string]any{"error": "forced"}), nil
		}
		out := make([]agentCredentialStub, 0, len(f.credentials))
		for _, c := range f.credentials {
			out = append(out, c)
		}
		return agentJSONResp(200, out), nil
	case strings.HasPrefix(path, "/api/v1/agents/") && strings.HasSuffix(path, "/skills"):
		agentID := strings.TrimSuffix(strings.TrimPrefix(path, "/api/v1/agents/"), "/skills")
		var out []map[string]any
		for _, slug := range f.agentSkillBindings[agentID] {
			out = append(out, map[string]any{
				"id":       "binding_" + slug,
				"agent_id": agentID,
				"skill":    map[string]any{"slug": slug},
			})
		}
		return agentJSONResp(200, out), nil
	case strings.HasPrefix(path, "/api/v1/agents/") && strings.HasSuffix(path, "/credentials"):
		agentID := strings.TrimSuffix(strings.TrimPrefix(path, "/api/v1/agents/"), "/credentials")
		var out []map[string]any
		for _, env := range f.agentCredBindings[agentID] {
			out = append(out, map[string]any{
				"id":           "credbind_" + env,
				"agent_id":     agentID,
				"env_var_name": env,
			})
		}
		return agentJSONResp(200, out), nil
	}
	return agentJSONResp(404, map[string]any{"error": "not stubbed: " + path}), nil
}

func (f *agentFakeClient) Post(_ context.Context, path string, body any) (*internalapi.Response, error) {
	f.record("POST", path, body)
	switch {
	case path == "/api/v1/agents":
		status := f.postAgentsStatus
		if status == 0 {
			status = 201
		}
		if status < 200 || status >= 300 {
			return agentJSONResp(status, map[string]any{"error": "forced"}), nil
		}
		return agentJSONResp(status, map[string]any{"id": f.createResponseID}), nil
	case strings.HasPrefix(path, "/api/v1/agents/") && strings.HasSuffix(path, "/skills"):
		status := f.postSkillBindStatus
		if status == 0 {
			status = 201
		}
		return agentJSONResp(status, map[string]any{"id": "bind_new"}), nil
	case strings.HasPrefix(path, "/api/v1/agents/") && strings.HasSuffix(path, "/credentials"):
		status := f.postCredBindStatus
		if status == 0 {
			status = 201
		}
		return agentJSONResp(status, map[string]any{"id": "credbind_new"}), nil
	}
	return agentJSONResp(404, map[string]any{"error": "not stubbed: " + path}), nil
}

func (f *agentFakeClient) Patch(_ context.Context, path string, body any) (*internalapi.Response, error) {
	f.record("PATCH", path, body)
	if strings.HasPrefix(path, "/api/v1/agents/") {
		status := f.patchAgentStatus
		if status == 0 {
			status = 200
		}
		return agentJSONResp(status, map[string]any{"ok": true}), nil
	}
	return agentJSONResp(404, map[string]any{"error": "not stubbed: " + path}), nil
}

func (f *agentFakeClient) Put(_ context.Context, path string, body any) (*internalapi.Response, error) {
	f.record("PUT", path, body)
	return agentJSONResp(404, map[string]any{"error": "not stubbed: " + path}), nil
}

func (f *agentFakeClient) Delete(_ context.Context, path string) (*internalapi.Response, error) {
	f.record("DELETE", path, nil)
	return agentJSONResp(404, map[string]any{"error": "not stubbed: " + path}), nil
}

// Compile-time interface assertion.
var _ internalapi.Client = (*agentFakeClient)(nil)

// findCall returns the first recorded call matching method+path or
// nil so assertions can use "if got == nil" instead of indexing.
func (f *agentFakeClient) findCall(method, path string) *agentFakeCall {
	for i := range f.calls {
		if f.calls[i].Method == method && f.calls[i].Path == path {
			return &f.calls[i]
		}
	}
	return nil
}

// countCalls returns how many recorded calls match method+path. Used
// for the binding tests where we want to assert "exactly N POSTs to
// /skills" without caring about ordering.
func (f *agentFakeClient) countCalls(method, path string) int {
	n := 0
	for _, c := range f.calls {
		if c.Method == method && c.Path == path {
			n++
		}
	}
	return n
}

// ── Helpers ─────────────────────────────────────────────────────────────────

func agentSampleDoc() *AgentDocument {
	memOn := true
	return &AgentDocument{
		APIVersion: agentAPIVersion,
		Kind:       agentKind,
		Metadata: internalapi.Metadata{
			Name: "Tomas",
			Slug: "tomas",
		},
		Spec: AgentSpec{
			CrewSlug:       "engineering",
			RoleTitle:      "Technical Architect",
			AgentRole:      "LEAD",
			CLIAdapter:     "CLAUDE_CODE",
			LLM:            LLMSpec{Provider: "ANTHROPIC", Model: "claude-haiku-4-5"},
			ToolProfile:    "FULL",
			TimeoutSeconds: 3600,
			MemoryEnabled:  &memOn,
			Prompt:         "You are Tomas, the Technical Architect.",
		},
	}
}

func agentCtxWithCrew(slug string) internalapi.WorkspaceContext {
	return internalapi.WorkspaceContext{
		DeclaredCrews: []internalapi.SlugLookup{
			{Slug: slug, Name: slug},
		},
	}
}

// ── 1. Validate: happy path ─────────────────────────────────────────────────

func TestAgent_Validate_HappyPath(t *testing.T) {
	doc := agentSampleDoc()
	if err := doc.Validate(agentCtxWithCrew("engineering")); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestAgent_Validate_HappyPath_EmptyContext_Tolerated(t *testing.T) {
	// Validate should NOT fail just because the caller hasn't seeded a
	// workspace context. The crew_slug FK check degrades to "skip"
	// when neither declared nor remote crews are known.
	doc := agentSampleDoc()
	if err := doc.Validate(internalapi.WorkspaceContext{}); err != nil {
		t.Fatalf("Validate with empty ctx: %v", err)
	}
}

// ── 2. Validate: required fields ────────────────────────────────────────────

func TestAgent_Validate_MissingName(t *testing.T) {
	doc := agentSampleDoc()
	doc.Metadata.Name = ""
	err := doc.Validate(agentCtxWithCrew("engineering"))
	if err == nil || !strings.Contains(err.Error(), "metadata.name is required") {
		t.Fatalf("want name-required error, got %v", err)
	}
}

func TestAgent_Validate_MissingSlug(t *testing.T) {
	doc := agentSampleDoc()
	doc.Metadata.Slug = ""
	err := doc.Validate(agentCtxWithCrew("engineering"))
	if err == nil || !strings.Contains(err.Error(), "metadata.slug is required") {
		t.Fatalf("want slug-required error, got %v", err)
	}
}

func TestAgent_Validate_MissingCrewSlug(t *testing.T) {
	doc := agentSampleDoc()
	doc.Spec.CrewSlug = ""
	err := doc.Validate(agentCtxWithCrew("engineering"))
	if err == nil || !strings.Contains(err.Error(), "spec.crew_slug is required") {
		t.Fatalf("want crew_slug-required error, got %v", err)
	}
}

// ── 3. Validate: enum errors ────────────────────────────────────────────────

func TestAgent_Validate_BadAgentRole(t *testing.T) {
	doc := agentSampleDoc()
	doc.Spec.AgentRole = "BOSS"
	err := doc.Validate(agentCtxWithCrew("engineering"))
	if err == nil || !strings.Contains(err.Error(), "invalid agent_role") {
		t.Fatalf("want agent_role enum error, got %v", err)
	}
}

func TestAgent_Validate_BadCLIAdapter(t *testing.T) {
	doc := agentSampleDoc()
	doc.Spec.CLIAdapter = "BORG"
	err := doc.Validate(agentCtxWithCrew("engineering"))
	if err == nil || !strings.Contains(err.Error(), "invalid cli_adapter") {
		t.Fatalf("want cli_adapter enum error, got %v", err)
	}
}

func TestAgent_Validate_BadLLMProvider(t *testing.T) {
	doc := agentSampleDoc()
	doc.Spec.LLM.Provider = "DEEPSEEK"
	err := doc.Validate(agentCtxWithCrew("engineering"))
	if err == nil || !strings.Contains(err.Error(), "invalid llm.provider") {
		t.Fatalf("want llm.provider enum error, got %v", err)
	}
}

func TestAgent_Validate_BadToolProfile(t *testing.T) {
	doc := agentSampleDoc()
	doc.Spec.ToolProfile = "MAXIMUM"
	err := doc.Validate(agentCtxWithCrew("engineering"))
	if err == nil || !strings.Contains(err.Error(), "invalid tool_profile") {
		t.Fatalf("want tool_profile enum error, got %v", err)
	}
}

func TestAgent_Validate_NegativeTimeout(t *testing.T) {
	doc := agentSampleDoc()
	doc.Spec.TimeoutSeconds = -1
	err := doc.Validate(agentCtxWithCrew("engineering"))
	if err == nil || !strings.Contains(err.Error(), "non-negative") {
		t.Fatalf("want non-negative timeout error, got %v", err)
	}
}

// ── 4. Validate: prompt / prompt_file rules ─────────────────────────────────

func TestAgent_Validate_PromptAndPromptFileMutuallyExclusive(t *testing.T) {
	doc := agentSampleDoc()
	doc.Spec.PromptFile = "./prompts/tomas.md"
	err := doc.Validate(agentCtxWithCrew("engineering"))
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("want mutually-exclusive error, got %v", err)
	}
}

func TestAgent_Validate_NeitherPromptNorPromptFile(t *testing.T) {
	doc := agentSampleDoc()
	doc.Spec.Prompt = ""
	doc.Spec.PromptFile = ""
	err := doc.Validate(agentCtxWithCrew("engineering"))
	if err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("want prompt-required error, got %v", err)
	}
}

func TestAgent_Validate_PromptFileOnlyAccepted(t *testing.T) {
	// Direct prompt_file usage (no inline Prompt) should validate
	// even before parse-time resolution has run.
	doc := agentSampleDoc()
	doc.Spec.Prompt = ""
	doc.Spec.PromptFile = "./prompts/tomas.md"
	if err := doc.Validate(agentCtxWithCrew("engineering")); err != nil {
		t.Fatalf("Validate with prompt_file only: %v", err)
	}
}

// ── 5. Validate: FK against workspace context ───────────────────────────────

func TestAgent_Validate_CrewSlugFKMissesContext(t *testing.T) {
	doc := agentSampleDoc()
	doc.Spec.CrewSlug = "ghost-crew"
	ctx := agentCtxWithCrew("engineering") // doesn't contain ghost-crew
	err := doc.Validate(ctx)
	if err == nil || !strings.Contains(err.Error(), "does not reference any declared or remote crew") {
		t.Fatalf("want FK error, got %v", err)
	}
}

func TestAgent_Validate_EmptySkillEntry(t *testing.T) {
	doc := agentSampleDoc()
	doc.Spec.Skills = []string{"foo", "   "}
	err := doc.Validate(agentCtxWithCrew("engineering"))
	if err == nil || !strings.Contains(err.Error(), "skills[1] is empty") {
		t.Fatalf("want empty-skill error, got %v", err)
	}
}

func TestAgent_Validate_EmptyEnvRefEntry(t *testing.T) {
	doc := agentSampleDoc()
	doc.Spec.EnvRefs = []string{""}
	err := doc.Validate(agentCtxWithCrew("engineering"))
	if err == nil || !strings.Contains(err.Error(), "env_refs[0] is empty") {
		t.Fatalf("want empty-env_ref error, got %v", err)
	}
}

func TestAgent_Validate_WrongAPIVersion(t *testing.T) {
	doc := agentSampleDoc()
	doc.APIVersion = "crewship/v2"
	err := doc.Validate(agentCtxWithCrew("engineering"))
	if err == nil || !strings.Contains(err.Error(), "apiVersion") {
		t.Fatalf("want apiVersion error, got %v", err)
	}
}

func TestAgent_Validate_WrongKind(t *testing.T) {
	doc := agentSampleDoc()
	doc.Kind = "Crew"
	err := doc.Validate(agentCtxWithCrew("engineering"))
	if err == nil || !strings.Contains(err.Error(), "kind") {
		t.Fatalf("want kind error, got %v", err)
	}
}

// ── 6. Plan: Create (no remote) ─────────────────────────────────────────────

func TestAgent_Plan_CreateNoRemote(t *testing.T) {
	doc := agentSampleDoc()
	doc.Spec.Skills = []string{"network-probe"}
	doc.Spec.EnvRefs = []string{"ANTHROPIC_API_KEY"}

	client := newAgentFake()
	client.crews["engineering"] = agentCrewStub{ID: "crew_eng", Slug: "engineering", Name: "Engineering"}
	client.skills["network-probe"] = agentSkillStub{ID: "skill_np", Slug: "network-probe", Name: "Network Probe"}
	client.credentials["ANTHROPIC_API_KEY"] = agentCredentialStub{ID: "cred_a", Name: "ANTHROPIC_API_KEY"}

	items, err := doc.Plan(context.Background(), client, nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("want 1 item, got %d", len(items))
	}
	if items[0].Action != internalapi.ActionCreate {
		t.Errorf("want ActionCreate, got %s", items[0].Action)
	}
	if items[0].Kind != "agent" {
		t.Errorf("want kind 'agent', got %q", items[0].Kind)
	}
	if items[0].Exec == nil {
		t.Fatal("Create item must have non-nil Exec")
	}

	// Execute and confirm the create POST body carries the resolved
	// crew_id (NOT crew_slug) and that the binding endpoints fire
	// exactly once each.
	if err := items[0].Exec(context.Background(), client); err != nil {
		t.Fatalf("Exec: %v", err)
	}

	createCall := client.findCall("POST", "/api/v1/agents")
	if createCall == nil {
		t.Fatal("expected POST /api/v1/agents to be recorded")
	}
	body, ok := createCall.Body.(map[string]any)
	if !ok {
		t.Fatalf("create body is not a map: %T", createCall.Body)
	}
	if got, _ := body["crew_id"].(string); got != "crew_eng" {
		t.Errorf("crew_id = %q, want crew_eng", got)
	}
	if _, has := body["crew_slug"]; has {
		t.Errorf("body should not carry crew_slug (server only accepts crew_id), got %v", body["crew_slug"])
	}
	if got, _ := body["name"].(string); got != "Tomas" {
		t.Errorf("name = %q, want Tomas", got)
	}
	if got, _ := body["slug"].(string); got != "tomas" {
		t.Errorf("slug = %q, want tomas", got)
	}
	if got, _ := body["agent_role"].(string); got != "LEAD" {
		t.Errorf("agent_role = %q, want LEAD", got)
	}
	if got, _ := body["cli_adapter"].(string); got != "CLAUDE_CODE" {
		t.Errorf("cli_adapter = %q, want CLAUDE_CODE", got)
	}
	if got, _ := body["llm_provider"].(string); got != "ANTHROPIC" {
		t.Errorf("llm_provider = %q, want ANTHROPIC", got)
	}
	if got, _ := body["llm_model"].(string); got != "claude-haiku-4-5" {
		t.Errorf("llm_model = %q, want claude-haiku-4-5", got)
	}
	if got, _ := body["system_prompt"].(string); got == "" {
		t.Error("system_prompt should be set from Spec.Prompt")
	}
	// The fake records the body map verbatim (no JSON round-trip), so
	// timeout_seconds stays a Go int. Coerce both shapes so the test
	// stays robust if a future client.Patch shim adds JSON marshalling.
	if !agentTestEqualsInt(body["timeout_seconds"], 3600) {
		t.Errorf("timeout_seconds = %v, want 3600", body["timeout_seconds"])
	}
	if got, _ := body["memory_enabled"].(bool); !got {
		t.Errorf("memory_enabled = %v, want true", got)
	}

	if n := client.countCalls("POST", "/api/v1/agents/agt_new/skills"); n != 1 {
		t.Errorf("want 1 skill bind, got %d", n)
	}
	if n := client.countCalls("POST", "/api/v1/agents/agt_new/credentials"); n != 1 {
		t.Errorf("want 1 credential bind, got %d", n)
	}
}

func TestAgent_Plan_CreateOmitsLLMProviderWhenNone(t *testing.T) {
	// llm.provider=NONE means "no provider"; the manifest should NOT
	// send llm_provider in the body. The model can still be sent (or
	// not — here we omit it).
	doc := agentSampleDoc()
	doc.Spec.LLM = LLMSpec{Provider: "NONE"}

	client := newAgentFake()
	client.crews["engineering"] = agentCrewStub{ID: "crew_eng", Slug: "engineering"}

	items, err := doc.Plan(context.Background(), client, nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if err := items[0].Exec(context.Background(), client); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	body, _ := client.findCall("POST", "/api/v1/agents").Body.(map[string]any)
	if _, has := body["llm_provider"]; has {
		t.Errorf("llm_provider=NONE should be omitted from POST body, got %v", body["llm_provider"])
	}
}

func TestAgent_Plan_CreateDefaultsTimeoutWhenZero(t *testing.T) {
	doc := agentSampleDoc()
	doc.Spec.TimeoutSeconds = 0

	client := newAgentFake()
	client.crews["engineering"] = agentCrewStub{ID: "crew_eng", Slug: "engineering"}

	items, err := doc.Plan(context.Background(), client, nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if err := items[0].Exec(context.Background(), client); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	body, _ := client.findCall("POST", "/api/v1/agents").Body.(map[string]any)
	if !agentTestEqualsInt(body["timeout_seconds"], defaultAgentTimeoutSeconds) {
		t.Errorf("timeout_seconds = %v, want default %d", body["timeout_seconds"], defaultAgentTimeoutSeconds)
	}
}

// agentTestEqualsInt compares a body field to an int value, tolerating
// both Go-int (when the fake records the map verbatim) and float64
// (when a future shim adds a JSON round-trip).
func agentTestEqualsInt(v any, want int) bool {
	switch x := v.(type) {
	case int:
		return x == want
	case int64:
		return int(x) == want
	case float64:
		return int(x) == want
	}
	return false
}

// ── 7. Plan: Update (remote exists, drift detected) ─────────────────────────

func TestAgent_Plan_UpdateDriftedField(t *testing.T) {
	doc := agentSampleDoc()
	doc.Spec.TimeoutSeconds = 7200
	doc.Spec.Skills = nil // no new bindings to drive Update purely on field diff
	doc.Spec.EnvRefs = nil

	client := newAgentFake()
	client.crews["engineering"] = agentCrewStub{ID: "crew_eng", Slug: "engineering"}
	crewID := "crew_eng"
	prov := "ANTHROPIC"
	model := "claude-haiku-4-5"
	roleTitle := "Technical Architect"
	prompt := "You are Tomas, the Technical Architect."
	client.agents["tomas"] = AgentRemote{
		ID:             "agt_tomas",
		Slug:           "tomas",
		Name:           "Tomas",
		RoleTitle:      &roleTitle,
		AgentRole:      "LEAD",
		CLIAdapter:     "CLAUDE_CODE",
		LLMProvider:    &prov,
		LLMModel:       &model,
		SystemPrompt:   &prompt,
		ToolProfile:    "FULL",
		TimeoutSeconds: 3600, // ← drift: manifest wants 7200
		MemoryEnabled:  true,
		CrewID:         &crewID,
	}

	remote, err := LookupAgentRemoteBySlug(context.Background(), client, "tomas")
	if err != nil {
		t.Fatalf("LookupAgentRemoteBySlug: %v", err)
	}
	if remote == nil {
		t.Fatal("expected remote to be non-nil")
	}

	items, err := doc.Plan(context.Background(), client, remote)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) != 1 || items[0].Action != internalapi.ActionUpdate {
		t.Fatalf("want single Update, got %+v", items)
	}
	if items[0].Exec == nil {
		t.Fatal("Update item must have non-nil Exec")
	}

	if err := items[0].Exec(context.Background(), client); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	patchCall := client.findCall("PATCH", "/api/v1/agents/agt_tomas")
	if patchCall == nil {
		t.Fatal("expected PATCH /api/v1/agents/agt_tomas")
	}
	body, _ := patchCall.Body.(map[string]any)
	if !agentTestEqualsInt(body["timeout_seconds"], 7200) {
		t.Errorf("patch.timeout_seconds = %v, want 7200", body["timeout_seconds"])
	}
	// Drift was only on timeout_seconds; the patch should be narrowly
	// scoped — name/role/llm/etc. all match the remote so they must
	// not appear in the PATCH body.
	if _, has := body["llm_provider"]; has {
		t.Errorf("did not expect llm_provider in patch (no remote drift), got %v", body["llm_provider"])
	}
	if _, has := body["name"]; has {
		t.Errorf("did not expect name in patch, got %v", body["name"])
	}
}

// ── 8. Plan: Unchanged ──────────────────────────────────────────────────────

func TestAgent_Plan_Unchanged(t *testing.T) {
	doc := agentSampleDoc()
	doc.Spec.Skills = nil
	doc.Spec.EnvRefs = nil
	// Match remote exactly on every field the diff inspects.
	prov := "ANTHROPIC"
	model := "claude-haiku-4-5"
	roleTitle := "Technical Architect"
	prompt := "You are Tomas, the Technical Architect."

	client := newAgentFake()
	client.crews["engineering"] = agentCrewStub{ID: "crew_eng", Slug: "engineering"}
	crewID := "crew_eng"
	client.agents["tomas"] = AgentRemote{
		ID:             "agt_tomas",
		Slug:           "tomas",
		Name:           "Tomas",
		RoleTitle:      &roleTitle,
		AgentRole:      "LEAD",
		CLIAdapter:     "CLAUDE_CODE",
		LLMProvider:    &prov,
		LLMModel:       &model,
		SystemPrompt:   &prompt,
		ToolProfile:    "FULL",
		TimeoutSeconds: 3600,
		MemoryEnabled:  true,
		CrewID:         &crewID,
	}

	remote, err := LookupAgentRemoteBySlug(context.Background(), client, "tomas")
	if err != nil {
		t.Fatalf("LookupAgentRemoteBySlug: %v", err)
	}
	items, err := doc.Plan(context.Background(), client, remote)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) != 1 || items[0].Action != internalapi.ActionUnchanged {
		t.Fatalf("want single Unchanged, got %+v", items)
	}
	if items[0].Exec != nil {
		t.Error("Unchanged items must have nil Exec")
	}
}

// ── 9. Plan: crew_slug resolution failure ───────────────────────────────────

func TestAgent_Plan_CrewSlugUnknown(t *testing.T) {
	doc := agentSampleDoc()

	client := newAgentFake()
	// No crews seeded — resolution should fail with a clear error.
	_, err := doc.Plan(context.Background(), client, nil)
	if err == nil || !strings.Contains(err.Error(), "resolve crew_slug") {
		t.Fatalf("want resolve-crew error, got %v", err)
	}
}

// ── 10. LookupCrewIDBySlug ─────────────────────────────────────────────────

func TestLookupCrewIDBySlug_Found(t *testing.T) {
	client := newAgentFake()
	client.crews["engineering"] = agentCrewStub{ID: "crew_eng", Slug: "engineering"}
	id, err := LookupCrewIDBySlug(context.Background(), client, "engineering")
	if err != nil {
		t.Fatalf("LookupCrewIDBySlug: %v", err)
	}
	if id != "crew_eng" {
		t.Errorf("id = %q, want crew_eng", id)
	}
}

func TestLookupCrewIDBySlug_NotFound(t *testing.T) {
	client := newAgentFake()
	_, err := LookupCrewIDBySlug(context.Background(), client, "ghost")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("want not-found error, got %v", err)
	}
}

// ── 11. LookupAgentRemoteBySlug ────────────────────────────────────────────

func TestLookupAgentRemoteBySlug_NilWhenAbsent(t *testing.T) {
	client := newAgentFake()
	got, err := LookupAgentRemoteBySlug(context.Background(), client, "tomas")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got != nil {
		t.Errorf("want nil remote when slug absent, got %+v", got)
	}
}

// ── 12. Binding failures ────────────────────────────────────────────────────

func TestAgent_BindSkills_UnknownSkillFails(t *testing.T) {
	doc := agentSampleDoc()
	doc.Spec.Skills = []string{"ghost-skill"}

	client := newAgentFake()
	client.crews["engineering"] = agentCrewStub{ID: "crew_eng", Slug: "engineering"}
	// No skills seeded — binding step should fail with the slug name.

	items, err := doc.Plan(context.Background(), client, nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	err = items[0].Exec(context.Background(), client)
	if err == nil || !strings.Contains(err.Error(), "ghost-skill") {
		t.Fatalf("want unknown-skill error mentioning the slug, got %v", err)
	}
}

func TestAgent_BindEnvRefs_UnknownCredentialFails(t *testing.T) {
	doc := agentSampleDoc()
	doc.Spec.EnvRefs = []string{"MISSING_KEY"}

	client := newAgentFake()
	client.crews["engineering"] = agentCrewStub{ID: "crew_eng", Slug: "engineering"}
	// No credentials seeded.

	items, err := doc.Plan(context.Background(), client, nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	err = items[0].Exec(context.Background(), client)
	if err == nil || !strings.Contains(err.Error(), "MISSING_KEY") {
		t.Fatalf("want unknown-cred error mentioning the env name, got %v", err)
	}
}

func TestAgent_BindSkills_409Tolerated(t *testing.T) {
	doc := agentSampleDoc()
	doc.Spec.Skills = []string{"network-probe"}
	doc.Spec.EnvRefs = nil

	client := newAgentFake()
	client.crews["engineering"] = agentCrewStub{ID: "crew_eng", Slug: "engineering"}
	client.skills["network-probe"] = agentSkillStub{ID: "skill_np", Slug: "network-probe"}
	client.postSkillBindStatus = 409 // simulate "already bound" from a parallel write

	items, err := doc.Plan(context.Background(), client, nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if err := items[0].Exec(context.Background(), client); err != nil {
		t.Fatalf("Exec should tolerate 409, got %v", err)
	}
}

// ── 13. ExportAgents ────────────────────────────────────────────────────────

func TestExportAgents_RoundTripsCrewSlug(t *testing.T) {
	client := newAgentFake()
	client.crews["engineering"] = agentCrewStub{ID: "crew_eng", Slug: "engineering"}
	crewID := "crew_eng"
	client.agents["tomas"] = AgentRemote{
		ID: "agt_tomas", Slug: "tomas", Name: "Tomas",
		AgentRole: "LEAD", CLIAdapter: "CLAUDE_CODE", ToolProfile: "FULL",
		TimeoutSeconds: 3600, MemoryEnabled: true, CrewID: &crewID,
	}
	client.agentSkillBindings["agt_tomas"] = []string{"network-probe"}
	client.agentCredBindings["agt_tomas"] = []string{"ANTHROPIC_API_KEY"}

	docs, err := ExportAgents(context.Background(), client)
	if err != nil {
		t.Fatalf("ExportAgents: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("want 1 doc, got %d", len(docs))
	}
	d := docs[0]
	if d.Kind != agentKind || d.APIVersion != agentAPIVersion {
		t.Errorf("envelope drift: kind=%q apiVersion=%q", d.Kind, d.APIVersion)
	}
	if d.Spec.CrewSlug != "engineering" {
		t.Errorf("crew_slug = %q, want engineering", d.Spec.CrewSlug)
	}
	if d.Metadata.Slug != "tomas" {
		t.Errorf("slug = %q, want tomas", d.Metadata.Slug)
	}
	if len(d.Spec.Skills) != 1 || d.Spec.Skills[0] != "network-probe" {
		t.Errorf("skills = %v, want [network-probe]", d.Spec.Skills)
	}
	if len(d.Spec.EnvRefs) != 1 || d.Spec.EnvRefs[0] != "ANTHROPIC_API_KEY" {
		t.Errorf("env_refs = %v, want [ANTHROPIC_API_KEY]", d.Spec.EnvRefs)
	}
	if d.Spec.MemoryEnabled == nil || !*d.Spec.MemoryEnabled {
		t.Errorf("memory_enabled = %v, want explicit true", d.Spec.MemoryEnabled)
	}
}

func TestExportAgents_OrphanCrewIDOmittedFromSpec(t *testing.T) {
	// An agent whose crew_id doesn't resolve to any known crew (e.g.
	// the crew was hard-deleted but the FK didn't cascade) should
	// export with an empty crew_slug. Re-applying that doc will then
	// fail Validate, which is the right behaviour — the operator has
	// to declare which crew to land the agent in.
	client := newAgentFake()
	crewID := "crew_orphan"
	client.agents["tomas"] = AgentRemote{
		ID: "agt_tomas", Slug: "tomas", Name: "Tomas",
		AgentRole: "LEAD", CLIAdapter: "CLAUDE_CODE", ToolProfile: "FULL",
		CrewID: &crewID,
	}
	docs, err := ExportAgents(context.Background(), client)
	if err != nil {
		t.Fatalf("ExportAgents: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("want 1 doc, got %d", len(docs))
	}
	if docs[0].Spec.CrewSlug != "" {
		t.Errorf("orphan crew_id should produce empty crew_slug, got %q", docs[0].Spec.CrewSlug)
	}
}

// ── 14. Plan: server error surfaces via Exec ────────────────────────────────

func TestAgent_Plan_CreateServerErrorSurfacedFromExec(t *testing.T) {
	doc := agentSampleDoc()

	client := newAgentFake()
	client.crews["engineering"] = agentCrewStub{ID: "crew_eng", Slug: "engineering"}
	client.postAgentsStatus = 500

	items, err := doc.Plan(context.Background(), client, nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if err := items[0].Exec(context.Background(), client); err == nil ||
		!strings.Contains(err.Error(), "500") {
		t.Fatalf("want 500 surfaced from Exec, got %v", err)
	}
}
