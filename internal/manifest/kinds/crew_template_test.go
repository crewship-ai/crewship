package kinds

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/manifest/internalapi"
)

// ── Fake client (CrewTemplate-specific) ──────────────────────────────────────
//
// CrewTemplate routes touch two endpoints:
//
//	GET  /api/v1/crew-templates              — catalog listing
//	GET  /api/v1/crews                       — to find the override slug
//	POST /api/v1/crew-templates/{slug}/deploy — the one-shot mutation
//
// A focused fake keeps the test surface obvious. We deliberately don't
// share fakeClient with recipe_test.go because the recipe fake's
// fixture set (Installed flag) doesn't match CrewTemplate's concerns
// and intertwining them would make a recipe schema change ripple into
// these tests.
type crewTemplateFakeClient struct {
	wsID string

	templates map[string]crewTemplateStub
	crews     map[string]crewStub

	// deployCallback fires whenever POST /api/v1/crew-templates/{slug}/deploy
	// is invoked. Tests use it to capture the body the manifest sent.
	deployCallback func(slug string, body any)

	// deployStatus overrides the deploy endpoint's response code
	// (default 201). Used by the conflict-path test to make Apply
	// surface the server's 409.
	deployStatus int

	// templatesGetErr forces GET /api/v1/crew-templates to return a
	// non-2xx status; lets us assert error propagation from
	// LookupCrewTemplateRemote.
	templatesGetErr bool

	calls []string
}

func newCrewTemplateFake() *crewTemplateFakeClient {
	return &crewTemplateFakeClient{
		wsID:      "ws_test",
		templates: map[string]crewTemplateStub{},
		crews:     map[string]crewStub{},
	}
}

func (f *crewTemplateFakeClient) WorkspaceID() string { return f.wsID }

func (f *crewTemplateFakeClient) record(method, path string) {
	f.calls = append(f.calls, method+" "+path)
}

func ctJSONResp(status int, v any) *internalapi.Response {
	data, _ := json.Marshal(v)
	return &internalapi.Response{
		StatusCode: status,
		Body:       io.NopCloser(bytes.NewReader(data)),
	}
}

func (f *crewTemplateFakeClient) Get(_ context.Context, path string) (*internalapi.Response, error) {
	f.record("GET", path)
	switch path {
	case "/api/v1/crew-templates":
		if f.templatesGetErr {
			return ctJSONResp(500, map[string]any{"error": "boom"}), nil
		}
		out := make([]crewTemplateStub, 0, len(f.templates))
		for _, t := range f.templates {
			out = append(out, t)
		}
		return ctJSONResp(200, out), nil
	case "/api/v1/crews":
		out := make([]crewStub, 0, len(f.crews))
		for _, c := range f.crews {
			out = append(out, c)
		}
		return ctJSONResp(200, out), nil
	}
	return ctJSONResp(404, map[string]any{"error": "not found"}), nil
}

func (f *crewTemplateFakeClient) Post(_ context.Context, path string, body any) (*internalapi.Response, error) {
	f.record("POST", path)
	if strings.HasPrefix(path, "/api/v1/crew-templates/") && strings.HasSuffix(path, "/deploy") {
		slug := strings.TrimSuffix(strings.TrimPrefix(path, "/api/v1/crew-templates/"), "/deploy")
		if f.deployCallback != nil {
			f.deployCallback(slug, body)
		}
		status := f.deployStatus
		if status == 0 {
			status = 201
		}
		// If the deploy succeeded, materialise the resulting crew in
		// the fake so a follow-on Plan against the same client returns
		// Unchanged (mirrors the real server's post-commit state).
		if status >= 200 && status < 300 {
			if m, ok := body.(map[string]any); ok {
				crewSlug, _ := m["crew_slug"].(string)
				crewName, _ := m["crew_name"].(string)
				if crewSlug != "" {
					f.crews[crewSlug] = crewStub{
						ID:   "crew_" + crewSlug,
						Name: crewName,
						Slug: crewSlug,
					}
				}
			}
		}
		return ctJSONResp(status, map[string]any{
			"crew_id":     "crew_new",
			"crew_slug":   "stub",
			"agent_count": 3,
		}), nil
	}
	return ctJSONResp(404, map[string]any{"error": "not found"}), nil
}

