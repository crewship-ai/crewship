package kinds

// Tests for kind: Crew.
//
// Test scope (mirrors crew_template_test.go style + the project_test.go
// drift coverage):
//
//   1. Validate happy path
//   2. Validate error cases — apiVersion, kind, name, slug shape,
//      runtime_image missing, hex color, devcontainer/runtime_image
//      collision, devcontainer memory/cpu bounds, service shape
//      (DNS label, ports, healthcheck durations, volume names)
//   3. Plan Create — body fields + Exec issues POST /api/v1/crews
//   4. Plan Update — drift on individual fields produces a patch
//   5. Plan Unchanged — no drift = no patch + nil Exec
//   6. ExportCrews — round-trip parity for typed + raw fields
//
// The fake client is intentionally minimal: only the GET, POST, PATCH
// endpoints the Crew kind exercises are wired. Anything else returns
// 404 so a route the test didn't expect surfaces as a clear failure
// instead of a quietly-handled empty response.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/manifest/internalapi"
)

// ── Fake client ──────────────────────────────────────────────────────────

type crewFakeClient struct {
	wsID string

	// crews keyed by slug. Tests mutate this to seed pre-existing
	// state; POST + PATCH inject/update entries so a follow-on Plan
	// against the same client sees the after-state.
	crews map[string]CrewRemote

	// listErrStatus, if non-zero, forces GET /api/v1/crews to return
	// that status — exercises error-path coverage in
	// LookupCrewRemoteBySlug.
	listErrStatus int

	// postCallback / patchCallback fire on the corresponding verbs;
	// tests use them to assert on the exact body the manifest layer
	// chose to send.
	postCallback  func(path string, body any)
	patchCallback func(path string, body any)

	calls []string
}

func newCrewFake() *crewFakeClient {
	return &crewFakeClient{wsID: "ws_test", crews: map[string]CrewRemote{}}
}

func (f *crewFakeClient) WorkspaceID() string { return f.wsID }

func (f *crewFakeClient) record(method, path string) {
	f.calls = append(f.calls, method+" "+path)
}

func crewJSONResp(status int, v any) *internalapi.Response {
	data, _ := json.Marshal(v)
	return &internalapi.Response{
		StatusCode: status,
		Body:       io.NopCloser(bytes.NewReader(data)),
	}
}

func (f *crewFakeClient) Get(_ context.Context, path string) (*internalapi.Response, error) {
	f.record("GET", path)
	if path == "/api/v1/crews" {
		if f.listErrStatus != 0 {
			return crewJSONResp(f.listErrStatus, map[string]any{"error": "boom"}), nil
		}
		out := make([]CrewRemote, 0, len(f.crews))
		for _, c := range f.crews {
			out = append(out, c)
		}
		return crewJSONResp(200, out), nil
	}
	return crewJSONResp(404, map[string]any{"error": "not found"}), nil
}

func (f *crewFakeClient) Post(_ context.Context, path string, body any) (*internalapi.Response, error) {
	f.record("POST", path)
	if path == "/api/v1/crews" {
		if f.postCallback != nil {
			f.postCallback(path, body)
		}
		// Materialise so a follow-on Plan sees the row.
		if m, ok := body.(map[string]any); ok {
			slug, _ := m["slug"].(string)
			name, _ := m["name"].(string)
			row := CrewRemote{
				ID:          "crew_" + slug,
				WorkspaceID: f.wsID,
				Name:        name,
				Slug:        slug,
			}
			if v, ok := m["description"].(string); ok {
				row.Description = strPtr(v)
			}
			if v, ok := m["color"].(string); ok {
				row.Color = strPtr(v)
			}
			if v, ok := m["icon"].(string); ok {
				row.Icon = strPtr(v)
			}
			if v, ok := m["runtime_image"].(string); ok {
				row.RuntimeImage = strPtr(v)
			}
			if v, ok := m["devcontainer_config"].(string); ok {
				row.DevcontainerConfig = strPtr(v)
			}
			if v, ok := m["mise_config"].(string); ok {
				row.MiseConfig = strPtr(v)
			}
			if v, ok := m["services_json"].(string); ok {
				row.ServicesJSON = strPtr(v)
			}
			f.crews[slug] = row
		}
		return crewJSONResp(201, map[string]any{"id": "crew_new"}), nil
	}
	return crewJSONResp(404, nil), nil
}

