package main

import (
	"testing"
)

func TestSetEffort_ValidAndInvalid(t *testing.T) {
	// Reset latch.
	t.Cleanup(func() { effortMode = "" })

	for _, v := range []string{"minimal", "low", "medium", "high", "xhigh", "MEDIUM", "  high  "} {
		if err := SetEffort(v); err != nil {
			t.Errorf("SetEffort(%q) returned error: %v", v, err)
		}
	}
	if err := SetEffort(""); err != nil {
		t.Errorf("empty should reset, got error %v", err)
	}
	if effortMode != "" {
		t.Errorf("empty should clear, got %q", effortMode)
	}

	if err := SetEffort("bananas"); err == nil {
		t.Error("expected error on unknown level")
	}
}

func TestChatCreationBody_DefaultsAndMetadata(t *testing.T) {
	defer func() {
		planModeRequested = false
		effortMode = ""
	}()

	// Default — no metadata.
	planModeRequested = false
	effortMode = ""
	b := ChatCreationBody()
	if b["mode"] != "CHAT" || b["origin"] != "CLI" {
		t.Errorf("bad defaults: %v", b)
	}
	if _, ok := b["metadata"]; ok {
		t.Errorf("metadata should be absent when no flags set: %v", b)
	}

	// plan_mode populated.
	planModeRequested = true
	b = ChatCreationBody()
	md, ok := b["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("metadata missing: %v", b)
	}
	if md["plan_mode"] != true {
		t.Errorf("plan_mode not set: %v", md)
	}

	// effort populated alongside plan.
	effortMode = "high"
	b = ChatCreationBody()
	md = b["metadata"].(map[string]any)
	if md["effort"] != "high" {
		t.Errorf("effort not set: %v", md)
	}
}

func TestApplyPlanFlag_OnlyWhenLatchSet(t *testing.T) {
	defer func() { planModeRequested = false }()

	planModeRequested = false
	if got := ApplyPlanFlag("hi"); got != "hi" {
		t.Errorf("should be no-op when latch off, got %q", got)
	}

	planModeRequested = true
	out := ApplyPlanFlag("rewrite auth")
	if out == "rewrite auth" {
		t.Error("expected prefix injection")
	}
	// Re-application is idempotent.
	out2 := ApplyPlanFlag(out)
	if out2 != out {
		t.Error("ApplyPlanFlag should be idempotent")
	}
}
