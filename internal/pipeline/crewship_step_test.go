package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// crewshipDSL builds a one-step routine using the crewship kind.
func crewshipDSL(action string, args map[string]any) *DSL {
	return &DSL{
		DSLVersion: "1.0",
		Name:       "crewship-routine",
		Steps:      []Step{{ID: "act", Type: StepCrewship, Action: action, Args: args}},
	}
}

// An unknown verb must be refused at SAVE, not at run. A routine that saves
// clean and then refuses at 03:00 is worse than one that never saved: by then
// somebody has scheduled it and gone home.
func TestValidate_CrewshipUnknownVerbFailsAtSave(t *testing.T) {
	err := Validate(crewshipDSL("issue.destroy", map[string]any{"identifier": "ENG-1"}), nil, nil)
	if err == nil {
		t.Fatal("expected save-time refusal for an unknown crewship action, got nil")
	}
	if !strings.Contains(err.Error(), "issue.destroy") {
		t.Errorf("the error must name the offending verb, got %q", err)
	}
	if !strings.Contains(err.Error(), "unknown crewship action") {
		t.Errorf("expected the unknown-verb refusal, got %q", err)
	}
}

// A verb with no policy.Action behind it must ALSO be refused at save. Every
// crewship action is bounded by a crew's autonomy level; one with no Action
// has nothing bounding it, and "we'll gate it later" is how an ungoverned
// capability ships.
func TestValidate_CrewshipVerbWithoutPolicyActionFailsAtSave(t *testing.T) {
	// Pick a declared-but-ungoverned verb off the registry rather than
	// hard-coding one, so this test keeps working as verbs are enabled — and
	// skips honestly if every verb has an Action.
	var ungoverned string
	for _, v := range CrewshipVerbs() {
		if CrewshipVerbPolicyAction(v) == "" {
			ungoverned = v
			break
		}
	}
	if ungoverned == "" {
		t.Skip("every crewship verb now has a policy action — nothing left to refuse")
	}

	err := Validate(crewshipDSL(ungoverned, map[string]any{
		"identifier": "ENG-1", "body": "hi",
	}), nil, nil)
	if err == nil {
		t.Fatalf("expected save-time refusal for %q (no policy action), got nil", ungoverned)
	}
	if !strings.Contains(err.Error(), "no policy action") {
		t.Errorf("the error must say WHY it was refused, got %q", err)
	}
	if !strings.Contains(err.Error(), ungoverned) {
		t.Errorf("the error must name the offending verb, got %q", err)
	}
}

// A governed verb with its required args saves. The gate must bound the kind,
// not forbid it — without this, "refuse everything" would pass the two above.
func TestValidate_CrewshipGovernedVerbSaves(t *testing.T) {
	enabled := EnabledCrewshipVerbs()
	if len(enabled) == 0 {
		t.Fatal("no crewship verb is enabled — the step kind would be dead on arrival")
	}
	if err := Validate(crewshipDSL("issue.create", map[string]any{
		"title": "Routine found something",
	}), nil, nil); err != nil {
		t.Fatalf("a governed verb with its required args must save: %v", err)
	}
}

// Missing required args are an authoring mistake, caught at authoring time.
func TestValidate_CrewshipMissingRequiredArgFailsAtSave(t *testing.T) {
	err := Validate(crewshipDSL("issue.create", map[string]any{"priority": "high"}), nil, nil)
	if err == nil {
		t.Fatal("expected save-time refusal for a missing required arg, got nil")
	}
	if !strings.Contains(err.Error(), "title") {
		t.Errorf("the error must name the missing arg, got %q", err)
	}
}

// A template inside args that names one of OUR namespaces and gets it wrong
// is caught at save. A comment that renders with a hole in it is the same
// silent-hole failure the notify walk exists to prevent, on a more public
// surface.
func TestValidate_CrewshipArgsTemplatesAreChecked(t *testing.T) {
	err := Validate(crewshipDSL("issue.create", map[string]any{
		"title": "from {{ steps.ghost.output }}",
	}), nil, nil)
	if err == nil {
		t.Fatal("expected a bad step ref inside crewship args to be caught at save")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("the error must name the unresolvable ref, got %q", err)
	}
}

