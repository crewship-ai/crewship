package pipeline

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

// TestExtractManifest_NilSafe — a nil DSL returns an empty, never-nil manifest.
func TestExtractManifest_NilSafe(t *testing.T) {
	var d *DSL
	m := d.ExtractManifest()
	if m == nil {
		t.Fatal("ExtractManifest(nil) returned nil manifest")
	}
	assertNeverNil(t, m)
}

// TestExtractManifest_EmptyDSL — an empty DSL renders every slice as [] (not
// nil) so the JSON the UI reads is stable.
func TestExtractManifest_EmptyDSL(t *testing.T) {
	d := &DSL{Name: "x"}
	m := d.ExtractManifest()
	assertNeverNil(t, m)
	if m.HasHTTP || m.HasCode {
		t.Errorf("empty DSL should not flag HasHTTP/HasCode: %+v", m)
	}

	// And the marshaled JSON must show empty arrays, not null.
	b, _ := json.Marshal(m)
	for _, key := range []string{`"integrations":[]`, `"egress":[]`, `"credentials":[]`, `"agents":[]`, `"routines":[]`, `"datastores":[]`, `"tools":[]`} {
		if !contains(string(b), key) {
			t.Errorf("manifest JSON missing %s: %s", key, b)
		}
	}
}

// TestExtractManifest_DerivesAgentsAndRoutines — agent_run → Agents,
// call_pipeline → Routines, deduped + sorted.
func TestExtractManifest_DerivesAgentsAndRoutines(t *testing.T) {
	d := &DSL{
		Name: "p",
		Steps: []Step{
			{ID: "s1", Type: StepAgentRun, AgentSlug: "zeta"},
			{ID: "s2", Type: StepAgentRun, AgentSlug: "alpha"},
			{ID: "s3", Type: StepAgentRun, AgentSlug: "alpha"}, // dup
			{ID: "s4", Type: StepCallPipeline, PipelineSlug: "child-b"},
			{ID: "s5", Type: StepCallPipeline, PipelineSlug: "child-a"},
		},
	}
	m := d.ExtractManifest()
	if !reflect.DeepEqual(m.Agents, []string{"alpha", "zeta"}) {
		t.Errorf("Agents = %v, want [alpha zeta] (deduped+sorted)", m.Agents)
	}
	if !reflect.DeepEqual(m.Routines, []string{"child-a", "child-b"}) {
		t.Errorf("Routines = %v, want [child-a child-b]", m.Routines)
	}
}

// TestExtractManifest_EgressFromHTTPAndDeclared — egress = EgressTargets PLUS
// hosts parsed from http step URLs; templated URLs are skipped; deduped+sorted.
func TestExtractManifest_EgressFromHTTPAndDeclared(t *testing.T) {
	d := &DSL{
		Name:          "p",
		EgressTargets: []string{"declared.example.com", "api.github.com"},
		Steps: []Step{
			{ID: "h1", Type: StepHTTP, HTTP: &HTTPStep{Method: "GET", URL: "https://api.github.com/repos"}},
			{ID: "h2", Type: StepHTTP, HTTP: &HTTPStep{Method: "POST", URL: "https://hooks.slack.com/services/x"}},
			{ID: "h3", Type: StepHTTP, HTTP: &HTTPStep{Method: "GET", URL: "https://{{ inputs.host }}/path"}}, // templated → skipped
		},
	}
	m := d.ExtractManifest()
	want := []string{"api.github.com", "declared.example.com", "hooks.slack.com"}
	if !reflect.DeepEqual(m.Egress, want) {
		t.Errorf("Egress = %v, want %v", m.Egress, want)
	}
	if !m.HasHTTP {
		t.Error("HasHTTP should be true")
	}
}

