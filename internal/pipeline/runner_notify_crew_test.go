package pipeline

import (
	"strings"
	"testing"
)

// #842 Phase 1: crew:<slug> notify targeting is intentionally NOT shipped yet —
// crews are groups of agents, the inbox targets users, and there is no
// crew→user ("human audience of a crew") mapping in the schema. Rather than
// guess a semantics (or silently ignore the target and surprise the author at
// run time), the notify target validator rejects crew: LOUDLY at save/validate,
// pointing the author at the tracking issue. See runner_notify.go.

func TestValidateNotifyTarget_CrewRejectedToPhase2(t *testing.T) {
	err := validateNotifyTarget("crew:engineering")
	if err == nil {
		t.Fatal("crew: target must be rejected at validate time (Phase 2), not accepted")
	}
	if !strings.Contains(err.Error(), "Phase 2") || !strings.Contains(err.Error(), "842") {
		t.Errorf("rejection should name Phase 2 and issue #842 so the author knows it's coming, got: %v", err)
	}
}

// A templated crew: target passes the author-time shape check (templates
// resolve at run time), but the runtime resolver must reject it with the same
// clear message once it renders to a literal crew:<slug>.
func TestResolveNotifyTarget_CrewRejectedAtRuntime(t *testing.T) {
	_, _, err := resolveNotifyTarget("crew:ops", "u_trigger")
	if err == nil {
		t.Fatal("crew: target must be rejected at run time (Phase 2)")
	}
	if !strings.Contains(err.Error(), "842") {
		t.Errorf("runtime rejection should name issue #842, got: %v", err)
	}
}