// A crewship step is not agentless-safe: an @mention in an issue body wakes an
// agent, an assignment dispatches one. A token-zero guarantee that holds
// "usually" is not one.
func TestValidate_CrewshipRejectedInAgentlessRoutine(t *testing.T) {
	dsl := crewshipDSL("issue.create", map[string]any{"title": "x"})
	dsl.Agentless = true
	err := Validate(dsl, nil, nil)
	if err == nil {
		t.Fatal("expected an agentless routine to refuse a crewship step")
	}
	if !strings.Contains(err.Error(), "agentless") {
		t.Errorf("the error must name the guarantee it protects, got %q", err)
	}
}

// recordingCrewship captures what the executor dispatched.
type recordingCrewship struct {
	seen []CrewshipRequest
	out  string
	err  error
}

func (r *recordingCrewship) Do(_ context.Context, req CrewshipRequest) (string, error) {
	r.seen = append(r.seen, req)
	if r.err != nil {
		return "", r.err
	}
	if r.out == "" {
		return `{"identifier":"ENG-9"}`, nil
	}
	return r.out, nil
}

// End to end through the executor: args render against the run context, the
// AUTHOR crew is the acting principal, and the chain budget is carried into
// whatever the verb creates.
func TestExecutor_CrewshipStep_DispatchesRenderedArgs(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	actions := &recordingCrewship{}
	exec := NewExecutor(store, resolver, newMockRunner(), nil).WithCrewshipActions(actions)

	dsl := &DSL{
		DSLVersion: "1.0",
		Name:       "file-it",
		Inputs:     []InputSpec{{Name: "summary", Type: "string"}},
		Steps: []Step{{
			ID: "act", Type: StepCrewship, Action: "issue.create",
			Args: map[string]any{
				"title":    "Routine says: {{ inputs.summary }}",
				"priority": "high",
				"labels":   []any{"auto-{{ inputs.summary }}"},
			},
		}},
	}
	res, err := exec.RunDefinition(context.Background(), dsl, RunInput{
		WorkspaceID:   "ws_test",
		AuthorCrewID:  "crew_author",
		AuthorAgentID: "agent_author",
		Mode:          ModeRun,
		Inputs:        map[string]any{"summary": "disk full"},
		ChainDepth:    3,
	})
	if err != nil {
		t.Fatalf("Run returned transport error: %v", err)
	}
	if res.Status != "COMPLETED" {
		t.Fatalf("expected COMPLETED, got %s (%s)", res.Status, res.ErrorMessage)
	}
	if len(actions.seen) != 1 {
		t.Fatalf("dispatched %d times, want 1", len(actions.seen))
	}
	got := actions.seen[0]
	if got.Verb != "issue.create" {
		t.Errorf("Verb = %q", got.Verb)
	}
	if got.Args["title"] != "Routine says: disk full" {
		t.Errorf("args were not rendered: %#v", got.Args["title"])
	}
	// Containers too: an issue's labels are a list of strings, and a template
	// buried one level down is still a template the author meant.
	labels, _ := got.Args["labels"].([]any)
	if len(labels) != 1 || labels[0] != "auto-disk full" {
		t.Errorf("nested args were not rendered: %#v", got.Args["labels"])
	}
	if got.CrewID != "crew_author" || got.AgentID != "agent_author" {
		t.Errorf("acting principal must be the routine's AUTHOR, got crew=%q agent=%q", got.CrewID, got.AgentID)
	}
	if got.WorkspaceID != "ws_test" {
		t.Errorf("WorkspaceID = %q", got.WorkspaceID)
	}
	if got.RunID == "" {
		t.Error("RunID must be carried — it is the provenance an automation reads back to inherit the chain")
	}
	// Without this the chain restarts at 0 on every journal hop and the cap
	// becomes unenforceable across process boundaries.
	if got.ChainDepth != 3 {
		t.Errorf("ChainDepth = %d, want 3 (the run's own depth, carried into what it creates)", got.ChainDepth)
	}
	if res.StepOutputs["act"] != `{"identifier":"ENG-9"}` {
		t.Errorf("the route's response must become the step output, got %q", res.StepOutputs["act"])
	}
}

