package kinds

// Tests for kind: Issue (issue.go).
//
// The fake client below is issue-prefixed to avoid collisions with
// the per-kind fakes already in this directory (agentFakeClient,
// milestoneFakeClient, recurringIssueFakeClient, …). Each test wires
// only the endpoints the scenario exercises so a future server-side
// change in an unrelated route doesn't ripple into Issue test
// failures.

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

type issueFakeCall struct {
	Method string
	Path   string
	Body   any
}

type issueFakeClient struct {
	wsID string

	// Fixtures the test seeds.
	crews    map[string]issueCrewStub    // keyed by slug
	projects map[string]issueProjectStub // keyed by slug
	agents   map[string]issueAgentStub   // keyed by slug
	labels   map[string]issueLabelStub   // keyed by name (== slug)

	// issuesByCrewID is the in-memory store the list endpoint
	// returns for ?crew_id=<id>. Tests pre-populate with the rows
	// they want to drive a particular plan path.
	issuesByCrewID map[string][]IssueRemote

	// Per-route status overrides — set to non-zero to force a
	// specific code on the next matching call.
	postIssueStatus  int
	patchIssueStatus int
	listIssuesStatus int
	listCrewsStatus  int

	calls []issueFakeCall
}

func newIssueFake() *issueFakeClient {
	return &issueFakeClient{
		wsID:           "ws_test",
		crews:          map[string]issueCrewStub{},
		projects:       map[string]issueProjectStub{},
		agents:         map[string]issueAgentStub{},
		labels:         map[string]issueLabelStub{},
		issuesByCrewID: map[string][]IssueRemote{},
	}
}

func (f *issueFakeClient) WorkspaceID() string { return f.wsID }

func (f *issueFakeClient) record(method, path string, body any) {
	f.calls = append(f.calls, issueFakeCall{Method: method, Path: path, Body: body})
}

func issueJSONResp(status int, v any) *internalapi.Response {
	data, _ := json.Marshal(v)
	return &internalapi.Response{
		StatusCode: status,
		Body:       io.NopCloser(bytes.NewReader(data)),
	}
}

func (f *issueFakeClient) Get(_ context.Context, path string) (*internalapi.Response, error) {
	f.record("GET", path, nil)
	switch {
	case path == "/api/v1/crews":
		if f.listCrewsStatus != 0 {
			return issueJSONResp(f.listCrewsStatus, map[string]any{"error": "forced"}), nil
		}
		out := make([]issueCrewStub, 0, len(f.crews))
		for _, c := range f.crews {
			out = append(out, c)
		}
		return issueJSONResp(200, out), nil
	case path == "/api/v1/projects":
		out := make([]issueProjectStub, 0, len(f.projects))
		for _, p := range f.projects {
			out = append(out, p)
		}
		return issueJSONResp(200, out), nil
	case path == "/api/v1/agents":
		out := make([]issueAgentStub, 0, len(f.agents))
		for _, a := range f.agents {
			out = append(out, a)
		}
		return issueJSONResp(200, out), nil
	case path == "/api/v1/labels":
		out := make([]issueLabelStub, 0, len(f.labels))
		for _, l := range f.labels {
			out = append(out, l)
		}
		return issueJSONResp(200, out), nil
	case strings.HasPrefix(path, "/api/v1/issues?"):
		if f.listIssuesStatus != 0 {
			return issueJSONResp(f.listIssuesStatus, map[string]any{"error": "forced"}), nil
		}
		// Crude query-string parse: only crew_id matters here.
		crewID := ""
		for _, kv := range strings.Split(strings.TrimPrefix(path, "/api/v1/issues?"), "&") {
			if strings.HasPrefix(kv, "crew_id=") {
				crewID = strings.TrimPrefix(kv, "crew_id=")
			}
		}
		rows := f.issuesByCrewID[crewID]
		return issueJSONResp(200, rows), nil
	}
	return issueJSONResp(404, map[string]any{"error": "not stubbed: " + path}), nil
}

func (f *issueFakeClient) Post(_ context.Context, path string, body any) (*internalapi.Response, error) {
	f.record("POST", path, body)
	if strings.HasPrefix(path, "/api/v1/crews/") && strings.HasSuffix(path, "/issues") {
		status := f.postIssueStatus
		if status == 0 {
			status = 201
		}
		if status < 200 || status >= 300 {
			return issueJSONResp(status, map[string]any{"error": "forced"}), nil
		}
		return issueJSONResp(status, map[string]any{"id": "iss_new", "identifier": "ENG-1"}), nil
	}
	return issueJSONResp(404, map[string]any{"error": "not stubbed: " + path}), nil
}

