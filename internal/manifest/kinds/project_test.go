package kinds

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/manifest/internalapi"
	"gopkg.in/yaml.v3"
)

// ── Test helpers ─────────────────────────────────────────────────────────
//
// Names are project-prefixed (projectHTTPClient, projectRecordedCall,
// projectValidDoc, …) because the kinds package is shared by every
// per-kind file in this directory and parallel agents are dropping
// neighbour _test.go files. Prefixing keeps this file's symbols
// non-colliding regardless of what label_test.go etc. choose to call
// their own helpers.

// projectHTTPClient is a tiny internalapi.Client implementation backed
// by an httptest.Server. It exists because the kinds package can't
// import internal/manifest's production *Client (cycle) and because
// the production client buries the HTTP plumbing under a layer of
// cli.Client adaptation that's overkill for testing a single kind.
//
// Every call is recorded into Calls so tests can assert path, method,
// and body shape without wiring up channel-based mocks.
type projectHTTPClient struct {
	base  *url.URL
	wsID  string
	Calls []projectRecordedCall
}

type projectRecordedCall struct {
	Method string
	Path   string
	Body   map[string]any
}

func newProjectHTTPClient(server *httptest.Server) *projectHTTPClient {
	u, _ := url.Parse(server.URL)
	return &projectHTTPClient{base: u, wsID: "ws_test"}
}

func (h *projectHTTPClient) WorkspaceID() string { return h.wsID }

func (h *projectHTTPClient) do(ctx context.Context, method, path string, body any) (*internalapi.Response, error) {
	var bodyMap map[string]any
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		_ = json.Unmarshal(raw, &bodyMap)
		reader = bytes.NewReader(raw)
	}
	h.Calls = append(h.Calls, projectRecordedCall{Method: method, Path: path, Body: bodyMap})

	req, err := http.NewRequestWithContext(ctx, method, h.base.String()+path, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	buf, _ := io.ReadAll(resp.Body)
	return &internalapi.Response{
		StatusCode: resp.StatusCode,
		Body:       bytes.NewReader(buf),
	}, nil
}

func (h *projectHTTPClient) Get(ctx context.Context, path string) (*internalapi.Response, error) {
	return h.do(ctx, "GET", path, nil)
}
func (h *projectHTTPClient) Post(ctx context.Context, path string, body any) (*internalapi.Response, error) {
	return h.do(ctx, "POST", path, body)
}
func (h *projectHTTPClient) Patch(ctx context.Context, path string, body any) (*internalapi.Response, error) {
	return h.do(ctx, "PATCH", path, body)
}
func (h *projectHTTPClient) Put(ctx context.Context, path string, body any) (*internalapi.Response, error) {
	return h.do(ctx, "PUT", path, body)
}
func (h *projectHTTPClient) Delete(ctx context.Context, path string) (*internalapi.Response, error) {
	return h.do(ctx, "DELETE", path, nil)
}

// projectFakeServer returns an httptest.Server that responds to the
// endpoints ExportProjects + Plan touch (`/projects` and `/agents`).
// Pass nil for either slice to model an empty workspace.
func projectFakeServer(t *testing.T, projects []map[string]any, agents []map[string]any) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/projects", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if projects == nil {
			projects = []map[string]any{}
		}
		_ = json.NewEncoder(w).Encode(projects)
	})
	mux.HandleFunc("/api/v1/agents", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if agents == nil {
			agents = []map[string]any{}
		}
		_ = json.NewEncoder(w).Encode(agents)
	})
	return httptest.NewServer(mux)
}

// projectValidDoc returns a ProjectDocument that passes every Validate
// rule and exercises all spec fields. Tests mutate one field at a
// time to assert the matching rule fires.
func projectValidDoc() ProjectDocument {
	return ProjectDocument{
		APIVersion: "crewship/v1",
		Kind:       "Project",
		Metadata: internalapi.Metadata{
			Name:        "Q2 Roadmap",
			Slug:        "q2-roadmap",
			Description: "All Q2 deliverables",
		},
		Spec: ProjectSpec{
			Color:         "#3B82F6",
			Status:        "planned",
			Priority:      "medium",
			Health:        "on_track",
			TargetDate:    "2026-06-30",
			LeadAgentSlug: "pepa",
		},
	}
}

