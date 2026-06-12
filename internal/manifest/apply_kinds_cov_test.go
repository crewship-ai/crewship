package manifest

import (
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/manifest/internalapi"
)

func TestMapKindAction(t *testing.T) {
	cases := []struct {
		in   internalapi.PlanAction
		want Action
	}{
		{internalapi.ActionCreate, ActionCreate},
		{internalapi.ActionUpdate, ActionUpdate},
		{internalapi.ActionDelete, ActionDelete},
		{internalapi.ActionUnchanged, ActionUnchanged},
	}
	for _, tc := range cases {
		if got := mapKindAction(tc.in); got != tc.want {
			t.Errorf("mapKindAction(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestPlanItemDesc(t *testing.T) {
	withDesc := internalapi.PlanItem{Slug: "s", Description: "long description"}
	if got := planItemDesc(withDesc); got != "long description" {
		t.Errorf("description should win, got %q", got)
	}
	slugOnly := internalapi.PlanItem{Slug: "s"}
	if got := planItemDesc(slugOnly); got != "s" {
		t.Errorf("slug fallback broken, got %q", got)
	}
}

func TestJoinLines(t *testing.T) {
	if got := joinLines(nil); got != "" {
		t.Errorf("empty input should yield empty string, got %q", got)
	}
	if got := joinLines([]string{"a"}); got != "a" {
		t.Errorf("single line, got %q", got)
	}
	if got := joinLines([]string{"a", "b", "c"}); got != "a\nb\nc" {
		t.Errorf("multi line, got %q", got)
	}
}

func TestWrapKindExec_NilInnerStaysNil(t *testing.T) {
	if wrapKindExec(nil, NewClient(newCovStub())) != nil {
		t.Error("nil inner closure must wrap to nil so unchanged items skip exec")
	}
}

func TestWrapKindExec_RunsInnerWithAdapter(t *testing.T) {
	var sawClient internalapi.Client
	inner := func(_ context.Context, c internalapi.Client) error {
		sawClient = c
		return nil
	}
	wrapped := wrapKindExec(inner, NewClient(newCovStub()))
	if wrapped == nil {
		t.Fatal("non-nil inner must produce a wrapper")
	}
	if err := wrapped(context.Background(), nil, Options{}); err != nil {
		t.Fatalf("wrapped exec: %v", err)
	}
	if sawClient == nil {
		t.Error("inner closure should receive an adapted client")
	}
}

func TestPlanNewKinds_NilClientGuard(t *testing.T) {
	pb := &planBuilder{client: nil, plan: &Plan{}}
	err := pb.planNewKinds(context.Background(), &Bundle{})
	if err == nil || !strings.Contains(err.Error(), "nil client") {
		t.Fatalf("want nil-client guard, got %v", err)
	}
}

// kindsCovStub answers every GET with an empty list and every mutation
// with a created/ok body, so each kind's remote lookup reports "not
// found" and Plan emits a Create. Per-path overrides allow failure
// injection for the lookup error paths.
type kindsCovStub struct {
	overrides map[string]covRoute // "GET /path" → response
	calls     []fakeCall
}

func newKindsCovStub() *kindsCovStub {
	return &kindsCovStub{overrides: map[string]covRoute{}}
}

func (s *kindsCovStub) on(method, path string, status int, body string) {
	s.overrides[method+" "+path] = covRoute{status: status, body: body}
}

func (s *kindsCovStub) respond(method, path string, body any) (*http.Response, error) {
	bmap, _ := body.(map[string]any)
	s.calls = append(s.calls, fakeCall{Method: method, Path: path, Body: bmap})
	if r, ok := s.overrides[method+" "+path]; ok {
		if r.err != nil {
			return nil, r.err
		}
		return &http.Response{StatusCode: r.status, Body: io.NopCloser(strings.NewReader(r.body)), Header: http.Header{}}, nil
	}
	switch method {
	case "GET":
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`[]`)), Header: http.Header{}}, nil
	case "POST":
		return &http.Response{StatusCode: 201, Body: io.NopCloser(strings.NewReader(`{"id":"new_1"}`)), Header: http.Header{}}, nil
	default:
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{}`)), Header: http.Header{}}, nil
	}
}

func (s *kindsCovStub) Get(_ context.Context, path string) (*http.Response, error) {
	return s.respond("GET", path, nil)
}
func (s *kindsCovStub) Post(_ context.Context, path string, body any) (*http.Response, error) {
	return s.respond("POST", path, body)
}
func (s *kindsCovStub) Patch(_ context.Context, path string, body any) (*http.Response, error) {
	return s.respond("PATCH", path, body)
}
func (s *kindsCovStub) Delete(_ context.Context, path string) (*http.Response, error) {
	return s.respond("DELETE", path, nil)
}
func (s *kindsCovStub) GetWorkspaceID() string { return "ws_kinds" }

func TestBuildPlan_MultiKindDispatch(t *testing.T) {
	body := []byte(`
apiVersion: crewship/v1
kind: Project
metadata: { name: Q2, slug: q2 }
spec: { status: active }
---
apiVersion: crewship/v1
kind: Label
metadata: { name: bug, slug: bug }
spec: { color: "#EF4444" }
---
apiVersion: crewship/v1
kind: Skill
metadata: { name: House, slug: house }
spec: { description: a house skill, inline: "---\nname: house\ndescription: x\n---\nbody" }
---
apiVersion: crewship/v1
kind: Milestone
metadata: { name: Beta, slug: beta }
spec: { project_slug: q2 }
`)
	bundle, err := Load(body)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	stub := newKindsCovStub()
	// The milestone's parent project must exist remotely for the
	// milestone lookup to succeed (the FK is resolved server-side).
	stub.on("GET", "/api/v1/projects", 200, `[{"id":"p1","slug":"q2","name":"Q2","status":"active"}]`)
	plan, err := BuildPlan(context.Background(), NewClient(stub), bundle, Options{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	seen := map[string]Action{}
	for _, it := range plan.Items {
		seen[it.Kind] = it.Action
	}
	// Kind labels as emitted by the per-kind Plan implementations:
	// Project uses the capitalised doc-kind name, the others the
	// lowercase resource name.
	for _, kind := range []string{"Project", "label", "skill", "milestone"} {
		if _, ok := seen[kind]; !ok {
			t.Errorf("plan missing %s item; got %v", kind, seen)
		}
	}
	for _, kind := range []string{"label", "skill", "milestone"} {
		if seen[kind] != ActionCreate {
			t.Errorf("%s action = %v, want create on empty workspace", kind, seen[kind])
		}
	}
	// The remote project matches the manifest, so the project row
	// plans unchanged.
	if seen["Project"] != ActionUnchanged {
		t.Errorf("Project action = %v, want unchanged (remote matches)", seen["Project"])
	}
}

// TestBuildPlan_FullCompleteExample drives the SPEC-2 dispatcher with
// the shipped kitchen-sink example so every phase loop in planNewKinds
// runs against a clean (empty) remote workspace. The remote lookups go
// to kindsCovStub; only the milestone's parent project needs to exist
// remotely because LookupMilestoneRemote resolves the FK server-side.
func TestBuildPlan_FullCompleteExample(t *testing.T) {
	// The example's InstanceSetting interpolates ${SMTP_PASSWORD} at
	// plan time and errors when the variable is unset.
	t.Setenv("SMTP_PASSWORD", "test-smtp-secret")
	path := filepath.Join("..", "..", "examples", "manifests", "full-complete.yaml")
	bundle, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile(%s): %v", path, err)
	}
	stub := newKindsCovStub()
	stub.on("GET", "/api/v1/projects", 200, `[{"id":"p1","slug":"q2-launch","name":"Q2 Launch","status":"active"}]`)
	// The project's lead_agent_slug FK resolves through GET
	// /api/v1/agents at plan time, so "trapper" must exist remotely.
	stub.on("GET", "/api/v1/agents", 200, `[{"id":"ag1","slug":"trapper","name":"Trapper"}]`)
	// Recipe lookup GETs the slug-addressed object endpoint; the
	// stub's default `[]` body cannot decode into RecipeRemote, so
	// answer with an explicit 404 → "not found" → plan Create.
	stub.on("GET", "/api/v1/recipes/code-review", 404, `{"error":"not found"}`)
	// The CrewTemplate kind re-asserts the SOURCE template's existence
	// against the catalog at plan time.
	stub.on("GET", "/api/v1/crew-templates", 200, `[{"id":"tpl1","name":"Research team","slug":"research-team","is_builtin":true}]`)
	// Connectors install from the server catalog; a missing entry is
	// fatal at plan time, so "linear" must resolve to a catalog row.
	stub.on("GET", "/api/v1/connectors/linear", 200, `{"id":"conn1","slug":"linear","installed":false,"required_credentials":["LINEAR_API_KEY"]}`)

	plan, err := BuildPlan(context.Background(), NewClient(stub), bundle, Options{Mode: ApplyUpsert})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if len(plan.Items) == 0 {
		t.Fatal("kitchen-sink example produced an empty plan")
	}
	// Every SPEC-2 kind declared in the example must surface at least
	// one plan item — a missing kind means a dispatcher phase was
	// silently skipped.
	seen := map[string]bool{}
	for _, it := range plan.Items {
		seen[strings.ToLower(it.Kind)] = true
	}
	for _, kind := range []string{
		"project", "label", "milestone", "workflowtemplate", "triage_rule",
		"recurring_issue", "saved_view", "routine", "recipe", "crew_template",
		"connector", "feature_flag", "instancesetting", "hook",
	} {
		if !seen[kind] {
			t.Errorf("plan has no item for kind %s; kinds seen: %v", kind, seen)
		}
	}
}

// TestBuildPlan_TopLevelCrewAgentIntegrationIssue exercises the
// dispatcher phases 14.1–14.5 (top-level Crew / Agent / Integration /
// Issue kinds) that the kitchen-sink example does not declare.
func TestBuildPlan_TopLevelCrewAgentIntegrationIssue(t *testing.T) {
	body := []byte(`
apiVersion: crewship/v1
kind: Crew
metadata: { name: Ops, slug: ops }
spec:
  runtime_image: ghcr.io/x/runtime:1
---
apiVersion: crewship/v1
kind: Agent
metadata: { name: Amy, slug: amy }
spec:
  crew_slug: ops
  prompt: be useful
---
apiVersion: crewship/v1
kind: Integration
metadata: { name: github, slug: github }
spec:
  scope: crew
  crew_slug: ops
  transport: stdio
  command: gh-mcp
---
apiVersion: crewship/v1
kind: Issue
metadata: { name: Fix login, slug: fix-login }
spec:
  crew_slug: ops
`)
	bundle, err := Load(body)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	stub := newKindsCovStub()
	// Agent/Integration/Issue specs resolve crew_slug → crew_id via a
	// live GET /api/v1/crews, so "ops" must already exist remotely.
	stub.on("GET", "/api/v1/crews", 200, `[{"id":"c1","slug":"ops","name":"Ops"}]`)
	plan, err := BuildPlan(context.Background(), NewClient(stub), bundle, Options{Mode: ApplyUpsert})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	seen := map[string]bool{}
	for _, it := range plan.Items {
		seen[strings.ToLower(it.Kind)] = true
	}
	for _, kind := range []string{"crew", "agent", "integration", "issue"} {
		if !seen[kind] {
			t.Errorf("plan has no item for top-level kind %s; kinds seen: %v", kind, seen)
		}
	}
}

func TestPlanNewKinds_LookupErrorsPropagate(t *testing.T) {
	run := func(t *testing.T, manifest, failMethod, failPath, wantErr string) {
		t.Helper()
		bundle, err := Load([]byte(manifest))
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		stub := newKindsCovStub()
		stub.on(failMethod, failPath, 500, `{"error":"injected"}`)
		_, err = BuildPlan(context.Background(), NewClient(stub), bundle, Options{})
		if err == nil || !strings.Contains(err.Error(), wantErr) {
			t.Fatalf("want error containing %q, got %v", wantErr, err)
		}
	}

	t.Run("project lookup fails", func(t *testing.T) {
		run(t, `
apiVersion: crewship/v1
kind: Project
metadata: { name: P, slug: p }
spec: { status: active }
`, "GET", "/api/v1/projects", `project "p": lookup remote`)
	})
	t.Run("milestone lookup fails", func(t *testing.T) {
		run(t, `
apiVersion: crewship/v1
kind: Milestone
metadata: { name: M, slug: m }
spec: { project_slug: p }
---
apiVersion: crewship/v1
kind: Project
metadata: { name: P, slug: p }
spec: { status: active }
`, "GET", "/api/v1/projects", `project "p": lookup remote`)
	})
}
