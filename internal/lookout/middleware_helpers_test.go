package lookout

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// middleware.go — BlockedError.Error format + severityRank ordering.
//
// BlockedError surfaces verbatim through HTTP error bodies and agent
// log lines; its message format is the load-bearing contract for
// operators triaging a block. severityRank is the tie-breaker
// InputGuard/OutputGuard use to pick which finding becomes the primary
// — a wrong ordering would surface a low-severity zero-width finding
// in the BlockedError when a high-severity DAN jailbreak should win.
// ---------------------------------------------------------------------------

func TestBlockedError_Error_Format(t *testing.T) {
	cases := []struct {
		name, direction string
		finding         Finding
		wantContains    []string
	}{
		{
			name:      "input-block-DAN",
			direction: "input",
			finding: Finding{
				Kind:     KindJailbreak,
				Severity: SeverityHigh,
				Detail:   "DAN jailbreak",
			},
			wantContains: []string{"lookout:", "input blocked", string(KindJailbreak), "DAN jailbreak"},
		},
		{
			name:      "output-block-secret",
			direction: "output",
			finding: Finding{
				Kind:     KindSecretOpenAI,
				Severity: SeverityCritical,
				Detail:   "OpenAI API key",
			},
			wantContains: []string{"lookout:", "output blocked", string(KindSecretOpenAI), "OpenAI API key"},
		},
		{
			name:      "empty-detail-still-renders",
			direction: "input",
			finding: Finding{
				Kind:     KindRoleOverride,
				Severity: SeverityMedium,
				Detail:   "",
			},
			// Even with empty detail, the format must produce a valid
			// non-empty string — the HTTP error body relies on this.
			wantContains: []string{"lookout:", "input blocked", string(KindRoleOverride), "()"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := &BlockedError{Direction: tc.direction, Finding: tc.finding}
			got := e.Error()
			for _, frag := range tc.wantContains {
				if !strings.Contains(got, frag) {
					t.Errorf("Error() = %q, missing fragment %q", got, frag)
				}
			}
		})
	}
}

func TestBlockedError_Error_Stable(t *testing.T) {
	// Pin the EXACT format string so a refactor surfaces here before
	// the HTTP error body changes shape. Existing call sites parse
	// nothing out of it, but operator runbooks reference the prefix.
	e := &BlockedError{
		Direction: "output",
		Finding:   Finding{Kind: KindSecretAnthropic, Detail: "anthropic key leaked"},
	}
	want := fmt.Sprintf("lookout: output blocked: %s (anthropic key leaked)", KindSecretAnthropic)
	if got := e.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestBlockedError_IsBlocked_ErrorsAsCompatible(t *testing.T) {
	// errors.As must unwrap *BlockedError through one level of
	// errors.Join (which InputGuard/OutputGuard use to merge the
	// block with a journal-emit failure). Pin the unwrap so a
	// regression that nests too deeply or wraps with %w incorrectly
	// breaks IsBlocked silently.
	be := &BlockedError{Direction: "input", Finding: Finding{Kind: KindZeroWidth}}
	joined := errors.Join(be, errors.New("emit failed"))
	if !IsBlocked(joined) {
		t.Error("IsBlocked(errors.Join(block, otherErr)) = false; want true")
	}
	// Non-block plain error: false.
	if IsBlocked(errors.New("plain")) {
		t.Error("IsBlocked(plain error) = true; want false")
	}
	// Nil: false.
	if IsBlocked(nil) {
		t.Error("IsBlocked(nil) = true; want false")
	}
}

// ---- severityRank ----

func TestSeverityRank_OrderedDescending(t *testing.T) {
	// The exact ordering pin: Critical > High > Medium > Low > unknown.
	// The guards use this to pick the "primary" finding for the
	// BlockedError when multiple findings fire on the same scan. A
	// regression that swapped High and Critical would surface a
	// medium-severity finding as the primary in the worst case.
	cases := []struct {
		s    Severity
		rank int
	}{
		{SeverityCritical, 4},
		{SeverityHigh, 3},
		{SeverityMedium, 2},
		{SeverityLow, 1},
		{Severity("unknown"), 0},
		{Severity(""), 0},
	}
	for _, tc := range cases {
		t.Run(string(tc.s), func(t *testing.T) {
			if got := severityRank(tc.s); got != tc.rank {
				t.Errorf("severityRank(%q) = %d, want %d", tc.s, got, tc.rank)
			}
		})
	}
}

func TestSeverityRank_TotalOrderInvariant(t *testing.T) {
	// Strict monotone: Critical > High > Medium > Low > 0. This is the
	// contract the guards' "pick highest" loop depends on.
	known := []Severity{SeverityCritical, SeverityHigh, SeverityMedium, SeverityLow}
	for i := 0; i < len(known)-1; i++ {
		if severityRank(known[i]) <= severityRank(known[i+1]) {
			t.Errorf("severityRank(%q) = %d is not > severityRank(%q) = %d",
				known[i], severityRank(known[i]), known[i+1], severityRank(known[i+1]))
		}
	}
	// Lowest known must still be > unknown.
	if severityRank(SeverityLow) <= severityRank(Severity("unknown")) {
		t.Error("severityRank(Low) should be > severityRank(unknown)")
	}
}

func TestSeverityRank_DrivesPrimaryFindingSelection(t *testing.T) {
	// Integration-style: InputGuard / OutputGuard pick the finding with
	// the highest severityRank as the BlockedError's primary. Without
	// running the full guard (which needs a journal emitter), simulate
	// the source's selection loop and verify Critical wins over High,
	// and a Low finding never wins over High.
	findings := []Finding{
		{Kind: KindZeroWidth, Severity: SeverityLow, Detail: "low zw"},
		{Kind: KindRoleOverride, Severity: SeverityHigh, Detail: "high role"},
		{Kind: KindSecretAnthropic, Severity: SeverityCritical, Detail: "critical secret"},
		{Kind: KindRTLOverride, Severity: SeverityMedium, Detail: "med rtl"},
	}

	primary := findings[0]
	for _, f := range findings[1:] {
		if severityRank(f.Severity) > severityRank(primary.Severity) {
			primary = f
		}
	}
	if primary.Severity != SeverityCritical {
		t.Errorf("primary picked = %+v, want the Critical finding", primary)
	}
	if primary.Kind != KindSecretAnthropic {
		t.Errorf("primary.Kind = %q, want %q", primary.Kind, KindSecretAnthropic)
	}
}
