package kinds

// Tests for kind: Integration (integration.go).
//
// The fake client below is integration-prefixed to avoid collisions
// with the per-kind fakes already in this directory (agentFakeClient,
// labelFakeClient, …). Each test wires only the endpoints the
// scenario exercises so a future server-side change in an unrelated
// route doesn't ripple into Integration test failures.

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

type integrationFakeCall struct {
	Method string
	Path   string
	Body   any
}

// integrationFakeClient implements internalapi.Client with in-memory
// state. Tests seed crews + per-scope integration rows; the client
// records every call so assertions can verify both the request path
// (workspace vs crew endpoint) and the body shape (args_json /
// env_json serialisation).
type integrationFakeClient struct {
	wsID string

	// Fixtures the test seeds.
	crews            map[string]integrationCrewStub // keyed by slug
	workspaceServers map[string]IntegrationRemote   // keyed by name (slug)
	crewServers      map[string][]IntegrationRemote // keyed by crewID
	createResponseID string                         // returned by every POST

	// Per-route status overrides — set to non-zero to force a specific
	// code on the next matching call.
	listWorkspaceStatus   int
	listCrewIntStatus     int
	listCrewsStatus       int
	createWorkspaceStatus int
	createCrewStatus      int
	patchStatus           int
	deleteStatus          int

	calls []integrationFakeCall
}

func newIntegrationFake() *integrationFakeClient {
	return &integrationFakeClient{
		wsID:             "ws_test",
		crews:            map[string]integrationCrewStub{},
		workspaceServers: map[string]IntegrationRemote{},
		crewServers:      map[string][]IntegrationRemote{},
		createResponseID: "int_new",
	}
}

func (f *integrationFakeClient) WorkspaceID() string { return f.wsID }

func (f *integrationFakeClient) record(method, path string, body any) {
	f.calls = append(f.calls, integrationFakeCall{Method: method, Path: path, Body: body})
}

func integrationJSONResp(status int, v any) *internalapi.Response {
	data, _ := json.Marshal(v)
	return &internalapi.Response{
		StatusCode: status,
		Body:       io.NopCloser(bytes.NewReader(data)),
	}
}

func (f *integrationFakeClient) Get(_ context.Context, path string) (*internalapi.Response, error) {
	f.record("GET", path, nil)
	switch {
	case path == "/api/v1/integrations":
		if f.listWorkspaceStatus != 0 {
			return integrationJSONResp(f.listWorkspaceStatus, map[string]any{"error": "forced"}), nil
		}
		out := make([]IntegrationRemote, 0, len(f.workspaceServers))
		for _, r := range f.workspaceServers {
			out = append(out, r)
		}
		return integrationJSONResp(200, out), nil
	case path == "/api/v1/crews":
		if f.listCrewsStatus != 0 {
			return integrationJSONResp(f.listCrewsStatus, map[string]any{"error": "forced"}), nil
		}
		out := make([]integrationCrewStub, 0, len(f.crews))
		for _, c := range f.crews {
			out = append(out, c)
		}
		return integrationJSONResp(200, out), nil
	case strings.HasPrefix(path, "/api/v1/crews/") && strings.HasSuffix(path, "/integrations"):
		if f.listCrewIntStatus != 0 {
			return integrationJSONResp(f.listCrewIntStatus, map[string]any{"error": "forced"}), nil
		}
		crewID := strings.TrimSuffix(strings.TrimPrefix(path, "/api/v1/crews/"), "/integrations")
		out := append([]IntegrationRemote(nil), f.crewServers[crewID]...)
		return integrationJSONResp(200, out), nil
	}
	return integrationJSONResp(404, map[string]any{"error": "not stubbed: " + path}), nil
}

func (f *integrationFakeClient) Post(_ context.Context, path string, body any) (*internalapi.Response, error) {
	f.record("POST", path, body)
	switch {
	case path == "/api/v1/integrations":
		status := f.createWorkspaceStatus
		if status == 0 {
			status = 201
		}
		if status < 200 || status >= 300 {
			return integrationJSONResp(status, map[string]any{"error": "forced"}), nil
		}
		return integrationJSONResp(status, map[string]any{"id": f.createResponseID}), nil
	case strings.HasPrefix(path, "/api/v1/crews/") && strings.HasSuffix(path, "/integrations"):
		status := f.createCrewStatus
		if status == 0 {
			status = 201
		}
		if status < 200 || status >= 300 {
			return integrationJSONResp(status, map[string]any{"error": "forced"}), nil
		}
		return integrationJSONResp(status, map[string]any{"id": f.createResponseID}), nil
	}
	return integrationJSONResp(404, map[string]any{"error": "not stubbed: " + path}), nil
}

