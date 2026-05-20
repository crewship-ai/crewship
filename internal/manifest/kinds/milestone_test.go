package kinds

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/manifest/internalapi"
	"gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------------------
// milestoneFakeClient — in-memory stand-in for internalapi.Client.
//
// Named with a `milestone` prefix because other kind tests in this
// package (label_test.go, triage_rule_test.go, project_test.go, …)
// declare their own fake-client types and helpers with conflicting
// short names. Using a milestone* prefix everywhere keeps the test
// binary linkable regardless of which sibling files exist.
// ---------------------------------------------------------------------------

type milestoneFakeClient struct {
	wsID string

	// projects is the list returned by GET /api/v1/projects.
	projects []milestoneProjectStub

	// milestonesByProject keys the list returned by GET
	// /api/v1/projects/{id}/milestones on the project_id.
	milestonesByProject map[string][]MilestoneRemote

	calls []milestoneFakeCall
}

type milestoneFakeCall struct {
	Method string
	Path   string
	Body   map[string]any
}

func newMilestoneFakeClient() *milestoneFakeClient {
	return &milestoneFakeClient{
		wsID:                "ws_test",
		milestonesByProject: map[string][]MilestoneRemote{},
	}
}

func (f *milestoneFakeClient) WorkspaceID() string { return f.wsID }

func (f *milestoneFakeClient) record(method, path string, body any) {
	bmap, _ := body.(map[string]any)
	f.calls = append(f.calls, milestoneFakeCall{Method: method, Path: path, Body: bmap})
}

func (f *milestoneFakeClient) jsonResp(status int, v any) (*internalapi.Response, error) {
	data, _ := json.Marshal(v)
	return &internalapi.Response{
		StatusCode: status,
		Body:       bytes.NewReader(data),
	}, nil
}

func (f *milestoneFakeClient) Get(_ context.Context, path string) (*internalapi.Response, error) {
	f.record("GET", path, nil)
	switch {
	case path == "/api/v1/projects":
		return f.jsonResp(200, f.projects)
	case strings.HasPrefix(path, "/api/v1/projects/") && strings.HasSuffix(path, "/milestones"):
		projectID := strings.TrimSuffix(strings.TrimPrefix(path, "/api/v1/projects/"), "/milestones")
		return f.jsonResp(200, f.milestonesByProject[projectID])
	}
	return f.jsonResp(404, map[string]any{"error": "not stubbed: " + path})
}

func (f *milestoneFakeClient) Post(_ context.Context, path string, body any) (*internalapi.Response, error) {
	f.record("POST", path, body)
	if strings.HasPrefix(path, "/api/v1/projects/") && strings.HasSuffix(path, "/milestones") {
		return f.jsonResp(201, body)
	}
	return f.jsonResp(404, map[string]any{"error": "not stubbed: " + path})
}

func (f *milestoneFakeClient) Patch(_ context.Context, path string, body any) (*internalapi.Response, error) {
	f.record("PATCH", path, body)
	if strings.HasPrefix(path, "/api/v1/milestones/") {
		return f.jsonResp(200, body)
	}
	return f.jsonResp(404, map[string]any{"error": "not stubbed: " + path})
}

func (f *milestoneFakeClient) Put(_ context.Context, path string, body any) (*internalapi.Response, error) {
	f.record("PUT", path, body)
	return f.jsonResp(404, map[string]any{"error": "not stubbed: " + path})
}

func (f *milestoneFakeClient) Delete(_ context.Context, path string) (*internalapi.Response, error) {
	f.record("DELETE", path, nil)
	if strings.HasPrefix(path, "/api/v1/milestones/") {
		return f.jsonResp(204, map[string]any{})
	}
	return f.jsonResp(404, map[string]any{"error": "not stubbed: " + path})
}

// findCall returns the first recorded call matching method+path or
// nil so assertions can use a clean "if got == nil" check instead of
// indexing.
func (f *milestoneFakeClient) findCall(method, path string) *milestoneFakeCall {
	for i := range f.calls {
		if f.calls[i].Method == method && f.calls[i].Path == path {
			return &f.calls[i]
		}
	}
	return nil
}

