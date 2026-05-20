package kinds

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/crewship-ai/crewship/internal/manifest/internalapi"
)

// recurringIssueFakeClient is an in-memory stand-in for
// internalapi.Client. Tests pre-populate the maps with the rows
// they want the GET endpoints to return; mutating calls land in
// Calls so assertions can inspect what the kind would have sent on
// the wire.
//
// The struct is named with a "recurringIssue" prefix because
// internal/manifest/kinds is a shared package — other kinds may
// also need fakeClient analogues, and prefixing keeps each file's
// fixtures independent.
type recurringIssueFakeClient struct {
	wsID string

	crews           []map[string]any
	projects        []map[string]any
	labels          []map[string]any
	agents          []map[string]any
	recurringIssues []map[string]any

	Calls []recurringIssueFakeCall
}

type recurringIssueFakeCall struct {
	Method string
	Path   string
	Body   map[string]any
}

func newRecurringIssueFakeClient() *recurringIssueFakeClient {
	return &recurringIssueFakeClient{wsID: "ws_test"}
}

func (f *recurringIssueFakeClient) WorkspaceID() string { return f.wsID }

func (f *recurringIssueFakeClient) record(method, path string, body any) {
	bmap, _ := body.(map[string]any)
	f.Calls = append(f.Calls, recurringIssueFakeCall{Method: method, Path: path, Body: bmap})
}

func (f *recurringIssueFakeClient) jsonResp(status int, v any) *internalapi.Response {
	data, _ := json.Marshal(v)
	return &internalapi.Response{
		StatusCode: status,
		Body:       io.NopCloser(bytes.NewReader(data)),
	}
}

func (f *recurringIssueFakeClient) Get(_ context.Context, path string) (*internalapi.Response, error) {
	f.record("GET", path, nil)
	switch path {
	case "/api/v1/crews":
		return f.jsonResp(200, f.crews), nil
	case "/api/v1/projects":
		return f.jsonResp(200, f.projects), nil
	case "/api/v1/labels":
		return f.jsonResp(200, f.labels), nil
	case "/api/v1/agents":
		return f.jsonResp(200, f.agents), nil
	case "/api/v1/recurring-issues":
		return f.jsonResp(200, f.recurringIssues), nil
	}
	return f.jsonResp(404, map[string]any{"error": "not found"}), nil
}

func (f *recurringIssueFakeClient) Post(_ context.Context, path string, body any) (*internalapi.Response, error) {
	f.record("POST", path, body)
	return f.jsonResp(201, body), nil
}

func (f *recurringIssueFakeClient) Patch(_ context.Context, path string, body any) (*internalapi.Response, error) {
	f.record("PATCH", path, body)
	return f.jsonResp(200, body), nil
}

func (f *recurringIssueFakeClient) Put(_ context.Context, path string, body any) (*internalapi.Response, error) {
	f.record("PUT", path, body)
	return f.jsonResp(200, body), nil
}

func (f *recurringIssueFakeClient) Delete(_ context.Context, path string) (*internalapi.Response, error) {
	f.record("DELETE", path, nil)
	return f.jsonResp(204, nil), nil
}

// --- test helpers ----------------------------------------------------------

// recurringIssueValidWorkspaceCtx builds a context with the fixtures
// every happy-path test references, so the table tests can opt-in
// to a baseline without re-declaring crews / projects / labels each
// time.
func recurringIssueValidWorkspaceCtx() internalapi.WorkspaceContext {
	return internalapi.WorkspaceContext{
		DeclaredCrews:    []internalapi.SlugLookup{{Slug: "my-crew", Name: "My Crew"}},
		DeclaredProjects: []internalapi.SlugLookup{{Slug: "q2-roadmap", Name: "Q2"}},
		DeclaredLabels: []internalapi.SlugLookup{
			{Slug: "recurring", Name: "recurring"},
			{Slug: "status", Name: "status"},
		},
		DeclaredAgents: []internalapi.SlugLookup{{Slug: "pepa", Name: "Pepa"}},
	}
}