func (f *crewFakeClient) Patch(_ context.Context, path string, body any) (*internalapi.Response, error) {
	f.record("PATCH", path)
	if strings.HasPrefix(path, "/api/v1/crews/") {
		if f.patchCallback != nil {
			f.patchCallback(path, body)
		}
		return crewJSONResp(200, map[string]any{"ok": true}), nil
	}
	return crewJSONResp(404, nil), nil
}

func (f *crewFakeClient) Put(_ context.Context, _ string, _ any) (*internalapi.Response, error) {
	return crewJSONResp(404, nil), nil
}

func (f *crewFakeClient) Delete(_ context.Context, path string) (*internalapi.Response, error) {
	f.record("DELETE", path)
	return crewJSONResp(404, nil), nil
}

func strPtr(s string) *string { return &s }

// makeCrewDoc returns a minimal valid CrewDocument for use as the
// starting point in Validate / Plan tests. Individual tests mutate
// fields to exercise specific code paths.
func makeCrewDoc() *CrewDocument {
	return &CrewDocument{
		APIVersion: crewAPIVersion,
		Kind:       crewKind,
		Metadata: internalapi.Metadata{
			Name: "Engineering",
			Slug: "engineering",
		},
		Spec: CrewSpec{
			Description:  "Backend + frontend work",
			Icon:         "terminal",
			Color:        "#3B82F6",
			RuntimeImage: "mcr.microsoft.com/devcontainers/javascript-node:22-bookworm",
		},
	}
}

// ── 1. Validate: happy path ──────────────────────────────────────────────

func TestCrew_Validate_HappyPath(t *testing.T) {
	doc := makeCrewDoc()
	if err := doc.Validate(internalapi.WorkspaceContext{}); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestCrew_Validate_HappyPath_PaletteColor(t *testing.T) {
	// Palette tokens like "blue" / "green" should pass the color
	// check unchanged — the existing example yamls use them.
	doc := makeCrewDoc()
	doc.Spec.Color = "blue"
	if err := doc.Validate(internalapi.WorkspaceContext{}); err != nil {
		t.Fatalf("Validate with palette color: %v", err)
	}
}

func TestCrew_Validate_HappyPath_FullStack(t *testing.T) {
	// Whole-spec exercise: devcontainer + mise + services. Validates
	// the cross-field interactions don't reject a realistic doc.
	doc := makeCrewDoc()
	doc.Spec.Devcontainer = &Devcontainer{
		Features: map[string]any{
			"ghcr.io/devcontainers/features/common-utils:2": map[string]any{"username": "agent"},
		},
		Env:               map[string]string{"PATH": "/home/agent/.local/bin:/usr/local/bin:/usr/bin"},
		MemoryMB:          4096,
		CPUs:              2.0,
		PostCreateCommand: "npm install -g typescript",
		Raw:               map[string]any{"remoteUser": "agent"},
	}
	doc.Spec.Mise = &MiseConfig{Tools: map[string]string{"node": "22", "python": "3.12"}}
	doc.Spec.Services = []Service{{
		Name:    "postgres",
		Image:   "postgres:16-alpine",
		Env:     map[string]string{"POSTGRES_DB": "app", "POSTGRES_USER": "postgres"},
		EnvRefs: []string{"POSTGRES_PASSWORD"},
		Ports:   []string{"5432"},
		Volumes: []Volume{{Name: "pg-data", Mount: "/var/lib/postgresql/data"}},
		Healthcheck: &Healthcheck{
			Test:     []string{"CMD-SHELL", "pg_isready -U postgres"},
			Interval: "5s", Timeout: "3s", Retries: 5, StartPeriod: "10s",
		},
	}}
	if err := doc.Validate(internalapi.WorkspaceContext{}); err != nil {
		t.Fatalf("Validate full-stack: %v", err)
	}
}

// ── 2. Validate: error cases ─────────────────────────────────────────────

func TestCrew_Validate_WrongAPIVersion(t *testing.T) {
	doc := makeCrewDoc()
	doc.APIVersion = "crewship/v2"
	err := doc.Validate(internalapi.WorkspaceContext{})
	if err == nil || !strings.Contains(err.Error(), "apiVersion") {
		t.Fatalf("want apiVersion error, got %v", err)
	}
}

func TestCrew_Validate_WrongKind(t *testing.T) {
	doc := makeCrewDoc()
	doc.Kind = "NotCrew"
	err := doc.Validate(internalapi.WorkspaceContext{})
	if err == nil || !strings.Contains(err.Error(), "kind") {
		t.Fatalf("want kind error, got %v", err)
	}
}

func TestCrew_Validate_MissingName(t *testing.T) {
	doc := makeCrewDoc()
	doc.Metadata.Name = ""
	err := doc.Validate(internalapi.WorkspaceContext{})
	if err == nil || !strings.Contains(err.Error(), "metadata.name is required") {
		t.Fatalf("want name-required error, got %v", err)
	}
}

func TestCrew_Validate_MissingSlug(t *testing.T) {
	doc := makeCrewDoc()
	doc.Metadata.Slug = ""
	err := doc.Validate(internalapi.WorkspaceContext{})
	if err == nil || !strings.Contains(err.Error(), "metadata.slug is required") {
		t.Fatalf("want slug-required error, got %v", err)
	}
}

func TestCrew_Validate_InvalidSlug(t *testing.T) {
	cases := []struct {
		name string
		slug string
	}{
		{"uppercase", "Engineering"},
		{"leading hyphen", "-eng"},
		{"trailing hyphen", "eng-"},
		{"underscore", "eng_team"},
		{"space", "eng team"},
		{"dot", "eng.team"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := makeCrewDoc()
			doc.Metadata.Slug = tc.slug
			err := doc.Validate(internalapi.WorkspaceContext{})
			if err == nil {
				t.Fatalf("want validation error for slug %q, got nil", tc.slug)
			}
		})
	}
}

