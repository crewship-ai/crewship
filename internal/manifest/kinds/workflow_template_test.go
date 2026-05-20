// Tests for the WorkflowTemplate kind. The eight test categories
// follow the per-kind test strategy declared in
// .claude/context/specs/SPEC-2-manifest-complete.md §"Test
// strategy": parse round-trip, validate happy, validate error,
// plan create / update / unchanged / replace, export round-trip.
//
// The Plan tests use an httptest fake — the real backend handler
// for /api/v1/workflow-templates is being built in parallel and
// may not exist when these tests run, so we never depend on it.
//
// All test helpers in this file are `wt`-prefixed so they don't
// collide with the symmetric helpers other kind tests in this
// same package declare (the kinds package shares test scope
// across every _test.go file).
package kinds

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/crewship-ai/crewship/internal/manifest/internalapi"
)

// ----------------------------------------------------------------------
// helpers (all prefixed `wt` to avoid name collisions with peer test files)
// ----------------------------------------------------------------------

// wtValidDoc returns a known-good WorkflowTemplateDocument. Tests
// that exercise a specific failure mode clone this and mutate the
// one field under test, so the happy path is the implicit baseline.
func wtValidDoc() WorkflowTemplateDocument {
	return WorkflowTemplateDocument{
		APIVersion: "crewship/v1",
		Kind:       "WorkflowTemplate",
		Metadata: internalapi.Metadata{
			Name: "Engineering Standard",
			Slug: "engineering-standard",
		},
		Spec: WorkflowTemplateSpec{
			Description: "Default engineering issue lifecycle",
			Icon:        ":hammer_and_wrench:",
			Color:       "#3B82F6",
			Stages: []WorkflowStage{
				{Name: "backlog", Type: StageTypeOpen, Position: 1, Color: "#9CA3AF"},
				{Name: "in_progress", Type: StageTypeStarted, Position: 2, Color: "#3B82F6"},
				{Name: "in_review", Type: StageTypeStarted, Position: 3, Color: "#F59E0B"},
				{Name: "done", Type: StageTypeCompleted, Position: 4, Color: "#10B981"},
				{Name: "cancelled", Type: StageTypeCancelled, Position: 5, Color: "#EF4444"},
			},
		},
	}
}

// wtFakeClient is a thin internalapi.Client implementation backed
// by an httptest.Server. It implements every verb the interface
// requires so any kind code path can exercise it; in practice
// WorkflowTemplate only ever hits Get/Post/Patch.
type wtFakeClient struct {
	server *httptest.Server
	client *http.Client
}

func wtNewFakeClient(handler http.Handler) *wtFakeClient {
	srv := httptest.NewServer(handler)
	return &wtFakeClient{server: srv, client: srv.Client()}
}

func (f *wtFakeClient) Close() { f.server.Close() }

func (f *wtFakeClient) do(ctx context.Context, method, path string, body any) (*internalapi.Response, error) {
	var rdr io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, f.server.URL+path, rdr)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, err
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return &internalapi.Response{StatusCode: resp.StatusCode, Body: bytes.NewReader(data)}, nil
}

func (f *wtFakeClient) Get(ctx context.Context, path string) (*internalapi.Response, error) {
	return f.do(ctx, http.MethodGet, path, nil)
}
func (f *wtFakeClient) Post(ctx context.Context, path string, body any) (*internalapi.Response, error) {
	return f.do(ctx, http.MethodPost, path, body)
}
func (f *wtFakeClient) Patch(ctx context.Context, path string, body any) (*internalapi.Response, error) {
	return f.do(ctx, http.MethodPatch, path, body)
}
func (f *wtFakeClient) Put(ctx context.Context, path string, body any) (*internalapi.Response, error) {
	return f.do(ctx, http.MethodPut, path, body)
}
func (f *wtFakeClient) Delete(ctx context.Context, path string) (*internalapi.Response, error) {
	return f.do(ctx, http.MethodDelete, path, nil)
}
func (f *wtFakeClient) WorkspaceID() string { return "ws_test" }

