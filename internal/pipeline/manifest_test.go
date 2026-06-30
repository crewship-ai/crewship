package pipeline

import (
	"encoding/json"
	"reflect"
	"testing"
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