func (f *crewTemplateFakeClient) Patch(_ context.Context, _ string, _ any) (*internalapi.Response, error) {
	return ctJSONResp(404, nil), nil
}
func (f *crewTemplateFakeClient) Put(_ context.Context, _ string, _ any) (*internalapi.Response, error) {
	return ctJSONResp(404, nil), nil
}
func (f *crewTemplateFakeClient) Delete(_ context.Context, path string) (*internalapi.Response, error) {
	f.record("DELETE", path)
	return ctJSONResp(404, nil), nil
}

// helper: minimal valid document for happy-path tests.
func makeCrewTemplateDoc() *CrewTemplateDocument {
	return &CrewTemplateDocument{
		APIVersion: crewTemplateAPIVersion,
		Kind:       crewTemplateKind,
		Metadata: internalapi.Metadata{
			Name: "Engineering team",
			Slug: "engineering-team",
		},
		Spec: CrewTemplateSpec{
			Deploy:           true,
			CrewSlugOverride: "my-eng-team",
		},
	}
}

// ── 1. Validate: happy path ───────────────────────────────────────────────

func TestCrewTemplate_Validate_HappyPath(t *testing.T) {
	doc := makeCrewTemplateDoc()
	if err := doc.Validate(internalapi.WorkspaceContext{}); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestCrewTemplate_Validate_AcceptsInputs(t *testing.T) {
	doc := makeCrewTemplateDoc()
	doc.Spec.Inputs = map[string]any{
		"devcontainer_image": "node:20",
		"region":             "eu-west-1",
	}
	if err := doc.Validate(internalapi.WorkspaceContext{}); err != nil {
		t.Fatalf("Validate with inputs: %v", err)
	}
}

// ── 2. Validate: error paths ──────────────────────────────────────────────

func TestCrewTemplate_Validate_MissingName(t *testing.T) {
	doc := makeCrewTemplateDoc()
	doc.Metadata.Name = ""
	err := doc.Validate(internalapi.WorkspaceContext{})
	if err == nil || !strings.Contains(err.Error(), "metadata.name is required") {
		t.Fatalf("want name-required error, got %v", err)
	}
}

func TestCrewTemplate_Validate_MissingTemplateSlug(t *testing.T) {
	doc := makeCrewTemplateDoc()
	doc.Metadata.Slug = ""
	err := doc.Validate(internalapi.WorkspaceContext{})
	if err == nil || !strings.Contains(err.Error(), "metadata.slug is required") {
		t.Fatalf("want template-slug-required error, got %v", err)
	}
}

func TestCrewTemplate_Validate_MissingCrewSlugOverride(t *testing.T) {
	doc := makeCrewTemplateDoc()
	doc.Spec.CrewSlugOverride = ""
	err := doc.Validate(internalapi.WorkspaceContext{})
	if err == nil || !strings.Contains(err.Error(), "spec.crew_slug_override is required") {
		t.Fatalf("want crew_slug_override-required error, got %v", err)
	}
}

func TestCrewTemplate_Validate_InvalidCrewSlugOverride(t *testing.T) {
	cases := []struct {
		name     string
		override string
	}{
		{"uppercase letters", "MyEngTeam"},
		{"leading hyphen", "-eng"},
		{"trailing hyphen", "eng-"},
		{"underscore", "my_eng_team"},
		{"space", "my eng"},
		{"empty after trim — different code path", "  "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := makeCrewTemplateDoc()
			doc.Spec.CrewSlugOverride = tc.override
			err := doc.Validate(internalapi.WorkspaceContext{})
			if err == nil {
				t.Fatalf("want validation error for override %q, got nil", tc.override)
			}
		})
	}
}