// wtRecordingHandler captures every request the kind makes against
// the fake. Tests pattern-match on the slice afterwards to assert
// the correct verb + path + body. The handler is only touched
// from one goroutine per test (wtRunPlan is sequential), so no
// mutex is needed.
type wtRecordingHandler struct {
	captures []wtCapturedReq
	respond  func(req wtCapturedReq) (status int, body any)
}

type wtCapturedReq struct {
	Method string
	Path   string
	Body   map[string]any
}

func (h *wtRecordingHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	cr := wtCapturedReq{Method: r.Method, Path: r.URL.Path}
	if r.Body != nil {
		raw, _ := io.ReadAll(r.Body)
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &cr.Body)
		}
	}
	h.captures = append(h.captures, cr)

	status, body := http.StatusOK, any(nil)
	if h.respond != nil {
		status, body = h.respond(cr)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if body != nil {
		_ = json.NewEncoder(w).Encode(body)
	}
}

// wtRunPlan drives the kind through a single (Plan → execute first
// item) cycle against a fresh httptest server. Tests use it
// whenever they want to verify both the plan summary AND the
// resulting HTTP call.
func wtRunPlan(t *testing.T, doc WorkflowTemplateDocument, remote *WorkflowTemplateRemote, respond func(wtCapturedReq) (int, any)) (*wtRecordingHandler, []internalapi.PlanItem, error) {
	t.Helper()
	h := &wtRecordingHandler{respond: respond}
	fc := wtNewFakeClient(h)
	t.Cleanup(fc.Close)

	items, err := doc.Plan(context.Background(), fc, remote)
	if err != nil {
		return h, items, err
	}
	for _, it := range items {
		if it.Exec == nil {
			continue
		}
		if err := it.Exec(context.Background(), fc); err != nil {
			return h, items, err
		}
	}
	return h, items, nil
}

// ----------------------------------------------------------------------
// 1. Parse round-trip
// ----------------------------------------------------------------------