// No dispatcher wired = fail closed with a hint. A step whose entire purpose
// is a side effect must never look like it succeeded having done nothing.
func TestExecutor_CrewshipStep_FailsClosedWhenUnwired(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	exec := NewExecutor(store, resolver, newMockRunner(), nil)

	res, err := exec.RunDefinition(context.Background(),
		crewshipDSL("issue.create", map[string]any{"title": "x"}),
		RunInput{WorkspaceID: "ws_test", AuthorCrewID: "crew_a", Mode: ModeRun})
	if err != nil {
		t.Fatalf("Run returned transport error: %v", err)
	}
	if res.Status != "FAILED" {
		t.Fatalf("expected FAILED with no dispatcher wired, got %s", res.Status)
	}
	if !strings.Contains(res.ErrorMessage, "CrewshipActions") {
		t.Errorf("the failure must name the missing wiring, got %q", res.ErrorMessage)
	}
}

// dry_run must not write. A preview that creates a real issue is not a
// preview.
func TestExecutor_CrewshipStep_DryRunDoesNotDispatch(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	actions := &recordingCrewship{}
	exec := NewExecutor(store, resolver, newMockRunner(), nil).WithCrewshipActions(actions)

	if _, err := exec.RunDefinition(context.Background(),
		crewshipDSL("issue.create", map[string]any{"title": "x"}),
		RunInput{WorkspaceID: "ws_test", AuthorCrewID: "crew_a", Mode: ModeDryRun}); err != nil {
		t.Fatalf("dry-run returned transport error: %v", err)
	}
	if len(actions.seen) != 0 {
		t.Errorf("dry run dispatched %d real writes", len(actions.seen))
	}
}

// The registry is the only list. If the schema's action enum and the Go
// registry drift, a routine an editor validated is one the server refuses.
func TestCrewshipVerbs_MatchTheJSONSchema(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Skip("no caller info")
	}
	path := filepath.Join(filepath.Dir(thisFile), "..", "..", "schemas", "routine.v1.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	var doc struct {
		Defs struct {
			Step struct {
				Properties struct {
					Action struct {
						Enum []string `json:"enum"`
					} `json:"action"`
				} `json:"properties"`
			} `json:"Step"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse schema: %v", err)
	}
	inSchema := map[string]bool{}
	for _, v := range doc.Defs.Step.Properties.Action.Enum {
		inSchema[v] = true
	}
	registry := CrewshipVerbs()
	if len(inSchema) != len(registry) {
		t.Errorf("crewship verb count mismatch: schema=%d, registry=%d", len(inSchema), len(registry))
	}
	for _, v := range registry {
		if !inSchema[v] {
			t.Errorf("schema action enum is missing %q", v)
		}
	}
}

// Every registry entry must be dispatchable: a route and, when governed, a
// policy action. A verb that validates but has no route would 404 at 03:00.
func TestCrewshipVerbs_RegistryIsCoherent(t *testing.T) {
	for _, v := range CrewshipVerbs() {
		method, path, ok := CrewshipVerbRoute(v)
		if !ok || method == "" || !strings.HasPrefix(path, "/api/v1/internal/") {
			t.Errorf("verb %q has no usable internal route (%s %s)", v, method, path)
		}
	}
	if err := ValidateCrewshipAction("s", ""); err == nil {
		t.Error("an empty action must be refused")
	}
	if err := ValidateCrewshipAction("s", "nope"); !errors.Is(err, ErrCrewshipVerbUnknown) {
		t.Errorf("an unknown action must wrap ErrCrewshipVerbUnknown, got %v", err)
	}
}