func (f *issueFakeClient) Patch(_ context.Context, path string, body any) (*internalapi.Response, error) {
	f.record("PATCH", path, body)
	if strings.HasPrefix(path, "/api/v1/crews/") && strings.Contains(path, "/issues/") {
		status := f.patchIssueStatus
		if status == 0 {
			status = 200
		}
		return issueJSONResp(status, map[string]any{"ok": true}), nil
	}
	return issueJSONResp(404, map[string]any{"error": "not stubbed: " + path}), nil
}

func (f *issueFakeClient) Put(_ context.Context, path string, body any) (*internalapi.Response, error) {
	f.record("PUT", path, body)
	return issueJSONResp(404, map[string]any{"error": "not stubbed: " + path}), nil
}

func (f *issueFakeClient) Delete(_ context.Context, path string) (*internalapi.Response, error) {
	f.record("DELETE", path, nil)
	return issueJSONResp(404, map[string]any{"error": "not stubbed: " + path}), nil
}

// Compile-time interface assertion.
var _ internalapi.Client = (*issueFakeClient)(nil)

// findCall returns the first recorded call matching method+path or
// nil so assertions can use "if got == nil" instead of indexing.
func (f *issueFakeClient) findCall(method, path string) *issueFakeCall {
	for i := range f.calls {
		if f.calls[i].Method == method && f.calls[i].Path == path {
			return &f.calls[i]
		}
	}
	return nil
}

// ── Helpers ─────────────────────────────────────────────────────────────────

func issueSampleDoc() *IssueDocument {
	return &IssueDocument{
		APIVersion: issueAPIVersion,
		Kind:       issueKind,
		Metadata: internalapi.Metadata{
			Name: "Ping latency check for prod hosts",
			Slug: "ping-latency-prod",
		},
		Spec: IssueSpec{
			CrewSlug:     "engineering",
			Title:        "Ping latency check for prod hosts",
			Description:  "Test ping latency against 5 production hosts...",
			Priority:     "medium",
			Status:       "backlog",
			AssigneeSlug: "viktor",
			ProjectSlug:  "network-probes",
			Labels:       []string{"network", "monitoring"},
		},
	}
}

func issueCtxFull() internalapi.WorkspaceContext {
	return internalapi.WorkspaceContext{
		DeclaredCrews:    []internalapi.SlugLookup{{Slug: "engineering", Name: "Engineering"}},
		DeclaredProjects: []internalapi.SlugLookup{{Slug: "network-probes", Name: "Network Probes"}},
		DeclaredAgents:   []internalapi.SlugLookup{{Slug: "viktor", Name: "Viktor"}},
		DeclaredLabels: []internalapi.SlugLookup{
			{Slug: "network", Name: "network"},
			{Slug: "monitoring", Name: "monitoring"},
		},
	}
}

// issueSeedFakeFull populates a fake client with every fixture the
// sample document references.
func issueSeedFakeFull(f *issueFakeClient) {
	f.crews["engineering"] = issueCrewStub{ID: "crew_eng", Slug: "engineering", Name: "Engineering"}
	f.projects["network-probes"] = issueProjectStub{ID: "proj_np", Slug: "network-probes", Name: "Network Probes"}
	f.agents["viktor"] = issueAgentStub{ID: "agt_viktor", Slug: "viktor", Name: "Viktor"}
	f.labels["network"] = issueLabelStub{ID: "lbl_net", Name: "network"}
	f.labels["monitoring"] = issueLabelStub{ID: "lbl_mon", Name: "monitoring"}
}

// ── 1. Validate: happy path ─────────────────────────────────────────────────