// TestExtractManifest_IntegrationsAndCredsPassthrough — integrations come
// through NormalizedIntegrationsRequired; credentials pass through CredsRequired.
func TestExtractManifest_IntegrationsAndCredsPassthrough(t *testing.T) {
	d := &DSL{
		Name:                 "p",
		IntegrationsRequired: []string{"  GitHub ", "slack", "github"}, // normalize + dedupe
		CredsRequired:        []CredReq{{Type: "stripe", Scope: "read"}},
		Steps:                []Step{{ID: "s1", Type: StepAgentRun, AgentSlug: "a"}},
	}
	m := d.ExtractManifest()
	if !reflect.DeepEqual(m.Integrations, []string{"github", "slack"}) {
		t.Errorf("Integrations = %v, want [github slack]", m.Integrations)
	}
	if len(m.Credentials) != 1 || m.Credentials[0].Type != "stripe" {
		t.Errorf("Credentials = %+v, want one stripe", m.Credentials)
	}
}

// TestExtractManifest_CredentialsDedupedSorted — the doc promises every ref
// slice is deduped + sorted; credentials must honor it so the manifest JSON is
// deterministic regardless of how the author ordered/repeated declarations.
func TestExtractManifest_CredentialsDedupedSorted(t *testing.T) {
	d := &DSL{
		Name: "p",
		CredsRequired: []CredReq{
			{Type: "stripe", Scope: "write"},
			{Type: "github", Scope: "repo"},
			{Type: "stripe", Scope: "write"},     // exact dup
			{Type: "  github ", Scope: " repo "}, // dup after trim
		},
		Steps: []Step{{ID: "s1", Type: StepAgentRun, AgentSlug: "a"}},
	}
	m := d.ExtractManifest()
	want := []CredReq{{Type: "github", Scope: "repo"}, {Type: "stripe", Scope: "write"}}
	if !reflect.DeepEqual(m.Credentials, want) {
		t.Errorf("Credentials = %+v, want %+v (deduped + sorted)", m.Credentials, want)
	}
}

// TestExtractManifest_CodeStepsAndDeclaredTools — code-step runtimes become
// ToolRefs and merge with declared Resources.Tools; datastores pass through;
// deduped + sorted.
func TestExtractManifest_CodeStepsAndDeclaredTools(t *testing.T) {
	d := &DSL{
		Name: "p",
		Steps: []Step{
			{ID: "c1", Type: StepCode, Code: &CodeStep{Runtime: "cel", Code: "1"}},
			{ID: "c2", Type: StepCode, Code: &CodeStep{Runtime: "expr", Code: "2"}},
			{ID: "c3", Type: StepCode, Code: &CodeStep{Runtime: "expr", Code: "3"}}, // dup runtime
		},
		Resources: &RoutineResources{
			Datastores: []DatastoreRef{
				{Type: "redis", Name: "cache"},
				{Type: "postgres", Name: "main", Note: "writes table runs"},
			},
			Tools: []ToolRef{
				{Type: "ansible", Name: "deploy.yml"},
				{Type: "cel"}, // dup vs code-runtime cel
			},
		},
	}
	m := d.ExtractManifest()
	if !m.HasCode {
		t.Error("HasCode should be true")
	}
	wantTools := []ToolRef{
		{Type: "ansible", Name: "deploy.yml"},
		{Type: "cel"},
		{Type: "expr"},
	}
	if !reflect.DeepEqual(m.Tools, wantTools) {
		t.Errorf("Tools = %+v, want %+v", m.Tools, wantTools)
	}
	wantDS := []DatastoreRef{
		{Type: "postgres", Name: "main", Note: "writes table runs"},
		{Type: "redis", Name: "cache"},
	}
	if !reflect.DeepEqual(m.Datastores, wantDS) {
		t.Errorf("Datastores = %+v, want %+v", m.Datastores, wantDS)
	}
}

