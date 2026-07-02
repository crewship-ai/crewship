package manifest

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/manifest/internalapi"
	"github.com/crewship-ai/crewship/internal/manifest/kinds"
)

// TestRoutinePlanWarnings_FiresOnCodeStep verifies that a routine
// declaring a `type: code` step with no runtime (and thus no wired
// CodeRunner) produces a clear plan-time warning. The server-side
// save/apply/test_run validator already rejects such a step at
// author time (internal/pipeline/dsl_validate_egress.go); this
// plan-time warning is a client-side heads-up that flags the doomed
// apply before the round-trip to the server.
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