// projectValidCtx returns a WorkspaceContext that satisfies
// projectValidDoc's lead_agent_slug FK. Other tests redeclare or
// empty this to make the FK fail.
func projectValidCtx() internalapi.WorkspaceContext {
	return internalapi.WorkspaceContext{
		DeclaredAgents: []internalapi.SlugLookup{{Slug: "pepa", Name: "Pepa"}},
	}
}

// ── 1. Parse round-trip ──────────────────────────────────────────────────

func TestProject_ParseRoundTrip(t *testing.T) {
	const source = `apiVersion: crewship/v1
kind: Project
metadata:
  name: Q2 Roadmap
  slug: q2-roadmap
  description: All Q2 deliverables
spec:
  color: "#3B82F6"
  status: planned
  priority: medium
  health: on_track
  target_date: "2026-06-30"
  lead_agent_slug: pepa
`
	var doc ProjectDocument
	if err := yaml.Unmarshal([]byte(source), &doc); err != nil {
		t.Fatalf("yaml unmarshal: %v", err)
	}
	if doc.Metadata.Slug != "q2-roadmap" {
		t.Errorf("slug=%q want q2-roadmap", doc.Metadata.Slug)
	}
	if doc.Spec.Color != "#3B82F6" {
		t.Errorf("color=%q want #3B82F6", doc.Spec.Color)
	}
	if doc.Spec.LeadAgentSlug != "pepa" {
		t.Errorf("lead=%q want pepa", doc.Spec.LeadAgentSlug)
	}

	out, err := yaml.Marshal(&doc)
	if err != nil {
		t.Fatalf("yaml marshal: %v", err)
	}

	var roundTripped ProjectDocument
	if err := yaml.Unmarshal(out, &roundTripped); err != nil {
		t.Fatalf("yaml unmarshal (round-trip): %v", err)
	}
	// ProjectDocument contains a map via Metadata.Labels, so direct
	// equality won't compile. Compare the fields the spec round-trips.
	if roundTripped.APIVersion != doc.APIVersion ||
		roundTripped.Kind != doc.Kind ||
		roundTripped.Metadata.Name != doc.Metadata.Name ||
		roundTripped.Metadata.Slug != doc.Metadata.Slug ||
		roundTripped.Metadata.Description != doc.Metadata.Description ||
		roundTripped.Spec != doc.Spec {
		t.Errorf("round-trip mismatch:\n want %+v\n got  %+v", doc, roundTripped)
	}
}

// ── 2. Validate happy path ───────────────────────────────────────────────

func TestProject_Validate_HappyPath(t *testing.T) {
	doc := projectValidDoc()
	if err := doc.Validate(projectValidCtx()); err != nil {
		t.Fatalf("Validate happy path: %v", err)
	}
}

func TestProject_Validate_AllOptionalFieldsEmpty(t *testing.T) {
	doc := ProjectDocument{
		APIVersion: "crewship/v1",
		Kind:       "Project",
		Metadata:   internalapi.Metadata{Name: "Min", Slug: "min"},
		Spec:       ProjectSpec{},
	}
	if err := doc.Validate(internalapi.WorkspaceContext{}); err != nil {
		t.Fatalf("minimal doc should validate: %v", err)
	}
}

// ── 3. Validate error paths ──────────────────────────────────────────────

func TestProject_Validate_BadAPIVersion(t *testing.T) {
	d := projectValidDoc()
	d.APIVersion = "crewship/v2"
	projectMustValidateError(t, d, projectValidCtx(), "unsupported apiVersion")
}

func TestProject_Validate_BadKind(t *testing.T) {
	d := projectValidDoc()
	d.Kind = "Label"
	projectMustValidateError(t, d, projectValidCtx(), "kind must be")
}

