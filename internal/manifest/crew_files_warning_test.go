package manifest

import (
	"context"
	"strings"
	"testing"
)

// Files under a crew's shared tree are owned by the container user, so
// the server can only overwrite one by replaying the write inside the
// container. Against a stopped crew that is a 409 — and because apply is
// fail-fast it lands MID-RUN, after earlier resources are committed.
//
// The plan already knows both halves twenty seconds earlier. Saying so
// in the dry-run is the difference between "read this before you commit
// to anything" and "find out after the first three items landed".

func warnPlan(t *testing.T, status string, crewID string, fileCount int) []string {
	t.Helper()
	pb := &planBuilder{plan: &Plan{}, client: &Client{}}
	pb.containerStatusFn = func(string) (bool, bool) {
		switch status {
		case "running":
			return true, true
		case "stopped":
			return false, true
		default:
			return false, false
		}
	}
	pb.warnIfCrewStopped(context.Background(), "uctarna", crewID, fileCount)
	return pb.plan.Warnings
}

func TestWarnIfCrewStopped_WarnsAndNamesTheRemedy(t *testing.T) {
	w := warnPlan(t, "stopped", "crew-1", 3)
	if len(w) != 1 {
		t.Fatalf("warnings = %v, want exactly one", w)
	}
	got := w[0]
	for _, want := range []string{"uctarna", "not running", "409", "crewship crew start uctarna"} {
		if !strings.Contains(got, want) {
			t.Errorf("warning is missing %q:\n%s", want, got)
		}
	}
}

func TestWarnIfCrewStopped_SilentWhenRunning(t *testing.T) {
	if w := warnPlan(t, "running", "crew-1", 3); len(w) != 0 {
		t.Errorf("warned about a running crew: %v", w)
	}
}

// A crew being CREATED in this same apply has no container yet and its
// files are a first write to an unowned path. Warning there would fire
// on every fresh install and train people to skip the warnings block.
func TestWarnIfCrewStopped_SilentForANewCrew(t *testing.T) {
	if w := warnPlan(t, "stopped", "", 3); len(w) != 0 {
		t.Errorf("warned about a crew that does not exist yet: %v", w)
	}
}

func TestWarnIfCrewStopped_SilentWithNoFiles(t *testing.T) {
	if w := warnPlan(t, "stopped", "crew-1", 0); len(w) != 0 {
		t.Errorf("warned about a crew the manifest writes nothing to: %v", w)
	}
}

// "Could not tell" must not become "is stopped". A probe that fails —
// no IPC socket, an unreachable crewshipd, a status this build does not
// know — has to stay quiet, or a timeout produces a scary line about a
// crew that is running fine.
func TestWarnIfCrewStopped_SilentWhenStatusIsUnknown(t *testing.T) {
	for _, status := range []string{"unknown", "not_configured", "error", "creating"} {
		if w := warnPlan(t, status, "crew-1", 2); len(w) != 0 {
			t.Errorf("status %q produced a warning on a guess: %v", status, w)
		}
	}
}
