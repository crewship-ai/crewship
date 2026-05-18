package orchestrator

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// workflow.go — ExpandCustomTemplate.
//
// Path that turns a user-supplied JSON template into a TaskPlan ready
// for MissionEngine to insert as mission_tasks. Sibling tests in
// workflow_test.go cover ExpandTemplate (the builtin-name path) but
// not the custom-JSON path, so a parser regression here would silently
// break every "bring your own workflow" mission start.
//
// The shape of the returned plan matters per-field — the API handler
// passes PlannedTask straight into mission_tasks INSERTs.
// ---------------------------------------------------------------------------

func TestExpandCustomTemplate_InvalidJSON_WrapsWithMarker(t *testing.T) {
	// Source: `fmt.Errorf("invalid template JSON: %w", err)`. The
	// "invalid template JSON" prefix is the operator-triage signal —
	// without it, callers see only the raw json.SyntaxError which is
	// indistinguishable from any other JSON parse failure in the run.
	_, err := ExpandCustomTemplate("{not valid", AgentMapping{})
	if err == nil {
		t.Fatal("expected error on malformed JSON")
	}
	if !strings.Contains(err.Error(), "invalid template JSON") {
		t.Errorf("err = %v, want \"invalid template JSON\" prefix (operator triage)", err)
	}
}

func TestExpandCustomTemplate_EmptyString_RejectedAsInvalid(t *testing.T) {
	// Empty string is invalid JSON. Pin so a future "treat empty as
	// empty plan" shortcut has to flip this test deliberately — the
	// current contract is that the caller must pass a real `{}` if
	// they want an empty template.
	_, err := ExpandCustomTemplate("", AgentMapping{})
	if err == nil {
		t.Fatal("expected error on empty string")
	}
}

func TestExpandCustomTemplate_EmptyObject_ReturnsEmptyPlan(t *testing.T) {
	// `{}` is valid JSON for the WorkflowTemplate zero value. Steps
	// is nil → plan.Tasks ends up nil too. The API handler renders
	// this as "no tasks" rather than crashing.
	plan, err := ExpandCustomTemplate(`{}`, AgentMapping{})
	if err != nil {
		t.Fatalf("ExpandCustomTemplate: %v", err)
	}
	if plan == nil {
		t.Fatal("plan = nil; want non-nil with empty Tasks")
	}
	if len(plan.Tasks) != 0 {
		t.Errorf("Tasks = %d, want 0", len(plan.Tasks))
	}
}

func TestExpandCustomTemplate_SingleStep_PopulatesAllFields(t *testing.T) {
	// One-step happy path: pin that every field round-trips correctly
	// from the JSON into PlannedTask.
	in := `{
		"name": "user-workflow",
		"steps": [
			{
				"id": "build",
				"title": "Build the thing",
				"description": "compile + lint",
				"agent_slug": "alice",
				"max_iterations": 5
			}
		]
	}`
	plan, err := ExpandCustomTemplate(in, AgentMapping{})
	if err != nil {
		t.Fatalf("ExpandCustomTemplate: %v", err)
	}
	if len(plan.Tasks) != 1 {
		t.Fatalf("Tasks = %d, want 1", len(plan.Tasks))
	}
	tk := plan.Tasks[0]
	if tk.TempID != "build" {
		t.Errorf("TempID = %q", tk.TempID)
	}
	if tk.Title != "Build the thing" {
		t.Errorf("Title = %q", tk.Title)
	}
	if tk.Description != "compile + lint" {
		t.Errorf("Description = %q", tk.Description)
	}
	if tk.TaskOrder != 1 {
		t.Errorf("TaskOrder = %d, want 1 (i+1)", tk.TaskOrder)
	}
	if tk.Status != "PENDING" {
		t.Errorf("Status = %q, want PENDING (no DependsOn)", tk.Status)
	}
	if tk.AgentID != "alice" {
		t.Errorf("AgentID = %q, want \"alice\" (agent_slug passes through verbatim — caller resolves)", tk.AgentID)
	}
	if tk.MaxIterations == nil || *tk.MaxIterations != 5 {
		t.Errorf("MaxIterations = %v, want *5", tk.MaxIterations)
	}
}