func (f *integrationFakeClient) Patch(_ context.Context, path string, body any) (*internalapi.Response, error) {
	f.record("PATCH", path, body)
	if strings.HasPrefix(path, "/api/v1/integrations/") ||
		(strings.HasPrefix(path, "/api/v1/crews/") && strings.Contains(path, "/integrations/")) {
		status := f.patchStatus
		if status == 0 {
			status = 200
		}
		return integrationJSONResp(status, map[string]any{"ok": true}), nil
	}
	return integrationJSONResp(404, map[string]any{"error": "not stubbed: " + path}), nil
}

func (f *integrationFakeClient) Put(_ context.Context, path string, body any) (*internalapi.Response, error) {
	f.record("PUT", path, body)
	return integrationJSONResp(404, map[string]any{"error": "not stubbed: " + path}), nil
}

func (f *integrationFakeClient) Delete(_ context.Context, path string) (*internalapi.Response, error) {
	f.record("DELETE", path, nil)
	if strings.HasPrefix(path, "/api/v1/integrations/") ||
		(strings.HasPrefix(path, "/api/v1/crews/") && strings.Contains(path, "/integrations/")) {
		status := f.deleteStatus
		if status == 0 {
			status = 200
		}
		return integrationJSONResp(status, map[string]any{"status": "deleted"}), nil
	}
	return integrationJSONResp(404, map[string]any{"error": "not stubbed: " + path}), nil
}

// Compile-time interface assertion.
var _ internalapi.Client = (*integrationFakeClient)(nil)

func (f *integrationFakeClient) findCall(method, path string) *integrationFakeCall {
	for i := range f.calls {
		if f.calls[i].Method == method && f.calls[i].Path == path {
			return &f.calls[i]
		}
	}
	return nil
}

// ── Sample documents ────────────────────────────────────────────────────────

// integrationSampleWorkspaceDoc returns a happy-path workspace-scoped
// streamable-http document — the most common shape (Linear, Sentry,
// hosted MCP endpoints).
func integrationSampleWorkspaceDoc() *IntegrationDocument {
	return &IntegrationDocument{
		APIVersion: integrationAPIVersion,
		Kind:       integrationKind,
		Metadata: internalapi.Metadata{
			Name: "linear",
			Slug: "linear",
		},
		Spec: IntegrationSpec{
			Scope:       integrationScopeWorkspace,
			DisplayName: "Linear",
			Transport:   integrationTransportHTTP,
			Endpoint:    "https://mcp.linear.app/streamable-http",
			Icon:        "linear",
			EnvMapping: map[string]string{
				"LINEAR_API_KEY": "LINEAR_API_KEY",
			},
		},
	}
}

// integrationSampleCrewDoc returns a happy-path crew-scoped stdio
// document — the second-most-common shape (locally launched MCP
// processes that need credentials injected via env).
func integrationSampleCrewDoc() *IntegrationDocument {
	return &IntegrationDocument{
		APIVersion: integrationAPIVersion,
		Kind:       integrationKind,
		Metadata: internalapi.Metadata{
			Name: "google-workspace",
			Slug: "google-workspace",
		},
		Spec: IntegrationSpec{
			Scope:       integrationScopeCrew,
			CrewSlug:    "engineering",
			DisplayName: "Google Workspace",
			Transport:   integrationTransportStdio,
			Command:     "npx",
			Args:        []string{"-y", "@anthropic-ai/google-workspace-mcp"},
			Env: map[string]string{
				"NODE_ENV": "production",
			},
			EnvMapping: map[string]string{
				"GOOGLE_ACCESS_TOKEN": "GOOGLE_ACCESS_TOKEN",
			},
			Icon: "google",
		},
	}
}

func integrationCtxWithCrew(slug string) internalapi.WorkspaceContext {
	return internalapi.WorkspaceContext{
		DeclaredCrews: []internalapi.SlugLookup{
			{Slug: slug, Name: slug},
		},
	}
}

