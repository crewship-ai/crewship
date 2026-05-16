package scrubber

import (
	"strings"
	"testing"
)

func TestValidate_EmptyInput(t *testing.T) {
	s := New()
	res := s.Validate("", ModeBlock)
	if res.Decision != DecisionAllow {
		t.Fatalf("expected DecisionAllow on empty input, got %v", res.Decision)
	}
	if len(res.Hits) != 0 {
		t.Fatalf("expected no hits on empty input, got %d", len(res.Hits))
	}
}

func TestValidate_CleanInput(t *testing.T) {
	s := New()
	res := s.Validate("just some plain memory text\n## section\n- bullet\n", ModeBlock)
	if res.Decision != DecisionAllow {
		t.Fatalf("expected DecisionAllow on clean input, got %v", res.Decision)
	}
	if len(res.Hits) != 0 {
		t.Fatalf("expected no hits on clean input, got %d (%+v)", len(res.Hits), res.Hits)
	}
}

func TestValidate_BlockMode_RejectsAnthropicKey(t *testing.T) {
	s := New()
	input := "remember my key sk-ant-api03-abcd1234efgh5678ijkl"
	res := s.Validate(input, ModeBlock)
	if res.Decision != DecisionReject {
		t.Fatalf("expected DecisionReject in block mode with secret, got %v", res.Decision)
	}
	if len(res.Hits) == 0 {
		t.Fatalf("expected at least one hit, got 0")
	}
	found := false
	for _, h := range res.Hits {
		if h.Pattern == "anthropic_key" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected anthropic_key hit, got %+v", res.Hits)
	}
}

func TestValidate_WarnMode_AllowsWithHits(t *testing.T) {
	s := New()
	input := "key=sk-ant-api03-abcd1234efgh5678ijkl"
	res := s.Validate(input, ModeWarn)
	if res.Decision != DecisionAllow {
		t.Fatalf("expected DecisionAllow in warn mode, got %v", res.Decision)
	}
	if len(res.Hits) == 0 {
		t.Fatalf("expected hits to be populated even in warn mode, got 0")
	}
}

func TestValidate_RedactMode_ReturnsCleaned(t *testing.T) {
	s := New()
	input := "key=sk-ant-api03-abcd1234efgh5678ijkl rest"
	res := s.Validate(input, ModeRedact)
	if res.Decision != DecisionAllow {
		t.Fatalf("expected DecisionAllow in redact mode, got %v", res.Decision)
	}
	if !strings.Contains(res.Cleaned, "[REDACTED:anthropic_key]") {
		t.Fatalf("expected redacted marker in Cleaned, got %q", res.Cleaned)
	}
	if strings.Contains(res.Cleaned, "sk-ant-api03") {
		t.Fatalf("redacted output still contains the key: %q", res.Cleaned)
	}
	if len(res.Hits) == 0 {
		t.Fatalf("expected hits in redact mode too")
	}
}

func TestValidate_MultipleHits_BlockMode(t *testing.T) {
	s := New()
	input := "anthropic sk-ant-api03-abcd1234efgh5678ijkl\nopenai sk-proj-abcd1234efgh5678ijkl\n"
	res := s.Validate(input, ModeBlock)
	if res.Decision != DecisionReject {
		t.Fatalf("expected DecisionReject, got %v", res.Decision)
	}
	if len(res.Hits) < 2 {
		t.Fatalf("expected at least two hits, got %d (%+v)", len(res.Hits), res.Hits)
	}
}

func TestValidate_ZeroWidthBypass_StillDetected(t *testing.T) {
	s := New()
	// Insert ZWSP between `sk-ant-` and the body — Scrub already strips
	// these before matching; Validate must use the same normalisation.
	input := "key sk-ant-​api03-abcd1234efgh5678ijkl"
	res := s.Validate(input, ModeBlock)
	if res.Decision != DecisionReject {
		t.Fatalf("zero-width bypass should still be rejected in block mode, got %v", res.Decision)
	}
}

func TestValidate_AllowlistRegex_SkipsMatchingPattern(t *testing.T) {
	s := New()
	// Allowlist for the exact placeholder shape an ops doc uses.
	res := s.ValidateWithAllowlist(
		"example sk-ant-EXAMPLE_PLACEHOLDER_DO_NOT_USE",
		ModeBlock,
		`sk-ant-EXAMPLE_[A-Z_]+`,
	)
	if res.Decision != DecisionAllow {
		t.Fatalf("allowlist should rescue placeholder, got %v with hits %+v", res.Decision, res.Hits)
	}
}

func TestValidate_AllowlistRegex_DoesNotRescueRealKey(t *testing.T) {
	s := New()
	res := s.ValidateWithAllowlist(
		"key sk-ant-api03-realabcd1234efgh5678",
		ModeBlock,
		`sk-ant-EXAMPLE_[A-Z_]+`,
	)
	if res.Decision != DecisionReject {
		t.Fatalf("real key should still be rejected despite allowlist, got %v", res.Decision)
	}
}

func TestValidate_AllowlistRegex_Invalid_FailsClosed(t *testing.T) {
	s := New()
	// Bad regex must not silently bypass scrubber — caller gets the
	// strict result (i.e. reject on secret).
	res := s.ValidateWithAllowlist(
		"key sk-ant-api03-realabcd1234efgh5678",
		ModeBlock,
		`(unclosed[`,
	)
	if res.Decision != DecisionReject {
		t.Fatalf("invalid allowlist must fail-closed (reject), got %v", res.Decision)
	}
}
