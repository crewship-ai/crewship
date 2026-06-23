package manifest

// Second coverage pass for apply_kinds.go: exercises the per-phase
// error returns in planNewKinds (lookup + plan failures for each
// SPEC-2 kind), the SkipTestGate decorator branch, and the nil-spec
// guard in buildKindWorkspaceContext.

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

// planKindsExpectError loads a manifest, injects one failing route into
// the kinds stub, runs BuildPlan and asserts the wrapped error message.
func planKindsExpectError(t *testing.T, manifest string, routes map[string]covRoute, wantErr string) {
	t.Helper()
	bundle, err := Load([]byte(manifest))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	stub := newKindsCovStub()
	for key, r := range routes {
		parts := strings.SplitN(key, " ", 2)
		stub.on(parts[0], parts[1], r.status, r.body)
	}
	_, err = BuildPlan(context.Background(), NewClient(stub), bundle, Options{})
	if err == nil || !strings.Contains(err.Error(), wantErr) {
		t.Fatalf("want error containing %q, got %v", wantErr, err)
	}
}

func TestPlanNewKinds_PerKindErrorBranches(t *testing.T) {
	fail := covRoute{status: 500, body: `{"error":"injected"}`}

	cases := []struct {
		name     string
		manifest string
		routes   map[string]covRoute
		wantErr  string
	}{
		{
			name: "skill lookup fails",
			manifest: `
apiVersion: crewship/v1
kind: Skill
metadata: { name: House, slug: house }
spec: { description: d, inline: "---\nname: house\ndescription: x\n---\nbody" }
`,
			routes:  map[string]covRoute{"GET /api/v1/skills": fail},
			wantErr: `skill "house": lookup remote`,
		},
		{
			name: "instance setting plan fails",
			manifest: `
apiVersion: crewship/v1
kind: InstanceSetting
metadata: { name: cfg, slug: cfg }
spec:
  settings:
    branding.product_name: Outlands
`,
			routes:  map[string]covRoute{"GET /api/v1/instance/settings": fail},
			wantErr: `instance_setting "cfg": plan`,
		},
		{
			name: "recipe lookup fails",
			manifest: `
apiVersion: crewship/v1
kind: Recipe
metadata: { name: CR, slug: code-review }
spec: { install: true }
`,
			routes:  map[string]covRoute{"GET /api/v1/recipes/code-review": fail},
			wantErr: `recipe "code-review": lookup remote`,
		},
		{
			name: "crew template lookup fails",
			manifest: `
apiVersion: crewship/v1
kind: CrewTemplate
metadata: { name: Research, slug: research-team }
spec: { deploy: true, crew_slug_override: research }
`,
			routes:  map[string]covRoute{"GET /api/v1/crew-templates": fail},
			wantErr: `crew_template "research-team": lookup remote`,
		},
		{
			name: "crew lookup fails",
			manifest: `
apiVersion: crewship/v1
kind: Crew
metadata: { name: Ops, slug: ops }
spec: { runtime_image: ghcr.io/x/runtime:1 }
`,
			routes:  map[string]covRoute{"GET /api/v1/crews": fail},
			wantErr: `crew "ops": lookup remote`,
		},
		{
			name: "agent lookup fails",
			manifest: `
apiVersion: crewship/v1
kind: Crew
metadata: { name: Ops, slug: ops }
spec: { runtime_image: ghcr.io/x/runtime:1 }
---
apiVersion: crewship/v1
kind: Agent
metadata: { name: Amy, slug: amy }
spec: { crew_slug: ops, prompt: be useful }
`,
			routes:  map[string]covRoute{"GET /api/v1/agents": fail},
			wantErr: `agent "amy": lookup remote`,
		},
		{
			name: "agent plan fails resolving crew slug remotely",
			manifest: `
apiVersion: crewship/v1
kind: Crew
metadata: { name: Ops, slug: ops }
spec: { runtime_image: ghcr.io/x/runtime:1 }
---
apiVersion: crewship/v1
kind: Agent
metadata: { name: Amy, slug: amy }
spec: { crew_slug: ops, prompt: be useful }
`,
			// Crews list stays empty (stub default), so the agent's
			// plan-time crew_slug → crew_id resolution comes up empty.
			routes:  map[string]covRoute{},
			wantErr: `agent "amy": plan`,
		},
		{
			name: "integration lookup fails with defaulted workspace scope",
			manifest: `
apiVersion: crewship/v1
kind: Integration
metadata: { name: github, slug: github }
spec: { transport: stdio, command: gh-mcp }
`,
			routes:  map[string]covRoute{"GET /api/v1/integrations": fail},
			wantErr: `integration "github": lookup remote`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			planKindsExpectError(t, tc.manifest, tc.routes, tc.wantErr)
		})
	}
}

