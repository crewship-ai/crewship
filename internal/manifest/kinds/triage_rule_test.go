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

// ── test fixtures ────────────────────────────────────────────────────
//
// All helper symbols in this file are prefixed `triage*` so they
// don't collide with other kinds' _test.go files in the same
// package — `go test ./internal/manifest/kinds/...` compiles every
// _test.go into a single binary, and parallel agents working on
// neighbouring kinds may have picked the same generic names
// (fakeClient, validDoc, etc.) for their own fixtures.

// validTriageDoc returns a fully-populated, valid TriageRuleDocument
// used as the starting point for the happy-path tests. Tests mutate
// a local copy when they need a variant; the function is a fresh-
// value factory rather than a package-level var to keep cases
// independent.
func validTriageDoc() TriageRuleDocument {
	return TriageRuleDocument{
		APIVersion: "crewship/v1",
		Kind:       "TriageRule",
		Metadata: internalapi.Metadata{
			Name: "Bug auto-label",
			Slug: "bug-auto-label",
		},
		Spec: TriageRuleSpec{
			Enabled:  true,
			Priority: 100,
			Match: TriageMatch{
				TitleContains: []string{"error", "crash"},
			},
			Actions: TriageActions{
				AddLabels:           []string{"bug"},
				SetPriority:         "high",
				AssignToProjectSlug: "q2-roadmap",
			},
		},
	}
}

// validTriageCtx returns a WorkspaceContext that satisfies every FK
// the validTriageDoc references. Tests that want to trigger an FK
// error strip an entry from a copy of this context.
func validTriageCtx() internalapi.WorkspaceContext {
	return internalapi.WorkspaceContext{
		DeclaredLabels: []internalapi.SlugLookup{
			{Slug: "bug", Name: "bug"},
		},
		DeclaredProjects: []internalapi.SlugLookup{
			{Slug: "q2-roadmap", Name: "Q2 Roadmap"},
		},
		DeclaredAgents: []internalapi.SlugLookup{
			{Slug: "pepa", Name: "Pepa"},
		},
		DeclaredCrews: []internalapi.SlugLookup{
			{Slug: "uo-outlands", Name: "UO Outlands"},
		},
	}
}

// ── fake client (recorder) ──────────────────────────────────────────
//
// Minimal internalapi.Client implementation that records every
// call. Named `triageFakeClient` to avoid colliding with other
// kinds' fakeClient in the same _test compilation unit.

type triageFakeCall struct {
	Method string
	Path   string
	Body   map[string]any
}

type triageFakeClient struct {
	wsID  string
	calls []triageFakeCall

	// listBody is the JSON the next Get(/api/v1/triage-rules) returns.
	// Tests override this to stub the export and lookup endpoints.
	listBody string
	// postStatus / patchStatus let tests force a non-2xx response.
	postStatus  int
	patchStatus int
}

func newTriageFakeClient() *triageFakeClient {
	return &triageFakeClient{wsID: "ws_test", postStatus: 201, patchStatus: 200}
}

func (f *triageFakeClient) WorkspaceID() string { return f.wsID }

func (f *triageFakeClient) record(method, path string, body any) {
	bmap, _ := body.(map[string]any)
	f.calls = append(f.calls, triageFakeCall{Method: method, Path: path, Body: bmap})
}

func triageMkResp(status int, body string) *internalapi.Response {
	return &internalapi.Response{
		StatusCode: status,
		Body:       io.NopCloser(bytes.NewReader([]byte(body))),
	}
}

func (f *triageFakeClient) Get(_ context.Context, path string) (*internalapi.Response, error) {
	f.record("GET", path, nil)
	if path == "/api/v1/triage-rules" {
		return triageMkResp(200, f.listBody), nil
	}
	return triageMkResp(404, `{"error":"not found"}`), nil
}

func (f *triageFakeClient) Post(_ context.Context, path string, body any) (*internalapi.Response, error) {
	f.record("POST", path, body)
	return triageMkResp(f.postStatus, `{"ok":true}`), nil
}

func (f *triageFakeClient) Patch(_ context.Context, path string, body any) (*internalapi.Response, error) {
	f.record("PATCH", path, body)
	return triageMkResp(f.patchStatus, `{"ok":true}`), nil
}

func (f *triageFakeClient) Put(_ context.Context, path string, body any) (*internalapi.Response, error) {
	f.record("PUT", path, body)
	return triageMkResp(200, `{"ok":true}`), nil
}