func TestProject_Validate_MissingName(t *testing.T) {
	d := projectValidDoc()
	d.Metadata.Name = ""
	projectMustValidateError(t, d, projectValidCtx(), "metadata.name is required")
}

func TestProject_Validate_MissingSlug(t *testing.T) {
	d := projectValidDoc()
	d.Metadata.Slug = ""
	projectMustValidateError(t, d, projectValidCtx(), "metadata.slug is required")
}

func TestProject_Validate_BadStatus(t *testing.T) {
	d := projectValidDoc()
	d.Spec.Status = "wat"
	projectMustValidateError(t, d, projectValidCtx(), "invalid status")
}

func TestProject_Validate_BadPriority(t *testing.T) {
	d := projectValidDoc()
	d.Spec.Priority = "extremely"
	projectMustValidateError(t, d, projectValidCtx(), "invalid priority")
}

func TestProject_Validate_BadHealth(t *testing.T) {
	d := projectValidDoc()
	d.Spec.Health = "fine"
	projectMustValidateError(t, d, projectValidCtx(), "invalid health")
}

func TestProject_Validate_BadColor(t *testing.T) {
	d := projectValidDoc()
	d.Spec.Color = "blue"
	projectMustValidateError(t, d, projectValidCtx(), "invalid color")
}

func TestProject_Validate_BadTargetDate(t *testing.T) {
	d := projectValidDoc()
	d.Spec.TargetDate = "tomorrow"
	projectMustValidateError(t, d, projectValidCtx(), "invalid target_date")
}

func TestProject_Validate_LeadAgentMissing(t *testing.T) {
	d := projectValidDoc()
	d.Spec.LeadAgentSlug = "ghost"
	projectMustValidateError(t, d, projectValidCtx(), "lead_agent_slug")
}

func TestProject_Validate_LeadAgentRemoteOnly(t *testing.T) {
	// FK lookup unions declared + remote; remote-only agents are
	// valid lead references.
	d := projectValidDoc()
	d.Spec.LeadAgentSlug = "remote-agent"
	ctx := internalapi.WorkspaceContext{
		RemoteAgents: []internalapi.SlugLookup{{Slug: "remote-agent"}},
	}
	if err := d.Validate(ctx); err != nil {
		t.Errorf("remote-only lead should validate: %v", err)
	}
}

// projectMustValidateError fails the test unless Validate returns an
// error containing `substr`. Keeps each error-path test to a single
// line.
func projectMustValidateError(t *testing.T, d ProjectDocument, ctx internalapi.WorkspaceContext, substr string) {
	t.Helper()
	err := d.Validate(ctx)
	if err == nil {
		t.Fatalf("want validate error containing %q, got nil", substr)
	}
	if !strings.Contains(err.Error(), substr) {
		t.Fatalf("want error containing %q, got %q", substr, err.Error())
	}
}

// ── 4. Plan: create ──────────────────────────────────────────────────────

func TestProject_Plan_Create(t *testing.T) {
	doc := projectValidDoc()

	srv := projectFakeServer(t,
		nil,
		[]map[string]any{{"id": "agent_001", "slug": "pepa", "name": "Pepa"}},
	)
	defer srv.Close()
	c := newProjectHTTPClient(srv)

	items, err := doc.Plan(context.Background(), c, nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) != 1 || items[0].Action != internalapi.ActionCreate {
		t.Fatalf("want one Create item, got %+v", items)
	}

	// Execute and inspect the POST body.
	if err := items[0].Exec(context.Background(), c); err != nil {
		// projectFakeServer's POST /projects route isn't defined; we
		// expect a non-2xx — but Exec wraps it as an error. Test
		// instead that the request was actually made + carried the
		// right body.
		_ = err
	}

	var post *projectRecordedCall
	for i, call := range c.Calls {
		if call.Method == "POST" && call.Path == "/api/v1/projects" {
			post = &c.Calls[i]
			break
		}
	}
	if post == nil {
		t.Fatalf("expected POST /api/v1/projects, got %v", c.Calls)
	}
	if got, _ := post.Body["name"].(string); got != "Q2 Roadmap" {
		t.Errorf("POST.name=%q want Q2 Roadmap", got)
	}
	if got, _ := post.Body["slug"].(string); got != "q2-roadmap" {
		t.Errorf("POST.slug=%q want q2-roadmap", got)
	}
	if got, _ := post.Body["color"].(string); got != "#3B82F6" {
		t.Errorf("POST.color=%q want #3B82F6", got)
	}
	if got, _ := post.Body["status"].(string); got != "planned" {
		t.Errorf("POST.status=%q want planned", got)
	}
	if got, _ := post.Body["lead_id"].(string); got != "agent_001" {
		t.Errorf("POST.lead_id=%q want agent_001", got)
	}
	if got, _ := post.Body["lead_type"].(string); got != "agent" {
		t.Errorf("POST.lead_type=%q want agent", got)
	}
}