func TestIssue_Validate_HappyPath(t *testing.T) {
	doc := issueSampleDoc()
	if err := doc.Validate(issueCtxFull()); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestIssue_Validate_HappyPath_EmptyContext_Tolerated(t *testing.T) {
	// Validate should NOT fail just because the caller hasn't
	// seeded a workspace context — the FK checks degrade to "skip".
	doc := issueSampleDoc()
	if err := doc.Validate(internalapi.WorkspaceContext{}); err != nil {
		t.Fatalf("Validate with empty ctx: %v", err)
	}
}

func TestIssue_Validate_TitleFallsBackToMetadataName(t *testing.T) {
	// Manifest authored with only metadata.name should validate.
	doc := issueSampleDoc()
	doc.Spec.Title = ""
	if err := doc.Validate(issueCtxFull()); err != nil {
		t.Fatalf("Validate with metadata.name fallback: %v", err)
	}
}

// ── 2. Validate: required fields ────────────────────────────────────────────

func TestIssue_Validate_MissingSlug(t *testing.T) {
	doc := issueSampleDoc()
	doc.Metadata.Slug = ""
	err := doc.Validate(issueCtxFull())
	if err == nil || !strings.Contains(err.Error(), "metadata.slug is required") {
		t.Fatalf("want slug-required error, got %v", err)
	}
}

func TestIssue_Validate_MissingCrewSlug(t *testing.T) {
	doc := issueSampleDoc()
	doc.Spec.CrewSlug = ""
	err := doc.Validate(issueCtxFull())
	if err == nil || !strings.Contains(err.Error(), "spec.crew_slug is required") {
		t.Fatalf("want crew_slug-required error, got %v", err)
	}
}

func TestIssue_Validate_MissingTitleAndName(t *testing.T) {
	doc := issueSampleDoc()
	doc.Spec.Title = ""
	doc.Metadata.Name = ""
	err := doc.Validate(issueCtxFull())
	if err == nil || !strings.Contains(err.Error(), "spec.title") {
		t.Fatalf("want title-required error, got %v", err)
	}
}

// ── 3. Validate: enum errors ────────────────────────────────────────────────

func TestIssue_Validate_BadPriority(t *testing.T) {
	doc := issueSampleDoc()
	doc.Spec.Priority = "SUPER_URGENT"
	err := doc.Validate(issueCtxFull())
	if err == nil || !strings.Contains(err.Error(), "invalid priority") {
		t.Fatalf("want priority enum error, got %v", err)
	}
}

func TestIssue_Validate_BadStatus(t *testing.T) {
	doc := issueSampleDoc()
	doc.Spec.Status = "PENDING"
	err := doc.Validate(issueCtxFull())
	if err == nil || !strings.Contains(err.Error(), "invalid status") {
		t.Fatalf("want status enum error, got %v", err)
	}
}

func TestIssue_Validate_WrongAPIVersion(t *testing.T) {
	doc := issueSampleDoc()
	doc.APIVersion = "crewship/v2"
	err := doc.Validate(issueCtxFull())
	if err == nil || !strings.Contains(err.Error(), "apiVersion") {
		t.Fatalf("want apiVersion error, got %v", err)
	}
}

func TestIssue_Validate_WrongKind(t *testing.T) {
	doc := issueSampleDoc()
	doc.Kind = "Crew"
	err := doc.Validate(issueCtxFull())
	if err == nil || !strings.Contains(err.Error(), "kind") {
		t.Fatalf("want kind error, got %v", err)
	}
}

// ── 4. Validate: FK against workspace context ───────────────────────────────

func TestIssue_Validate_CrewSlugFKMissesContext(t *testing.T) {
	doc := issueSampleDoc()
	doc.Spec.CrewSlug = "ghost-crew"
	err := doc.Validate(issueCtxFull())
	if err == nil || !strings.Contains(err.Error(), "does not reference any declared or remote crew") {
		t.Fatalf("want crew FK error, got %v", err)
	}
}

func TestIssue_Validate_ProjectSlugFKMissesContext(t *testing.T) {
	doc := issueSampleDoc()
	doc.Spec.ProjectSlug = "ghost-project"
	err := doc.Validate(issueCtxFull())
	if err == nil || !strings.Contains(err.Error(), "does not reference any declared or remote project") {
		t.Fatalf("want project FK error, got %v", err)
	}
}

func TestIssue_Validate_AssigneeSlugFKMissesContext(t *testing.T) {
	doc := issueSampleDoc()
	doc.Spec.AssigneeSlug = "ghost-agent"
	err := doc.Validate(issueCtxFull())
	if err == nil || !strings.Contains(err.Error(), "does not reference any declared or remote agent") {
		t.Fatalf("want agent FK error, got %v", err)
	}
}

func TestIssue_Validate_LabelSlugFKMissesContext(t *testing.T) {
	doc := issueSampleDoc()
	doc.Spec.Labels = []string{"network", "phantom-label"}
	err := doc.Validate(issueCtxFull())
	if err == nil || !strings.Contains(err.Error(), "unknown label") {
		t.Fatalf("want label FK error, got %v", err)
	}
}

func TestIssue_Validate_EmptyLabelEntry(t *testing.T) {
	doc := issueSampleDoc()
	doc.Spec.Labels = []string{"network", "   "}
	err := doc.Validate(issueCtxFull())
	if err == nil || !strings.Contains(err.Error(), "labels[1] is empty") {
		t.Fatalf("want empty-label error, got %v", err)
	}
}

// ── 5. Plan: Create (no remote) ─────────────────────────────────────────────

func TestIssue_Plan_CreateNoRemote(t *testing.T) {
	doc := issueSampleDoc()
	client := newIssueFake()
	issueSeedFakeFull(client)

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
	if items[0].Kind != "issue" {
		t.Errorf("want kind 'issue', got %q", items[0].Kind)
	}
	if items[0].Exec == nil {
		t.Fatal("Create item must have non-nil Exec")
	}

	if err := items[0].Exec(context.Background(), client); err != nil {
		t.Fatalf("Exec: %v", err)
	}

	createCall := client.findCall("POST", "/api/v1/crews/crew_eng/issues")
	if createCall == nil {
		t.Fatal("expected POST /api/v1/crews/crew_eng/issues to be recorded")
	}
	body, ok := createCall.Body.(map[string]any)
	if !ok {
		t.Fatalf("create body is not a map: %T", createCall.Body)
	}
	if got, _ := body["title"].(string); got != "Ping latency check for prod hosts" {
		t.Errorf("title = %q, want full title", got)
	}
	if got, _ := body["priority"].(string); got != "medium" {
		t.Errorf("priority = %q, want medium", got)
	}
	if got, _ := body["assignee_type"].(string); got != "agent" {
		t.Errorf("assignee_type = %q, want agent", got)
	}
	if got, _ := body["assignee_id"].(string); got != "agt_viktor" {
		t.Errorf("assignee_id = %q, want agt_viktor", got)
	}
	if got, _ := body["project_id"].(string); got != "proj_np" {
		t.Errorf("project_id = %q, want proj_np", got)
	}
	// Verify slug FKs are NOT sent (server only knows IDs).
	for _, k := range []string{"crew_slug", "project_slug", "assignee_slug"} {
		if _, has := body[k]; has {
			t.Errorf("body should not carry %q, got %v", k, body[k])
		}
	}
	// Labels: should be resolved to IDs, sorted deterministically.
	labels, ok := body["labels"].([]string)
	if !ok {
		t.Fatalf("labels not a []string, got %T", body["labels"])
	}
	if len(labels) != 2 || labels[0] != "lbl_mon" || labels[1] != "lbl_net" {
		t.Errorf("labels = %v, want [lbl_mon lbl_net] (sorted)", labels)
	}
}

func TestIssue_Plan_CreateDefaultsPriorityWhenUnset(t *testing.T) {
	doc := issueSampleDoc()
	doc.Spec.Priority = ""

	client := newIssueFake()
	issueSeedFakeFull(client)

	items, err := doc.Plan(context.Background(), client, nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if err := items[0].Exec(context.Background(), client); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	body, _ := client.findCall("POST", "/api/v1/crews/crew_eng/issues").Body.(map[string]any)
	if got, _ := body["priority"].(string); got != defaultIssuePriority {
		t.Errorf("priority = %q, want default %q", got, defaultIssuePriority)
	}
}

func TestIssue_Plan_CreateOmitsOptionalFieldsWhenUnset(t *testing.T) {
	doc := issueSampleDoc()
	doc.Spec.AssigneeSlug = ""
	doc.Spec.ProjectSlug = ""
	doc.Spec.Labels = nil
	doc.Spec.Description = ""

	client := newIssueFake()
	client.crews["engineering"] = issueCrewStub{ID: "crew_eng", Slug: "engineering"}

	items, err := doc.Plan(context.Background(), client, nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if err := items[0].Exec(context.Background(), client); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	body, _ := client.findCall("POST", "/api/v1/crews/crew_eng/issues").Body.(map[string]any)
	for _, k := range []string{"assignee_type", "assignee_id", "project_id", "labels", "description"} {
		if _, has := body[k]; has {
			t.Errorf("body should not carry %q when unset, got %v", k, body[k])
		}
	}
}

// ── 6. Plan: FK resolution failures ─────────────────────────────────────────

func TestIssue_Plan_CrewSlugUnknown(t *testing.T) {
	doc := issueSampleDoc()
	client := newIssueFake()
	// No crews seeded — resolution should fail with a clear error.
	_, err := doc.Plan(context.Background(), client, nil)
	if err == nil || !strings.Contains(err.Error(), "resolve crew_slug") {
		t.Fatalf("want resolve-crew error, got %v", err)
	}
}

func TestIssue_Plan_ProjectSlugUnknown(t *testing.T) {
	doc := issueSampleDoc()
	client := newIssueFake()
	client.crews["engineering"] = issueCrewStub{ID: "crew_eng", Slug: "engineering"}
	// project_slug is declared but not seeded server-side.
	_, err := doc.Plan(context.Background(), client, nil)
	if err == nil || !strings.Contains(err.Error(), "resolve project_slug") {
		t.Fatalf("want resolve-project error, got %v", err)
	}
}

func TestIssue_Plan_AssigneeSlugUnknown(t *testing.T) {
	doc := issueSampleDoc()
	doc.Spec.ProjectSlug = "" // sidestep the project lookup
	client := newIssueFake()
	client.crews["engineering"] = issueCrewStub{ID: "crew_eng", Slug: "engineering"}
	_, err := doc.Plan(context.Background(), client, nil)
	if err == nil || !strings.Contains(err.Error(), "resolve assignee_slug") {
		t.Fatalf("want resolve-assignee error, got %v", err)
	}
}

func TestIssue_Plan_LabelSlugUnknown(t *testing.T) {
	doc := issueSampleDoc()
	doc.Spec.ProjectSlug = ""
	doc.Spec.AssigneeSlug = ""
	doc.Spec.Labels = []string{"ghost-label"}
	client := newIssueFake()
	client.crews["engineering"] = issueCrewStub{ID: "crew_eng", Slug: "engineering"}
	_, err := doc.Plan(context.Background(), client, nil)
	if err == nil || !strings.Contains(err.Error(), "ghost-label") {
		t.Fatalf("want label-not-found error mentioning the slug, got %v", err)
	}
}

// ── 7. Plan: Update (remote exists, drift detected) ─────────────────────────

func TestIssue_Plan_UpdateDriftedTitle(t *testing.T) {
	doc := issueSampleDoc()

	client := newIssueFake()
	issueSeedFakeFull(client)
	ident := "ENG-7"
	desc := "Test ping latency against 5 production hosts..."
	assigneeID := "agt_viktor"
	assigneeType := "agent"
	projectID := "proj_np"
	remote := IssueRemote{
		ID:           "iss_xyz",
		CrewID:       "crew_eng",
		CrewSlug:     "engineering",
		Identifier:   &ident,
		Title:        "OLD TITLE", // ← drift: manifest has new title
		Description:  &desc,
		Status:       "BACKLOG",
		Priority:     "medium",
		AssigneeType: &assigneeType,
		AssigneeID:   &assigneeID,
		ProjectID:    &projectID,
		Labels: []issueRemoteLabel{
			{ID: "lbl_mon", Name: "monitoring"},
			{ID: "lbl_net", Name: "network"},
		},
	}

	items, err := doc.Plan(context.Background(), client, &remote)
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
	patchCall := client.findCall("PATCH", "/api/v1/crews/crew_eng/issues/ENG-7")
	if patchCall == nil {
		t.Fatal("expected PATCH /api/v1/crews/crew_eng/issues/ENG-7")
	}
	body, _ := patchCall.Body.(map[string]any)
	if got, _ := body["title"].(string); got != "Ping latency check for prod hosts" {
		t.Errorf("patch.title = %q, want full title", got)
	}
	// Drift was only on title; the patch should be narrowly scoped.
	for _, k := range []string{"priority", "assignee_id", "project_id", "labels"} {
		if _, has := body[k]; has {
			t.Errorf("did not expect %q in patch (no drift), got %v", k, body[k])
		}
	}
}

func TestIssue_Plan_UpdateDriftedStatusCanonicalised(t *testing.T) {
	doc := issueSampleDoc()
	doc.Spec.Status = "in_progress" // lowercase in manifest

	client := newIssueFake()
	issueSeedFakeFull(client)
	ident := "ENG-7"
	assigneeID := "agt_viktor"
	assigneeType := "agent"
	projectID := "proj_np"
	desc := "Test ping latency against 5 production hosts..."
	remote := IssueRemote{
		ID:           "iss_xyz",
		CrewID:       "crew_eng",
		Identifier:   &ident,
		Title:        "Ping latency check for prod hosts",
		Description:  &desc,
		Status:       "BACKLOG", // ← drift target
		Priority:     "medium",
		AssigneeType: &assigneeType,
		AssigneeID:   &assigneeID,
		ProjectID:    &projectID,
		Labels: []issueRemoteLabel{
			{ID: "lbl_mon", Name: "monitoring"},
			{ID: "lbl_net", Name: "network"},
		},
	}

	items, err := doc.Plan(context.Background(), client, &remote)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if items[0].Action != internalapi.ActionUpdate {
		t.Fatalf("want Update, got %s", items[0].Action)
	}
	if err := items[0].Exec(context.Background(), client); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	body, _ := client.findCall("PATCH", "/api/v1/crews/crew_eng/issues/ENG-7").Body.(map[string]any)
	if got, _ := body["status"].(string); got != "IN_PROGRESS" {
		t.Errorf("patch.status = %q, want IN_PROGRESS (canonicalised)", got)
	}
}

func TestIssue_Plan_UpdateLabelDrift(t *testing.T) {
	doc := issueSampleDoc()

	client := newIssueFake()
	issueSeedFakeFull(client)
	ident := "ENG-7"
	desc := "Test ping latency against 5 production hosts..."
	assigneeID := "agt_viktor"
	assigneeType := "agent"
	projectID := "proj_np"
	remote := IssueRemote{
		ID:           "iss_xyz",
		CrewID:       "crew_eng",
		Identifier:   &ident,
		Title:        "Ping latency check for prod hosts",
		Description:  &desc,
		Status:       "BACKLOG",
		Priority:     "medium",
		AssigneeType: &assigneeType,
		AssigneeID:   &assigneeID,
		ProjectID:    &projectID,
		// Remote has only one label; manifest declares two → drift.
		Labels: []issueRemoteLabel{
			{ID: "lbl_net", Name: "network"},
		},
	}

	items, err := doc.Plan(context.Background(), client, &remote)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if items[0].Action != internalapi.ActionUpdate {
		t.Fatalf("want Update due to label drift, got %s", items[0].Action)
	}
	if err := items[0].Exec(context.Background(), client); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	body, _ := client.findCall("PATCH", "/api/v1/crews/crew_eng/issues/ENG-7").Body.(map[string]any)
	labels, ok := body["labels"].([]string)
	if !ok {
		t.Fatalf("patch.labels is not []string: %T", body["labels"])
	}
	if len(labels) != 2 || labels[0] != "lbl_mon" || labels[1] != "lbl_net" {
		t.Errorf("patch.labels = %v, want [lbl_mon lbl_net]", labels)
	}
}

// ── 8. Plan: Unchanged ──────────────────────────────────────────────────────

func TestIssue_Plan_Unchanged(t *testing.T) {
	doc := issueSampleDoc()

	client := newIssueFake()
	issueSeedFakeFull(client)
	ident := "ENG-7"
	desc := "Test ping latency against 5 production hosts..."
	assigneeID := "agt_viktor"
	assigneeType := "agent"
	projectID := "proj_np"
	remote := IssueRemote{
		ID:           "iss_xyz",
		CrewID:       "crew_eng",
		Identifier:   &ident,
		Title:        "Ping latency check for prod hosts",
		Description:  &desc,
		Status:       "BACKLOG",
		Priority:     "medium",
		AssigneeType: &assigneeType,
		AssigneeID:   &assigneeID,
		ProjectID:    &projectID,
		Labels: []issueRemoteLabel{
			{ID: "lbl_mon", Name: "monitoring"},
			{ID: "lbl_net", Name: "network"},
		},
	}

	items, err := doc.Plan(context.Background(), client, &remote)
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

// ── 9. LookupIssueRemoteBySlug ─────────────────────────────────────────────

func TestLookupIssueRemoteBySlug_NilWhenAbsent(t *testing.T) {
	client := newIssueFake()
	client.crews["engineering"] = issueCrewStub{ID: "crew_eng", Slug: "engineering"}
	got, err := LookupIssueRemoteBySlug(context.Background(), client, "ping-latency-prod", "engineering", "Ping latency check for prod hosts")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got != nil {
		t.Errorf("want nil remote when title absent, got %+v", got)
	}
}

func TestLookupIssueRemoteBySlug_FindByTitle(t *testing.T) {
	client := newIssueFake()
	client.crews["engineering"] = issueCrewStub{ID: "crew_eng", Slug: "engineering"}
	ident := "ENG-7"
	client.issuesByCrewID["crew_eng"] = []IssueRemote{
		{ID: "iss_xyz", CrewID: "crew_eng", Identifier: &ident, Title: "Ping latency check for prod hosts", Status: "BACKLOG", Priority: "medium"},
	}
	got, err := LookupIssueRemoteBySlug(context.Background(), client, "ping-latency-prod", "engineering", "Ping latency check for prod hosts")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got == nil {
		t.Fatal("want non-nil remote")
	}
	if got.ID != "iss_xyz" {
		t.Errorf("ID = %q, want iss_xyz", got.ID)
	}
}

func TestLookupIssueRemoteBySlug_CrewMissing(t *testing.T) {
	client := newIssueFake()
	// No crews seeded — should bubble up the crew not-found error.
	_, err := LookupIssueRemoteBySlug(context.Background(), client, "anything", "ghost", "any title")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("want crew not-found error, got %v", err)
	}
}

// ── 10. issueLookupCrewIDBySlug / resolvers ─────────────────────────────────

func TestIssueLookupCrewIDBySlug_Found(t *testing.T) {
	client := newIssueFake()
	client.crews["engineering"] = issueCrewStub{ID: "crew_eng", Slug: "engineering"}
	id, err := issueLookupCrewIDBySlug(context.Background(), client, "engineering")
	if err != nil {
		t.Fatalf("issueLookupCrewIDBySlug: %v", err)
	}
	if id != "crew_eng" {
		t.Errorf("id = %q, want crew_eng", id)
	}
}

func TestIssueLookupCrewIDBySlug_NotFound(t *testing.T) {
	client := newIssueFake()
	_, err := issueLookupCrewIDBySlug(context.Background(), client, "ghost")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("want not-found error, got %v", err)
	}
}

func TestIssueLookupCrewIDBySlug_EmptySlug(t *testing.T) {
	client := newIssueFake()
	_, err := issueLookupCrewIDBySlug(context.Background(), client, "")
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("want empty-slug error, got %v", err)
	}
}

func TestIssueResolveOptionalProjectID_EmptyReturnsEmpty(t *testing.T) {
	client := newIssueFake()
	id, err := issueResolveOptionalProjectID(context.Background(), client, "")
	if err != nil {
		t.Fatalf("resolve empty: %v", err)
	}
	if id != "" {
		t.Errorf("want empty id for empty slug, got %q", id)
	}
}

func TestIssueResolveLabelIDs_DeterministicSort(t *testing.T) {
	client := newIssueFake()
	client.labels["b-label"] = issueLabelStub{ID: "lbl_b", Name: "b-label"}
	client.labels["a-label"] = issueLabelStub{ID: "lbl_a", Name: "a-label"}
	ids, err := issueResolveLabelIDs(context.Background(), client, []string{"b-label", "a-label"})
	if err != nil {
		t.Fatalf("resolve labels: %v", err)
	}
	if len(ids) != 2 || ids[0] != "lbl_a" || ids[1] != "lbl_b" {
		t.Errorf("ids = %v, want sorted [lbl_a lbl_b]", ids)
	}
}

// ── 11. Plan: server error surfaces via Exec ────────────────────────────────

func TestIssue_Plan_CreateServerErrorSurfacedFromExec(t *testing.T) {
	doc := issueSampleDoc()

	client := newIssueFake()
	issueSeedFakeFull(client)
	client.postIssueStatus = 500

	items, err := doc.Plan(context.Background(), client, nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if err := items[0].Exec(context.Background(), client); err == nil ||
		!strings.Contains(err.Error(), "500") {
		t.Fatalf("want 500 surfaced from Exec, got %v", err)
	}
}

// ── 12. ExportIssues ────────────────────────────────────────────────────────

func TestExportIssues_RoundTripsCrewProjectAgentLabels(t *testing.T) {
	client := newIssueFake()
	client.crews["engineering"] = issueCrewStub{ID: "crew_eng", Slug: "engineering", Name: "Engineering"}
	client.projects["network-probes"] = issueProjectStub{ID: "proj_np", Slug: "network-probes", Name: "Network Probes"}
	client.agents["viktor"] = issueAgentStub{ID: "agt_viktor", Slug: "viktor", Name: "Viktor"}

	ident := "ENG-7"
	desc := "test desc"
	assigneeID := "agt_viktor"
	assigneeType := "agent"
	projectID := "proj_np"
	client.issuesByCrewID["crew_eng"] = []IssueRemote{
		{
			ID:           "iss_xyz",
			CrewID:       "crew_eng",
			CrewSlug:     "engineering",
			Identifier:   &ident,
			Title:        "Ping latency check",
			Description:  &desc,
			Status:       "BACKLOG",
			Priority:     "medium",
			AssigneeType: &assigneeType,
			AssigneeID:   &assigneeID,
			ProjectID:    &projectID,
			Labels: []issueRemoteLabel{
				{ID: "lbl_net", Name: "network"},
				{ID: "lbl_mon", Name: "monitoring"},
			},
		},
	}

	docs, err := ExportIssues(context.Background(), client)
	if err != nil {
		t.Fatalf("ExportIssues: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("want 1 doc, got %d", len(docs))
	}
	d := docs[0]
	if d.Kind != issueKind || d.APIVersion != issueAPIVersion {
		t.Errorf("envelope drift: kind=%q apiVersion=%q", d.Kind, d.APIVersion)
	}
	if d.Spec.CrewSlug != "engineering" {
		t.Errorf("crew_slug = %q, want engineering", d.Spec.CrewSlug)
	}
	if d.Spec.ProjectSlug != "network-probes" {
		t.Errorf("project_slug = %q, want network-probes", d.Spec.ProjectSlug)
	}
	if d.Spec.AssigneeSlug != "viktor" {
		t.Errorf("assignee_slug = %q, want viktor", d.Spec.AssigneeSlug)
	}
	if d.Metadata.Slug != "engineering--ping-latency-check" {
		t.Errorf("slug = %q, want crew-namespaced kebab", d.Metadata.Slug)
	}
	if d.Metadata.Name != "Ping latency check" {
		t.Errorf("name = %q, want original title", d.Metadata.Name)
	}
	if len(d.Spec.Labels) != 2 || d.Spec.Labels[0] != "monitoring" || d.Spec.Labels[1] != "network" {
		t.Errorf("labels = %v, want sorted [monitoring network]", d.Spec.Labels)
	}
}

func TestExportIssues_OrphanProjectIDOmittedFromSpec(t *testing.T) {
	// An issue whose project_id doesn't resolve to any known
	// project should export with an empty project_slug.
	client := newIssueFake()
	client.crews["engineering"] = issueCrewStub{ID: "crew_eng", Slug: "engineering"}
	ident := "ENG-1"
	orphanProj := "proj_orphan"
	client.issuesByCrewID["crew_eng"] = []IssueRemote{
		{
			ID: "iss_a", CrewID: "crew_eng", Identifier: &ident,
			Title: "Orphan", Status: "BACKLOG", Priority: "none",
			ProjectID: &orphanProj,
		},
	}
	docs, err := ExportIssues(context.Background(), client)
	if err != nil {
		t.Fatalf("ExportIssues: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("want 1 doc, got %d", len(docs))
	}
	if docs[0].Spec.ProjectSlug != "" {
		t.Errorf("orphan project_id should produce empty project_slug, got %q", docs[0].Spec.ProjectSlug)
	}
}

func TestExportIssues_EmptyWorkspace(t *testing.T) {
	client := newIssueFake()
	// No crews → empty export, no error.
	docs, err := ExportIssues(context.Background(), client)
	if err != nil {
		t.Fatalf("ExportIssues: %v", err)
	}
	if len(docs) != 0 {
		t.Errorf("want 0 docs, got %d", len(docs))
	}
}

// ── 13. issueSameLabelSet (helper) ──────────────────────────────────────────

func TestIssueSameLabelSet(t *testing.T) {
	cases := []struct {
		name     string
		declared []string
		remote   []issueRemoteLabel
		want     bool
	}{
		{"both empty", nil, nil, true},
		{"same set, different order", []string{"a", "b"}, []issueRemoteLabel{{Name: "b"}, {Name: "a"}}, true},
		{"different len", []string{"a"}, []issueRemoteLabel{{Name: "a"}, {Name: "b"}}, false},
		{"different names", []string{"a"}, []issueRemoteLabel{{Name: "b"}}, false},
		{"declared empty, remote has labels → drift", nil, []issueRemoteLabel{{Name: "x"}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := issueSameLabelSet(tc.declared, tc.remote)
			if got != tc.want {
				t.Errorf("want %v, got %v", tc.want, got)
			}
		})
	}
}

// ── 14. resolvedTitle precedence ────────────────────────────────────────────

func TestIssueResolvedTitle_PrefersSpecTitle(t *testing.T) {
	doc := &IssueDocument{
		Metadata: internalapi.Metadata{Name: "Fallback"},
		Spec:     IssueSpec{Title: "Authored"},
	}
	if got := doc.resolvedTitle(); got != "Authored" {
		t.Errorf("title = %q, want Authored (spec.title wins)", got)
	}
}

func TestIssueResolvedTitle_FallsBackToMetadataName(t *testing.T) {
	doc := &IssueDocument{
		Metadata: internalapi.Metadata{Name: "Fallback"},
		Spec:     IssueSpec{},
	}
	if got := doc.resolvedTitle(); got != "Fallback" {
		t.Errorf("title = %q, want Fallback (metadata.name wins)", got)
	}
}
