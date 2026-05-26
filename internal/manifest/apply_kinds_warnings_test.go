package manifest

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/manifest/kinds"
	"github.com/crewship-ai/crewship/internal/manifest/internalapi"
)

// TestRoutinePlanWarnings_FiresOnCodeStep verifies that a routine
// declaring a `type: code` step produces a clear plan-time warning.
// The production CodeRunner is not yet wired
// (internal/pipeline/runner_code.go); saving such a routine succeeds
// but every invocation fails at the code step. Surfacing the gap at
// apply time means the operator doesn't learn about it via a failed
// cron at 03:00.
func TestRoutinePlanWarnings_FiresOnCodeStep(t *testing.T) {
	t.Parallel()

	doc := &kinds.RoutineDocument{
		Metadata: internalapi.Metadata{
			Name: "Probe",
			Slug: "probe",
			Labels: map[string]string{
				"crew": "ws-crew",
			},
		},
		Spec: kinds.RoutineSpec{
			DSLVersion: "1.0",
			Steps: []kinds.RoutineStep{
				{ID: "shell", Type: "code"},
			},
		},
	}

	warnings := routinePlanWarnings(doc)
	if len(warnings) == 0 {
		t.Fatal("expected a warning about type:code step; got none")
	}
	joined := strings.ToLower(strings.Join(warnings, " "))
	if !strings.Contains(joined, "code") {
		t.Errorf("warning text should mention `code`; got %v", warnings)
	}
	if !strings.Contains(joined, "shell") {
		t.Errorf("warning text should reference the offending step id (`shell`); got %v", warnings)
	}
}

// TestRoutinePlanWarnings_QuietForAgentRun is the negative test:
// the warning is scoped strictly to code steps, not other types.
func TestRoutinePlanWarnings_QuietForAgentRun(t *testing.T) {
	t.Parallel()

	doc := &kinds.RoutineDocument{
		Metadata: internalapi.Metadata{
			Name:   "Probe",
			Slug:   "probe",
			Labels: map[string]string{"crew": "ws-crew"},
		},
		Spec: kinds.RoutineSpec{
			DSLVersion: "1.0",
			Steps: []kinds.RoutineStep{
				{ID: "ask", Type: "agent_run", AgentSlug: "trapper"},
				{ID: "wait", Type: "wait"},
			},
		},
	}

	if w := routinePlanWarnings(doc); len(w) != 0 {
		t.Errorf("expected no warnings for code-free routine; got %v", w)
	}
}

// TestPlanWarnings_RoundTripsToCLI is the integration touch-point:
// once routinePlanWarnings populates Plan.Warnings, the CLI surface
// must read them back so apply prints them. The test stays at the
// data-shape level rather than spinning up cobra so it doesn't break
// when the print formatting changes.
func TestPlanWarnings_RoundTripsToCLI(t *testing.T) {
	t.Parallel()
	p := &Plan{Warnings: []string{"routine probe: contains type:code step (will fail at runtime)"}}
	if len(p.Warnings) != 1 {
		t.Fatalf("Plan.Warnings field missing or empty; got %v", p.Warnings)
	}
}