// ── 1. Validate: happy paths ────────────────────────────────────────────────

func TestIntegration_Validate_HappyWorkspace(t *testing.T) {
	doc := integrationSampleWorkspaceDoc()
	if err := doc.Validate(internalapi.WorkspaceContext{}); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestIntegration_Validate_HappyCrew(t *testing.T) {
	doc := integrationSampleCrewDoc()
	if err := doc.Validate(integrationCtxWithCrew("engineering")); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestIntegration_Validate_HappyEmptyScopeDefaultsToWorkspace(t *testing.T) {
	// Empty scope is treated as workspace — the simplest possible
	// integration document should validate without forcing the user
	// to spell out scope: workspace.
	doc := integrationSampleWorkspaceDoc()
	doc.Spec.Scope = ""
	if err := doc.Validate(internalapi.WorkspaceContext{}); err != nil {
		t.Fatalf("Validate empty scope: %v", err)
	}
}

// ── 2. Validate: required-field errors ──────────────────────────────────────

func TestIntegration_Validate_MissingName(t *testing.T) {
	doc := integrationSampleWorkspaceDoc()
	doc.Metadata.Name = ""
	err := doc.Validate(internalapi.WorkspaceContext{})
	if err == nil || !strings.Contains(err.Error(), "metadata.name is required") {
		t.Fatalf("want name-required error, got %v", err)
	}
}

func TestIntegration_Validate_MissingSlug(t *testing.T) {
	doc := integrationSampleWorkspaceDoc()
	doc.Metadata.Slug = ""
	err := doc.Validate(internalapi.WorkspaceContext{})
	if err == nil || !strings.Contains(err.Error(), "metadata.slug is required") {
		t.Fatalf("want slug-required error, got %v", err)
	}
}

func TestIntegration_Validate_SlugMustEqualName(t *testing.T) {
	doc := integrationSampleWorkspaceDoc()
	doc.Metadata.Slug = "linear-app"
	doc.Metadata.Name = "linear"
	err := doc.Validate(internalapi.WorkspaceContext{})
	if err == nil || !strings.Contains(err.Error(), "metadata.slug must equal metadata.name") {
		t.Fatalf("want slug==name error, got %v", err)
	}
}

func TestIntegration_Validate_MissingTransport(t *testing.T) {
	doc := integrationSampleWorkspaceDoc()
	doc.Spec.Transport = ""
	err := doc.Validate(internalapi.WorkspaceContext{})
	if err == nil || !strings.Contains(err.Error(), "spec.transport is required") {
		t.Fatalf("want transport-required error, got %v", err)
	}
}

func TestIntegration_Validate_BadTransport(t *testing.T) {
	doc := integrationSampleWorkspaceDoc()
	doc.Spec.Transport = "websocket"
	err := doc.Validate(internalapi.WorkspaceContext{})
	if err == nil || !strings.Contains(err.Error(), `spec.transport "websocket"`) {
		t.Fatalf("want bad-transport error, got %v", err)
	}
}

func TestIntegration_Validate_HTTPMissingEndpoint(t *testing.T) {
	doc := integrationSampleWorkspaceDoc()
	doc.Spec.Endpoint = ""
	err := doc.Validate(internalapi.WorkspaceContext{})
	if err == nil || !strings.Contains(err.Error(), "spec.endpoint is required") {
		t.Fatalf("want endpoint-required error, got %v", err)
	}
}

func TestIntegration_Validate_StdioMissingCommand(t *testing.T) {
	doc := integrationSampleCrewDoc()
	doc.Spec.Command = ""
	err := doc.Validate(integrationCtxWithCrew("engineering"))
	if err == nil || !strings.Contains(err.Error(), "spec.command is required") {
		t.Fatalf("want command-required error, got %v", err)
	}
}

// ── 3. Validate: scope rules ────────────────────────────────────────────────

func TestIntegration_Validate_BadScope(t *testing.T) {
	doc := integrationSampleWorkspaceDoc()
	doc.Spec.Scope = "global"
	err := doc.Validate(internalapi.WorkspaceContext{})
	if err == nil || !strings.Contains(err.Error(), `spec.scope "global"`) {
		t.Fatalf("want bad-scope error, got %v", err)
	}
}

func TestIntegration_Validate_CrewScopeWithoutCrewSlug(t *testing.T) {
	doc := integrationSampleCrewDoc()
	doc.Spec.CrewSlug = ""
	err := doc.Validate(internalapi.WorkspaceContext{})
	if err == nil || !strings.Contains(err.Error(), "spec.crew_slug is required") {
		t.Fatalf("want crew_slug-required error, got %v", err)
	}
}

func TestIntegration_Validate_WorkspaceScopeWithCrewSlugRejected(t *testing.T) {
	// crew_slug set under workspace scope is almost certainly a
	// typo — the user meant scope: crew. Surface loudly.
	doc := integrationSampleWorkspaceDoc()
	doc.Spec.CrewSlug = "engineering"
	err := doc.Validate(integrationCtxWithCrew("engineering"))
	if err == nil || !strings.Contains(err.Error(), "spec.crew_slug must be empty") {
		t.Fatalf("want crew_slug-empty error, got %v", err)
	}
}

func TestIntegration_Validate_CrewSlugFKMissesContext(t *testing.T) {
	doc := integrationSampleCrewDoc()
	doc.Spec.CrewSlug = "ghost-crew"
	ctx := integrationCtxWithCrew("engineering") // doesn't contain ghost-crew
	err := doc.Validate(ctx)
	if err == nil || !strings.Contains(err.Error(), "does not reference any declared or remote crew") {
		t.Fatalf("want FK error, got %v", err)
	}
}

func TestIntegration_Validate_CrewSlugFKDegradesOnEmptyCtx(t *testing.T) {
	// Empty workspace context should NOT trigger the FK check; we
	// don't have any data to validate against. Plan will surface
	// the unresolved crew_slug at runtime.
	doc := integrationSampleCrewDoc()
	doc.Spec.CrewSlug = "ghost-crew"
	if err := doc.Validate(internalapi.WorkspaceContext{}); err != nil {
		t.Fatalf("Validate with empty ctx: %v", err)
	}
}

// ── 4. Validate: env / args shape ───────────────────────────────────────────

func TestIntegration_Validate_EmptyEnvKey(t *testing.T) {
	doc := integrationSampleCrewDoc()
	doc.Spec.Env = map[string]string{"  ": "value"}
	err := doc.Validate(integrationCtxWithCrew("engineering"))
	if err == nil || !strings.Contains(err.Error(), "spec.env has an empty key") {
		t.Fatalf("want empty-env-key error, got %v", err)
	}
}

func TestIntegration_Validate_EmptyEnvMappingValue(t *testing.T) {
	doc := integrationSampleCrewDoc()
	doc.Spec.EnvMapping = map[string]string{"FOO": ""}
	err := doc.Validate(integrationCtxWithCrew("engineering"))
	if err == nil || !strings.Contains(err.Error(), `spec.env_mapping["FOO"] is empty`) {
		t.Fatalf("want empty-env-mapping-value error, got %v", err)
	}
}

func TestIntegration_Validate_EmptyArgsEntry(t *testing.T) {
	doc := integrationSampleCrewDoc()
	doc.Spec.Args = []string{"-y", ""}
	err := doc.Validate(integrationCtxWithCrew("engineering"))
	if err == nil || !strings.Contains(err.Error(), "spec.args[1] is empty") {
		t.Fatalf("want empty-args error, got %v", err)
	}
}

// ── 5. Validate: envelope errors ────────────────────────────────────────────

func TestIntegration_Validate_WrongAPIVersion(t *testing.T) {
	doc := integrationSampleWorkspaceDoc()
	doc.APIVersion = "crewship/v2"
	err := doc.Validate(internalapi.WorkspaceContext{})
	if err == nil || !strings.Contains(err.Error(), "apiVersion") {
		t.Fatalf("want apiVersion error, got %v", err)
	}
}

func TestIntegration_Validate_WrongKind(t *testing.T) {
	doc := integrationSampleWorkspaceDoc()
	doc.Kind = "Crew"
	err := doc.Validate(internalapi.WorkspaceContext{})
	if err == nil || !strings.Contains(err.Error(), "kind") {
		t.Fatalf("want kind error, got %v", err)
	}
}

// ── 6. Plan: Create workspace ───────────────────────────────────────────────

func TestIntegration_Plan_CreateWorkspaceNoRemote(t *testing.T) {
	doc := integrationSampleWorkspaceDoc()
	client := newIntegrationFake()

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
	if items[0].Kind != "integration" {
		t.Errorf("want kind 'integration', got %q", items[0].Kind)
	}
	if items[0].Exec == nil {
		t.Fatal("Create item must have non-nil Exec")
	}

	// Execute and verify the workspace POST endpoint was hit.
	if err := items[0].Exec(context.Background(), client); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	createCall := client.findCall("POST", "/api/v1/integrations")
	if createCall == nil {
		t.Fatal("expected POST /api/v1/integrations to be recorded")
	}
	body, ok := createCall.Body.(map[string]any)
	if !ok {
		t.Fatalf("create body is not a map: %T", createCall.Body)
	}
	if got, _ := body["name"].(string); got != "linear" {
		t.Errorf("name = %q, want linear", got)
	}
	if got, _ := body["transport"].(string); got != "streamable-http" {
		t.Errorf("transport = %q, want streamable-http", got)
	}
	if got, _ := body["endpoint"].(string); got != "https://mcp.linear.app/streamable-http" {
		t.Errorf("endpoint = %q, want linear streamable url", got)
	}
	if got, _ := body["display_name"].(string); got != "Linear" {
		t.Errorf("display_name = %q, want Linear", got)
	}
	// env_mapping should survive the body (extra-key tolerance on the
	// handler) AND env_json should be present with the merged map.
	envJSON, ok := body["env_json"].(string)
	if !ok || envJSON == "" {
		t.Errorf("env_json missing or wrong type: %T %v", body["env_json"], body["env_json"])
	} else {
		var got map[string]string
		if err := json.Unmarshal([]byte(envJSON), &got); err != nil {
			t.Fatalf("env_json not parsable: %v", err)
		}
		if got["LINEAR_API_KEY"] != "LINEAR_API_KEY" {
			t.Errorf("env_json[LINEAR_API_KEY] = %q, want LINEAR_API_KEY", got["LINEAR_API_KEY"])
		}
	}
}

// ── 7. Plan: Create crew ────────────────────────────────────────────────────

func TestIntegration_Plan_CreateCrewNoRemote(t *testing.T) {
	doc := integrationSampleCrewDoc()
	client := newIntegrationFake()
	client.crews["engineering"] = integrationCrewStub{ID: "crew_eng", Slug: "engineering", Name: "Engineering"}

	items, err := doc.Plan(context.Background(), client, nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) != 1 || items[0].Action != internalapi.ActionCreate {
		t.Fatalf("want single Create, got %+v", items)
	}
	if err := items[0].Exec(context.Background(), client); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	createCall := client.findCall("POST", "/api/v1/crews/crew_eng/integrations")
	if createCall == nil {
		t.Fatal("expected POST /api/v1/crews/crew_eng/integrations")
	}
	body, _ := createCall.Body.(map[string]any)
	if got, _ := body["command"].(string); got != "npx" {
		t.Errorf("command = %q, want npx", got)
	}
	// args_json should carry the JSON-encoded slice.
	argsJSON, ok := body["args_json"].(string)
	if !ok {
		t.Fatalf("args_json missing: %T %v", body["args_json"], body["args_json"])
	}
	var args []string
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		t.Fatalf("args_json not parsable: %v", err)
	}
	if len(args) != 2 || args[0] != "-y" || args[1] != "@anthropic-ai/google-workspace-mcp" {
		t.Errorf("args = %v, want [-y @anthropic-ai/google-workspace-mcp]", args)
	}

	// env_json should carry the MERGED env+env_mapping. Env wins on
	// collision; here the two maps have disjoint keys so both
	// survive.
	envJSON, _ := body["env_json"].(string)
	var env map[string]string
	if err := json.Unmarshal([]byte(envJSON), &env); err != nil {
		t.Fatalf("env_json not parsable: %v", err)
	}
	if env["NODE_ENV"] != "production" {
		t.Errorf("env[NODE_ENV] = %q, want production", env["NODE_ENV"])
	}
	if env["GOOGLE_ACCESS_TOKEN"] != "GOOGLE_ACCESS_TOKEN" {
		t.Errorf("env[GOOGLE_ACCESS_TOKEN] = %q, want GOOGLE_ACCESS_TOKEN", env["GOOGLE_ACCESS_TOKEN"])
	}
}

func TestIntegration_Plan_CreateCrew_UnknownCrewSlugSurfacesAtPlan(t *testing.T) {
	// crew_slug typo should be surfaced at Plan time (NOT only at
	// Exec) so a dry-run catches it before any mutation runs.
	doc := integrationSampleCrewDoc()
	doc.Spec.CrewSlug = "ghost-crew"

	client := newIntegrationFake()
	client.crews["engineering"] = integrationCrewStub{ID: "crew_eng", Slug: "engineering"}

	_, err := doc.Plan(context.Background(), client, nil)
	if err == nil || !strings.Contains(err.Error(), `crew with slug "ghost-crew" not found`) {
		t.Fatalf("want unknown-crew error, got %v", err)
	}
}

// ── 8. Plan: Env merge precedence ───────────────────────────────────────────

func TestIntegration_Plan_EnvOverridesEnvMapping(t *testing.T) {
	// Literal Env values must WIN over EnvMapping values for the
	// same key — that's the package-comment contract for env_json
	// composition.
	doc := integrationSampleWorkspaceDoc()
	doc.Spec.Env = map[string]string{"LINEAR_API_KEY": "literal-override"}
	doc.Spec.EnvMapping = map[string]string{"LINEAR_API_KEY": "LINEAR_API_KEY"}

	client := newIntegrationFake()
	items, _ := doc.Plan(context.Background(), client, nil)
	_ = items[0].Exec(context.Background(), client)

	body, _ := client.findCall("POST", "/api/v1/integrations").Body.(map[string]any)
	envJSON, _ := body["env_json"].(string)
	var env map[string]string
	_ = json.Unmarshal([]byte(envJSON), &env)
	if env["LINEAR_API_KEY"] != "literal-override" {
		t.Errorf("env[LINEAR_API_KEY] = %q, want literal-override (Env must win over EnvMapping)", env["LINEAR_API_KEY"])
	}
}

// ── 9. Plan: Update (drift) ─────────────────────────────────────────────────

func TestIntegration_Plan_UpdateDriftedEndpoint(t *testing.T) {
	doc := integrationSampleWorkspaceDoc()
	doc.Spec.Endpoint = "https://mcp.linear.app/streamable-http-v2"

	oldEndpoint := "https://mcp.linear.app/streamable-http"
	remote := &IntegrationRemote{
		ID:          "int_linear",
		Name:        "linear",
		DisplayName: "Linear",
		Transport:   "streamable-http",
		Endpoint:    &oldEndpoint,
		Enabled:     true,
		Scope:       integrationScopeWorkspace,
	}

	client := newIntegrationFake()
	items, err := doc.Plan(context.Background(), client, remote)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) != 1 || items[0].Action != internalapi.ActionUpdate {
		t.Fatalf("want single Update, got %+v", items)
	}
	if err := items[0].Exec(context.Background(), client); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	patchCall := client.findCall("PATCH", "/api/v1/integrations/int_linear")
	if patchCall == nil {
		t.Fatal("expected PATCH /api/v1/integrations/int_linear")
	}
	body, _ := patchCall.Body.(map[string]any)
	if got, _ := body["endpoint"].(string); got != "https://mcp.linear.app/streamable-http-v2" {
		t.Errorf("endpoint = %q, want streamable-http-v2", got)
	}
	// display_name + transport match remote — must not appear in
	// the narrow patch.
	if _, has := body["display_name"]; has {
		t.Errorf("did not expect display_name in patch, got %v", body["display_name"])
	}
	if _, has := body["transport"]; has {
		t.Errorf("did not expect transport in patch, got %v", body["transport"])
	}
}