func TestCrewTemplate_Validate_WrongAPIVersion(t *testing.T) {
	doc := makeCrewTemplateDoc()
	doc.APIVersion = "crewship/v2"
	err := doc.Validate(internalapi.WorkspaceContext{})
	if err == nil || !strings.Contains(err.Error(), "apiVersion") {
		t.Fatalf("want apiVersion error, got %v", err)
	}
}

// ── 3. Plan: deploy fresh (no crew yet) → Create ──────────────────────────

func TestCrewTemplate_Plan_DeployFresh(t *testing.T) {
	doc := makeCrewTemplateDoc()
	doc.Spec.Inputs = map[string]any{"devcontainer_image": "node:20"}

	client := newCrewTemplateFake()
	client.templates["engineering-team"] = crewTemplateStub{
		ID:        "tmpl_eng",
		Name:      "Engineering team",
		Slug:      "engineering-team",
		IsBuiltin: true,
	}
	remote, err := LookupCrewTemplateRemote(context.Background(), client, doc)
	if err != nil {
		t.Fatalf("LookupCrewTemplateRemote: %v", err)
	}

	items, err := doc.Plan(context.Background(), client, remote)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("want 1 item, got %d", len(items))
	}
	if items[0].Action != internalapi.ActionCreate {
		t.Errorf("want ActionCreate, got %s", items[0].Action)
	}
	if items[0].Kind != "crew_template" {
		t.Errorf("want kind 'crew_template', got %q", items[0].Kind)
	}
	if items[0].Exec == nil {
		t.Fatal("Create item must have non-nil Exec")
	}

	// Run Exec and capture the body the deploy endpoint received.
	var capturedSlug string
	var capturedBody map[string]any
	client.deployCallback = func(slug string, body any) {
		capturedSlug = slug
		if m, ok := body.(map[string]any); ok {
			capturedBody = m
		}
	}
	if err := items[0].Exec(context.Background(), client); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if capturedSlug != "engineering-team" {
		t.Errorf("want deploy against engineering-team, got %q", capturedSlug)
	}
	if capturedBody == nil {
		t.Fatal("deploy body was nil")
	}
	if got, _ := capturedBody["crew_name"].(string); got != "Engineering team" {
		t.Errorf("crew_name = %q, want %q", got, "Engineering team")
	}
	if got, _ := capturedBody["crew_slug"].(string); got != "my-eng-team" {
		t.Errorf("crew_slug = %q, want %q", got, "my-eng-team")
	}
	inputs, _ := capturedBody["inputs"].(map[string]any)
	if got, _ := inputs["devcontainer_image"].(string); got != "node:20" {
		t.Errorf("inputs.devcontainer_image = %q, want node:20", got)
	}
}

// ── 4. Plan: already deployed (crew exists under override slug) → Unchanged ─