func TestCrew_Validate_MissingRuntimeImage(t *testing.T) {
	doc := makeCrewDoc()
	doc.Spec.RuntimeImage = ""
	err := doc.Validate(internalapi.WorkspaceContext{})
	if err == nil || !strings.Contains(err.Error(), "runtime_image is required") {
		t.Fatalf("want runtime_image-required error, got %v", err)
	}
}

func TestCrew_Validate_InvalidHexColor(t *testing.T) {
	doc := makeCrewDoc()
	doc.Spec.Color = "#zzz" // starts with '#' so the hex check fires.
	err := doc.Validate(internalapi.WorkspaceContext{})
	if err == nil || !strings.Contains(err.Error(), "color") {
		t.Fatalf("want hex-color error, got %v", err)
	}
}

func TestCrew_Validate_DevcontainerImageMismatch(t *testing.T) {
	doc := makeCrewDoc()
	doc.Spec.Devcontainer = &Devcontainer{Image: "ubuntu:24.04"} // != spec.runtime_image
	err := doc.Validate(internalapi.WorkspaceContext{})
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("want image-mismatch error, got %v", err)
	}
}

func TestCrew_Validate_NegativeMemory(t *testing.T) {
	doc := makeCrewDoc()
	doc.Spec.Devcontainer = &Devcontainer{MemoryMB: -1}
	err := doc.Validate(internalapi.WorkspaceContext{})
	if err == nil || !strings.Contains(err.Error(), "memory_mb") {
		t.Fatalf("want memory_mb-negative error, got %v", err)
	}
}

func TestCrew_Validate_ServiceBadName(t *testing.T) {
	doc := makeCrewDoc()
	doc.Spec.Services = []Service{{Name: "Bad_Name", Image: "redis:7"}}
	err := doc.Validate(internalapi.WorkspaceContext{})
	if err == nil || !strings.Contains(err.Error(), "DNS label") {
		t.Fatalf("want DNS-label error, got %v", err)
	}
}

func TestCrew_Validate_ServiceMissingImage(t *testing.T) {
	doc := makeCrewDoc()
	doc.Spec.Services = []Service{{Name: "redis"}}
	err := doc.Validate(internalapi.WorkspaceContext{})
	if err == nil || !strings.Contains(err.Error(), "image is required") {
		t.Fatalf("want image-required error, got %v", err)
	}
}