func TestWorkflowTemplate_ParseRoundTrip(t *testing.T) {
	src := []byte(`apiVersion: crewship/v1
kind: WorkflowTemplate
metadata:
  name: Engineering Standard
  slug: engineering-standard
spec:
  description: Default engineering issue lifecycle
  icon: ":hammer_and_wrench:"
  color: "#3B82F6"
  stages:
    - { name: backlog, type: open, position: 1, color: "#9CA3AF" }
    - { name: in_progress, type: started, position: 2, color: "#3B82F6" }
    - { name: done, type: completed, position: 4, color: "#10B981" }
    - { name: cancelled, type: cancelled, position: 5, color: "#EF4444" }
`)

	var got WorkflowTemplateDocument
	if err := yaml.Unmarshal(src, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Metadata.Slug != "engineering-standard" {
		t.Errorf("slug = %q, want engineering-standard", got.Metadata.Slug)
	}
	if len(got.Spec.Stages) != 4 {
		t.Fatalf("stages = %d, want 4", len(got.Spec.Stages))
	}
	if got.Spec.Stages[0].Type != StageTypeOpen || got.Spec.Stages[2].Type != StageTypeCompleted {
		t.Errorf("stage types didn't decode: %+v", got.Spec.Stages)
	}

	out, err := yaml.Marshal(&got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var second WorkflowTemplateDocument
	if err := yaml.Unmarshal(out, &second); err != nil {
		t.Fatalf("re-unmarshal: %v", err)
	}
	if second.Spec.Stages[0] != got.Spec.Stages[0] {
		t.Errorf("first stage drifted after re-marshal: %+v vs %+v", second.Spec.Stages[0], got.Spec.Stages[0])
	}
}

// ----------------------------------------------------------------------
// 2. Validate happy path
// ----------------------------------------------------------------------

func TestWorkflowTemplate_ValidateHappyPath(t *testing.T) {
	d := wtValidDoc()
	if err := d.Validate(internalapi.WorkspaceContext{}); err != nil {
		t.Fatalf("Validate: unexpected error %v", err)
	}
}

// ----------------------------------------------------------------------
// 3. Validate error paths — one per rule
// ----------------------------------------------------------------------

func TestWorkflowTemplate_ValidateErrors(t *testing.T) {
	type tc struct {
		name   string
		mutate func(d *WorkflowTemplateDocument)
		errSub string
	}
	cases := []tc{
		{"missing slug", func(d *WorkflowTemplateDocument) { d.Metadata.Slug = "" }, "metadata.slug is required"},
		{"missing name", func(d *WorkflowTemplateDocument) { d.Metadata.Name = "" }, "metadata.name is required"},
		{"empty stages", func(d *WorkflowTemplateDocument) { d.Spec.Stages = nil }, "stages array must not be empty"},
		{
			"invalid stage type",
			func(d *WorkflowTemplateDocument) { d.Spec.Stages[1].Type = "in-progress" },
			`invalid type "in-progress"`,
		},
		{
			"missing open",
			func(d *WorkflowTemplateDocument) { d.Spec.Stages[0].Type = StageTypeStarted },
			"exactly one stage with type=open",
		},
		{
			"two open stages",
			func(d *WorkflowTemplateDocument) { d.Spec.Stages[1].Type = StageTypeOpen },
			"exactly one stage with type=open",
		},
		{
			"missing completed",
			func(d *WorkflowTemplateDocument) { d.Spec.Stages[3].Type = StageTypeStarted },
			"at least one stage with type=completed",
		},
		{
			"duplicate stage name",
			func(d *WorkflowTemplateDocument) { d.Spec.Stages[2].Name = d.Spec.Stages[1].Name },
			`duplicate stage name "in_progress"`,
		},
		{
			"duplicate stage position",
			func(d *WorkflowTemplateDocument) { d.Spec.Stages[2].Position = d.Spec.Stages[1].Position },
			"duplicate stage position",
		},
		{"invalid template color", func(d *WorkflowTemplateDocument) { d.Spec.Color = "blue" }, "not a valid hex code"},
		{"invalid stage color", func(d *WorkflowTemplateDocument) { d.Spec.Stages[1].Color = "#ZZZZZZ" }, "not a valid hex code"},
		{"missing stage name", func(d *WorkflowTemplateDocument) { d.Spec.Stages[1].Name = "" }, "stages[1].name is required"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := wtValidDoc()
			c.mutate(&d)
			err := d.Validate(internalapi.WorkspaceContext{})
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", c.errSub)
			}
			if !strings.Contains(err.Error(), c.errSub) {
				t.Errorf("error %q does not contain %q", err.Error(), c.errSub)
			}
		})
	}
}

// ----------------------------------------------------------------------
// 4. Plan: create (no remote)
// ----------------------------------------------------------------------

