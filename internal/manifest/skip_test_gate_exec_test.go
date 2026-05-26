package manifest

import (
	"context"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/manifest/internalapi"
)

// TestWrapKindExec_AppliesSkipTestGateOnExec catches a regression that
// the per-decorator unit test alone could not: planNewKinds wraps the
// PLANNING client with withSkipTestGate, but wrapKindExec built a
// fresh undecorated adapter for the EXEC path. End result: the routine
// save body that actually went to the server during apply lost the
// skip_test_gate field, so OWNER/ADMIN callers still hit HTTP 422 on
// the first apply.
//
// This test drives the exec wrapper directly: a captured inner closure
// POSTs to /pipelines/save and we assert the body the underlying
// APIClient sees carries skip_test_gate=true (i.e. the decorator was
// active on the exec path).
func TestWrapKindExec_AppliesSkipTestGateOnExec(t *testing.T) {
	t.Parallel()

	fake := newFakeAPI(t)
	client := NewClient(fake)

	inner := func(ctx context.Context, c internalapi.Client) error {
		_, _ = c.Post(ctx,
			"/api/v1/workspaces/ws_test/pipelines/save",
			map[string]any{"slug": "r", "name": "r"},
		)
		return nil
	}

	wrapped := wrapKindExec(inner, client)
	if wrapped == nil {
		t.Fatal("wrapKindExec returned nil for non-nil inner")
	}
	if err := wrapped(context.Background(), client, Options{SkipTestGate: true}); err != nil {
		t.Fatalf("wrapped exec: %v", err)
	}

	savePost := findCallBySuffix(fake.Calls, "POST", "/pipelines/save")
	if savePost == nil {
		t.Fatal("no POST to /pipelines/save was recorded — fake didn't see the call")
	}
	if got, _ := savePost.Body["skip_test_gate"].(bool); !got {
		t.Errorf("exec-path body missing skip_test_gate=true (decorator only active on plan path).\nbody=%v", savePost.Body)
	}
}

// TestWrapKindExec_NoSkipTestGateWhenOff is the negative pair: with
// Options.SkipTestGate=false the field must NOT be injected, so
// MANAGER+ callers (or apply runs that don't opt in) still go through
// the normal test_run gate.
func TestWrapKindExec_NoSkipTestGateWhenOff(t *testing.T) {
	t.Parallel()

	fake := newFakeAPI(t)
	client := NewClient(fake)

	inner := func(ctx context.Context, c internalapi.Client) error {
		_, _ = c.Post(ctx,
			"/api/v1/workspaces/ws_test/pipelines/save",
			map[string]any{"slug": "r", "name": "r"},
		)
		return nil
	}

	wrapped := wrapKindExec(inner, client)
	if err := wrapped(context.Background(), client, Options{SkipTestGate: false}); err != nil {
		t.Fatalf("wrapped exec: %v", err)
	}

	savePost := findCallBySuffix(fake.Calls, "POST", "/pipelines/save")
	if savePost == nil {
		t.Fatal("no POST to /pipelines/save was recorded")
	}
	if _, ok := savePost.Body["skip_test_gate"]; ok {
		t.Errorf("exec-path body carried skip_test_gate even though Options.SkipTestGate=false.\nbody=%v", savePost.Body)
	}
}

// findCallBySuffix returns the first fakeCall whose method matches and
// whose path ends with suffix. Slice scan rather than a map lookup so
// the helper doesn't impose ordering assumptions on the caller.
func findCallBySuffix(calls []fakeCall, method, suffix string) *fakeCall {
	for i := range calls {
		if calls[i].Method == method && strings.HasSuffix(calls[i].Path, suffix) {
			return &calls[i]
		}
	}
	return nil
}