func TestCrew_Validate_ServiceDuplicateName(t *testing.T) {
	doc := makeCrewDoc()
	doc.Spec.Services = []Service{
		{Name: "redis", Image: "redis:7"},
		{Name: "redis", Image: "redis:7-alpine"},
	}
	err := doc.Validate(internalapi.WorkspaceContext{})
	if err == nil || !strings.Contains(err.Error(), "declared more than once") {
		t.Fatalf("want duplicate-name error, got %v", err)
	}
}

func TestCrew_Validate_ServiceBadPort(t *testing.T) {
	doc := makeCrewDoc()
	doc.Spec.Services = []Service{{
		Name: "web", Image: "nginx:alpine",
		Ports: []string{"8080:80"}, // host:container — not allowed on the crew network.
	}}
	err := doc.Validate(internalapi.WorkspaceContext{})
	if err == nil || !strings.Contains(err.Error(), "numeric port") {
		t.Fatalf("want numeric-port error, got %v", err)
	}
}

func TestCrew_Validate_ServiceBindMount(t *testing.T) {
	doc := makeCrewDoc()
	doc.Spec.Services = []Service{{
		Name: "postgres", Image: "postgres:16",
		Volumes: []Volume{{Name: "/host/path", Mount: "/var/lib/postgresql/data"}},
	}}
	err := doc.Validate(internalapi.WorkspaceContext{})
	if err == nil || !strings.Contains(err.Error(), "bind mounts") {
		t.Fatalf("want bind-mount error, got %v", err)
	}
}

func TestCrew_Validate_ServiceHealthcheckBadDuration(t *testing.T) {
	doc := makeCrewDoc()
	doc.Spec.Services = []Service{{
		Name: "redis", Image: "redis:7",
		Healthcheck: &Healthcheck{
			Test:     []string{"CMD", "redis-cli", "ping"},
			Interval: "5sec", // wrong unit suffix; should be "5s".
		},
	}}
	err := doc.Validate(internalapi.WorkspaceContext{})
	if err == nil || !strings.Contains(err.Error(), "duration") {
		t.Fatalf("want duration-parse error, got %v", err)
	}
}

func TestCrew_Validate_ServiceHealthcheckMissingTest(t *testing.T) {
	doc := makeCrewDoc()
	doc.Spec.Services = []Service{{
		Name: "redis", Image: "redis:7",
		Healthcheck: &Healthcheck{Interval: "5s"},
	}}
	err := doc.Validate(internalapi.WorkspaceContext{})
	if err == nil || !strings.Contains(err.Error(), "test command") {
		t.Fatalf("want missing-test error, got %v", err)
	}
}

func TestCrew_Validate_ServiceDuplicateMount(t *testing.T) {
	doc := makeCrewDoc()
	doc.Spec.Services = []Service{{
		Name: "postgres", Image: "postgres:16",
		Volumes: []Volume{
			{Name: "pg-data", Mount: "/var/lib/postgresql/data"},
			{Name: "pg-extra", Mount: "/var/lib/postgresql/data"},
		},
	}}
	err := doc.Validate(internalapi.WorkspaceContext{})
	if err == nil || !strings.Contains(err.Error(), "more than once") {
		t.Fatalf("want duplicate-mount error, got %v", err)
	}
}

// ── 3. Plan: Create when no remote exists ────────────────────────────────