func TestCrewTemplate_Plan_AlreadyDeployed(t *testing.T) {
	doc := makeCrewTemplateDoc()

	client := newCrewTemplateFake()
	client.templates["engineering-team"] = crewTemplateStub{
		ID: "tmpl_eng", Name: "Engineering team", Slug: "engineering-team",
	}
	client.crews["my-eng-team"] = crewStub{
		ID: "crew_existing", Name: "Engineering team", Slug: "my-eng-team",
	}

	remote, err := LookupCrewTemplateRemote(context.Background(), client, doc)
	if err != nil {
		t.Fatalf("LookupCrewTemplateRemote: %v", err)
	}
	if remote.DeployedCrew == nil {
		t.Fatal("expected remote.DeployedCrew to be populated for already-deployed case")
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
	if !strings.Contains(items[0].Description, "already deployed") {
		t.Errorf("want 'already deployed' in description, got %q", items[0].Description)
	}
}

// ── 5. Plan: deploy=false acts as a no-op (with warning when crew exists) ───

func TestCrewTemplate_Plan_DeployFalseCrewMissing(t *testing.T) {
	doc := makeCrewTemplateDoc()
	doc.Spec.Deploy = false

	client := newCrewTemplateFake()
	client.templates["engineering-team"] = crewTemplateStub{
		ID: "tmpl_eng", Name: "Engineering team", Slug: "engineering-team",
	}
	remote, err := LookupCrewTemplateRemote(context.Background(), client, doc)
	if err != nil {
		t.Fatalf("LookupCrewTemplateRemote: %v", err)
	}

	items, err := doc.Plan(context.Background(), client, remote)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) != 1 || items[0].Action != internalapi.ActionUnchanged {
		t.Fatalf("want single Unchanged, got %+v", items)
	}
	if items[0].Exec != nil {
		t.Error("deploy=false must produce nil Exec")
	}
	if !strings.Contains(items[0].Description, "deploy=false") {
		t.Errorf("want 'deploy=false' in description, got %q", items[0].Description)
	}
}

func TestCrewTemplate_Plan_DeployFalseCrewPresent(t *testing.T) {
	doc := makeCrewTemplateDoc()
	doc.Spec.Deploy = false

	client := newCrewTemplateFake()
	client.templates["engineering-team"] = crewTemplateStub{
		ID: "tmpl_eng", Name: "Engineering team", Slug: "engineering-team",
	}
	client.crews["my-eng-team"] = crewStub{
		ID: "crew_existing", Name: "Engineering team", Slug: "my-eng-team",
	}
	remote, err := LookupCrewTemplateRemote(context.Background(), client, doc)
	if err != nil {
		t.Fatalf("LookupCrewTemplateRemote: %v", err)
	}

	items, err := doc.Plan(context.Background(), client, remote)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) != 1 || items[0].Action != internalapi.ActionUnchanged {
		t.Fatalf("want single Unchanged, got %+v", items)
	}
	// Warning should make the "no undeploy" semantics explicit.
	if !strings.Contains(items[0].Description, "cannot undeploy") {
		t.Errorf("want 'cannot undeploy' in description, got %q", items[0].Description)
	}
}

// ── 6. Plan: source template missing → Plan errors ─────────────────────────

func TestCrewTemplate_Plan_SourceTemplateMissing(t *testing.T) {
	doc := makeCrewTemplateDoc()

	client := newCrewTemplateFake()
	// Note: no template seeded → LookupCrewTemplateRemote will report
	// TemplateExists=false and Plan should refuse to emit Create.

	remote, err := LookupCrewTemplateRemote(context.Background(), client, doc)
	if err != nil {
		t.Fatalf("LookupCrewTemplateRemote: %v", err)
	}
	if remote.TemplateExists {
		t.Fatal("expected TemplateExists=false when no template seeded")
	}

	_, err = doc.Plan(context.Background(), client, remote)
	if err == nil || !strings.Contains(err.Error(), "source template not found") {
		t.Fatalf("want source-template-not-found error, got %v", err)
	}
}

func TestCrewTemplate_Plan_NilRemoteRejected(t *testing.T) {
	doc := makeCrewTemplateDoc()
	client := newCrewTemplateFake()
	_, err := doc.Plan(context.Background(), client, nil)
	if err == nil || !strings.Contains(err.Error(), "non-nil remote") {
		t.Fatalf("want nil-remote error, got %v", err)
	}
}

// ── 7. Plan: deploy server error surfaces via Exec ─────────────────────────