// Compile-time assertion: milestoneFakeClient satisfies the interface
// kinds use. This catches drift in internalapi.Client before any
// runtime test ever runs.
var _ internalapi.Client = (*milestoneFakeClient)(nil)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func milestoneSampleDoc() *MilestoneDocument {
	return &MilestoneDocument{
		APIVersion: "crewship/v1",
		Kind:       "Milestone",
		Metadata: internalapi.Metadata{
			Name: "v1.0 launch",
			Slug: "v1-launch",
		},
		Spec: MilestoneSpec{
			ProjectSlug: "q2-roadmap",
			Description: "Public 1.0 release",
			TargetDate:  "2026-06-15",
			Status:      "planned",
		},
	}
}

func milestoneCtxWithProject(slug string) internalapi.WorkspaceContext {
	return internalapi.WorkspaceContext{
		DeclaredProjects: []internalapi.SlugLookup{
			{Slug: slug, Name: slug},
		},
	}
}

func milestoneStrPtr(s string) *string { return &s }

// ---------------------------------------------------------------------------
// 1. Parse round-trip
// ---------------------------------------------------------------------------

func TestMilestone_ParseRoundTrip(t *testing.T) {
	in := `
apiVersion: crewship/v1
kind: Milestone
metadata:
  name: v1.0 launch
  slug: v1-launch
spec:
  project_slug: q2-roadmap
  description: Public 1.0 release
  target_date: "2026-06-15"
  status: planned
`
	var doc MilestoneDocument
	if err := yaml.Unmarshal([]byte(in), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if doc.Metadata.Slug != "v1-launch" {
		t.Errorf("slug roundtrip: got %q", doc.Metadata.Slug)
	}
	if doc.Spec.ProjectSlug != "q2-roadmap" {
		t.Errorf("project_slug roundtrip: got %q", doc.Spec.ProjectSlug)
	}
	if doc.Spec.TargetDate != "2026-06-15" {
		t.Errorf("target_date roundtrip: got %q", doc.Spec.TargetDate)
	}
	if doc.Spec.Status != "planned" {
		t.Errorf("status roundtrip: got %q", doc.Spec.Status)
	}

	// Re-marshal and re-decode — must yield an equivalent value.
	out, err := yaml.Marshal(&doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var doc2 MilestoneDocument
	if err := yaml.Unmarshal(out, &doc2); err != nil {
		t.Fatalf("re-unmarshal: %v", err)
	}
	if !reflect.DeepEqual(doc, doc2) {
		t.Errorf("round-trip mismatch:\n in: %+v\nout: %+v", doc, doc2)
	}
}

// ---------------------------------------------------------------------------
// 2. Validate happy path
// ---------------------------------------------------------------------------

func TestMilestone_Validate_Happy(t *testing.T) {
	doc := milestoneSampleDoc()
	if err := doc.Validate(milestoneCtxWithProject("q2-roadmap")); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// 3. Validate error paths (one assertion per rule)
// ---------------------------------------------------------------------------

func TestMilestone_Validate_Errors(t *testing.T) {
	cases := []struct {
		name        string
		mutate      func(d *MilestoneDocument)
		ctx         internalapi.WorkspaceContext
		wantContain string
	}{
		{
			name:        "missing project_slug",
			mutate:      func(d *MilestoneDocument) { d.Spec.ProjectSlug = "" },
			ctx:         milestoneCtxWithProject("q2-roadmap"),
			wantContain: "project_slug is required",
		},
		{
			name:        "project_slug not in workspace context",
			mutate:      func(d *MilestoneDocument) { d.Spec.ProjectSlug = "ghost-project" },
			ctx:         milestoneCtxWithProject("q2-roadmap"),
			wantContain: "does not reference any declared or remote project",
		},
		{
			name:        "bad status",
			mutate:      func(d *MilestoneDocument) { d.Spec.Status = "in-progress" },
			ctx:         milestoneCtxWithProject("q2-roadmap"),
			wantContain: "status",
		},
		{
			name:        "bad date format",
			mutate:      func(d *MilestoneDocument) { d.Spec.TargetDate = "06/15/2026" },
			ctx:         milestoneCtxWithProject("q2-roadmap"),
			wantContain: "target_date",
		},
		{
			name:        "invalid calendar date",
			mutate:      func(d *MilestoneDocument) { d.Spec.TargetDate = "2026-13-40" },
			ctx:         milestoneCtxWithProject("q2-roadmap"),
			wantContain: "target_date",
		},
		{
			name:        "missing metadata.name",
			mutate:      func(d *MilestoneDocument) { d.Metadata.Name = "" },
			ctx:         milestoneCtxWithProject("q2-roadmap"),
			wantContain: "metadata.name",
		},
		{
			name:        "missing metadata.slug",
			mutate:      func(d *MilestoneDocument) { d.Metadata.Slug = "" },
			ctx:         milestoneCtxWithProject("q2-roadmap"),
			wantContain: "metadata.slug",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := milestoneSampleDoc()
			tc.mutate(doc)
			err := doc.Validate(tc.ctx)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantContain)
			}
			if !strings.Contains(err.Error(), tc.wantContain) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantContain)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 4. Plan: create (no remote)
// ---------------------------------------------------------------------------

func TestMilestone_Plan_Create(t *testing.T) {
	fake := newMilestoneFakeClient()
	fake.projects = []milestoneProjectStub{
		{ID: "proj_abc", Slug: "q2-roadmap", Name: "Q2 Roadmap"},
	}
	doc := milestoneSampleDoc()

	items, err := doc.Plan(context.Background(), fake, nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 plan item, got %d", len(items))
	}
	item := items[0]
	if item.Action != internalapi.ActionCreate {
		t.Errorf("action: got %v want Create", item.Action)
	}
	if item.Slug != "v1-launch" {
		t.Errorf("slug: got %q", item.Slug)
	}

	// Execute and confirm the POST hit the nested path with the right body.
	if err := item.Exec(context.Background(), fake); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	call := fake.findCall("POST", "/api/v1/projects/proj_abc/milestones")
	if call == nil {
		t.Fatalf("expected POST to nested milestones endpoint; got calls: %+v", fake.calls)
	}
	if call.Body["name"] != "v1.0 launch" {
		t.Errorf("body.name: got %v", call.Body["name"])
	}
	if call.Body["target_date"] != "2026-06-15" {
		t.Errorf("body.target_date: got %v", call.Body["target_date"])
	}
	if call.Body["status"] != "planned" {
		t.Errorf("body.status: got %v", call.Body["status"])
	}
	if call.Body["description"] != "Public 1.0 release" {
		t.Errorf("body.description: got %v", call.Body["description"])
	}
	if _, hasProjectID := call.Body["project_id"]; hasProjectID {
		t.Error("create body should NOT carry project_id (parent is path-scoped)")
	}
}

// Plan-create when the parent project does not exist on the server
// should surface a clear error instead of silently creating an
// orphan.
func TestMilestone_Plan_Create_MissingProject(t *testing.T) {
	fake := newMilestoneFakeClient()
	// no projects pre-loaded
	doc := milestoneSampleDoc()

	_, err := doc.Plan(context.Background(), fake, nil)
	if err == nil {
		t.Fatal("expected error when project_slug is unresolvable")
	}
	if !strings.Contains(err.Error(), "q2-roadmap") {
		t.Errorf("error should name the missing project slug: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 5. Plan: update (drifted remote)
// ---------------------------------------------------------------------------

func TestMilestone_Plan_Update_Drifted(t *testing.T) {
	fake := newMilestoneFakeClient()
	fake.projects = []milestoneProjectStub{
		{ID: "proj_abc", Slug: "q2-roadmap", Name: "Q2 Roadmap"},
	}
	doc := milestoneSampleDoc()

	remote := &MilestoneRemote{
		ID:          "mil_001",
		ProjectID:   "proj_abc",
		Name:        "v1.0 launch",                      // matches
		Description: milestoneStrPtr("Old description"), // drifted
		TargetDate:  milestoneStrPtr("2026-06-15"),      // matches
		Status:      "active",                           // drifted from "planned"
	}

	items, err := doc.Plan(context.Background(), fake, remote)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) != 1 || items[0].Action != internalapi.ActionUpdate {
		t.Fatalf("expected single Update item, got %+v", items)
	}

	if err := items[0].Exec(context.Background(), fake); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	call := fake.findCall("PATCH", "/api/v1/milestones/mil_001")
	if call == nil {
		t.Fatalf("expected PATCH to /api/v1/milestones/mil_001; got calls: %+v", fake.calls)
	}

	// Sparse PATCH — only the two drifted fields should be present.
	if _, has := call.Body["name"]; has {
		t.Error("PATCH should NOT include name (matches remote)")
	}
	if _, has := call.Body["target_date"]; has {
		t.Error("PATCH should NOT include target_date (matches remote)")
	}
	if call.Body["description"] != "Public 1.0 release" {
		t.Errorf("PATCH.description: got %v", call.Body["description"])
	}
	if call.Body["status"] != "planned" {
		t.Errorf("PATCH.status: got %v", call.Body["status"])
	}
}

// ---------------------------------------------------------------------------
// 6. Plan: unchanged
// ---------------------------------------------------------------------------

func TestMilestone_Plan_Unchanged(t *testing.T) {
	fake := newMilestoneFakeClient()
	fake.projects = []milestoneProjectStub{
		{ID: "proj_abc", Slug: "q2-roadmap", Name: "Q2 Roadmap"},
	}
	doc := milestoneSampleDoc()

	remote := &MilestoneRemote{
		ID:          "mil_001",
		ProjectID:   "proj_abc",
		Name:        "v1.0 launch",
		Description: milestoneStrPtr("Public 1.0 release"),
		TargetDate:  milestoneStrPtr("2026-06-15"),
		Status:      "planned",
	}

	items, err := doc.Plan(context.Background(), fake, remote)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) != 1 || items[0].Action != internalapi.ActionUnchanged {
		t.Fatalf("expected single Unchanged item, got %+v", items)
	}
	if items[0].Exec != nil {
		t.Error("Unchanged plan item should not carry an Exec closure")
	}
}

// ---------------------------------------------------------------------------
// 7. Plan: lookup helper (project resolution + nested list)
// ---------------------------------------------------------------------------

func TestMilestone_LookupRemote_ResolvesProjectAndMatchesByName(t *testing.T) {
	fake := newMilestoneFakeClient()
	fake.projects = []milestoneProjectStub{
		{ID: "proj_abc", Slug: "q2-roadmap", Name: "Q2 Roadmap"},
		{ID: "proj_def", Slug: "other", Name: "Other"},
	}
	fake.milestonesByProject["proj_abc"] = []MilestoneRemote{
		{ID: "mil_001", ProjectID: "proj_abc", Name: "v1.0 launch", Status: "planned"},
		{ID: "mil_002", ProjectID: "proj_abc", Name: "v2.0 launch", Status: "planned"},
	}
	doc := milestoneSampleDoc()

	remote, err := LookupMilestoneRemote(context.Background(), fake, doc)
	if err != nil {
		t.Fatalf("LookupMilestoneRemote: %v", err)
	}
	if remote == nil {
		t.Fatal("expected to find remote milestone")
	}
	if remote.ID != "mil_001" {
		t.Errorf("got remote id %q want mil_001", remote.ID)
	}

	// LookupMilestoneRemote should have hit BOTH endpoints in order:
	// project list first, then nested milestones.
	if got := fake.findCall("GET", "/api/v1/projects"); got == nil {
		t.Error("expected GET /api/v1/projects to resolve project_slug")
	}
	if got := fake.findCall("GET", "/api/v1/projects/proj_abc/milestones"); got == nil {
		t.Error("expected GET /api/v1/projects/proj_abc/milestones to list nested rows")
	}

	// A miss should return (nil, nil) rather than an error.
	doc.Metadata.Name = "nonexistent milestone"
	miss, err := LookupMilestoneRemote(context.Background(), fake, doc)
	if err != nil {
		t.Fatalf("Lookup miss: %v", err)
	}
	if miss != nil {
		t.Errorf("expected nil for unmatched name, got %+v", miss)
	}
}

// ---------------------------------------------------------------------------
// 8. Export round-trip
// ---------------------------------------------------------------------------

func TestMilestone_Export_RoundTrip(t *testing.T) {
	fake := newMilestoneFakeClient()
	fake.projects = []milestoneProjectStub{
		{ID: "proj_abc", Slug: "q2-roadmap", Name: "Q2 Roadmap"},
		{ID: "proj_def", Slug: "infra-2026", Name: "Infra 2026"},
	}
	fake.milestonesByProject["proj_abc"] = []MilestoneRemote{
		{
			ID:          "mil_001",
			ProjectID:   "proj_abc",
			Name:        "v1.0 launch",
			Description: milestoneStrPtr("Public 1.0 release"),
			TargetDate:  milestoneStrPtr("2026-06-15"),
			Status:      "planned",
		},
	}
	fake.milestonesByProject["proj_def"] = []MilestoneRemote{
		{
			ID:        "mil_002",
			ProjectID: "proj_def",
			Name:      "Network rollout",
			Status:    "active",
		},
	}

	docs, err := ExportMilestones(context.Background(), fake)
	if err != nil {
		t.Fatalf("ExportMilestones: %v", err)
	}
	if len(docs) != 2 {
		t.Fatalf("want 2 docs, got %d", len(docs))
	}

	// Find the v1.0 launch doc.
	var v1 *MilestoneDocument
	for _, d := range docs {
		if d.Metadata.Name == "v1.0 launch" {
			v1 = d
			break
		}
	}
	if v1 == nil {
		t.Fatal("expected v1.0 launch in exported docs")
	}
	if v1.APIVersion != "crewship/v1" {
		t.Errorf("apiVersion: got %q", v1.APIVersion)
	}
	if v1.Kind != "Milestone" {
		t.Errorf("kind: got %q", v1.Kind)
	}
	if v1.Spec.ProjectSlug != "q2-roadmap" {
		t.Errorf("project_slug should round-trip to q2-roadmap, got %q", v1.Spec.ProjectSlug)
	}
	if v1.Spec.Description != "Public 1.0 release" {
		t.Errorf("description: got %q", v1.Spec.Description)
	}
	if v1.Spec.TargetDate != "2026-06-15" {
		t.Errorf("target_date: got %q", v1.Spec.TargetDate)
	}
	if v1.Spec.Status != "planned" {
		t.Errorf("status: got %q", v1.Spec.Status)
	}
	// Synthetic slug should be kebab-cased from name.
	if v1.Metadata.Slug != "v1-0-launch" {
		t.Errorf("synthetic slug should be kebab-cased from name; got %q", v1.Metadata.Slug)
	}

	// And the exported doc should immediately re-validate against a
	// workspaceCtx that knows the parent project.
	ctx := internalapi.WorkspaceContext{
		RemoteProjects: []internalapi.SlugLookup{
			{Slug: "q2-roadmap"}, {Slug: "infra-2026"},
		},
	}
	if err := v1.Validate(ctx); err != nil {
		t.Errorf("exported doc failed re-validate: %v", err)
	}
}

// ---------------------------------------------------------------------------
// extra helper coverage (kebab slug edges + checkStatus body bubbling)
// ---------------------------------------------------------------------------

func TestMilestone_HelperEdges(t *testing.T) {
	if got := milestoneSlugFromName("  Hello World!  "); got != "hello-world" {
		t.Errorf("slug from name: got %q", got)
	}
	if got := milestoneSlugFromName(""); got != "milestone" {
		t.Errorf("empty name should fall back to %q, got %q", "milestone", got)
	}
	if got := milestoneDerefOrEmpty(nil); got != "" {
		t.Errorf("derefOrEmpty(nil): got %q", got)
	}
	v := "ok"
	if got := milestoneDerefOrEmpty(&v); got != "ok" {
		t.Errorf("derefOrEmpty(&v): got %q", got)
	}

	// milestoneCheckStatus surfaces non-2xx with payload visible in
	// the wrapped message so a failing Apply prints the server's
	// reason without an extra log dive.
	r := &internalapi.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader("boom"))}
	err := milestoneCheckStatus(r, "op")
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("checkStatus should include body: got %v", err)
	}
}