func TestCrew_Plan_Create_FullPayload(t *testing.T) {
	doc := makeCrewDoc()
	doc.Spec.Devcontainer = &Devcontainer{
		Features: map[string]any{
			"ghcr.io/devcontainers/features/github-cli:1": map[string]any{},
		},
		Env:      map[string]string{"FOO": "bar"},
		MemoryMB: 4096,
		CPUs:     2.0,
	}
	doc.Spec.Mise = &MiseConfig{Tools: map[string]string{"node": "22"}}
	doc.Spec.Services = []Service{{
		Name: "redis", Image: "redis:7-alpine", Ports: []string{"6379"},
	}}

	items, err := doc.Plan(context.Background(), newCrewFake(), nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) != 1 || items[0].Action != internalapi.ActionCreate {
		t.Fatalf("want single ActionCreate, got %+v", items)
	}
	if items[0].Kind != "crew" {
		t.Errorf("want kind=crew, got %q", items[0].Kind)
	}
	if items[0].Exec == nil {
		t.Fatal("Create item must have non-nil Exec")
	}

	// Run Exec and capture the wire body. The fake stores the row
	// keyed by slug so this also tests the materialisation path the
	// follow-on Plan tests rely on.
	var capturedBody map[string]any
	client := newCrewFake()
	client.postCallback = func(_ string, body any) {
		if m, ok := body.(map[string]any); ok {
			capturedBody = m
		}
	}
	if err := items[0].Exec(context.Background(), client); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if capturedBody == nil {
		t.Fatal("POST body was nil")
	}
	// Spot-check core fields land on the wire.
	if got, _ := capturedBody["name"].(string); got != "Engineering" {
		t.Errorf("name = %q, want Engineering", got)
	}
	if got, _ := capturedBody["slug"].(string); got != "engineering" {
		t.Errorf("slug = %q, want engineering", got)
	}
	if got, _ := capturedBody["runtime_image"].(string); !strings.Contains(got, "node:22") {
		t.Errorf("runtime_image = %q, missing node:22", got)
	}
	if got, _ := capturedBody["description"].(string); got != "Backend + frontend work" {
		t.Errorf("description = %q", got)
	}
	if got, _ := capturedBody["color"].(string); got != "#3B82F6" {
		t.Errorf("color = %q", got)
	}
	// container_memory_mb / container_cpus mirrored from the
	// devcontainer block (the docker provider reads BOTH the
	// devcontainer JSON and the columns).
	if got, _ := capturedBody["container_memory_mb"].(int); got != 4096 {
		t.Errorf("container_memory_mb = %d, want 4096", got)
	}
	if got, _ := capturedBody["container_cpus"].(float64); got != 2.0 {
		t.Errorf("container_cpus = %v, want 2.0", got)
	}
	// devcontainer_config is the assembled JSON string. Spot-check
	// shape: must contain "image" and the feature id.
	if got, _ := capturedBody["devcontainer_config"].(string); !strings.Contains(got, "github-cli") || !strings.Contains(got, "node:22") {
		t.Errorf("devcontainer_config missing expected keys: %s", got)
	}
	// mise_config: tools.node = "22"
	if got, _ := capturedBody["mise_config"].(string); !strings.Contains(got, `"node":"22"`) {
		t.Errorf("mise_config = %q", got)
	}
	// services_json: redis present
	if got, _ := capturedBody["services_json"].(string); !strings.Contains(got, `"redis"`) {
		t.Errorf("services_json = %q", got)
	}
}

func TestCrew_Plan_Create_MinimalPayload(t *testing.T) {
	// Bare minimum: no devcontainer, no mise, no services. The
	// optional config columns should be ABSENT from the body, not
	// sent as empty strings (which would null them on the row).
	doc := makeCrewDoc()
	items, err := doc.Plan(context.Background(), newCrewFake(), nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	var body map[string]any
	client := newCrewFake()
	client.postCallback = func(_ string, b any) {
		if m, ok := b.(map[string]any); ok {
			body = m
		}
	}
	if err := items[0].Exec(context.Background(), client); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	for _, k := range []string{"devcontainer_config", "mise_config", "services_json", "container_memory_mb", "container_cpus"} {
		if _, present := body[k]; present {
			t.Errorf("minimal payload should omit %q, got %v", k, body[k])
		}
	}
}

// ── 4. Plan: Update on drift ─────────────────────────────────────────────

func TestCrew_Plan_Update_NameDrift(t *testing.T) {
	doc := makeCrewDoc()
	remote := &CrewRemote{
		ID: "crew_eng", WorkspaceID: "ws_test",
		Name: "OldName", Slug: "engineering",
		RuntimeImage: strPtr(doc.Spec.RuntimeImage),
		Description:  strPtr(doc.Spec.Description),
		Icon:         strPtr(doc.Spec.Icon),
		Color:        strPtr(doc.Spec.Color),
	}
	items, err := doc.Plan(context.Background(), newCrewFake(), remote)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) != 1 || items[0].Action != internalapi.ActionUpdate {
		t.Fatalf("want ActionUpdate, got %+v", items)
	}
	// Capture patch body via Exec.
	var patch map[string]any
	client := newCrewFake()
	client.patchCallback = func(_ string, b any) {
		if m, ok := b.(map[string]any); ok {
			patch = m
		}
	}
	if err := items[0].Exec(context.Background(), client); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if got, _ := patch["name"].(string); got != "Engineering" {
		t.Errorf("patch.name = %q, want Engineering", got)
	}
	// Everything else matched the remote — must NOT appear in the patch.
	for _, k := range []string{"description", "color", "icon", "runtime_image"} {
		if _, present := patch[k]; present {
			t.Errorf("patch should omit unchanged field %q, got %v", k, patch[k])
		}
	}
}