func (f *triageFakeClient) Delete(_ context.Context, path string) (*internalapi.Response, error) {
	f.record("DELETE", path, nil)
	return triageMkResp(204, ``), nil
}

// ─────────────────────────────────────────────────────────────────────
// 1. Parse round-trip — YAML decodes into TriageRuleDocument with
//    every field preserved.
// ─────────────────────────────────────────────────────────────────────

func TestTriageRule_Parse_RoundTrip(t *testing.T) {
	src := []byte(`apiVersion: crewship/v1
kind: TriageRule
metadata:
  name: Bug auto-label
  slug: bug-auto-label
spec:
  enabled: true
  priority: 50
  match:
    title_contains: [error, crash, exception]
    body_contains: [stack trace]
    from_agent_slug: pepa
    from_crew_slug: uo-outlands
  actions:
    add_labels: [bug, urgent]
    set_priority: high
    assign_to_project_slug: q2-roadmap
    assign_to_agent_slug: pepa
    set_status: in_review
`)
	var got TriageRuleDocument
	if err := yaml.Unmarshal(src, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.Kind != "TriageRule" {
		t.Errorf("Kind = %q, want TriageRule", got.Kind)
	}
	if got.Metadata.Slug != "bug-auto-label" {
		t.Errorf("Slug = %q", got.Metadata.Slug)
	}
	if got.Spec.Priority != 50 {
		t.Errorf("Priority = %d, want 50", got.Spec.Priority)
	}
	if !got.Spec.Enabled {
		t.Error("Enabled = false, want true")
	}
	wantTitles := []string{"error", "crash", "exception"}
	if !triageEqStrings(got.Spec.Match.TitleContains, wantTitles) {
		t.Errorf("title_contains = %v, want %v", got.Spec.Match.TitleContains, wantTitles)
	}
	if got.Spec.Match.FromAgentSlug != "pepa" {
		t.Errorf("from_agent_slug = %q", got.Spec.Match.FromAgentSlug)
	}
	if got.Spec.Match.FromCrewSlug != "uo-outlands" {
		t.Errorf("from_crew_slug = %q", got.Spec.Match.FromCrewSlug)
	}
	if !triageEqStrings(got.Spec.Actions.AddLabels, []string{"bug", "urgent"}) {
		t.Errorf("add_labels = %v", got.Spec.Actions.AddLabels)
	}
	if got.Spec.Actions.SetPriority != "high" {
		t.Errorf("set_priority = %q", got.Spec.Actions.SetPriority)
	}
	if got.Spec.Actions.AssignToProjectSlug != "q2-roadmap" {
		t.Errorf("assign_to_project_slug = %q", got.Spec.Actions.AssignToProjectSlug)
	}
	if got.Spec.Actions.AssignToAgentSlug != "pepa" {
		t.Errorf("assign_to_agent_slug = %q", got.Spec.Actions.AssignToAgentSlug)
	}
	if got.Spec.Actions.SetStatus != "in_review" {
		t.Errorf("set_status = %q", got.Spec.Actions.SetStatus)
	}

	// Re-emit and re-decode → identical structure.
	out, err := yaml.Marshal(got)
	if err != nil {
		t.Fatalf("re-marshal: %v", err)
	}
	var round TriageRuleDocument
	if err := yaml.Unmarshal(out, &round); err != nil {
		t.Fatalf("re-unmarshal: %v", err)
	}
	if !triageEqStrings(round.Spec.Match.TitleContains, wantTitles) {
		t.Errorf("round-trip lost title_contains: %v", round.Spec.Match.TitleContains)
	}
}

// ─────────────────────────────────────────────────────────────────────
// 2. Validate happy path — fully-populated doc passes.
// ─────────────────────────────────────────────────────────────────────

func TestTriageRule_Validate_HappyPath(t *testing.T) {
	d := validTriageDoc()
	ctx := validTriageCtx()
	if err := d.Validate(ctx); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────
// 3. Validate error paths — exhaustive matrix of rejection reasons.
// ─────────────────────────────────────────────────────────────────────

func TestTriageRule_Validate_EmptyMatch(t *testing.T) {
	d := validTriageDoc()
	d.Spec.Match = TriageMatch{} // all fields empty
	err := d.Validate(validTriageCtx())
	if err == nil {
		t.Fatal("expected error for empty match, got nil")
	}
	if !strings.Contains(err.Error(), "spec.match must have at least one") {
		t.Errorf("error = %v; want substring about empty match", err)
	}
}

func TestTriageRule_Validate_UnknownLabelSlug(t *testing.T) {
	d := validTriageDoc()
	d.Spec.Actions.AddLabels = []string{"bug", "ghost-label"}
	err := d.Validate(validTriageCtx())
	if err == nil {
		t.Fatal("expected error for unknown label slug, got nil")
	}
	if !strings.Contains(err.Error(), "ghost-label") {
		t.Errorf("error = %v; want substring 'ghost-label'", err)
	}
	if !strings.Contains(err.Error(), "unknown label slug") {
		t.Errorf("error = %v; want 'unknown label slug' wording", err)
	}
}

func TestTriageRule_Validate_UnknownProjectSlug(t *testing.T) {
	d := validTriageDoc()
	d.Spec.Actions.AssignToProjectSlug = "ghost-project"
	err := d.Validate(validTriageCtx())
	if err == nil || !strings.Contains(err.Error(), "ghost-project") {
		t.Fatalf("want unknown-project error, got %v", err)
	}
}

func TestTriageRule_Validate_UnknownAgentSlug(t *testing.T) {
	d := validTriageDoc()
	d.Spec.Actions.AssignToAgentSlug = "ghost-agent"
	err := d.Validate(validTriageCtx())
	if err == nil || !strings.Contains(err.Error(), "ghost-agent") {
		t.Fatalf("want unknown-agent error, got %v", err)
	}
}

func TestTriageRule_Validate_UnknownCrewInMatch(t *testing.T) {
	d := validTriageDoc()
	d.Spec.Match.FromCrewSlug = "ghost-crew"
	err := d.Validate(validTriageCtx())
	if err == nil || !strings.Contains(err.Error(), "ghost-crew") {
		t.Fatalf("want unknown-crew error, got %v", err)
	}
}

func TestTriageRule_Validate_MissingMetadata(t *testing.T) {
	d := validTriageDoc()
	d.Metadata.Slug = ""
	d.Metadata.Name = ""
	err := d.Validate(validTriageCtx())
	if err == nil {
		t.Fatal("expected error for missing metadata, got nil")
	}
	if !strings.Contains(err.Error(), "metadata.slug") || !strings.Contains(err.Error(), "metadata.name") {
		t.Errorf("error = %v; want both metadata.slug and metadata.name mentioned", err)
	}
}

// ─────────────────────────────────────────────────────────────────────
// 4. Plan: create (no remote) — emits Action=Create with the correct
//    POST body shape (name + slug + enabled + priority + JSON TEXT cols).
// ─────────────────────────────────────────────────────────────────────

func TestTriageRule_Plan_Create_NoRemote(t *testing.T) {
	d := validTriageDoc()
	client := newTriageFakeClient()

	items, err := d.Plan(context.Background(), client, nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("want 1 plan item, got %d", len(items))
	}
	item := items[0]
	if item.Action != internalapi.ActionCreate {
		t.Errorf("Action = %v, want Create", item.Action)
	}
	if item.Slug != "bug-auto-label" {
		t.Errorf("Slug = %q", item.Slug)
	}
	if item.Exec == nil {
		t.Fatal("Exec is nil — create item must have a closure")
	}

	// Run the closure and inspect the POST body.
	if err := item.Exec(context.Background(), client); err != nil {
		t.Fatalf("Exec: %v", err)
	}

	if len(client.calls) != 1 {
		t.Fatalf("want 1 client call, got %d", len(client.calls))
	}
	call := client.calls[0]
	if call.Method != "POST" || call.Path != "/api/v1/triage-rules" {
		t.Errorf("call = %s %s; want POST /api/v1/triage-rules", call.Method, call.Path)
	}
	if call.Body["name"] != "Bug auto-label" {
		t.Errorf("name = %v", call.Body["name"])
	}
	if call.Body["slug"] != "bug-auto-label" {
		t.Errorf("slug = %v", call.Body["slug"])
	}
	if call.Body["enabled"] != true {
		t.Errorf("enabled = %v", call.Body["enabled"])
	}
	if call.Body["priority"] != 100 {
		t.Errorf("priority = %v, want 100", call.Body["priority"])
	}
	// match_json + actions_json should be JSON-encoded strings.
	mj, _ := call.Body["match_json"].(string)
	if !strings.Contains(mj, `"title_contains":["error","crash"]`) {
		t.Errorf("match_json = %q; missing title_contains payload", mj)
	}
	aj, _ := call.Body["actions_json"].(string)
	if !strings.Contains(aj, `"add_labels":["bug"]`) {
		t.Errorf("actions_json = %q; missing add_labels payload", aj)
	}
	if !strings.Contains(aj, `"assign_to_project_slug":"q2-roadmap"`) {
		t.Errorf("actions_json = %q; missing assign_to_project_slug", aj)
	}
}

// Priority of zero in the manifest is treated as "use default (100)".
// Authors can leave it off entirely.
func TestTriageRule_Plan_Create_DefaultsZeroPriorityTo100(t *testing.T) {
	d := validTriageDoc()
	d.Spec.Priority = 0
	client := newTriageFakeClient()

	items, err := d.Plan(context.Background(), client, nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if err := items[0].Exec(context.Background(), client); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if got := client.calls[0].Body["priority"]; got != 100 {
		t.Errorf("priority = %v, want 100", got)
	}
}

// ─────────────────────────────────────────────────────────────────────
// 5. Plan: update — remote drifted (different priority) emits Update
//    with PATCH /api/v1/triage-rules/{id}.
// ─────────────────────────────────────────────────────────────────────

func TestTriageRule_Plan_Update_DriftedRemote(t *testing.T) {
	d := validTriageDoc()
	d.Spec.Priority = 50 // declared
	matchJSON, _ := json.Marshal(d.Spec.Match)
	actionsJSON, _ := json.Marshal(d.Spec.Actions)

	remote := &TriageRuleRemote{
		ID:          "rule_abc123",
		Name:        d.Metadata.Name,
		Enabled:     d.Spec.Enabled,
		Priority:    999, // drifted
		MatchJSON:   string(matchJSON),
		ActionsJSON: string(actionsJSON),
	}

	client := newTriageFakeClient()
	items, err := d.Plan(context.Background(), client, remote)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) != 1 || items[0].Action != internalapi.ActionUpdate {
		t.Fatalf("want one Update item, got %+v", items)
	}
	if err := items[0].Exec(context.Background(), client); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if len(client.calls) != 1 {
		t.Fatalf("want 1 call, got %d", len(client.calls))
	}
	call := client.calls[0]
	if call.Method != "PATCH" {
		t.Errorf("method = %s, want PATCH", call.Method)
	}
	if call.Path != "/api/v1/triage-rules/rule_abc123" {
		t.Errorf("path = %q", call.Path)
	}
	if call.Body["priority"] != 50 {
		t.Errorf("PATCH body priority = %v, want 50", call.Body["priority"])
	}
}

// ─────────────────────────────────────────────────────────────────────
// 6. Plan: unchanged — declared spec matches remote exactly → no Exec.
// ─────────────────────────────────────────────────────────────────────

func TestTriageRule_Plan_Unchanged(t *testing.T) {
	d := validTriageDoc()
	matchJSON, _ := json.Marshal(d.Spec.Match)
	actionsJSON, _ := json.Marshal(d.Spec.Actions)

	remote := &TriageRuleRemote{
		ID:          "rule_unchanged",
		Name:        d.Metadata.Name,
		Enabled:     d.Spec.Enabled,
		Priority:    d.effectivePriority(),
		MatchJSON:   string(matchJSON),
		ActionsJSON: string(actionsJSON),
	}

	client := newTriageFakeClient()
	items, err := d.Plan(context.Background(), client, remote)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("want 1 item, got %d", len(items))
	}
	if items[0].Action != internalapi.ActionUnchanged {
		t.Errorf("Action = %v, want Unchanged", items[0].Action)
	}
	if items[0].Exec != nil {
		t.Error("Unchanged item must have nil Exec")
	}
}

// ─────────────────────────────────────────────────────────────────────
// 7. Plan: ApplyReplace semantics — parent layer passes remote=nil
//    after issuing its own Delete, so Plan re-emits Create. Verify
//    that contract: with remote=nil we always get exactly one
//    Create item regardless of whether something existed before.
// ─────────────────────────────────────────────────────────────────────

func TestTriageRule_Plan_ReplaceModeRecreate(t *testing.T) {
	d := validTriageDoc()
	client := newTriageFakeClient()

	// Simulate ApplyReplace: parent already deleted the existing
	// rule, then re-runs Plan with remote=nil to schedule a fresh
	// Create. The kind should not need any special-case branch
	// for replace — passing nil remote is sufficient.
	items, err := d.Plan(context.Background(), client, nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) != 1 || items[0].Action != internalapi.ActionCreate {
		t.Fatalf("want single Create item after replace-style delete, got %+v", items)
	}
	if items[0].Exec == nil {
		t.Fatal("recreate item must carry an Exec closure")
	}
}

// ─────────────────────────────────────────────────────────────────────
// 8. Export round-trip — fake server response → ExportTriageRules
//    produces an equivalent TriageRuleDocument with match/actions
//    unmarshaled into the structured spec.
// ─────────────────────────────────────────────────────────────────────

func TestTriageRule_Export_RoundTrip(t *testing.T) {
	original := validTriageDoc()
	matchJSON, _ := json.Marshal(original.Spec.Match)
	actionsJSON, _ := json.Marshal(original.Spec.Actions)

	listPayload := []map[string]any{
		{
			"id":           "rule_xyz",
			"name":         original.Metadata.Name,
			"enabled":      original.Spec.Enabled,
			"priority":     original.effectivePriority(),
			"match_json":   string(matchJSON),
			"actions_json": string(actionsJSON),
		},
	}
	listBytes, err := json.Marshal(listPayload)
	if err != nil {
		t.Fatalf("marshal list: %v", err)
	}

	client := newTriageFakeClient()
	client.listBody = string(listBytes)

	docs, err := ExportTriageRules(context.Background(), client)
	if err != nil {
		t.Fatalf("ExportTriageRules: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("want 1 doc, got %d", len(docs))
	}
	got := docs[0]
	if got.Kind != "TriageRule" {
		t.Errorf("Kind = %q", got.Kind)
	}
	if got.APIVersion != "crewship/v1" {
		t.Errorf("APIVersion = %q", got.APIVersion)
	}
	if got.Metadata.Name != original.Metadata.Name {
		t.Errorf("Name = %q", got.Metadata.Name)
	}
	if got.Metadata.Slug != "bug-auto-label" {
		t.Errorf("Slug = %q; want slugified form 'bug-auto-label'", got.Metadata.Slug)
	}
	if got.Spec.Priority != original.effectivePriority() {
		t.Errorf("Priority = %d", got.Spec.Priority)
	}
	if !triageEqStrings(got.Spec.Match.TitleContains, original.Spec.Match.TitleContains) {
		t.Errorf("title_contains lost: got %v want %v",
			got.Spec.Match.TitleContains, original.Spec.Match.TitleContains)
	}
	if !triageEqStrings(got.Spec.Actions.AddLabels, original.Spec.Actions.AddLabels) {
		t.Errorf("add_labels lost: got %v want %v",
			got.Spec.Actions.AddLabels, original.Spec.Actions.AddLabels)
	}
	if got.Spec.Actions.AssignToProjectSlug != original.Spec.Actions.AssignToProjectSlug {
		t.Errorf("assign_to_project_slug = %q", got.Spec.Actions.AssignToProjectSlug)
	}

	// And the export endpoint was hit.
	if len(client.calls) != 1 || client.calls[0].Path != "/api/v1/triage-rules" {
		t.Errorf("expected GET /api/v1/triage-rules, got %+v", client.calls)
	}
}

// Export tolerates malformed match_json on the server: it returns
// the rest of the document with a zero-value Match rather than
// failing the whole export.
func TestTriageRule_Export_TolerantOfCorruptJSON(t *testing.T) {
	listPayload := []map[string]any{
		{
			"id":           "rule_bad",
			"name":         "Broken rule",
			"enabled":      true,
			"priority":     100,
			"match_json":   "{not valid json",
			"actions_json": `{"add_labels":["bug"]}`,
		},
	}
	listBytes, _ := json.Marshal(listPayload)

	client := newTriageFakeClient()
	client.listBody = string(listBytes)

	docs, err := ExportTriageRules(context.Background(), client)
	if err != nil {
		t.Fatalf("ExportTriageRules: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("want 1 doc, got %d", len(docs))
	}
	if !docs[0].Spec.Match.IsEmpty() {
		t.Errorf("corrupt match_json should leave Match empty, got %+v", docs[0].Spec.Match)
	}
	if !triageEqStrings(docs[0].Spec.Actions.AddLabels, []string{"bug"}) {
		t.Errorf("valid actions_json should still decode: got %v", docs[0].Spec.Actions.AddLabels)
	}
}

// ─────────────────────────────────────────────────────────────────────
// helpers (prefixed `triage` to avoid collisions in shared _test pkg)
// ─────────────────────────────────────────────────────────────────────

func triageEqStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