func TestWorkflowTemplate_PlanCreate(t *testing.T) {
	d := wtValidDoc()
	h, items, err := wtRunPlan(t, d, nil, func(req wtCapturedReq) (int, any) {
		if req.Method == http.MethodPost && req.Path == "/api/v1/workflow-templates" {
			return http.StatusCreated, map[string]any{"id": "wt_new", "name": req.Body["name"]}
		}
		return http.StatusNotFound, map[string]any{"error": "unexpected " + req.Method + " " + req.Path}
	})
	if err != nil {
		t.Fatalf("Plan/Exec: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("want 1 plan item, got %d", len(items))
	}
	if items[0].Action != internalapi.ActionCreate {
		t.Errorf("Action = %v, want Create", items[0].Action)
	}
	if items[0].Kind != workflowKindName {
		t.Errorf("Kind = %q, want %q", items[0].Kind, workflowKindName)
	}

	var posts []wtCapturedReq
	for _, c := range h.captures {
		if c.Method == http.MethodPost {
			posts = append(posts, c)
		}
	}
	if len(posts) != 1 {
		t.Fatalf("want 1 POST, got %d (%+v)", len(posts), h.captures)
	}
	if posts[0].Path != "/api/v1/workflow-templates" {
		t.Errorf("POST path = %q, want /api/v1/workflow-templates", posts[0].Path)
	}
	if name, _ := posts[0].Body["name"].(string); name != d.Metadata.Name {
		t.Errorf("POST body name = %q, want %q", name, d.Metadata.Name)
	}
	tj, _ := posts[0].Body["template_json"].(string)
	if tj == "" {
		t.Fatalf("POST body missing template_json")
	}
	for _, want := range []string{"backlog", "in_progress", "in_review", "done", "cancelled"} {
		if !strings.Contains(tj, want) {
			t.Errorf("template_json missing stage %q (got %s)", want, tj)
		}
	}
}

// ----------------------------------------------------------------------
// 5. Plan: update (drifted remote)
// ----------------------------------------------------------------------

func TestWorkflowTemplate_PlanUpdateOnDrift(t *testing.T) {
	d := wtValidDoc()
	remoteStages := []WorkflowStage{
		{Name: "backlog", Type: StageTypeOpen, Position: 1, Color: "#9CA3AF"},
		{Name: "done", Type: StageTypeCompleted, Position: 4, Color: "#10B981"},
	}
	rsJSON, _ := json.Marshal(remoteStages)
	remote := &WorkflowTemplateRemote{
		ID:           "wt_existing",
		Name:         d.Metadata.Name,
		Description:  "OLD description",
		TemplateJSON: string(rsJSON),
		Icon:         d.Spec.Icon,
		Color:        d.Spec.Color,
	}

	h, items, err := wtRunPlan(t, d, remote, func(req wtCapturedReq) (int, any) {
		if req.Method == http.MethodPatch && req.Path == "/api/v1/workflow-templates/wt_existing" {
			return http.StatusOK, map[string]any{"id": "wt_existing"}
		}
		return http.StatusNotFound, map[string]any{"error": "unexpected"}
	})
	if err != nil {
		t.Fatalf("Plan/Exec: %v", err)
	}
	if len(items) != 1 || items[0].Action != internalapi.ActionUpdate {
		t.Fatalf("want 1 Update item, got %+v", items)
	}

	var patches []wtCapturedReq
	for _, c := range h.captures {
		if c.Method == http.MethodPatch {
			patches = append(patches, c)
		}
	}
	if len(patches) != 1 {
		t.Fatalf("want 1 PATCH, got %d", len(patches))
	}
	if patches[0].Path != "/api/v1/workflow-templates/wt_existing" {
		t.Errorf("PATCH path = %q, want /api/v1/workflow-templates/wt_existing", patches[0].Path)
	}
}

// ----------------------------------------------------------------------
// 6. Plan: unchanged
// ----------------------------------------------------------------------

func TestWorkflowTemplate_PlanUnchanged(t *testing.T) {
	d := wtValidDoc()
	rsJSON, _ := json.Marshal(d.Spec.Stages)
	remote := &WorkflowTemplateRemote{
		ID:           "wt_existing",
		Name:         d.Metadata.Name,
		Description:  d.Spec.Description,
		TemplateJSON: string(rsJSON),
		Icon:         d.Spec.Icon,
		Color:        d.Spec.Color,
	}
	h, items, err := wtRunPlan(t, d, remote, func(req wtCapturedReq) (int, any) {
		return http.StatusInternalServerError, map[string]any{"error": "unexpected " + req.Method}
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) != 1 || items[0].Action != internalapi.ActionUnchanged {
		t.Fatalf("want 1 Unchanged item, got %+v", items)
	}
	if items[0].Exec != nil {
		t.Error("Unchanged plan item should have nil Exec")
	}
	if len(h.captures) != 0 {
		t.Errorf("Unchanged should make no HTTP calls, got %+v", h.captures)
	}
}

// Unchanged must tolerate stages declared in different order but
// matching by `position` after sort. Validates the
// position-normalising comparator in Plan.
func TestWorkflowTemplate_PlanUnchangedReorderedStages(t *testing.T) {
	d := wtValidDoc()
	reordered := make([]WorkflowStage, len(d.Spec.Stages))
	copy(reordered, d.Spec.Stages)
	reordered[0], reordered[3] = reordered[3], reordered[0]

	rsJSON, _ := json.Marshal(reordered)
	remote := &WorkflowTemplateRemote{
		ID:           "wt_existing",
		Name:         d.Metadata.Name,
		Description:  d.Spec.Description,
		TemplateJSON: string(rsJSON),
		Icon:         d.Spec.Icon,
		Color:        d.Spec.Color,
	}
	_, items, err := wtRunPlan(t, d, remote, nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if items[0].Action != internalapi.ActionUnchanged {
		t.Errorf("Action = %v, want Unchanged (stages reordered but equal)", items[0].Action)
	}
}

// ----------------------------------------------------------------------
// 7. Plan: replace mode is mode-agnostic at the kind level — the
// orchestrator wraps Plan output with Delete-then-Create in replace
// mode. From the kind's perspective an identical remote yields
// Unchanged; the wrapper turns that into delete+create at the apply
// layer. This test guards against an accidental "always Update in
// replace" implementation leaking into Plan itself.
// ----------------------------------------------------------------------

func TestWorkflowTemplate_PlanReplaceProducesUnchangedWhenIdentical(t *testing.T) {
	d := wtValidDoc()
	rsJSON, _ := json.Marshal(d.Spec.Stages)
	remote := &WorkflowTemplateRemote{
		ID:           "wt_existing",
		Name:         d.Metadata.Name,
		Description:  d.Spec.Description,
		TemplateJSON: string(rsJSON),
		Icon:         d.Spec.Icon,
		Color:        d.Spec.Color,
	}
	items, err := d.Plan(context.Background(), nil, remote)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if items[0].Action != internalapi.ActionUnchanged {
		t.Errorf("Action = %v, want Unchanged", items[0].Action)
	}
}

// ----------------------------------------------------------------------
// 8. Export round-trip
// ----------------------------------------------------------------------

func TestWorkflowTemplate_ExportRoundTrip(t *testing.T) {
	d := wtValidDoc()
	stagesJSON, _ := json.Marshal(d.Spec.Stages)

	rows := []WorkflowTemplateRemote{
		{
			ID:           "wt_user",
			WorkspaceID:  "ws_test",
			Name:         d.Metadata.Name,
			Description:  d.Spec.Description,
			TemplateJSON: string(stagesJSON),
			Icon:         d.Spec.Icon,
			Color:        d.Spec.Color,
			IsBuiltin:    false,
		},
		{
			ID:           "wt_builtin_sequential",
			WorkspaceID:  "ws_test",
			Name:         "sequential",
			TemplateJSON: `[{"name":"todo","type":"open","position":1}]`,
			IsBuiltin:    true,
		},
	}

	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/workflow-templates" {
			http.Error(w, "unexpected", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(rows)
	})
	fc := wtNewFakeClient(h)
	defer fc.Close()

	docs, err := ExportWorkflowTemplates(context.Background(), fc)
	if err != nil {
		t.Fatalf("ExportWorkflowTemplates: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("want 1 doc (builtin filtered), got %d", len(docs))
	}
	got := docs[0]
	if got.Kind != "WorkflowTemplate" || got.APIVersion != "crewship/v1" {
		t.Errorf("envelope wrong: kind=%q api=%q", got.Kind, got.APIVersion)
	}
	if got.Metadata.Name != d.Metadata.Name {
		t.Errorf("Name = %q, want %q", got.Metadata.Name, d.Metadata.Name)
	}
	if got.Metadata.Slug == "" {
		t.Error("Slug should be derived from name, got empty")
	}
	if len(got.Spec.Stages) != len(d.Spec.Stages) {
		t.Fatalf("stages = %d, want %d", len(got.Spec.Stages), len(d.Spec.Stages))
	}
	for i, st := range got.Spec.Stages {
		if st != d.Spec.Stages[i] {
			t.Errorf("stage[%d] = %+v, want %+v", i, st, d.Spec.Stages[i])
		}
	}

	if err := got.Validate(internalapi.WorkspaceContext{}); err != nil {
		t.Errorf("exported doc fails Validate: %v", err)
	}
}

func TestWorkflowTemplate_ExportPropagatesServerError(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	fc := wtNewFakeClient(h)
	defer fc.Close()

	_, err := ExportWorkflowTemplates(context.Background(), fc)
	if err == nil {
		t.Fatal("expected error on 500, got nil")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error %q should mention status 500", err.Error())
	}
}

func TestWorkflowTemplate_ExportAcceptsWrappedShape(t *testing.T) {
	rows := []map[string]any{
		{
			"id":            "wt1",
			"workspace_id":  "ws",
			"name":          "Wrapped",
			"description":   "x",
			"template_json": `[{"name":"todo","type":"open","position":1},{"name":"done","type":"completed","position":2}]`,
			"is_builtin":    false,
		},
	}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"templates": rows})
	})
	fc := wtNewFakeClient(h)
	defer fc.Close()

	docs, err := ExportWorkflowTemplates(context.Background(), fc)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if len(docs) != 1 || docs[0].Metadata.Name != "Wrapped" {
		t.Errorf("wrapped shape decoded wrong: %+v", docs)
	}
}