// ── 10. Plan: Unchanged ─────────────────────────────────────────────────────

func TestIntegration_Plan_Unchanged(t *testing.T) {
	doc := integrationSampleWorkspaceDoc()
	// Drop env_mapping so the diff stays purely on the fields the
	// remote row actually carries.
	doc.Spec.EnvMapping = nil

	oldEndpoint := "https://mcp.linear.app/streamable-http"
	oldIcon := "linear"
	remote := &IntegrationRemote{
		ID:          "int_linear",
		Name:        "linear",
		DisplayName: "Linear",
		Transport:   "streamable-http",
		Endpoint:    &oldEndpoint,
		Icon:        &oldIcon,
		Enabled:     true,
		Scope:       integrationScopeWorkspace,
	}

	client := newIntegrationFake()
	items, err := doc.Plan(context.Background(), client, remote)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) != 1 || items[0].Action != internalapi.ActionUnchanged {
		t.Fatalf("want single Unchanged, got %+v", items)
	}
	if items[0].Exec != nil {
		t.Error("Unchanged item must have nil Exec")
	}
}

// ── 11. Plan: scope change replace ──────────────────────────────────────────

func TestIntegration_Plan_ScopeChangeWorkspaceToCrew(t *testing.T) {
	// Manifest declares crew scope; remote exists on workspace
	// scope. Expect a Delete + Create pair.
	doc := integrationSampleCrewDoc()

	client := newIntegrationFake()
	client.crews["engineering"] = integrationCrewStub{ID: "crew_eng", Slug: "engineering"}

	remote := &IntegrationRemote{
		ID:        "int_old",
		Name:      "google-workspace",
		Transport: "stdio",
		Scope:     integrationScopeWorkspace, // ← different scope
	}

	items, err := doc.Plan(context.Background(), client, remote)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("want 2 items (Delete+Create), got %d: %+v", len(items), items)
	}
	if items[0].Action != internalapi.ActionDelete {
		t.Errorf("items[0] should be Delete, got %s", items[0].Action)
	}
	if items[1].Action != internalapi.ActionCreate {
		t.Errorf("items[1] should be Create, got %s", items[1].Action)
	}
	if !strings.Contains(items[0].Description, "scope change workspace → crew") {
		t.Errorf("Delete description should call out scope change, got %q", items[0].Description)
	}

	// Execute both: Delete should hit workspace endpoint, Create
	// should hit crew endpoint.
	if err := items[0].Exec(context.Background(), client); err != nil {
		t.Fatalf("Delete Exec: %v", err)
	}
	if err := items[1].Exec(context.Background(), client); err != nil {
		t.Fatalf("Create Exec: %v", err)
	}
	if client.findCall("DELETE", "/api/v1/integrations/int_old") == nil {
		t.Error("expected DELETE /api/v1/integrations/int_old")
	}
	if client.findCall("POST", "/api/v1/crews/crew_eng/integrations") == nil {
		t.Error("expected POST /api/v1/crews/crew_eng/integrations")
	}
}