func TestCrewTemplate_Plan_DeployConflictSurfacedFromExec(t *testing.T) {
	doc := makeCrewTemplateDoc()

	client := newCrewTemplateFake()
	client.templates["engineering-team"] = crewTemplateStub{
		ID: "tmpl_eng", Name: "Engineering team", Slug: "engineering-team",
	}
	// Force the deploy endpoint to behave like the real server's 409
	// (errCrewSlugConflict) — Apply may see this if a parallel admin
	// grabbed the slug between Plan and Exec.
	client.deployStatus = 409

	remote, err := LookupCrewTemplateRemote(context.Background(), client, doc)
	if err != nil {
		t.Fatalf("LookupCrewTemplateRemote: %v", err)
	}
	items, err := doc.Plan(context.Background(), client, remote)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if items[0].Action != internalapi.ActionCreate {
		t.Fatalf("expected ActionCreate before conflict surfaces, got %s", items[0].Action)
	}
	err = items[0].Exec(context.Background(), client)
	if err == nil || !strings.Contains(err.Error(), "409") {
		t.Fatalf("want 409 error from Exec, got %v", err)
	}
}

// ── 8. ExportCrewTemplates: heuristic provenance match ─────────────────────

func TestExportCrewTemplates_MatchesByMatchingSlug(t *testing.T) {
	client := newCrewTemplateFake()
	// Two templates exist in the catalog.
	client.templates["engineering-team"] = crewTemplateStub{
		ID: "tmpl_a", Name: "Engineering team", Slug: "engineering-team", IsBuiltin: true,
	}
	client.templates["data-team"] = crewTemplateStub{
		ID: "tmpl_b", Name: "Data team", Slug: "data-team", IsBuiltin: true,
	}
	// Three crews exist:
	//   - engineering-team — slug matches a template → exported
	//   - data-team        — slug matches a template → exported
	//   - my-custom-crew   — no template match → skipped (Crew kind picks it up)
	client.crews["engineering-team"] = crewStub{ID: "crew_1", Name: "Engineering team", Slug: "engineering-team"}
	client.crews["data-team"] = crewStub{ID: "crew_2", Name: "Data team", Slug: "data-team"}
	client.crews["my-custom-crew"] = crewStub{ID: "crew_3", Name: "Custom", Slug: "my-custom-crew"}

	docs, err := ExportCrewTemplates(context.Background(), client)
	if err != nil {
		t.Fatalf("ExportCrewTemplates: %v", err)
	}
	if len(docs) != 2 {
		t.Fatalf("want 2 exported docs, got %d", len(docs))
	}
	seen := map[string]bool{}
	for _, d := range docs {
		if d.Kind != crewTemplateKind {
			t.Errorf("kind = %q, want %s", d.Kind, crewTemplateKind)
		}
		if d.APIVersion != crewTemplateAPIVersion {
			t.Errorf("apiVersion = %q, want %s", d.APIVersion, crewTemplateAPIVersion)
		}
		if !d.Spec.Deploy {
			t.Errorf("export should set Deploy=true, slug=%s", d.Metadata.Slug)
		}
		// crew_slug_override should equal the crew slug (heuristic case).
		if d.Spec.CrewSlugOverride != d.Metadata.Slug {
			t.Errorf("crew_slug_override = %q, want %q", d.Spec.CrewSlugOverride, d.Metadata.Slug)
		}
		seen[d.Metadata.Slug] = true
	}
	if !seen["engineering-team"] || !seen["data-team"] {
		t.Errorf("missing expected slugs in export: %v", seen)
	}
	if seen["my-custom-crew"] {
		t.Error("non-template crew should not appear in CrewTemplate export")
	}
}

func TestExportCrewTemplates_EmptyWhenNoTemplates(t *testing.T) {
	client := newCrewTemplateFake()
	// Crew exists but no template catalog → nothing to round-trip.
	client.crews["lonely"] = crewStub{ID: "crew_x", Name: "Lonely", Slug: "lonely"}

	docs, err := ExportCrewTemplates(context.Background(), client)
	if err != nil {
		t.Fatalf("ExportCrewTemplates: %v", err)
	}
	if len(docs) != 0 {
		t.Errorf("want 0 docs when catalog empty, got %d", len(docs))
	}
}