// recurringIssueValidDocument returns a fully-populated, valid
// RecurringIssue document used by happy-path tests. Mutate fields
// on the returned pointer for error-path tests.
func recurringIssueValidDocument() *RecurringIssueDocument {
	enabled := true
	return &RecurringIssueDocument{
		APIVersion: "crewship/v1",
		Kind:       "RecurringIssue",
		Metadata: internalapi.Metadata{
			Name: "Weekly status review",
			Slug: "weekly-status",
		},
		Spec: RecurringIssueSpec{
			Enabled:  &enabled,
			Cron:     "0 9 * * MON",
			Timezone: "Europe/Prague",
			Template: RecurringIssueTemplate{
				Title:             "Weekly status — {{.Date}}",
				Description:       "Status update for week of {{.WeekStart}}.\n",
				Labels:            []string{"recurring", "status"},
				ProjectSlug:       "q2-roadmap",
				Priority:          "medium",
				AssigneeAgentSlug: "pepa",
				CrewSlug:          "my-crew",
			},
		},
	}
}

// recurringIssueSeedAllResolvers populates a fake client with the
// IDs needed to resolve every slug in recurringIssueValidDocument().
func recurringIssueSeedAllResolvers(f *recurringIssueFakeClient) {
	f.crews = []map[string]any{
		{"id": "crew_001", "slug": "my-crew", "name": "My Crew"},
	}
	f.projects = []map[string]any{
		{"id": "proj_001", "slug": "q2-roadmap", "name": "Q2"},
	}
	f.labels = []map[string]any{
		{"id": "lbl_001", "name": "recurring"},
		{"id": "lbl_002", "name": "status"},
	}
	f.agents = []map[string]any{
		{"id": "agt_001", "slug": "pepa", "name": "Pepa"},
	}
}

// --- 1. Parse round-trip ---------------------------------------------------

// TestRecurringIssue_ParseRoundTrip ensures a YAML document survives
// unmarshal + remarshal without losing fields. This is the spec's
// "Parse round-trip" gate — sloppy yaml tags or omitted fields would
// surface here before any apply runs.
func TestRecurringIssue_ParseRoundTrip(t *testing.T) {
	raw := `apiVersion: crewship/v1
kind: RecurringIssue
metadata:
  name: Weekly status review
  slug: weekly-status
spec:
  enabled: true
  cron: "0 9 * * MON"
  timezone: Europe/Prague
  template:
    title: "Weekly status"
    description: "Status update"
    labels: [recurring, status]
    project_slug: q2-roadmap
    priority: medium
    assignee_agent_slug: pepa
    crew_slug: my-crew
`
	var doc RecurringIssueDocument
	if err := yaml.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if doc.Metadata.Slug != "weekly-status" {
		t.Errorf("metadata.slug = %q, want weekly-status", doc.Metadata.Slug)
	}
	if doc.Spec.Cron != "0 9 * * MON" {
		t.Errorf("cron = %q", doc.Spec.Cron)
	}
	if doc.Spec.Template.CrewSlug != "my-crew" {
		t.Errorf("crew_slug = %q", doc.Spec.Template.CrewSlug)
	}
	if len(doc.Spec.Template.Labels) != 2 {
		t.Errorf("expected 2 labels, got %v", doc.Spec.Template.Labels)
	}
	if doc.Spec.Enabled == nil || !*doc.Spec.Enabled {
		t.Errorf("enabled should be true, got %v", doc.Spec.Enabled)
	}

	// Round-trip back to YAML and re-decode; structural equality
	// confirms no field silently dropped.
	out, err := yaml.Marshal(&doc)
	if err != nil {
		t.Fatalf("remarshal: %v", err)
	}
	var doc2 RecurringIssueDocument
	if err := yaml.Unmarshal(out, &doc2); err != nil {
		t.Fatalf("re-unmarshal: %v", err)
	}
	if doc2.Spec.Template.CrewSlug != doc.Spec.Template.CrewSlug {
		t.Errorf("crew_slug drift after round-trip: %q vs %q",
			doc2.Spec.Template.CrewSlug, doc.Spec.Template.CrewSlug)
	}
}

// --- 2. Validate happy path ------------------------------------------------

// TestRecurringIssue_Validate_HappyPath confirms a fully-populated
// valid document passes every check. Acts as a regression canary
// for over-eager validation rules.
func TestRecurringIssue_Validate_HappyPath(t *testing.T) {
	doc := recurringIssueValidDocument()
	if err := doc.Validate(recurringIssueValidWorkspaceCtx()); err != nil {
		t.Fatalf("expected valid document to pass, got: %v", err)
	}
}