// ── 12. Lookup helpers ──────────────────────────────────────────────────────

func TestIntegration_LookupWorkspace_Found(t *testing.T) {
	client := newIntegrationFake()
	client.workspaceServers["linear"] = IntegrationRemote{
		ID:        "int_linear",
		Name:      "linear",
		Transport: "streamable-http",
	}
	got, err := LookupIntegrationRemoteBySlug(context.Background(), client, "linear", integrationScopeWorkspace, "")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil remote")
	}
	if got.ID != "int_linear" {
		t.Errorf("ID = %q, want int_linear", got.ID)
	}
	if got.Scope != integrationScopeWorkspace {
		t.Errorf("Scope = %q, want workspace", got.Scope)
	}
}

func TestIntegration_LookupWorkspace_NotFound(t *testing.T) {
	client := newIntegrationFake()
	got, err := LookupIntegrationRemoteBySlug(context.Background(), client, "linear", integrationScopeWorkspace, "")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for not-found, got %+v", got)
	}
}

func TestIntegration_LookupCrew_Found(t *testing.T) {
	client := newIntegrationFake()
	client.crews["engineering"] = integrationCrewStub{ID: "crew_eng", Slug: "engineering"}
	client.crewServers["crew_eng"] = []IntegrationRemote{
		{ID: "int_g", Name: "google-workspace", Transport: "stdio"},
	}
	got, err := LookupIntegrationRemoteBySlug(context.Background(), client, "google-workspace", integrationScopeCrew, "engineering")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil remote")
	}
	if got.CrewID != "crew_eng" {
		t.Errorf("CrewID = %q, want crew_eng", got.CrewID)
	}
	if got.Scope != integrationScopeCrew {
		t.Errorf("Scope = %q, want crew", got.Scope)
	}
}

