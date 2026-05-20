package manifest

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// kindsFakeAPI is a focused HTTP fake for the SPEC-2 kinds dispatch.
// It deliberately only handles the routes the new-kind tests exercise
// (Projects + Labels are enough to prove the wiring); kinds whose
// servers don't exist yet (workflow-templates etc.) get tested at the
// kinds-package level, not here.
type kindsFakeAPI struct {
	wsID     string
	projects []map[string]any
	labels   []map[string]any
	Calls    []fakeCall
}

func newKindsFakeAPI() *kindsFakeAPI {
	return &kindsFakeAPI{wsID: "ws_test"}
}

func (f *kindsFakeAPI) GetWorkspaceID() string { return f.wsID }

func (f *kindsFakeAPI) record(method, path string, body any) {
	bmap, _ := body.(map[string]any)
	f.Calls = append(f.Calls, fakeCall{Method: method, Path: path, Body: bmap})
}

func (f *kindsFakeAPI) Get(_ context.Context, path string) (*http.Response, error) {
	f.record("GET", path, nil)
	switch {
	case path == "/api/v1/projects":
		return jsonResp(200, f.projects), nil
	case path == "/api/v1/labels":
		return jsonResp(200, f.labels), nil
	case path == "/api/v1/agents", strings.HasPrefix(path, "/api/v1/agents?"):
		// Project.Export resolves lead_agent_id → slug via /api/v1/agents.
		// We don't need agent rows for the create-side tests, but the
		// endpoint must respond cleanly so Plan's pre-flight doesn't 404.
		return jsonResp(200, []map[string]any{}), nil
	}
	return jsonResp(404, map[string]any{"error": "not found"}), nil
}

func (f *kindsFakeAPI) Post(_ context.Context, path string, body any) (*http.Response, error) {
	f.record("POST", path, body)
	bmap, _ := body.(map[string]any)
	switch path {
	case "/api/v1/projects":
		row := cloneMap(bmap)
		row["id"] = "proj_001"
		f.projects = append(f.projects, row)
		return jsonResp(201, row), nil
	case "/api/v1/labels":
		row := cloneMap(bmap)
		row["id"] = "lbl_001"
		f.labels = append(f.labels, row)
		return jsonResp(201, row), nil
	}
	return jsonResp(404, map[string]any{"error": "not found"}), nil
}

func (f *kindsFakeAPI) Patch(_ context.Context, path string, body any) (*http.Response, error) {
	f.record("PATCH", path, body)
	return jsonResp(200, body), nil
}

func (f *kindsFakeAPI) Delete(_ context.Context, path string) (*http.Response, error) {
	f.record("DELETE", path, nil)
	return jsonResp(204, nil), nil
}

func jsonResp(code int, v any) *http.Response {
	data, _ := json.Marshal(v)
	return &http.Response{
		StatusCode: code,
		Body:       io.NopCloser(bytes.NewReader(data)),
		Header:     http.Header{},
	}
}

func cloneMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// TestBuildPlan_NewKindsRouting is the smoke test for SPEC-2 dispatch:
// a single-Project manifest now produces a Create plan item, where
// previously the new-kind slices were populated by parse but never
// reached the planner. Three assertions pin the contract:
//
//  1. Plan exists and contains the expected number of items.
//  2. The item carries the SPEC-2 kind name ("Project"), not the
//     legacy lowercase string ("project") — kindOrder + the CLI's
//     filter both key on this.
//  3. Action is Create on a clean workspace (no remote project with
//     the same slug).
func TestBuildPlan_NewKindsRouting(t *testing.T) {
	body := []byte(`
apiVersion: crewship/v1
kind: Project
metadata: { name: Q2, slug: q2-launch }
spec: { status: active, priority: high }
`)
	bundle, err := Load(body)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(bundle.Projects) != 1 {
		t.Fatalf("expected 1 project, got %d", len(bundle.Projects))
	}

	api := newKindsFakeAPI()
	client := NewClient(api)
	plan, err := BuildPlan(context.Background(), client, bundle, Options{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if len(plan.Items) == 0 {
		t.Fatalf("plan is empty — dispatch never reached project; calls=%+v", api.Calls)
	}
	found := false
	for _, it := range plan.Items {
		if it.Kind == "Project" {
			found = true
			if it.Action != ActionCreate {
				t.Errorf("Project: want ActionCreate (no remote), got %v", it.Action)
			}
		}
	}
	if !found {
		t.Errorf("no Project item in plan; items=%+v", plan.Items)
	}
}

// TestApply_NewKindsExecutesPOST proves the dispatch path actually
// issues the create request to the server. This is the regression
// guard against "plan looks right but Apply is a no-op" — the bug
// pattern where the closure is wired but never called.
func TestApply_NewKindsExecutesPOST(t *testing.T) {
	body := []byte(`
apiVersion: crewship/v1
kind: Project
metadata: { name: Q2, slug: q2-launch }
spec: { status: active }
`)
	bundle, _ := Load(body)
	api := newKindsFakeAPI()
	client := NewClient(api)

	res, err := Apply(context.Background(), client, bundle, Options{Yes: true})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.Created == 0 {
		t.Fatalf("expected Created>=1, got %d (LastError=%v)", res.Created, res.LastError)
	}

	// Find the POST /api/v1/projects call and verify its body shape.
	var postBody map[string]any
	for _, c := range api.Calls {
		if c.Method == "POST" && c.Path == "/api/v1/projects" {
			postBody = c.Body
			break
		}
	}
	if postBody == nil {
		t.Fatalf("no POST /api/v1/projects call recorded; calls=%+v", api.Calls)
	}
	if got, _ := postBody["name"].(string); got != "Q2" {
		t.Errorf("POST body name = %q, want Q2", got)
	}
	if got, _ := postBody["slug"].(string); got != "q2-launch" {
		t.Errorf("POST body slug = %q, want q2-launch", got)
	}
}

// TestBuildPlan_ValidateAggregatesKindErrors confirms validateAllKinds
// collects errors across multiple bad documents rather than failing
// on the first one — that's what gives operators a "fix all 3 typos
// at once" experience instead of whack-a-mole.
func TestBuildPlan_ValidateAggregatesKindErrors(t *testing.T) {
	// Project with empty status (not in enum) AND a Milestone whose
	// project_slug doesn't resolve. Both should appear in the error.
	body := []byte(`
apiVersion: crewship/v1
kind: Project
metadata: { name: Bad, slug: bad-proj }
spec: { status: not-a-real-status }
---
apiVersion: crewship/v1
kind: Milestone
metadata: { name: Lonely, slug: lonely }
spec: { project_slug: does-not-exist }
`)
	bundle, err := Load(body)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	api := newKindsFakeAPI()
	client := NewClient(api)
	_, err = BuildPlan(context.Background(), client, bundle, Options{})
	if err == nil {
		t.Fatal("expected validation error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "bad-proj") || !strings.Contains(msg, "lonely") {
		t.Errorf("error should mention both bad slugs, got: %s", msg)
	}
}