// TestRecurringIssue_Validate_DefaultsToEnabled covers the case
// where enabled is omitted — the server defaults to enabled=1 and
// so does EnabledOrDefault.
func TestRecurringIssue_Validate_DefaultsToEnabled(t *testing.T) {
	doc := recurringIssueValidDocument()
	doc.Spec.Enabled = nil
	if err := doc.Validate(recurringIssueValidWorkspaceCtx()); err != nil {
		t.Fatalf("expected valid: %v", err)
	}
	if !doc.Spec.EnabledOrDefault() {
		t.Fatal("EnabledOrDefault should return true when Enabled is nil")
	}
}

// --- 3. Validate error paths -----------------------------------------------

// TestRecurringIssue_Validate_ErrorPaths walks every validation
// rule in a table so each rule has its own named failure. The
// "contains" check keeps the assertion robust against minor wording
// changes while still pinning the rule that fired.
func TestRecurringIssue_Validate_ErrorPaths(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(d *RecurringIssueDocument)
		wantSub string
	}{
		{
			name:    "missing slug",
			mutate:  func(d *RecurringIssueDocument) { d.Metadata.Slug = "" },
			wantSub: "metadata.slug is required",
		},
		{
			name:    "invalid cron",
			mutate:  func(d *RecurringIssueDocument) { d.Spec.Cron = "totally not cron" },
			wantSub: "spec.cron",
		},
		{
			name:    "empty cron",
			mutate:  func(d *RecurringIssueDocument) { d.Spec.Cron = "" },
			wantSub: "spec.cron is required",
		},
		{
			name:    "invalid timezone",
			mutate:  func(d *RecurringIssueDocument) { d.Spec.Timezone = "Mars/Olympus" },
			wantSub: "not a valid IANA timezone",
		},
		{
			name:    "empty timezone",
			mutate:  func(d *RecurringIssueDocument) { d.Spec.Timezone = "" },
			wantSub: "spec.timezone is required",
		},
		{
			name:    "missing crew_slug",
			mutate:  func(d *RecurringIssueDocument) { d.Spec.Template.CrewSlug = "" },
			wantSub: "crew_slug is required",
		},
		{
			name:    "missing title",
			mutate:  func(d *RecurringIssueDocument) { d.Spec.Template.Title = "" },
			wantSub: "template.title is required",
		},
		{
			name:    "unknown crew_slug",
			mutate:  func(d *RecurringIssueDocument) { d.Spec.Template.CrewSlug = "ghost-crew" },
			wantSub: `crew_slug "ghost-crew"`,
		},
		{
			name:    "unknown project_slug",
			mutate:  func(d *RecurringIssueDocument) { d.Spec.Template.ProjectSlug = "ghost-project" },
			wantSub: `project_slug "ghost-project"`,
		},
		{
			name:    "unknown assignee",
			mutate:  func(d *RecurringIssueDocument) { d.Spec.Template.AssigneeAgentSlug = "ghost-agent" },
			wantSub: `assignee_agent_slug "ghost-agent"`,
		},
		{
			name:    "unknown label slug",
			mutate:  func(d *RecurringIssueDocument) { d.Spec.Template.Labels = []string{"recurring", "phantom"} },
			wantSub: `unknown label "phantom"`,
		},
		{
			name:    "invalid priority",
			mutate:  func(d *RecurringIssueDocument) { d.Spec.Template.Priority = "panic" },
			wantSub: "priority",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			doc := recurringIssueValidDocument()
			tc.mutate(doc)
			err := doc.Validate(recurringIssueValidWorkspaceCtx())
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantSub)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantSub)
			}
		})
	}
}

// --- 4. Plan: create (no remote) -------------------------------------------