func TestIntegration_LookupCrew_MissingCrewSlugFails(t *testing.T) {
	client := newIntegrationFake()
	_, err := LookupIntegrationRemoteBySlug(context.Background(), client, "linear", integrationScopeCrew, "")
	if err == nil || !strings.Contains(err.Error(), "crew_slug is required for crew-scope lookup") {
		t.Fatalf("want crew_slug-required error, got %v", err)
	}
}

// ── 13. Export ──────────────────────────────────────────────────────────────

func TestIntegration_Export_WorkspaceAndCrew(t *testing.T) {
	endp := "https://mcp.linear.app/streamable-http"
	icon := "linear"
	argsStr := `["-y","@anthropic-ai/google-workspace-mcp"]`
	envStr := `{"NODE_ENV":"production","GOOGLE_ACCESS_TOKEN":"GOOGLE_ACCESS_TOKEN"}`
	cmd := "npx"

	client := newIntegrationFake()
	client.crews["engineering"] = integrationCrewStub{ID: "crew_eng", Slug: "engineering", Name: "Engineering"}
	client.workspaceServers["linear"] = IntegrationRemote{
		ID:          "int_linear",
		Name:        "linear",
		DisplayName: "Linear",
		Transport:   "streamable-http",
		Endpoint:    &endp,
		Icon:        &icon,
		Enabled:     true,
	}
	client.crewServers["crew_eng"] = []IntegrationRemote{
		{
			ID:          "int_gw",
			Name:        "google-workspace",
			DisplayName: "Google Workspace",
			Transport:   "stdio",
			Command:     &cmd,
			ArgsJSON:    &argsStr,
			EnvJSON:     &envStr,
			Enabled:     true,
		},
	}

	docs, err := ExportIntegrations(context.Background(), client)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if len(docs) != 2 {
		t.Fatalf("want 2 docs, got %d", len(docs))
	}
	// Sorted by scope (crew < workspace alphabetically).
	if docs[0].Spec.Scope != integrationScopeCrew {
		t.Errorf("docs[0].Scope = %q, want crew", docs[0].Spec.Scope)
	}
	if docs[0].Metadata.Slug != "google-workspace" {
		t.Errorf("docs[0].Slug = %q, want google-workspace", docs[0].Metadata.Slug)
	}
	if docs[0].Spec.CrewSlug != "engineering" {
		t.Errorf("docs[0].CrewSlug = %q, want engineering", docs[0].Spec.CrewSlug)
	}
	if len(docs[0].Spec.Args) != 2 || docs[0].Spec.Args[0] != "-y" {
		t.Errorf("docs[0].Args = %v, want [-y ...]", docs[0].Spec.Args)
	}
	if docs[0].Spec.Env["NODE_ENV"] != "production" {
		t.Errorf("docs[0].Env[NODE_ENV] = %q, want production", docs[0].Spec.Env["NODE_ENV"])
	}
	if docs[1].Spec.Scope != integrationScopeWorkspace {
		t.Errorf("docs[1].Scope = %q, want workspace", docs[1].Spec.Scope)
	}
	if docs[1].Metadata.Slug != "linear" {
		t.Errorf("docs[1].Slug = %q, want linear", docs[1].Metadata.Slug)
	}
}

// ── 14. Plan: enabled toggle ────────────────────────────────────────────────

func TestIntegration_Plan_UpdateEnabledFalse(t *testing.T) {
	doc := integrationSampleWorkspaceDoc()
	doc.Spec.EnvMapping = nil
	enabled := false
	doc.Spec.Enabled = &enabled

	endp := "https://mcp.linear.app/streamable-http"
	icon := "linear"
	remote := &IntegrationRemote{
		ID:          "int_linear",
		Name:        "linear",
		DisplayName: "Linear",
		Transport:   "streamable-http",
		Endpoint:    &endp,
		Icon:        &icon,
		Enabled:     true, // ← drift: manifest wants false
		Scope:       integrationScopeWorkspace,
	}

	client := newIntegrationFake()
	items, err := doc.Plan(context.Background(), client, remote)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) != 1 || items[0].Action != internalapi.ActionUpdate {
		t.Fatalf("want single Update, got %+v", items)
	}
	if err := items[0].Exec(context.Background(), client); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	body, _ := client.findCall("PATCH", "/api/v1/integrations/int_linear").Body.(map[string]any)
	en, ok := body["enabled"].(bool)
	if !ok || en {
		t.Errorf("patch.enabled = %v, want false", body["enabled"])
	}
}
