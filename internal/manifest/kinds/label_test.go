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

// labelFakeClient is a minimal in-memory internalapi.Client for testing
// LabelDocument's Plan/Export paths. It records every call so tests
// can assert what the manifest layer would have sent over the wire
// without spinning up an httptest server.
//
// The fake intentionally mirrors the shape of the real backend
// labelResponse: id, name, color, label_group, (optional)
// description. Rows are keyed by name (the backend's natural key),
// which is also the slug under the slug==name invariant.
type labelFakeClient struct {
	t       *testing.T
	wsID    string
	rows    map[string]LabelRemote // keyed by name
	nextID  int
	calls   []labelFakeCall
	listErr error // injected to test error paths
}

type labelFakeCall struct {
	Method string
	Path   string
	Body   any
}

func newLabelFakeClient(t *testing.T) *labelFakeClient {
	t.Helper()
	return &labelFakeClient{
		t:    t,
		wsID: "ws_test",
		rows: map[string]LabelRemote{},
	}
}

func (f *labelFakeClient) WorkspaceID() string { return f.wsID }

func (f *labelFakeClient) record(method, path string, body any) {
	f.calls = append(f.calls, labelFakeCall{Method: method, Path: path, Body: body})
}

func (f *labelFakeClient) respond(status int, v any) *internalapi.Response {
	data, _ := json.Marshal(v)
	return &internalapi.Response{
		StatusCode: status,
		Body:       bytes.NewReader(data),
	}
}

func (f *labelFakeClient) Get(_ context.Context, path string) (*internalapi.Response, error) {
	f.record("GET", path, nil)
	if f.listErr != nil {
		return nil, f.listErr
	}
	if path == "/api/v1/labels" {
		out := make([]LabelRemote, 0, len(f.rows))
		for _, r := range f.rows {
			out = append(out, r)
		}
		return f.respond(200, out), nil
	}
	return f.respond(404, map[string]any{"error": "not found"}), nil
}

func (f *labelFakeClient) Post(_ context.Context, path string, body any) (*internalapi.Response, error) {
	f.record("POST", path, body)
	if path == "/api/v1/labels" {
		b, _ := body.(map[string]any)
		f.nextID++
		name, _ := b["name"].(string)
		color, _ := b["color"].(string)
		desc, _ := b["description"].(string)
		row := LabelRemote{
			ID:          labelStringifyID(f.nextID),
			Name:        name,
			Color:       color,
			Description: desc,
		}
		f.rows[name] = row
		return f.respond(201, row), nil
	}
	return f.respond(404, nil), nil
}

func (f *labelFakeClient) Patch(_ context.Context, path string, body any) (*internalapi.Response, error) {
	f.record("PATCH", path, body)
	return f.respond(200, body), nil
}

func (f *labelFakeClient) Put(_ context.Context, path string, body any) (*internalapi.Response, error) {
	f.record("PUT", path, body)
	return f.respond(200, body), nil
}

func (f *labelFakeClient) Delete(_ context.Context, path string) (*internalapi.Response, error) {
	f.record("DELETE", path, nil)
	return f.respond(204, nil), nil
}

func labelStringifyID(n int) string {
	switch {
	case n < 10:
		return "lbl_000" + labelIntStr(n)
	case n < 100:
		return "lbl_00" + labelIntStr(n)
	default:
		return "lbl_" + labelIntStr(n)
	}
}

func labelIntStr(n int) string {
	if n == 0 {
		return "0"
	}
	out := ""
	for n > 0 {
		out = string(rune('0'+n%10)) + out
		n /= 10
	}
	return out
}

// labelSampleDoc returns a fully-specified LabelDocument that every test
// can shallow-copy and tweak. Keeping construction in one place
// means a future schema field doesn't require touching every test.
func labelSampleDoc() LabelDocument {
	return LabelDocument{
		APIVersion: "crewship/v1",
		Kind:       "Label",
		Metadata: internalapi.Metadata{
			Name: "bug",
			Slug: "bug",
		},
		Spec: LabelSpec{
			Color:       "#EF4444",
			Description: "Something is broken",
		},
	}
}

// ── 1. Parse round-trip ──────────────────────────────────────────────