func TestCrew_Plan_Update_ServicesDrift(t *testing.T) {
	// Manifest declares a redis sidecar; remote has a Postgres
	// sidecar. Patch must replace services_json with the manifest's
	// version.
	doc := makeCrewDoc()
	doc.Spec.Services = []Service{{Name: "redis", Image: "redis:7-alpine", Ports: []string{"6379"}}}

	existingServices := `[{"name":"postgres","image":"postgres:16-alpine","ports":["5432"]}]`
	remote := &CrewRemote{
		ID: "crew_eng", WorkspaceID: "ws_test",
		Name: doc.Metadata.Name, Slug: doc.Metadata.Slug,
		Description:  strPtr(doc.Spec.Description),
		Icon:         strPtr(doc.Spec.Icon),
		Color:        strPtr(doc.Spec.Color),
		RuntimeImage: strPtr(doc.Spec.RuntimeImage),
		ServicesJSON: strPtr(existingServices),
	}
	items, err := doc.Plan(context.Background(), newCrewFake(), remote)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if items[0].Action != internalapi.ActionUpdate {
		t.Fatalf("want ActionUpdate, got %s", items[0].Action)
	}
	var patch map[string]any
	client := newCrewFake()
	client.patchCallback = func(_ string, b any) {
		if m, ok := b.(map[string]any); ok {
			patch = m
		}
	}
	if err := items[0].Exec(context.Background(), client); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	got, _ := patch["services_json"].(string)
	if !strings.Contains(got, `"redis"`) || strings.Contains(got, `"postgres"`) {
		t.Errorf("patch services_json should swap postgres→redis, got %q", got)
	}
}

func TestCrew_Plan_Update_DevcontainerDrift(t *testing.T) {
	doc := makeCrewDoc()
	doc.Spec.Devcontainer = &Devcontainer{
		Features: map[string]any{"ghcr.io/devcontainers/features/github-cli:1": map[string]any{}},
	}
	remote := &CrewRemote{
		ID: "crew_eng", WorkspaceID: "ws_test",
		Name: doc.Metadata.Name, Slug: doc.Metadata.Slug,
		Description:        strPtr(doc.Spec.Description),
		Icon:               strPtr(doc.Spec.Icon),
		Color:              strPtr(doc.Spec.Color),
		RuntimeImage:       strPtr(doc.Spec.RuntimeImage),
		DevcontainerConfig: strPtr(`{"image":"different:image"}`),
	}
	items, err := doc.Plan(context.Background(), newCrewFake(), remote)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if items[0].Action != internalapi.ActionUpdate {
		t.Fatalf("want ActionUpdate, got %s", items[0].Action)
	}
}

// ── 5. Plan: Unchanged on match ──────────────────────────────────────────

func TestCrew_Plan_Unchanged(t *testing.T) {
	doc := makeCrewDoc()
	remote := &CrewRemote{
		ID: "crew_eng", WorkspaceID: "ws_test",
		Name: doc.Metadata.Name, Slug: doc.Metadata.Slug,
		Description:  strPtr(doc.Spec.Description),
		Icon:         strPtr(doc.Spec.Icon),
		Color:        strPtr(doc.Spec.Color),
		RuntimeImage: strPtr(doc.Spec.RuntimeImage),
	}
	items, err := doc.Plan(context.Background(), newCrewFake(), remote)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) != 1 || items[0].Action != internalapi.ActionUnchanged {
		t.Fatalf("want ActionUnchanged, got %+v", items)
	}
	if items[0].Exec != nil {
		t.Error("Unchanged item must have nil Exec")
	}
}

