package kinds

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/manifest/internalapi"
	"gopkg.in/yaml.v3"
)

// ── Test client ────────────────────────────────────────────────────────────

// savedViewFakeClient is the minimal in-memory internalapi.Client stub used by
// the SavedView plan/export tests. Mirrors the pattern in
// internal/manifest/apply_test.go but lives here so the kinds package
// can exercise its public surface without importing the parent.
//
// Each test populates GetResponses with canned responses keyed by
// path; mutating calls (Post/Patch/Delete) are recorded into Calls so
// assertions can verify URL + body shape.
type savedViewFakeClient struct {
	wsID         string
	GetResponses map[string]string
	Calls        []savedViewFakeCall
}

type savedViewFakeCall struct {
	Method string
	Path   string
	Body   any
}

func newSavedViewFakeClient() *savedViewFakeClient {
	return &savedViewFakeClient{
		wsID:         "ws_test",
		GetResponses: map[string]string{},
	}
}

func (f *savedViewFakeClient) WorkspaceID() string { return f.wsID }

func (f *savedViewFakeClient) record(method, path string, body any) {
	f.Calls = append(f.Calls, savedViewFakeCall{Method: method, Path: path, Body: body})
}

func (f *savedViewFakeClient) respond(status int, body string) *internalapi.Response {
	return &internalapi.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func (f *savedViewFakeClient) Get(_ context.Context, path string) (*internalapi.Response, error) {
	f.record("GET", path, nil)
	if body, ok := f.GetResponses[path]; ok {
		return f.respond(200, body), nil
	}
	return f.respond(404, `{"error":"not found"}`), nil
}

func (f *savedViewFakeClient) Post(_ context.Context, path string, body any) (*internalapi.Response, error) {
	f.record("POST", path, body)
	return f.respond(201, `{"id":"sv_new"}`), nil
}

func (f *savedViewFakeClient) Patch(_ context.Context, path string, body any) (*internalapi.Response, error) {
	f.record("PATCH", path, body)
	return f.respond(200, `{}`), nil
}

func (f *savedViewFakeClient) Put(_ context.Context, path string, body any) (*internalapi.Response, error) {
	f.record("PUT", path, body)
	return f.respond(200, `{}`), nil
}

func (f *savedViewFakeClient) Delete(_ context.Context, path string) (*internalapi.Response, error) {
	f.record("DELETE", path, nil)
	return f.respond(204, ``), nil
}

// ── Fixtures ───────────────────────────────────────────────────────────────

// savedViewFixtureCtx returns a WorkspaceContext seeded with the labels and
// projects the test fixtures reference. Centralised so adding a new
// FK in one test doesn't require touching every other.
func savedViewFixtureCtx() internalapi.WorkspaceContext {
	return internalapi.WorkspaceContext{
		DeclaredLabels: []internalapi.SlugLookup{
			{Slug: "bug", Name: "Bug"},
			{Slug: "regression", Name: "Regression"},
		},
		DeclaredProjects: []internalapi.SlugLookup{
			{Slug: "q2-roadmap", Name: "Q2 Roadmap"},
		},
		DeclaredAgents: []internalapi.SlugLookup{
			{Slug: "alice", Name: "Alice"},
		},
	}
}

// savedViewFixtureDoc returns a well-formed SavedViewDocument for happy-path
// tests; individual tests deep-copy and mutate as needed.
func savedViewFixtureDoc() SavedViewDocument {
	return SavedViewDocument{
		APIVersion: "crewship/v1",
		Kind:       "SavedView",
		Metadata: internalapi.Metadata{
			Name: "My open bugs",
			Slug: "my-open-bugs",
		},
		Spec: SavedViewSpec{
			Shared:     false,
			EntityType: "issue",
			Filter: SavedViewFilter{
				Status:            []string{"todo", "in_progress"},
				LabelSlugs:        []string{"bug"},
				AssigneeAgentSlug: "",
				ProjectSlug:       "",
			},
			Sort: SavedViewSort{
				Field:     "created_at",
				Direction: "desc",
			},
		},
	}
}

// ── 1. Parse round-trip ────────────────────────────────────────────────────

func TestSavedView_ParseRoundTrip(t *testing.T) {
	src := []byte(`apiVersion: crewship/v1
kind: SavedView
metadata:
  name: My open bugs
  slug: my-open-bugs
spec:
  shared: false
  entity_type: issue
  filter:
    status: [todo, in_progress]
    label_slugs: [bug]
    assignee_agent_slug: ""
    project_slug: ""
  sort:
    field: created_at
    direction: desc
`)
	var doc SavedViewDocument
	if err := yaml.Unmarshal(src, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if doc.Kind != "SavedView" {
		t.Errorf("kind = %q, want SavedView", doc.Kind)
	}
	if doc.Metadata.Slug != "my-open-bugs" {
		t.Errorf("slug = %q", doc.Metadata.Slug)
	}
	if doc.Spec.EntityType != "issue" {
		t.Errorf("entity_type = %q", doc.Spec.EntityType)
	}
	if doc.Spec.Sort.Direction != "desc" {
		t.Errorf("direction = %q", doc.Spec.Sort.Direction)
	}
	if len(doc.Spec.Filter.LabelSlugs) != 1 || doc.Spec.Filter.LabelSlugs[0] != "bug" {
		t.Errorf("label_slugs = %v", doc.Spec.Filter.LabelSlugs)
	}

	// Round-trip: re-emit and re-parse, must match the in-memory form.
	out, err := yaml.Marshal(&doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var doc2 SavedViewDocument
	if err := yaml.Unmarshal(out, &doc2); err != nil {
		t.Fatalf("re-unmarshal: %v", err)
	}
	if doc2.Spec.EntityType != doc.Spec.EntityType ||
		doc2.Metadata.Slug != doc.Metadata.Slug ||
		doc2.Spec.Sort.Field != doc.Spec.Sort.Field {
		t.Errorf("round-trip drift:\nbefore=%+v\nafter=%+v", doc, doc2)
	}
}

// ── 2. Validate happy path ─────────────────────────────────────────────────

func TestSavedView_Validate_HappyPath(t *testing.T) {
	doc := savedViewFixtureDoc()
	if err := doc.Validate(savedViewFixtureCtx()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSavedView_Validate_MissionAndRunEntityTypes(t *testing.T) {
	for _, et := range []string{"mission", "run"} {
		t.Run(et, func(t *testing.T) {
			doc := savedViewFixtureDoc()
			doc.Spec.EntityType = et
			doc.Spec.Filter.LabelSlugs = nil // labels are issue-only
			if err := doc.Validate(savedViewFixtureCtx()); err != nil {
				t.Errorf("entity_type=%q rejected: %v", et, err)
			}
		})
	}
}

// ── 3. Validate error paths ────────────────────────────────────────────────

func TestSavedView_Validate_BadEntityType(t *testing.T) {
	doc := savedViewFixtureDoc()
	doc.Spec.EntityType = "task"
	err := doc.Validate(savedViewFixtureCtx())
	if err == nil || !strings.Contains(err.Error(), "entity_type") {
		t.Fatalf("want entity_type error, got %v", err)
	}
}

func TestSavedView_Validate_BadSortDirection(t *testing.T) {
	doc := savedViewFixtureDoc()
	doc.Spec.Sort.Direction = "sideways"
	err := doc.Validate(savedViewFixtureCtx())
	if err == nil || !strings.Contains(err.Error(), "direction") {
		t.Fatalf("want direction error, got %v", err)
	}
}

func TestSavedView_Validate_MissingSortField(t *testing.T) {
	doc := savedViewFixtureDoc()
	doc.Spec.Sort.Field = ""
	err := doc.Validate(savedViewFixtureCtx())
	if err == nil || !strings.Contains(err.Error(), "sort.field") {
		t.Fatalf("want sort.field error, got %v", err)
	}
}

func TestSavedView_Validate_UnknownLabelSlug(t *testing.T) {
	doc := savedViewFixtureDoc()
	doc.Spec.Filter.LabelSlugs = []string{"bug", "does-not-exist"}
	err := doc.Validate(savedViewFixtureCtx())
	if err == nil {
		t.Fatal("expected validation error for unknown label slug")
	}
	if !strings.Contains(err.Error(), "does-not-exist") {
		t.Errorf("error should name the missing slug, got: %v", err)
	}
	if !strings.Contains(err.Error(), "label") {
		t.Errorf("error should mention label, got: %v", err)
	}
}

func TestSavedView_Validate_UnknownProjectSlug(t *testing.T) {
	doc := savedViewFixtureDoc()
	doc.Spec.Filter.ProjectSlug = "ghost-project"
	err := doc.Validate(savedViewFixtureCtx())
	if err == nil || !strings.Contains(err.Error(), "ghost-project") {
		t.Fatalf("want project error naming ghost-project, got %v", err)
	}
}

func TestSavedView_Validate_MissingSlug(t *testing.T) {
	doc := savedViewFixtureDoc()
	doc.Metadata.Slug = ""
	if err := doc.Validate(savedViewFixtureCtx()); err == nil {
		t.Fatal("expected slug-required error")
	}
}

// ── 4. Plan: create (no remote) ────────────────────────────────────────────

func TestSavedView_Plan_Create(t *testing.T) {
	doc := savedViewFixtureDoc()
	fc := newSavedViewFakeClient()
	items, err := doc.Plan(context.Background(), fc, nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("want 1 plan item, got %d", len(items))
	}
	if items[0].Action != internalapi.ActionCreate {
		t.Errorf("action = %v, want Create", items[0].Action)
	}
	if items[0].Slug != "my-open-bugs" {
		t.Errorf("slug = %q", items[0].Slug)
	}

	// Exec the closure and assert the POST body shape.
	if err := items[0].Exec(context.Background(), fc); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	var posted savedViewFakeCall
	for _, c := range fc.Calls {
		if c.Method == "POST" {
			posted = c
			break
		}
	}
	if posted.Method == "" {
		t.Fatal("expected a POST call")
	}
	if posted.Path != "/api/v1/saved-views" {
		t.Errorf("path = %q", posted.Path)
	}
	body, ok := posted.Body.(map[string]any)
	if !ok {
		t.Fatalf("body type = %T, want map", posted.Body)
	}
	if body["name"] != "My open bugs" {
		t.Errorf("body.name = %v", body["name"])
	}
	if body["entity_type"] != "issue" {
		t.Errorf("body.entity_type = %v", body["entity_type"])
	}
	if body["shared"] != false {
		t.Errorf("body.shared = %v", body["shared"])
	}
	// filter_json/sort_json are JSON strings — make sure they round-trip.
	var gotFilter SavedViewFilter
	if err := json.Unmarshal([]byte(body["filter_json"].(string)), &gotFilter); err != nil {
		t.Fatalf("filter_json not valid JSON: %v", err)
	}
	if len(gotFilter.LabelSlugs) != 1 || gotFilter.LabelSlugs[0] != "bug" {
		t.Errorf("filter_json.label_slugs = %v", gotFilter.LabelSlugs)
	}
	var gotSort SavedViewSort
	if err := json.Unmarshal([]byte(body["sort_json"].(string)), &gotSort); err != nil {
		t.Fatalf("sort_json not valid JSON: %v", err)
	}
	if gotSort.Field != "created_at" || gotSort.Direction != "desc" {
		t.Errorf("sort_json = %+v", gotSort)
	}
}

// ── 5. Plan: update (drifted remote) ───────────────────────────────────────

func TestSavedView_Plan_Update(t *testing.T) {
	doc := savedViewFixtureDoc()
	doc.Spec.Sort.Direction = "asc" // declared wants asc

	filterJSON, _ := json.Marshal(SavedViewFilter{LabelSlugs: []string{"bug"}, Status: []string{"todo", "in_progress"}})
	sortJSON, _ := json.Marshal(SavedViewSort{Field: "created_at", Direction: "desc"}) // remote has desc
	sortStr := string(sortJSON)
	remote := &SavedViewRemote{
		ID:         "sv_42",
		Name:       "My open bugs",
		Shared:     false,
		EntityType: "issue",
		FilterJSON: string(filterJSON),
		SortJSON:   &sortStr,
	}

	fc := newSavedViewFakeClient()
	items, err := doc.Plan(context.Background(), fc, remote)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) != 1 || items[0].Action != internalapi.ActionUpdate {
		t.Fatalf("want 1 Update item, got %+v", items)
	}

	if err := items[0].Exec(context.Background(), fc); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	var patched savedViewFakeCall
	for _, c := range fc.Calls {
		if c.Method == "PATCH" {
			patched = c
		}
	}
	if patched.Path != "/api/v1/saved-views/sv_42" {
		t.Errorf("patch path = %q", patched.Path)
	}
	body, ok := patched.Body.(map[string]any)
	if !ok {
		t.Fatalf("patch body type = %T", patched.Body)
	}
	if !strings.Contains(body["sort_json"].(string), `"direction":"asc"`) {
		t.Errorf("patch should send the new direction, got sort_json=%v", body["sort_json"])
	}
}

// ── 6. Plan: unchanged ─────────────────────────────────────────────────────

func TestSavedView_Plan_Unchanged(t *testing.T) {
	doc := savedViewFixtureDoc()
	postBody, err := doc.toPostBody()
	if err != nil {
		t.Fatalf("toPostBody: %v", err)
	}
	sortStr := postBody["sort_json"].(string)
	remote := &SavedViewRemote{
		ID:         "sv_42",
		Name:       doc.Metadata.Name,
		Shared:     doc.Spec.Shared,
		EntityType: doc.Spec.EntityType,
		FilterJSON: postBody["filter_json"].(string),
		SortJSON:   &sortStr,
	}

	fc := newSavedViewFakeClient()
	items, err := doc.Plan(context.Background(), fc, remote)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) != 1 || items[0].Action != internalapi.ActionUnchanged {
		t.Fatalf("want Unchanged, got %+v", items)
	}
	if items[0].Exec != nil {
		t.Error("Unchanged plan items should have nil Exec")
	}
}

// Label-order should not cause spurious drift — sort the slices before
// comparing or callers will get update-storms on every reapply.
func TestSavedView_Plan_LabelOrderInsensitive(t *testing.T) {
	doc := savedViewFixtureDoc()
	doc.Spec.Filter.LabelSlugs = []string{"bug", "regression"}

	filterJSON, _ := json.Marshal(SavedViewFilter{
		// Remote has the same labels but in reverse order.
		LabelSlugs: []string{"regression", "bug"},
		Status:     doc.Spec.Filter.Status,
	})
	sortJSON, _ := json.Marshal(doc.Spec.Sort)
	sortStr := string(sortJSON)
	remote := &SavedViewRemote{
		ID:         "sv_1",
		Name:       doc.Metadata.Name,
		Shared:     doc.Spec.Shared,
		EntityType: doc.Spec.EntityType,
		FilterJSON: string(filterJSON),
		SortJSON:   &sortStr,
	}
	items, err := doc.Plan(context.Background(), newSavedViewFakeClient(), remote)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if items[0].Action != internalapi.ActionUnchanged {
		t.Errorf("reordered labels should be unchanged, got %v", items[0].Action)
	}
}

// ── 7. Plan: delete-then-create on Replace ─────────────────────────────────

// Replace semantics are owned by the top-level apply pipeline, not the
// kind itself — the kind emits the same Create item, and apply.go
// prepends a Delete in Replace mode. This test asserts the building
// blocks an apply pipeline needs: (a) Plan(nil) emits Create, and
// (b) given an existing remote, the caller can fabricate a Delete
// PlanItem via the same client surface the kind uses. Documents the
// contract Apply relies on.
func TestSavedView_Plan_ReplaceContract(t *testing.T) {
	doc := savedViewFixtureDoc()

	// (a) Create against empty remote.
	items, err := doc.Plan(context.Background(), newSavedViewFakeClient(), nil)
	if err != nil || len(items) != 1 || items[0].Action != internalapi.ActionCreate {
		t.Fatalf("want Create from nil remote, got items=%+v err=%v", items, err)
	}

	// (b) Verify the Update path produces a usable PATCH closure when
	// the remote does exist (Replace mode pre-pends a Delete and falls
	// through to Create; Upsert lands on Update — both are wired in
	// apply.go but rely on the kind exposing the right closures here).
	filterJSON, _ := json.Marshal(doc.Spec.Filter)
	sortJSON, _ := json.Marshal(doc.Spec.Sort)
	sortStr := string(sortJSON)
	driftedRemote := &SavedViewRemote{
		ID:         "sv_42",
		Name:       "OLD NAME — drifted",
		Shared:     doc.Spec.Shared,
		EntityType: doc.Spec.EntityType,
		FilterJSON: string(filterJSON),
		SortJSON:   &sortStr,
	}
	items, err = doc.Plan(context.Background(), newSavedViewFakeClient(), driftedRemote)
	if err != nil {
		t.Fatalf("Plan with drifted remote: %v", err)
	}
	if items[0].Action != internalapi.ActionUpdate {
		t.Errorf("drifted name should produce Update, got %v", items[0].Action)
	}
	if items[0].Exec == nil {
		t.Error("Update item must have non-nil Exec for apply pipeline")
	}
}

// ── 8. Export round-trip ───────────────────────────────────────────────────

func TestSavedView_Export_RoundTrip(t *testing.T) {
	original := savedViewFixtureDoc()
	postBody, _ := original.toPostBody()
	sortStr := postBody["sort_json"].(string)

	rows := []SavedViewRemote{{
		ID:         "sv_42",
		Name:       original.Metadata.Name,
		Shared:     original.Spec.Shared,
		EntityType: original.Spec.EntityType,
		FilterJSON: postBody["filter_json"].(string),
		SortJSON:   &sortStr,
	}}
	body, _ := json.Marshal(rows)

	fc := newSavedViewFakeClient()
	fc.GetResponses["/api/v1/saved-views"] = string(body)

	docs, err := ExportSavedViews(context.Background(), fc)
	if err != nil {
		t.Fatalf("ExportSavedViews: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("want 1 doc, got %d", len(docs))
	}
	got := docs[0]
	if got.Kind != "SavedView" {
		t.Errorf("kind = %q", got.Kind)
	}
	if got.Spec.EntityType != original.Spec.EntityType {
		t.Errorf("entity_type drift: got=%q want=%q", got.Spec.EntityType, original.Spec.EntityType)
	}
	if got.Spec.Sort.Field != original.Spec.Sort.Field ||
		got.Spec.Sort.Direction != original.Spec.Sort.Direction {
		t.Errorf("sort drift: got=%+v want=%+v", got.Spec.Sort, original.Spec.Sort)
	}
	if !savedViewStringSliceEqualSorted(got.Spec.Filter.LabelSlugs, original.Spec.Filter.LabelSlugs) {
		t.Errorf("label_slugs drift: got=%v want=%v",
			got.Spec.Filter.LabelSlugs, original.Spec.Filter.LabelSlugs)
	}
	if got.Metadata.Name != original.Metadata.Name {
		t.Errorf("name drift: got=%q want=%q", got.Metadata.Name, original.Metadata.Name)
	}

	// Re-marshal the exported doc to YAML and assert the slug was
	// derived (server has no slug column, so Export computes it).
	out, err := yaml.Marshal(got)
	if err != nil {
		t.Fatalf("yaml marshal: %v", err)
	}
	if !bytes.Contains(out, []byte("slug: my-open-bugs")) {
		t.Errorf("export YAML missing derived slug:\n%s", out)
	}
}

func TestSavedView_Export_EmptyList(t *testing.T) {
	fc := newSavedViewFakeClient()
	fc.GetResponses["/api/v1/saved-views"] = `[]`
	docs, err := ExportSavedViews(context.Background(), fc)
	if err != nil {
		t.Fatalf("ExportSavedViews: %v", err)
	}
	if len(docs) != 0 {
		t.Errorf("want 0 docs, got %d", len(docs))
	}
}

func TestSavedView_Export_WrappedShape(t *testing.T) {
	rows := []SavedViewRemote{{
		ID:         "sv_1",
		Name:       "Wrapped",
		EntityType: "issue",
		FilterJSON: `{}`,
		SortJSON:   savedViewPtrString(`{"field":"created_at","direction":"desc"}`),
	}}
	wrapped := map[string]any{"saved_views": rows}
	body, _ := json.Marshal(wrapped)

	fc := newSavedViewFakeClient()
	fc.GetResponses["/api/v1/saved-views"] = string(body)
	docs, err := ExportSavedViews(context.Background(), fc)
	if err != nil {
		t.Fatalf("ExportSavedViews: %v", err)
	}
	if len(docs) != 1 || docs[0].Metadata.Name != "Wrapped" {
		t.Errorf("wrapped decode failed: %+v", docs)
	}
}

func savedViewPtrString(s string) *string { return &s }
