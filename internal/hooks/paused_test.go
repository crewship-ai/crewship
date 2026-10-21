package hooks

import (
	"context"
	"testing"
)

// The package's own tests run with hooks on; the pause is pinned here.
func init() { EnableForTests() }

func TestDispatch_PausedRunsNothing(t *testing.T) {
	SetEnabledForTest(t, false)
	// A nil database would be a DispatchError if Dispatch looked at the
	// registry at all; paused, it returns before touching it.
	if err := Dispatch(context.Background(), nil, nil, EventPreLLMCall, EventContext{WorkspaceID: "ws"}); err != nil {
		t.Fatalf("paused Dispatch = %v, want nil", err)
	}
}

func TestEnabled_FollowsTheSwitch(t *testing.T) {
	SetEnabledForTest(t, false)
	if Enabled() {
		t.Fatal("Enabled() = true while paused")
	}
	SetEnabledForTest(t, true)
	if !Enabled() {
		t.Fatal("Enabled() = false after enabling")
	}
}