func TestCrew_Plan_Unchanged_JSONStringNormalisation(t *testing.T) {
	// Same logical services JSON but with different key order +
	// whitespace. jsonStringEqual should normalise both sides so the
	// Plan sees Unchanged.
	doc := makeCrewDoc()
	doc.Spec.Services = []Service{{
		Name: "redis", Image: "redis:7-alpine", Ports: []string{"6379"},
	}}
	desired, err := doc.servicesJSON()
	if err != nil {
		t.Fatalf("servicesJSON: %v", err)
	}
	// Compute the manifest's wire shape, then add whitespace + a
	// key swap to simulate a server re-emit. The simplest faithful
	// stand-in is to round-trip through map→indent→back, since the
	// server itself could re-marshal under any encoder.
	var parsed []map[string]any
	if err := json.Unmarshal([]byte(desired), &parsed); err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	reEmitted, _ := json.MarshalIndent(parsed, "", "  ")
	remote := &CrewRemote{
		ID: "crew_eng", WorkspaceID: "ws_test",
		Name: doc.Metadata.Name, Slug: doc.Metadata.Slug,
		Description:  strPtr(doc.Spec.Description),
		Icon:         strPtr(doc.Spec.Icon),
		Color:        strPtr(doc.Spec.Color),
		RuntimeImage: strPtr(doc.Spec.RuntimeImage),
		ServicesJSON: strPtr(string(reEmitted)),
	}
	items, err := doc.Plan(context.Background(), newCrewFake(), remote)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if items[0].Action != internalapi.ActionUnchanged {
		t.Fatalf("want Unchanged after normalisation, got %s (desc=%q)", items[0].Action, items[0].Description)
	}
}

// ── 6. LookupCrewRemoteBySlug ────────────────────────────────────────────

func TestCrew_LookupBySlug_Hit(t *testing.T) {
	client := newCrewFake()
	client.crews["engineering"] = CrewRemote{
		ID: "crew_eng", WorkspaceID: "ws_test",
		Name: "Engineering", Slug: "engineering",
	}
	got, err := LookupCrewRemoteBySlug(context.Background(), client, "engineering")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got == nil || got.ID != "crew_eng" {
		t.Fatalf("want crew_eng, got %+v", got)
	}
}

func TestCrew_LookupBySlug_Miss(t *testing.T) {
	client := newCrewFake()
	got, err := LookupCrewRemoteBySlug(context.Background(), client, "missing")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got != nil {
		t.Fatalf("want nil remote on miss, got %+v", got)
	}
}

func TestCrew_LookupBySlug_EmptySlug(t *testing.T) {
	_, err := LookupCrewRemoteBySlug(context.Background(), newCrewFake(), "")
	if err == nil {
		t.Fatal("expected error for empty slug")
	}
}

func TestCrew_LookupBySlug_ListError(t *testing.T) {
	client := newCrewFake()
	client.listErrStatus = 500
	_, err := LookupCrewRemoteBySlug(context.Background(), client, "engineering")
	if err == nil {
		t.Fatal("expected error when list returns 500")
	}
}

// ── 7. ExportCrews: round-trip ───────────────────────────────────────────