func TestExpandCustomTemplate_AgentSlugBeatsAgentRole(t *testing.T) {
	// Source: `if step.AgentSlug != "" { task.AgentID = step.AgentSlug }
	// else if step.AgentRole != "" { mapping lookup }`. Pin slug wins
	// when both are present — a regression to "role wins" would silently
	// change which agent a step is dispatched to.
	in := `{
		"steps": [
			{"id": "x", "title": "X", "agent_slug": "specific-alice", "agent_role": "developer"}
		]
	}`
	plan, err := ExpandCustomTemplate(in, AgentMapping{"developer": "agent-dev-from-mapping"})
	if err != nil {
		t.Fatalf("ExpandCustomTemplate: %v", err)
	}
	if plan.Tasks[0].AgentID != "specific-alice" {
		t.Errorf("AgentID = %q, want \"specific-alice\" (slug must beat role)", plan.Tasks[0].AgentID)
	}
}

func TestExpandCustomTemplate_AgentRoleResolvedViaMapping(t *testing.T) {
	// Role-only path: AgentSlug empty → look up by role in mapping.
	// The mapping is the "which agent in this workspace plays which
	// role" translation table.
	in := `{
		"steps": [
			{"id": "x", "title": "X", "agent_role": "tester"}
		]
	}`
	plan, err := ExpandCustomTemplate(in, AgentMapping{
		"developer": "agent-dev",
		"tester":    "agent-bob",
	})
	if err != nil {
		t.Fatalf("ExpandCustomTemplate: %v", err)
	}
	if plan.Tasks[0].AgentID != "agent-bob" {
		t.Errorf("AgentID = %q, want \"agent-bob\" (role tester → mapping[tester])", plan.Tasks[0].AgentID)
	}
}

func TestExpandCustomTemplate_AgentRoleMissingFromMapping_AgentIDLeftEmpty(t *testing.T) {
	// If the role isn't in the mapping, AgentID stays empty —
	// the caller (mission handler) is responsible for either failing
	// the mission or defaulting. Pin the empty contract so a regression
	// to "panic on missing role" surfaces here.
	in := `{
		"steps": [
			{"id": "x", "title": "X", "agent_role": "phantom-role"}
		]
	}`
	plan, err := ExpandCustomTemplate(in, AgentMapping{"developer": "agent-dev"})
	if err != nil {
		t.Fatalf("ExpandCustomTemplate: %v", err)
	}
	if plan.Tasks[0].AgentID != "" {
		t.Errorf("AgentID = %q, want empty (unmapped role)", plan.Tasks[0].AgentID)
	}
}

func TestExpandCustomTemplate_NeitherSlugNorRole_AgentIDEmpty(t *testing.T) {
	// Defensive: neither field set. AgentID stays empty.
	in := `{"steps": [{"id": "x", "title": "X"}]}`
	plan, err := ExpandCustomTemplate(in, AgentMapping{})
	if err != nil {
		t.Fatalf("ExpandCustomTemplate: %v", err)
	}
	if plan.Tasks[0].AgentID != "" {
		t.Errorf("AgentID = %q, want empty (no agent fields set)", plan.Tasks[0].AgentID)
	}
}

func TestExpandCustomTemplate_DependsOn_TriggersBlockedStatus(t *testing.T) {
	// Source: any non-empty DependsOn flips Status to "BLOCKED".
	// MissionEngine relies on this to know which tasks to gate at
	// dispatch time — a regression to "PENDING regardless" would
	// dispatch downstream tasks before their dependencies completed.
	in := `{
		"steps": [
			{"id": "a", "title": "A"},
			{"id": "b", "title": "B", "depends_on": ["a"]}
		]
	}`
	plan, err := ExpandCustomTemplate(in, AgentMapping{})
	if err != nil {
		t.Fatalf("ExpandCustomTemplate: %v", err)
	}
	if plan.Tasks[0].Status != "PENDING" {
		t.Errorf("step a Status = %q, want PENDING (no deps)", plan.Tasks[0].Status)
	}
	if plan.Tasks[1].Status != "BLOCKED" {
		t.Errorf("step b Status = %q, want BLOCKED (has deps)", plan.Tasks[1].Status)
	}
	if len(plan.Tasks[1].DependsOn) != 1 || plan.Tasks[1].DependsOn[0] != "a" {
		t.Errorf("step b DependsOn = %v, want [a]", plan.Tasks[1].DependsOn)
	}
}