func TestLabel_ParseRoundTrip(t *testing.T) {
	yamlIn := `apiVersion: crewship/v1
kind: Label
metadata:
  name: bug
  slug: bug
spec:
  color: "#EF4444"
  description: Something is broken
`
	var doc LabelDocument
	if err := yaml.Unmarshal([]byte(yamlIn), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if doc.Metadata.Name != "bug" || doc.Metadata.Slug != "bug" {
		t.Errorf("metadata mismatch: %+v", doc.Metadata)
	}
	if doc.Spec.Color != "#EF4444" {
		t.Errorf("color = %q, want #EF4444", doc.Spec.Color)
	}
	if doc.Spec.Description != "Something is broken" {
		t.Errorf("description = %q", doc.Spec.Description)
	}

	out, err := yaml.Marshal(&doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var rt LabelDocument
	if err := yaml.Unmarshal(out, &rt); err != nil {
		t.Fatalf("re-unmarshal: %v", err)
	}
	// Field-by-field compare; LabelDocument is not comparable with ==
	// because Metadata embeds a map (Labels).
	if rt.APIVersion != doc.APIVersion ||
		rt.Kind != doc.Kind ||
		rt.Metadata.Name != doc.Metadata.Name ||
		rt.Metadata.Slug != doc.Metadata.Slug ||
		rt.Metadata.Description != doc.Metadata.Description ||
		rt.Spec != doc.Spec {
		t.Errorf("round-trip mismatch:\n  before=%+v\n  after =%+v", doc, rt)
	}
}

// ── 2. Validate happy path ───────────────────────────────────────────

func TestLabel_Validate_HappyPath(t *testing.T) {
	doc := labelSampleDoc()
	if err := doc.Validate(internalapi.WorkspaceContext{}); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

func TestLabel_Validate_NoColorOK(t *testing.T) {
	// Color is required by the backend but Validate's job is purely
	// structural — the empty-color case is allowed here so the
	// server-side 400 surfaces the real issue. The test pins that
	// behaviour so a future tightening of Validate is intentional.
	doc := labelSampleDoc()
	doc.Spec.Color = ""
	if err := doc.Validate(internalapi.WorkspaceContext{}); err != nil {
		t.Errorf("Validate with empty color should pass, got: %v", err)
	}
}

// ── 3. Validate error paths ──────────────────────────────────────────

func TestLabel_Validate_Errors(t *testing.T) {
	cases := []struct {
		name        string
		mutate      func(*LabelDocument)
		wantContain string
	}{
		{
			name:        "missing name",
			mutate:      func(d *LabelDocument) { d.Metadata.Name = "" },
			wantContain: "metadata.name is required",
		},
		{
			name:        "missing slug",
			mutate:      func(d *LabelDocument) { d.Metadata.Slug = "" },
			wantContain: "metadata.slug is required",
		},
		{
			// The load-bearing invariant test: cross-kind FKs use
			// slug, the backend keys on name, and the spec mandates
			// slug == name to keep both consistent.
			name:        "slug != name",
			mutate:      func(d *LabelDocument) { d.Metadata.Slug = "bug-slug" },
			wantContain: "metadata.slug must equal metadata.name",
		},
		{
			name:        "bad color (too short)",
			mutate:      func(d *LabelDocument) { d.Spec.Color = "#FFF" },
			wantContain: "^#[0-9A-Fa-f]{6}$",
		},
		{
			name:        "bad color (no hash)",
			mutate:      func(d *LabelDocument) { d.Spec.Color = "EF4444" },
			wantContain: "^#[0-9A-Fa-f]{6}$",
		},
		{
			name:        "bad color (non-hex chars)",
			mutate:      func(d *LabelDocument) { d.Spec.Color = "#ZZZZZZ" },
			wantContain: "^#[0-9A-Fa-f]{6}$",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := labelSampleDoc()
			tc.mutate(&doc)
			err := doc.Validate(internalapi.WorkspaceContext{})
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantContain) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantContain)
			}
		})
	}
}

// ── 4. Plan: create (no remote) ──────────────────────────────────────

func TestLabel_Plan_Create(t *testing.T) {
	doc := labelSampleDoc()
	fake := newLabelFakeClient(t)

	items, err := doc.Plan(context.Background(), fake, nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 plan item, got %d", len(items))
	}
	if items[0].Action != internalapi.ActionCreate {
		t.Errorf("Action = %v, want Create", items[0].Action)
	}
	if items[0].Exec == nil {
		t.Fatal("Create item must have Exec")
	}
	// Run the exec so we can verify the POST body shape.
	if err := items[0].Exec(context.Background(), fake); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if len(fake.calls) != 1 {
		t.Fatalf("expected 1 REST call, got %d", len(fake.calls))
	}
	call := fake.calls[0]
	if call.Method != "POST" || call.Path != "/api/v1/labels" {
		t.Errorf("call mismatch: %s %s", call.Method, call.Path)
	}
	body, ok := call.Body.(map[string]any)
	if !ok {
		t.Fatalf("body type %T", call.Body)
	}
	if body["name"] != "bug" || body["color"] != "#EF4444" {
		t.Errorf("body = %v", body)
	}
	if body["description"] != "Something is broken" {
		t.Errorf("expected description in POST body, got %v", body["description"])
	}
}

// ── 5. Plan: update (drifted remote) ─────────────────────────────────

func TestLabel_Plan_Update(t *testing.T) {
	doc := labelSampleDoc()
	remote := &LabelRemote{
		ID:          "lbl_42",
		Name:        "bug",
		Color:       "#000000", // drifted
		Description: "stale",   // drifted
	}
	fake := newLabelFakeClient(t)

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
	// Verify the PATCH targeted the right ID and carried only the
	// drifted fields.
	var patched labelFakeCall
	for _, c := range fake.calls {
		if c.Method == "PATCH" {
			patched = c
		}
	}
	if patched.Path != "/api/v1/labels/lbl_42" {
		t.Errorf("PATCH path = %q", patched.Path)
	}
	body, ok := patched.Body.(map[string]any)
	if !ok {
		t.Fatalf("PATCH body type %T", patched.Body)
	}
	if body["color"] != "#EF4444" {
		t.Errorf("PATCH should set color=#EF4444, got %v", body["color"])
	}
	if body["description"] != "Something is broken" {
		t.Errorf("PATCH should set description, got %v", body["description"])
	}
	if _, hasName := body["name"]; hasName {
		t.Errorf("PATCH should NOT include name (no drift): %v", body)
	}
}

