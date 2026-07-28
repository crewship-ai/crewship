package manifest

import (
	"context"
	"testing"
)

// Asking for one gate opened the other.
//
// skipTestGateClient had no field saying whether the TEST gate was wanted —
// its existence implied it — and mergeSkipTestGate set skip_test_gate=true
// unconditionally. So `withSkipGovernanceGate` on a plain client produced a
// wrapper that forwarded both, and `crewship apply --skip-governance-gate`
// (without --skip-test-gate) silently bypassed the server's
// required-passing-test gate as well.
//
// That is the exact outcome the file's own doc comment promised could not
// happen: "asking for one gate does not silently open the other." A comment
// asserting a property the code does not have is worse than no comment.

func TestGates_GovernanceAloneDoesNotSkipTheTestGate(t *testing.T) {
	fake := &fakeInternalAPIClient{}
	c := withSkipGovernanceGate(fake)

	if _, err := c.Post(context.Background(), "/api/v1/workspaces/ws_test/pipelines/save",
		map[string]any{"slug": "x"}); err != nil {
		t.Fatalf("post: %v", err)
	}
	if len(fake.posts) != 1 {
		t.Fatalf("want 1 post, got %d", len(fake.posts))
	}
	body, ok := fake.posts[0].body.(map[string]any)
	if !ok {
		t.Fatalf("body is not a map: %T", fake.posts[0].body)
	}
	if body["skip_governance_gate"] != true {
		t.Errorf("the gate that WAS asked for is missing: %+v", body)
	}
	if _, present := body["skip_test_gate"]; present {
		t.Errorf("--skip-governance-gate must not open the test gate too: %+v", body)
	}
}

func TestGates_TestGateAloneDoesNotSkipGovernance(t *testing.T) {
	fake := &fakeInternalAPIClient{}
	c := withSkipTestGate(fake)

	if _, err := c.Post(context.Background(), "/api/v1/workspaces/ws_test/pipelines/save",
		map[string]any{"slug": "x"}); err != nil {
		t.Fatalf("post: %v", err)
	}
	body := fake.posts[0].body.(map[string]any)
	if body["skip_test_gate"] != true {
		t.Errorf("the gate that WAS asked for is missing: %+v", body)
	}
	if _, present := body["skip_governance_gate"]; present {
		t.Errorf("--skip-test-gate must not open the governance gate: %+v", body)
	}
}

func TestGates_BothWhenBothAreAskedFor(t *testing.T) {
	fake := &fakeInternalAPIClient{}
	c := withSkipGovernanceGate(withSkipTestGate(fake))

	if _, err := c.Post(context.Background(), "/api/v1/workspaces/ws_test/pipelines/save",
		map[string]any{"slug": "x"}); err != nil {
		t.Fatalf("post: %v", err)
	}
	body := fake.posts[0].body.(map[string]any)
	if body["skip_test_gate"] != true || body["skip_governance_gate"] != true {
		t.Errorf("both flags were asked for and both must be sent: %+v", body)
	}
}

func TestGates_WrappingOrderDoesNotMatter(t *testing.T) {
	// planNewKinds and wrapKindExec compose these in different orders
	// depending on which flags are set; the result must not depend on it.
	fake := &fakeInternalAPIClient{}
	c := withSkipTestGate(withSkipGovernanceGate(fake))

	if _, err := c.Post(context.Background(), "/api/v1/workspaces/ws_test/pipelines/save",
		map[string]any{"slug": "x"}); err != nil {
		t.Fatalf("post: %v", err)
	}
	body := fake.posts[0].body.(map[string]any)
	if body["skip_test_gate"] != true || body["skip_governance_gate"] != true {
		t.Errorf("both flags must survive either wrapping order: %+v", body)
	}
	// And exactly one wrapper, not two nested ones each adding a flag.
	if len(fake.posts) != 1 {
		t.Errorf("want a single post, got %d", len(fake.posts))
	}
}

func TestGates_NeitherFlagLeavesTheBodyAlone(t *testing.T) {
	fake := &fakeInternalAPIClient{}
	if _, err := fake.Post(context.Background(), "/api/v1/workspaces/ws_test/pipelines/save",
		map[string]any{"slug": "x"}); err != nil {
		t.Fatalf("post: %v", err)
	}
	body := fake.posts[0].body.(map[string]any)
	for _, k := range []string{"skip_test_gate", "skip_governance_gate"} {
		if _, present := body[k]; present {
			t.Errorf("an unwrapped client must send neither gate flag, found %q", k)
		}
	}
}