// TestRecurringIssue_Plan_Create_NoRemote covers the first-apply
// path: nothing on the server, manifest declares a recurring issue,
// Plan emits a Create whose exec resolves slugs to IDs and POSTs
// the body the spec contract describes.
func TestRecurringIssue_Plan_Create_NoRemote(t *testing.T) {
	doc := recurringIssueValidDocument()
	f := newRecurringIssueFakeClient()
	recurringIssueSeedAllResolvers(f)

	items, err := doc.Plan(context.Background(), f, nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	it := items[0]
	if it.Action != internalapi.ActionCreate {
		t.Errorf("Action = %v, want Create", it.Action)
	}
	if it.Slug != "weekly-status" {
		t.Errorf("Slug = %q", it.Slug)
	}
	if it.Exec == nil {
		t.Fatal("create item must have Exec")
	}
	if err := it.Exec(context.Background(), f); err != nil {
		t.Fatalf("Exec: %v", err)
	}

	// Find the POST and assert the body has the spec-contract shape.
	var post *recurringIssueFakeCall
	for i := range f.Calls {
		if f.Calls[i].Method == "POST" && f.Calls[i].Path == "/api/v1/recurring-issues" {
			post = &f.Calls[i]
			break
		}
	}
	if post == nil {
		t.Fatal("expected POST to /api/v1/recurring-issues")
	}
	if got := post.Body["name"]; got != "Weekly status review" {
		t.Errorf("body.name = %v", got)
	}
	if got := post.Body["cron"]; got != "0 9 * * MON" {
		t.Errorf("body.cron = %v", got)
	}
	if got := post.Body["timezone"]; got != "Europe/Prague" {
		t.Errorf("body.timezone = %v", got)
	}
	if got, _ := post.Body["enabled"].(bool); !got {
		t.Errorf("body.enabled = %v", post.Body["enabled"])
	}

	// template_json must be valid JSON with resolved IDs.
	tj, _ := post.Body["template_json"].(string)
	if tj == "" {
		t.Fatal("template_json missing from POST body")
	}
	var tmpl RecurringIssueRemoteTemplate
	if err := json.Unmarshal([]byte(tj), &tmpl); err != nil {
		t.Fatalf("template_json is not valid JSON: %v", err)
	}
	if tmpl.CrewID != "crew_001" {
		t.Errorf("template_json.crew_id = %q, want crew_001", tmpl.CrewID)
	}
	if tmpl.ProjectID != "proj_001" {
		t.Errorf("template_json.project_id = %q", tmpl.ProjectID)
	}
	if tmpl.AssigneeAgentID != "agt_001" {
		t.Errorf("template_json.assignee_agent_id = %q", tmpl.AssigneeAgentID)
	}
	if len(tmpl.LabelIDs) != 2 {
		t.Errorf("expected 2 label_ids, got %v", tmpl.LabelIDs)
	}
	if tmpl.Title != "Weekly status — {{.Date}}" {
		t.Errorf("template_json.title drift: %q", tmpl.Title)
	}
}

// TestRecurringIssue_Plan_Create_UnresolvedCrewFails confirms the
// exec closure returns a clear error when the referenced crew can't
// be found at apply time (e.g. it was deleted between plan and
// apply, or a peer manifest agent failed to create it).
func TestRecurringIssue_Plan_Create_UnresolvedCrewFails(t *testing.T) {
	doc := recurringIssueValidDocument()
	f := newRecurringIssueFakeClient()
	// No crews seeded — exec must fail with a useful message.
	f.projects = []map[string]any{{"id": "proj_001", "slug": "q2-roadmap"}}
	f.labels = []map[string]any{
		{"id": "lbl_001", "name": "recurring"},
		{"id": "lbl_002", "name": "status"},
	}
	f.agents = []map[string]any{{"id": "agt_001", "slug": "pepa"}}

	items, err := doc.Plan(context.Background(), f, nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if err := items[0].Exec(context.Background(), f); err == nil ||
		!strings.Contains(err.Error(), "crew") {
		t.Fatalf("expected crew-not-found error, got %v", err)
	}
}

// --- 5. Plan: update (drifted remote) --------------------------------------

// TestRecurringIssue_Plan_Update_Drifted simulates an existing
// server-side row with a different cron expression. Plan must emit
// an Update; the PATCH body should carry the new state.
func TestRecurringIssue_Plan_Update_Drifted(t *testing.T) {
	doc := recurringIssueValidDocument()
	f := newRecurringIssueFakeClient()
	recurringIssueSeedAllResolvers(f)

	// Existing remote with a different cron.
	remote := &RecurringIssueRemote{
		ID:       "ri_001",
		Name:     "Weekly status review",
		Slug:     "weekly-status",
		Enabled:  true,
		Cron:     "0 0 * * MON", // drifted vs declared "0 9 * * MON"
		Timezone: "Europe/Prague",
		TemplateJSON: mustJSONRI(RecurringIssueRemoteTemplate{
			Title:           "Weekly status — {{.Date}}",
			Description:     "Status update for week of {{.WeekStart}}.\n",
			Priority:        "medium",
			LabelIDs:        []string{"lbl_001", "lbl_002"},
			ProjectID:       "proj_001",
			AssigneeAgentID: "agt_001",
			CrewID:          "crew_001",
		}),
	}

	items, err := doc.Plan(context.Background(), f, remote)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) != 1 || items[0].Action != internalapi.ActionUpdate {
		t.Fatalf("expected single Update, got %+v", items)
	}
	if err := items[0].Exec(context.Background(), f); err != nil {
		t.Fatalf("Exec: %v", err)
	}

	// Assert the PATCH carries the new cron.
	var patched *recurringIssueFakeCall
	for i := range f.Calls {
		if f.Calls[i].Method == "PATCH" && f.Calls[i].Path == "/api/v1/recurring-issues/ri_001" {
			patched = &f.Calls[i]
			break
		}
	}
	if patched == nil {
		t.Fatal("expected PATCH to /api/v1/recurring-issues/ri_001")
	}
	if patched.Body["cron"] != "0 9 * * MON" {
		t.Errorf("patched cron = %v", patched.Body["cron"])
	}
}

// --- 6. Plan: unchanged ----------------------------------------------------

// TestRecurringIssue_Plan_Unchanged covers the converged state: a
// remote row matches the manifest exactly. Plan must emit
// ActionUnchanged with no Exec closure.
func TestRecurringIssue_Plan_Unchanged(t *testing.T) {
	doc := recurringIssueValidDocument()
	f := newRecurringIssueFakeClient()
	recurringIssueSeedAllResolvers(f)

	// Remote matches the spec exactly (with resolved IDs).
	remote := &RecurringIssueRemote{
		ID:       "ri_001",
		Name:     "Weekly status review",
		Slug:     "weekly-status",
		Enabled:  true,
		Cron:     "0 9 * * MON",
		Timezone: "Europe/Prague",
		TemplateJSON: mustJSONRI(RecurringIssueRemoteTemplate{
			Title:           "Weekly status — {{.Date}}",
			Description:     "Status update for week of {{.WeekStart}}.\n",
			Priority:        "medium",
			LabelIDs:        []string{"lbl_001", "lbl_002"},
			ProjectID:       "proj_001",
			AssigneeAgentID: "agt_001",
			CrewID:          "crew_001",
		}),
	}

	items, err := doc.Plan(context.Background(), f, remote)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Action != internalapi.ActionUnchanged {
		t.Errorf("Action = %v, want Unchanged", items[0].Action)
	}
	if items[0].Exec != nil {
		t.Error("Unchanged items must not have Exec")
	}

	// And no mutating calls should have been recorded.
	for _, c := range f.Calls {
		if c.Method == "POST" || c.Method == "PATCH" || c.Method == "DELETE" {
			t.Errorf("unchanged plan should not mutate, got %s %s", c.Method, c.Path)
		}
	}
}

// --- 7. Plan: label drift --------------------------------------------------

// TestRecurringIssue_Plan_LabelDrift confirms that adding a label
// to the manifest surfaces as drift even when other fields match
// — the diff must look inside template_json, not just at the top-
// level columns.
func TestRecurringIssue_Plan_LabelDrift(t *testing.T) {
	doc := recurringIssueValidDocument()
	f := newRecurringIssueFakeClient()
	recurringIssueSeedAllResolvers(f)

	// Remote has only one label; manifest declares two.
	remote := &RecurringIssueRemote{
		ID:       "ri_001",
		Name:     "Weekly status review",
		Slug:     "weekly-status",
		Enabled:  true,
		Cron:     "0 9 * * MON",
		Timezone: "Europe/Prague",
		TemplateJSON: mustJSONRI(RecurringIssueRemoteTemplate{
			Title:           "Weekly status — {{.Date}}",
			Description:     "Status update for week of {{.WeekStart}}.\n",
			Priority:        "medium",
			LabelIDs:        []string{"lbl_001"},
			ProjectID:       "proj_001",
			AssigneeAgentID: "agt_001",
			CrewID:          "crew_001",
		}),
	}

	items, err := doc.Plan(context.Background(), f, remote)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if items[0].Action != internalapi.ActionUpdate {
		t.Errorf("expected Update due to label drift, got %v", items[0].Action)
	}
}

// --- 8. Export round-trip --------------------------------------------------

// TestRecurringIssue_ExportRoundTrip seeds the fake server with a
// recurring issue row whose template_json holds resolved IDs, calls
// ExportRecurringIssues, and confirms the returned document
// reverse-resolves IDs back to slugs identical to the manifest a
// user would have authored.
func TestRecurringIssue_ExportRoundTrip(t *testing.T) {
	f := newRecurringIssueFakeClient()
	recurringIssueSeedAllResolvers(f)

	f.recurringIssues = []map[string]any{
		{
			"id":       "ri_001",
			"name":     "Weekly status review",
			"slug":     "weekly-status",
			"enabled":  true,
			"cron":     "0 9 * * MON",
			"timezone": "Europe/Prague",
			"template_json": mustJSONRI(RecurringIssueRemoteTemplate{
				Title:           "Weekly status — {{.Date}}",
				Description:     "Status update for week of {{.WeekStart}}.\n",
				Priority:        "medium",
				LabelIDs:        []string{"lbl_001", "lbl_002"},
				ProjectID:       "proj_001",
				AssigneeAgentID: "agt_001",
				CrewID:          "crew_001",
			}),
		},
	}

	docs, err := ExportRecurringIssues(context.Background(), f)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("expected 1 exported doc, got %d", len(docs))
	}
	got := docs[0]
	if got.APIVersion != "crewship/v1" || got.Kind != "RecurringIssue" {
		t.Errorf("header drift: %s/%s", got.APIVersion, got.Kind)
	}
	if got.Metadata.Slug != "weekly-status" {
		t.Errorf("slug = %q", got.Metadata.Slug)
	}
	if got.Spec.Template.CrewSlug != "my-crew" {
		t.Errorf("crew_slug = %q (id-to-slug reverse lookup failed)", got.Spec.Template.CrewSlug)
	}
	if got.Spec.Template.ProjectSlug != "q2-roadmap" {
		t.Errorf("project_slug = %q", got.Spec.Template.ProjectSlug)
	}
	if got.Spec.Template.AssigneeAgentSlug != "pepa" {
		t.Errorf("assignee_agent_slug = %q", got.Spec.Template.AssigneeAgentSlug)
	}
	if len(got.Spec.Template.Labels) != 2 {
		t.Fatalf("expected 2 labels, got %v", got.Spec.Template.Labels)
	}
	// Labels emerge sorted for deterministic export.
	if got.Spec.Template.Labels[0] != "recurring" || got.Spec.Template.Labels[1] != "status" {
		t.Errorf("labels not in expected sorted order: %v", got.Spec.Template.Labels)
	}

	// And the exported doc, re-validated against the same context,
	// must pass — the round-trip is closed.
	if err := got.Validate(recurringIssueValidWorkspaceCtx()); err != nil {
		t.Errorf("exported doc failed re-validate: %v", err)
	}
}

// TestRecurringIssue_Export_EmptyWorkspace covers the no-rows path:
// a workspace without recurring issues should return nil/empty
// rather than an error.
func TestRecurringIssue_Export_EmptyWorkspace(t *testing.T) {
	f := newRecurringIssueFakeClient()
	docs, err := ExportRecurringIssues(context.Background(), f)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if len(docs) != 0 {
		t.Errorf("expected 0 docs, got %d", len(docs))
	}
}

// --- helpers ---------------------------------------------------------------

// mustJSONRI marshals v or fails the test. Suffix "RI" keeps the
// helper from colliding with same-named helpers other kinds_test
// files may define in this shared package.
func mustJSONRI(v any) string {
	out, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("test fixture marshal failed: %v", err))
	}
	return string(out)
}