func TestProject_Plan_Create_LeadSlugUnresolved(t *testing.T) {
	doc := projectValidDoc()
	doc.Spec.LeadAgentSlug = "ghost"

	srv := projectFakeServer(t, nil, []map[string]any{{"id": "agent_001", "slug": "pepa"}})
	defer srv.Close()
	c := newProjectHTTPClient(srv)

	_, err := doc.Plan(context.Background(), c, nil)
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("want unresolved-agent error, got %v", err)
	}
}

// ── 5. Plan: update (drifted remote) ─────────────────────────────────────

func TestProject_Plan_Update_OnlyDriftedFields(t *testing.T) {
	doc := projectValidDoc()
	doc.Spec.LeadAgentSlug = "" // strip lead to keep the patch focused

	srv := projectFakeServer(t, nil, nil)
	defer srv.Close()
	c := newProjectHTTPClient(srv)

	remote := &ProjectRemote{
		ID:       "proj_001",
		Slug:     "q2-roadmap",
		Name:     "Q2 Roadmap",
		Color:    "#000000", // drifted
		Status:   "planned",
		Priority: "medium",
		Health:   "on_track",
		// Description on remote is missing; manifest has one → patch.
		TargetDate: "2026-06-30",
	}

	items, err := doc.Plan(context.Background(), c, remote)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) != 1 || items[0].Action != internalapi.ActionUpdate {
		t.Fatalf("want one Update item, got %+v", items)
	}

	// Trigger Exec; remote `/projects/proj_001` PATCH is unhandled
	// in the fake server, but the projectRecordedCall tells us the body.
	_ = items[0].Exec(context.Background(), c)

	var patch *projectRecordedCall
	for i, call := range c.Calls {
		if call.Method == "PATCH" && call.Path == "/api/v1/projects/proj_001" {
			patch = &c.Calls[i]
			break
		}
	}
	if patch == nil {
		t.Fatalf("expected PATCH /projects/proj_001, got %v", c.Calls)
	}
	if _, ok := patch.Body["color"]; !ok {
		t.Errorf("patch should include drifted color, got %+v", patch.Body)
	}
	if _, ok := patch.Body["description"]; !ok {
		t.Errorf("patch should include drifted description, got %+v", patch.Body)
	}
	if _, ok := patch.Body["status"]; ok {
		t.Errorf("patch should NOT include unchanged status, got %+v", patch.Body)
	}
	if _, ok := patch.Body["priority"]; ok {
		t.Errorf("patch should NOT include unchanged priority, got %+v", patch.Body)
	}
}

// ── 6. Plan: unchanged ───────────────────────────────────────────────────

