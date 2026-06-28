package pipeline

import "testing"

func TestErrorFingerprint_StableAcrossVolatileTokens(t *testing.T) {
	// Two runs failing the same way at the same step but with different
	// run ids / numbers / timestamps must share a fingerprint.
	a := ErrorFingerprint("probe", `code step "probe": run_abc123 failed at 2026-06-28T10:00:00Z with code 503`)
	b := ErrorFingerprint("probe", `code step "probe": run_xyz789 failed at 2026-06-28T11:22:33Z with code 503`)
	if a != b {
		t.Fatalf("expected same fingerprint across volatile tokens, got %q vs %q", a, b)
	}
	if len(a) != 16 {
		t.Fatalf("fingerprint should be 16 hex chars, got %q", a)
	}
}

func TestErrorFingerprint_DistinctOnDifferentStepOrMessage(t *testing.T) {
	base := ErrorFingerprint("probe", "boom")
	if base == ErrorFingerprint("other", "boom") {
		t.Fatal("different failed step should yield a different fingerprint")
	}
	if base == ErrorFingerprint("probe", "totally different failure") {
		t.Fatal("different message should yield a different fingerprint")
	}
}

func TestBoolToEnvStr(t *testing.T) {
	if boolToEnvStr(true) != "true" || boolToEnvStr(false) != "false" {
		t.Fatal("boolToEnvStr must render true/false")
	}
}

func TestNormalizeTag(t *testing.T) {
	if got := normalizeTag("  Prod-Alert  "); got != "prod-alert" {
		t.Fatalf("normalizeTag lower+trim: got %q", got)
	}
	long := make([]byte, 200)
	for i := range long {
		long[i] = 'a'
	}
	if got := normalizeTag(string(long)); len(got) != 128 {
		t.Fatalf("normalizeTag should cap at 128, got %d", len(got))
	}
}