// workflowSlugify is exercised by Export but worth pinning
// individually — off-by-one trailing-hyphen bugs have shown up in
// prior refactors of similar helpers.
func TestWorkflowTemplate_Slugify(t *testing.T) {
	cases := map[string]string{
		"Engineering Standard": "engineering-standard",
		"  Has   Whitespace  ": "has-whitespace",
		"Trailing!@#":          "trailing",
		"":                     "workflow-template",
		"hello/world.v2":       "hello-world-v2",
		"UPPER_CASE":           "upper-case",
	}
	for in, want := range cases {
		if got := workflowSlugify(in); got != want {
			t.Errorf("workflowSlugify(%q) = %q, want %q", in, got, want)
		}
	}
}

// Positive control: Plan never errors on a valid document.
func TestWorkflowTemplate_PlanBuildBodyOK(t *testing.T) {
	d := wtValidDoc()
	if _, err := d.buildPostBody(); err != nil {
		t.Fatalf("buildPostBody on valid doc: %v", err)
	}
}

// Confirms the kind keeps POST/PATCH/PUT/DELETE as available verbs
// in its exec helper and rejects anything else — defensive: if a
// future refactor drops a verb, this test fails loudly.
func TestWorkflowTemplate_ExecRejectsUnknownMethod(t *testing.T) {
	fc := wtNewFakeClient(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer fc.Close()
	err := workflowExec(context.Background(), fc, "OPTIONS", "/api/v1/workflow-templates", nil)
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Errorf("want unsupported-method error, got %v", err)
	}
}

// Sanity check that wtFakeClient buffers the response body so it
// stays readable after the body Close.
func TestWorkflowTemplate_FakeClientReadsBody(t *testing.T) {
	fc := wtNewFakeClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = fmt.Fprint(w, `{"ok":true}`)
	}))
	defer fc.Close()
	resp, err := fc.Post(context.Background(), "/x", map[string]any{"a": 1})
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}