func TestProject_Plan_Unchanged(t *testing.T) {
	doc := projectValidDoc()
	doc.Spec.LeadAgentSlug = "" // drop lead so we don't have to model it remote-side

	srv := projectFakeServer(t, nil, nil)
	defer srv.Close()
	c := newProjectHTTPClient(srv)

	remote := &ProjectRemote{
		ID:          "proj_001",
		Slug:        "q2-roadmap",
		Name:        "Q2 Roadmap",
		Description: "All Q2 deliverables",
		Color:       "#3B82F6",
		Status:      "planned",
		Priority:    "medium",
		Health:      "on_track",
		TargetDate:  "2026-06-30",
	}

	items, err := doc.Plan(context.Background(), c, remote)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) != 1 || items[0].Action != internalapi.ActionUnchanged {
		t.Fatalf("want one Unchanged item, got %+v", items)
	}
	if items[0].Exec != nil {
		t.Errorf("Unchanged item should have nil Exec, got non-nil")
	}
}

// ── 7. Plan: delete on ApplyReplace ──────────────────────────────────────

func TestProject_PlanReplace_EmitsDeleteThenCreate(t *testing.T) {
	doc := projectValidDoc()
	doc.Spec.LeadAgentSlug = ""

	srv := projectFakeServer(t, nil, nil)
	defer srv.Close()
	c := newProjectHTTPClient(srv)

	remote := &ProjectRemote{
		ID:   "proj_old",
		Slug: "q2-roadmap",
		Name: "Q2 OLD",
	}

	items, err := doc.PlanReplace(context.Background(), c, remote)
	if err != nil {
		t.Fatalf("PlanReplace: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("want 2 items (Delete+Create), got %d: %+v", len(items), items)
	}
	if items[0].Action != internalapi.ActionDelete {
		t.Errorf("first item action=%v want Delete", items[0].Action)
	}
	if items[1].Action != internalapi.ActionCreate {
		t.Errorf("second item action=%v want Create", items[1].Action)
	}
}

func TestProject_PlanReplace_NoRemoteSkipsDelete(t *testing.T) {
	doc := projectValidDoc()
	doc.Spec.LeadAgentSlug = ""

	srv := projectFakeServer(t, nil, nil)
	defer srv.Close()
	c := newProjectHTTPClient(srv)

	items, err := doc.PlanReplace(context.Background(), c, nil)
	if err != nil {
		t.Fatalf("PlanReplace: %v", err)
	}
	if len(items) != 1 || items[0].Action != internalapi.ActionCreate {
		t.Fatalf("want single Create when no remote, got %+v", items)
	}
}

// ── 8. Export round-trip ─────────────────────────────────────────────────

func TestProject_Export_RoundTrip(t *testing.T) {
	desc := "All Q2 deliverables"
	target := "2026-06-30"
	leadType := "agent"
	leadID := "agent_001"

	rows := []map[string]any{
		{
			"id":           "proj_001",
			"workspace_id": "ws_test",
			"name":         "Q2 Roadmap",
			"slug":         "q2-roadmap",
			"description":  desc,
			"color":        "#3B82F6",
			"status":       "planned",
			"priority":     "medium",
			"health":       "on_track",
			"target_date":  target,
			"lead_type":    leadType,
			"lead_id":      leadID,
		},
	}
	agents := []map[string]any{
		{"id": "agent_001", "slug": "pepa", "name": "Pepa"},
	}

	srv := projectFakeServer(t, rows, agents)
	defer srv.Close()
	c := newProjectHTTPClient(srv)

	docs, err := ExportProjects(context.Background(), c)
	if err != nil {
		t.Fatalf("ExportProjects: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("want 1 exported doc, got %d", len(docs))
	}

	got := docs[0]
	want := projectValidDoc()
	if got.APIVersion != want.APIVersion ||
		got.Kind != want.Kind ||
		got.Metadata.Name != want.Metadata.Name ||
		got.Metadata.Slug != want.Metadata.Slug ||
		got.Metadata.Description != want.Metadata.Description ||
		got.Spec != want.Spec {
		t.Errorf("export mismatch:\n want %+v\n got  %+v", want, got)
	}
}

func TestProject_Export_LeadResolveFailsContinuesGracefully(t *testing.T) {
	// Agents endpoint 500s → ExportProjects should still emit docs,
	// just with empty lead_agent_slug.
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/projects", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{
				"id":        "proj_001",
				"name":      "X",
				"slug":      "x",
				"color":     "#ffffff",
				"status":    "active",
				"priority":  "medium",
				"health":    "on_track",
				"lead_type": "agent",
				"lead_id":   "agent_001",
			},
		})
	})
	mux.HandleFunc("/api/v1/agents", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := newProjectHTTPClient(srv)

	docs, err := ExportProjects(context.Background(), c)
	if err != nil {
		t.Fatalf("ExportProjects should be resilient to agents errors: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("want 1 doc, got %d", len(docs))
	}
	if docs[0].Spec.LeadAgentSlug != "" {
		t.Errorf("expected empty LeadAgentSlug when agent lookup fails, got %q", docs[0].Spec.LeadAgentSlug)
	}
}

func TestProject_Export_DeterministicOrder(t *testing.T) {
	// Server returns rows in unsorted order; export must sort by slug.
	rows := []map[string]any{
		{"id": "a", "name": "Zee", "slug": "zee", "color": "#000000", "status": "active", "priority": "medium", "health": "on_track"},
		{"id": "b", "name": "Alpha", "slug": "alpha", "color": "#000000", "status": "active", "priority": "medium", "health": "on_track"},
		{"id": "c", "name": "Middle", "slug": "middle", "color": "#000000", "status": "active", "priority": "medium", "health": "on_track"},
	}
	srv := projectFakeServer(t, rows, nil)
	defer srv.Close()
	c := newProjectHTTPClient(srv)

	docs, err := ExportProjects(context.Background(), c)
	if err != nil {
		t.Fatalf("ExportProjects: %v", err)
	}
	want := []string{"alpha", "middle", "zee"}
	for i, d := range docs {
		if d.Metadata.Slug != want[i] {
			t.Errorf("docs[%d].slug=%q want %q", i, d.Metadata.Slug, want[i])
		}
	}
}

// ── 9. FetchProjectBySlug helper ─────────────────────────────────────────

func TestProject_FetchProjectBySlug_Found(t *testing.T) {
	rows := []map[string]any{
		{"id": "proj_001", "slug": "q2-roadmap", "name": "Q2", "color": "#fff", "status": "active", "priority": "medium", "health": "on_track"},
	}
	srv := projectFakeServer(t, rows, nil)
	defer srv.Close()
	c := newProjectHTTPClient(srv)

	remote, err := FetchProjectBySlug(context.Background(), c, "q2-roadmap")
	if err != nil {
		t.Fatalf("FetchProjectBySlug: %v", err)
	}
	if remote == nil || remote.ID != "proj_001" {
		t.Fatalf("want proj_001, got %+v", remote)
	}
}

func TestProject_FetchProjectBySlug_NotFound(t *testing.T) {
	srv := projectFakeServer(t, nil, nil)
	defer srv.Close()
	c := newProjectHTTPClient(srv)

	remote, err := FetchProjectBySlug(context.Background(), c, "missing")
	if err != nil {
		t.Fatalf("FetchProjectBySlug: %v", err)
	}
	if remote != nil {
		t.Errorf("want nil remote for missing slug, got %+v", remote)
	}
}

// ── 10. projectExpectSuccess helper ──────────────────────────────────────

func TestProject_projectExpectSuccess(t *testing.T) {
	if err := projectExpectSuccess(&internalapi.Response{StatusCode: 201}, "op"); err != nil {
		t.Errorf("2xx should pass, got %v", err)
	}
	err := projectExpectSuccess(&internalapi.Response{
		StatusCode: 400,
		Body:       strings.NewReader(`{"error":"bad"}`),
	}, "create")
	if err == nil || !strings.Contains(err.Error(), "create") {
		t.Errorf("want 4xx error mentioning op, got %v", err)
	}
	// nil response
	if err := projectExpectSuccess(nil, "op"); err == nil {
		t.Errorf("nil response should error")
	}
}

// Compile-time check: projectHTTPClient implements internalapi.Client.
var _ internalapi.Client = (*projectHTTPClient)(nil)