func TestExpandCustomTemplate_TaskOrderMatchesStepIndex_OneBased(t *testing.T) {
	// `TaskOrder: i + 1` — pin the 1-based ordering. mission_tasks.task_order
	// is used for "show me step N" UX; off-by-one would mis-label every
	// step in the mission timeline.
	in := `{"steps": [
		{"id": "a", "title": "A"},
		{"id": "b", "title": "B"},
		{"id": "c", "title": "C"}
	]}`
	plan, _ := ExpandCustomTemplate(in, AgentMapping{})
	for i, want := range []int{1, 2, 3} {
		if plan.Tasks[i].TaskOrder != want {
			t.Errorf("Tasks[%d].TaskOrder = %d, want %d (1-based, i+1)", i, plan.Tasks[i].TaskOrder, want)
		}
	}
}

func TestExpandCustomTemplate_MaxIterations_ZeroIsNilPointer(t *testing.T) {
	// Source: `if step.MaxIterations > 0` — zero is treated as "use
	// engine default" and stored as nil pointer (not *0). Pin this
	// because mission_tasks.max_iterations is NULL for "default" and
	// a misread would cap at literal 0 iterations.
	in := `{"steps": [{"id": "x", "title": "X"}]}`
	plan, _ := ExpandCustomTemplate(in, AgentMapping{})
	if plan.Tasks[0].MaxIterations != nil {
		t.Errorf("MaxIterations = %v, want nil (omit → engine default)", *plan.Tasks[0].MaxIterations)
	}

	in2 := `{"steps": [{"id": "x", "title": "X", "max_iterations": 0}]}`
	plan2, _ := ExpandCustomTemplate(in2, AgentMapping{})
	if plan2.Tasks[0].MaxIterations != nil {
		t.Errorf("explicit zero MaxIterations = %v, want nil (0 is sentinel for default, not literal 0)", *plan2.Tasks[0].MaxIterations)
	}
}

func TestExpandCustomTemplate_MaxIterations_PositivePropagated(t *testing.T) {
	in := `{"steps": [{"id": "x", "title": "X", "max_iterations": 7}]}`
	plan, _ := ExpandCustomTemplate(in, AgentMapping{})
	if plan.Tasks[0].MaxIterations == nil {
		t.Fatal("MaxIterations = nil, want *7")
	}
	if *plan.Tasks[0].MaxIterations != 7 {
		t.Errorf("MaxIterations = %d, want 7", *plan.Tasks[0].MaxIterations)
	}
}

func TestExpandCustomTemplate_DescriptionOmittedWhenEmpty(t *testing.T) {
	// Source: `if step.Description != "" { task.Description = step.Description }`.
	// When the step JSON omits description, the field stays at the zero
	// value (empty string). Pin that the assignment is conditional —
	// not a strict requirement, but a regression that always set it
	// to "" would still pass this test, so we're really pinning that
	// the empty case doesn't panic / drop the step.
	in := `{"steps": [{"id": "x", "title": "X"}]}`
	plan, err := ExpandCustomTemplate(in, AgentMapping{})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if plan.Tasks[0].Description != "" {
		t.Errorf("Description = %q, want empty when omitted", plan.Tasks[0].Description)
	}
}

func TestExpandCustomTemplate_OverridesNotAppliedByThisPath(t *testing.T) {
	// ExpandCustomTemplate calls `expandWorkflow(tmpl, mapping, nil)`
	// — the third arg is always nil. So title overrides are NOT
	// available via this path (they're only available via the named-
	// builtin path). Pin so a future "support overrides on custom"
	// has to update this test in step.
	in := `{"steps": [{"id": "x", "title": "Original"}]}`
	plan, _ := ExpandCustomTemplate(in, AgentMapping{})
	if plan.Tasks[0].Title != "Original" {
		t.Errorf("Title = %q, want \"Original\" (custom path doesn't accept overrides)", plan.Tasks[0].Title)
	}
}