// TestExtractManifest_WalksHooks — agent/routine/http/code references inside
// routine-level and per-step hooks are part of the blast radius.
func TestExtractManifest_WalksHooks(t *testing.T) {
	d := &DSL{
		Name: "p",
		Steps: []Step{
			{
				ID: "s1", Type: StepAgentRun, AgentSlug: "worker",
				Hooks: &StepHooks{
					Before: &Step{ID: "h-before", Type: StepHTTP, HTTP: &HTTPStep{Method: "POST", URL: "https://before.example.com/x"}},
					After:  &Step{ID: "h-after", Type: StepCode, Code: &CodeStep{Runtime: "expr", Code: "1"}},
				},
			},
		},
		Hooks: &RoutineHooks{
			OnFailure: &Step{ID: "fail", Type: StepHTTP, HTTP: &HTTPStep{Method: "POST", URL: "https://onfail.example.com/y"}},
		},
	}
	m := d.ExtractManifest()
	if !m.HasHTTP {
		t.Error("HasHTTP should be true from hooks")
	}
	if !m.HasCode {
		t.Error("HasCode should be true from per-step after hook")
	}
	wantEgress := []string{"before.example.com", "onfail.example.com"}
	if !reflect.DeepEqual(m.Egress, wantEgress) {
		t.Errorf("Egress = %v, want %v (from hooks)", m.Egress, wantEgress)
	}
	// expr runtime from the after hook lands in Tools.
	if len(m.Tools) != 1 || m.Tools[0].Type != "expr" {
		t.Errorf("Tools = %+v, want one expr (from hook code step)", m.Tools)
	}
}

// TestExtractManifest_WalksForeachBody — a foreach body is part of the blast
// radius the UI renders.
//
// Same gap as StaticRiskReasons', with a different consequence: the manifest is
// what the data-flow diagram draws, so a routine whose only http call sits
// inside a fan-out reported `has_http:false` and an empty egress list — a
// screen that positively asserts "this routine reaches nothing" about a routine
// that reaches the internet. Agents and code runtimes disappeared the same way.
func TestExtractManifest_WalksForeachBody(t *testing.T) {
	d := &DSL{
		Name: "p",
		Steps: []Step{{
			ID: "fan", Type: StepForeach,
			Foreach: &ForeachStep{Items: "{{ inputs.rows }}", Steps: []Step{
				{ID: "b1", Type: StepAgentRun, AgentSlug: "worker"},
				{ID: "b2", Type: StepHTTP, HTTP: &HTTPStep{Method: "POST", URL: "https://body.example.com/x"}},
				{ID: "b3", Type: StepCode, Code: &CodeStep{Runtime: "expr", Code: "1"}},
			}},
		}},
	}
	m := d.ExtractManifest()
	if !m.HasHTTP {
		t.Error("HasHTTP should be true from an http step inside the foreach body")
	}
	if !m.HasCode {
		t.Error("HasCode should be true from a code step inside the foreach body")
	}
	if !reflect.DeepEqual(m.Egress, []string{"body.example.com"}) {
		t.Errorf("Egress = %v, want [body.example.com] (host from a body http step)", m.Egress)
	}
	if !reflect.DeepEqual(m.Agents, []string{"worker"}) {
		t.Errorf("Agents = %v, want [worker] (agent_run inside the foreach body)", m.Agents)
	}
	if len(m.Tools) != 1 || m.Tools[0].Type != "expr" {
		t.Errorf("Tools = %+v, want one expr (runtime of the body code step)", m.Tools)
	}
}