func TestExportCrews_RoundTripsTypedFields(t *testing.T) {
	// Seed a crew with a typed devcontainer + mise + one service, then
	// export and verify the round-trip lands in the typed fields
	// (not Raw).
	dc := `{"image":"node:22","features":{"ghcr.io/devcontainers/features/github-cli:1":{}},"containerEnv":{"FOO":"bar"},"postCreateCommand":"echo hi","hostRequirements":{"memory":"4096mb","cpus":2}}`
	mise := `{"tools":{"node":"22","python":"3.12"}}`
	services := `[{"name":"redis","image":"redis:7-alpine","ports":["6379"]}]`

	client := newCrewFake()
	client.crews["engineering"] = CrewRemote{
		ID: "crew_eng", WorkspaceID: "ws_test",
		Name: "Engineering", Slug: "engineering",
		Description:        strPtr("Backend"),
		Icon:               strPtr("terminal"),
		Color:              strPtr("#3B82F6"),
		RuntimeImage:       strPtr("node:22"),
		DevcontainerConfig: strPtr(dc),
		MiseConfig:         strPtr(mise),
		ServicesJSON:       strPtr(services),
	}
	docs, err := ExportCrews(context.Background(), client)
	if err != nil {
		t.Fatalf("ExportCrews: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("want 1 doc, got %d", len(docs))
	}
	d := docs[0]
	if d.Kind != crewKind || d.APIVersion != crewAPIVersion {
		t.Errorf("envelope = %s/%s", d.Kind, d.APIVersion)
	}
	if d.Spec.RuntimeImage != "node:22" {
		t.Errorf("runtime_image = %q", d.Spec.RuntimeImage)
	}
	if d.Spec.Devcontainer == nil {
		t.Fatal("devcontainer should be populated")
	}
	if d.Spec.Devcontainer.PostCreateCommand != "echo hi" {
		t.Errorf("post_create_command = %q", d.Spec.Devcontainer.PostCreateCommand)
	}
	if d.Spec.Devcontainer.MemoryMB != 4096 {
		t.Errorf("memory_mb = %d, want 4096", d.Spec.Devcontainer.MemoryMB)
	}
	if d.Spec.Devcontainer.CPUs != 2 {
		t.Errorf("cpus = %v", d.Spec.Devcontainer.CPUs)
	}
	if d.Spec.Devcontainer.Env["FOO"] != "bar" {
		t.Errorf("env.FOO = %q", d.Spec.Devcontainer.Env["FOO"])
	}
	if _, ok := d.Spec.Devcontainer.Features["ghcr.io/devcontainers/features/github-cli:1"]; !ok {
		t.Errorf("missing github-cli feature in export")
	}
	if d.Spec.Mise == nil || d.Spec.Mise.Tools["node"] != "22" {
		t.Errorf("mise.tools.node = %v", d.Spec.Mise)
	}
	if len(d.Spec.Services) != 1 || d.Spec.Services[0].Name != "redis" {
		t.Errorf("services = %+v", d.Spec.Services)
	}
}

func TestExportCrews_StashesUnknownDevcontainerKeysInRaw(t *testing.T) {
	// The "remoteUser" / "customizations" keys aren't modeled by
	// Devcontainer; they MUST land in Raw so a round-trip preserves
	// them.
	dc := `{"image":"node:22","remoteUser":"agent","customizations":{"vscode":{"settings":{"foo":"bar"}}}}`
	client := newCrewFake()
	client.crews["engineering"] = CrewRemote{
		ID: "crew_eng", WorkspaceID: "ws_test",
		Name: "Engineering", Slug: "engineering",
		RuntimeImage:       strPtr("node:22"),
		DevcontainerConfig: strPtr(dc),
	}
	docs, err := ExportCrews(context.Background(), client)
	if err != nil {
		t.Fatalf("ExportCrews: %v", err)
	}
	if len(docs) != 1 || docs[0].Spec.Devcontainer == nil {
		t.Fatalf("export missing devcontainer: %+v", docs)
	}
	raw := docs[0].Spec.Devcontainer.Raw
	if got, _ := raw["remoteUser"].(string); got != "agent" {
		t.Errorf("raw.remoteUser = %v", raw["remoteUser"])
	}
	if _, ok := raw["customizations"]; !ok {
		t.Errorf("raw.customizations missing")
	}
}

func TestExportCrews_SortedBySlug(t *testing.T) {
	client := newCrewFake()
	client.crews["zeta"] = CrewRemote{ID: "z", Name: "Z", Slug: "zeta", RuntimeImage: strPtr("img")}
	client.crews["alpha"] = CrewRemote{ID: "a", Name: "A", Slug: "alpha", RuntimeImage: strPtr("img")}
	client.crews["mu"] = CrewRemote{ID: "m", Name: "M", Slug: "mu", RuntimeImage: strPtr("img")}

	docs, err := ExportCrews(context.Background(), client)
	if err != nil {
		t.Fatalf("ExportCrews: %v", err)
	}
	got := []string{docs[0].Metadata.Slug, docs[1].Metadata.Slug, docs[2].Metadata.Slug}
	want := []string{"alpha", "mu", "zeta"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("docs[%d].slug = %q, want %q (full order: %v)", i, got[i], want[i], got)
		}
	}
}