// flakyPathStub wraps kindsCovStub but fails the named GET path from
// the Nth call onwards. Needed because the Project phase and the
// Milestone lookup share GET /api/v1/projects — a statically failing
// route would always kill the Project phase first.
type flakyPathStub struct {
	*kindsCovStub
	failPath  string
	failAfter int // number of successful calls allowed
	calls     int
}

func (s *flakyPathStub) Get(ctx context.Context, path string) (*http.Response, error) {
	if path == s.failPath {
		s.calls++
		if s.calls > s.failAfter {
			return &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader(`{"error":"injected"}`)), Header: http.Header{}}, nil
		}
	}
	return s.kindsCovStub.Get(ctx, path)
}

func TestPlanNewKinds_MilestoneLookupErrorPropagates(t *testing.T) {
	bundle, err := Load([]byte(`
apiVersion: crewship/v1
kind: Project
metadata: { name: Q2, slug: q2 }
spec: { status: active }
---
apiVersion: crewship/v1
kind: Milestone
metadata: { name: Beta, slug: beta }
spec: { project_slug: q2 }
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// First projects fetch (Project phase lookup) succeeds, second
	// (Milestone lookup) gets the 500.
	stub := &flakyPathStub{kindsCovStub: newKindsCovStub(), failPath: "/api/v1/projects", failAfter: 1}
	_, err = BuildPlan(context.Background(), NewClient(stub), bundle, Options{})
	want := `milestone "beta": lookup remote`
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("want error containing %q, got %v", want, err)
	}
}

func TestBuildPlan_SkipTestGateStillPlans(t *testing.T) {
	bundle, err := Load([]byte(`
apiVersion: crewship/v1
kind: Label
metadata: { name: bug, slug: bug }
spec: { color: "#EF4444" }
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	stub := newKindsCovStub()
	plan, err := BuildPlan(context.Background(), NewClient(stub), bundle, Options{SkipTestGate: true})
	if err != nil {
		t.Fatalf("BuildPlan with SkipTestGate: %v", err)
	}
	found := false
	for _, it := range plan.Items {
		if it.Kind == "label" && strings.Contains(it.Description, "bug") && it.Action == ActionCreate {
			found = true
		}
	}
	if !found {
		t.Errorf("SkipTestGate plan missing label create item; got %+v", plan.Items)
	}
}

func TestBuildKindWorkspaceContext_SkipsNilSpecDocuments(t *testing.T) {
	b := &Bundle{
		Documents: []Document{
			{Metadata: Metadata{Name: "Ghost", Slug: "ghost"}}, // nil Spec — must be skipped
			{
				Metadata: Metadata{Name: "Ops", Slug: "ops"},
				Spec:     &CrewSpec{Agents: []Agent{{Slug: "amy", Name: "Amy"}}},
			},
		},
	}
	ctx := buildKindWorkspaceContext(b)
	for _, c := range ctx.DeclaredCrews {
		if c.Slug == "ghost" {
			t.Errorf("nil-spec document leaked into DeclaredCrews: %+v", ctx.DeclaredCrews)
		}
	}
	foundOps := false
	for _, c := range ctx.DeclaredCrews {
		if c.Slug == "ops" {
			foundOps = true
		}
	}
	if !foundOps {
		t.Errorf("crew with spec missing from DeclaredCrews: %+v", ctx.DeclaredCrews)
	}
	foundAmy := false
	for _, a := range ctx.DeclaredAgents {
		if a.Slug == "amy" {
			foundAmy = true
		}
	}
	if !foundAmy {
		t.Errorf("nested agent missing from DeclaredAgents: %+v", ctx.DeclaredAgents)
	}
}