// TestExtractManifest_ForeachNestedAndHooked — the walk reaches a capability
// however deeply it is wrapped: a foreach parked in a routine hook, a foreach
// inside a foreach, and a per-step hook hanging off a body step.
//
// Nested foreach is refused by validateForeachStep today, so this shape does
// not arrive through the save door. It is pinned anyway because the manifest is
// also computed for imported bundles and for rows already stored, where no such
// refusal ran.
func TestExtractManifest_ForeachNestedAndHooked(t *testing.T) {
	// Built bottom-up: the interesting shape is the nesting, and a single
	// literal that deep reads as bracket soup.
	leaf := Step{
		ID: "leaf", Type: StepAgentRun, AgentSlug: "deep",
		Hooks: &StepHooks{After: &Step{
			ID: "leaf-after", Type: StepHTTP,
			HTTP: &HTTPStep{Method: "GET", URL: "https://nested.example.com/y"},
		}},
	}
	inner := Step{
		ID: "inner", Type: StepForeach,
		Foreach: &ForeachStep{Items: "{{ inputs.b }}", Steps: []Step{leaf}},
	}
	outer := Step{
		ID: "outer", Type: StepForeach,
		Foreach: &ForeachStep{Items: "{{ inputs.a }}", Steps: []Step{inner}},
	}
	d := &DSL{
		Name:  "p",
		Steps: []Step{outer},
		Hooks: &RoutineHooks{OnFailure: &Step{
			ID: "cleanup", Type: StepForeach,
			Foreach: &ForeachStep{Items: "{{ inputs.c }}", Steps: []Step{
				{ID: "notify-fail", Type: StepHTTP, HTTP: &HTTPStep{Method: "POST", URL: "https://hook.example.com/z"}},
			}},
		}},
	}
	m := d.ExtractManifest()
	if !reflect.DeepEqual(m.Agents, []string{"deep"}) {
		t.Errorf("Agents = %v, want [deep] (two foreach levels down)", m.Agents)
	}
	want := []string{"hook.example.com", "nested.example.com"}
	if !reflect.DeepEqual(m.Egress, want) {
		t.Errorf("Egress = %v, want %v (body-step hook + foreach inside a routine hook)", m.Egress, want)
	}
	if !m.HasHTTP {
		t.Error("HasHTTP should be true")
	}
}

// TestExtractManifest_ForeachNilBody — a malformed foreach step (no block) is
// skipped, not a panic. ExtractManifest runs on unvalidated DSLs.
func TestExtractManifest_ForeachNilBody(t *testing.T) {
	d := &DSL{Name: "p", Steps: []Step{{ID: "fan", Type: StepForeach}}}
	m := d.ExtractManifest()
	assertNeverNil(t, m)
	if m.HasHTTP || m.HasCode {
		t.Errorf("empty foreach should flag nothing: %+v", m)
	}
}

// TestExtractManifest_CyclicDSLTerminates — see the twin in risk_test.go. A
// self-referential Step graph is unreachable from JSON but constructible in Go,
// and the manifest walk is called on the same save path as the classifier, so
// it carries the same bound.
func TestExtractManifest_CyclicDSLTerminates(t *testing.T) {
	fe := &ForeachStep{Items: "{{ inputs.x }}", Steps: []Step{
		{ID: "inner", Type: StepAgentRun, AgentSlug: "worker"},
		{ID: "loop", Type: StepForeach},
	}}
	fe.Steps[1].Foreach = fe // self-reference
	d := &DSL{Name: "p", Steps: []Step{{ID: "fan", Type: StepForeach, Foreach: fe}}}

	done := make(chan *Manifest, 1)
	go func() { done <- d.ExtractManifest() }()
	select {
	case m := <-done:
		if !reflect.DeepEqual(m.Agents, []string{"worker"}) {
			t.Errorf("Agents = %v, want [worker]", m.Agents)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ExtractManifest did not terminate on a cyclic DSL")
	}
}

// --- helpers ---

func assertNeverNil(t *testing.T, m *Manifest) {
	t.Helper()
	if m.Integrations == nil {
		t.Error("Integrations is nil, want empty slice")
	}
	if m.Egress == nil {
		t.Error("Egress is nil, want empty slice")
	}
	if m.Credentials == nil {
		t.Error("Credentials is nil, want empty slice")
	}
	if m.Agents == nil {
		t.Error("Agents is nil, want empty slice")
	}
	if m.Routines == nil {
		t.Error("Routines is nil, want empty slice")
	}
	if m.Datastores == nil {
		t.Error("Datastores is nil, want empty slice")
	}
	if m.Tools == nil {
		t.Error("Tools is nil, want empty slice")
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