// ── 6. Plan: unchanged ───────────────────────────────────────────────

func TestLabel_Plan_Unchanged(t *testing.T) {
	doc := labelSampleDoc()
	remote := &LabelRemote{
		ID:          "lbl_99",
		Name:        "bug",
		Color:       "#EF4444",
		Description: "Something is broken",
	}
	fake := newLabelFakeClient(t)

	items, err := doc.Plan(context.Background(), fake, remote)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 plan item, got %d", len(items))
	}
	if items[0].Action != internalapi.ActionUnchanged {
		t.Errorf("Action = %v, want Unchanged", items[0].Action)
	}
	if items[0].Exec != nil {
		t.Error("Unchanged items must have nil Exec")
	}
}

// ── 7. Plan: delete on ApplyReplace ──────────────────────────────────
//
// LabelDocument.Plan itself never emits a Delete; the Replace pass
// in apply.go uses DeletePlanItem to drop the existing row before
// the Create runs. This test exercises that helper.

func TestLabel_DeletePlanItem(t *testing.T) {
	remote := LabelRemote{ID: "lbl_42", Name: "bug", Color: "#000000"}
	item := DeletePlanItem(remote)

	if item.Action != internalapi.ActionDelete {
		t.Errorf("Action = %v, want Delete", item.Action)
	}
	if item.Slug != "bug" {
		t.Errorf("Slug = %q, want bug", item.Slug)
	}
	fake := newLabelFakeClient(t)
	if err := item.Exec(context.Background(), fake); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	var del labelFakeCall
	for _, c := range fake.calls {
		if c.Method == "DELETE" {
			del = c
		}
	}
	if del.Path != "/api/v1/labels/lbl_42" {
		t.Errorf("DELETE path = %q", del.Path)
	}
}

// ── 8. Export round-trip ─────────────────────────────────────────────

func TestLabel_Export_RoundTrip(t *testing.T) {
	fake := newLabelFakeClient(t)
	fake.rows["bug"] = LabelRemote{
		ID:          "lbl_001",
		Name:        "bug",
		Color:       "#EF4444",
		Description: "Something is broken",
	}
	fake.rows["urgent"] = LabelRemote{
		ID:    "lbl_002",
		Name:  "urgent",
		Color: "#F59E0B",
	}

	docs, err := ExportLabels(context.Background(), fake)
	if err != nil {
		t.Fatalf("ExportLabels: %v", err)
	}
	if len(docs) != 2 {
		t.Fatalf("expected 2 documents, got %d", len(docs))
	}

	// Round-trip: every exported document must pass Validate (this
	// catches the slug==name invariant on the export side).
	for _, d := range docs {
		if err := d.Validate(internalapi.WorkspaceContext{}); err != nil {
			t.Errorf("exported doc %q failed Validate: %v", d.Metadata.Name, err)
		}
		if d.Metadata.Slug != d.Metadata.Name {
			t.Errorf("export must satisfy slug==name (got slug=%q name=%q)",
				d.Metadata.Slug, d.Metadata.Name)
		}
		if d.APIVersion != "crewship/v1" || d.Kind != "Label" {
			t.Errorf("envelope = %+v", d)
		}
	}

	// And the marshalled YAML must parse back into an equivalent
	// document. Find the "bug" doc explicitly — ExportLabels has no
	// ordering guarantee because the fake's map iteration is
	// non-deterministic (and the real backend has only emitted ASC
	// ordering by convention, not by contract).
	var bugDoc *LabelDocument
	for _, d := range docs {
		if d.Metadata.Name == "bug" {
			bugDoc = d
			break
		}
	}
	if bugDoc == nil {
		t.Fatalf("exported docs did not contain 'bug': %+v", docs)
	}
	out, err := yaml.Marshal(bugDoc)
	if err != nil {
		t.Fatalf("marshal exported doc: %v", err)
	}
	var rt LabelDocument
	if err := yaml.Unmarshal(out, &rt); err != nil {
		t.Fatalf("unmarshal exported yaml: %v", err)
	}
	if rt.Metadata.Name != "bug" || rt.Spec.Color == "" {
		t.Errorf("round-trip lost fields: %+v", rt)
	}
}

// ── Bonus: export propagates a transport error ────────────────────────

func TestLabel_Export_TransportError(t *testing.T) {
	fake := newLabelFakeClient(t)
	fake.listErr = io.ErrUnexpectedEOF

	if _, err := ExportLabels(context.Background(), fake); err == nil {
		t.Fatal("expected error, got nil")
	}
}
