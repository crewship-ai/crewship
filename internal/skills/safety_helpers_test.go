package skills

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// safety.go — AllowedSPDXLicenses + LicenseError.Error.
//
// AllowedSPDXLicenses is consumed by error messages + CLI help text so
// users know what's accepted. LicenseError.Error is what surfaces in
// the HTTP body / CLI stderr when an import is rejected — both
// branches (detected vs no-SPDX) need pinning so the operator-facing
// hint stays actionable.
// ---------------------------------------------------------------------------

func TestAllowedSPDXLicenses_ContainsKnownAllowlist(t *testing.T) {
	// Every member of the allowlist must appear in the snapshot.
	// LicenseAllowed and AllowedSPDXLicenses MUST stay in sync —
	// a regression that drops a license from one without the other
	// would surface as "we allow X but the help text doesn't say so"
	// (or vice-versa, "the help text claims X is allowed but
	// LicenseAllowed rejects it").
	want := []string{
		"0BSD", "Apache-2.0", "BSD-2-Clause", "BSD-3-Clause",
		"CC0-1.0", "ISC", "MIT", "MPL-2.0", "Unlicense",
	}
	got := AllowedSPDXLicenses()
	if len(got) != len(want) {
		t.Fatalf("got %d licenses (%v), want %d (%v)", len(got), got, len(want), want)
	}
	// Sorted by source contract ("a sorted snapshot").
	for i := 1; i < len(got); i++ {
		if got[i-1] > got[i] {
			t.Errorf("not sorted: got[%d]=%q > got[%d]=%q", i-1, got[i-1], i, got[i])
		}
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("got[%d] = %q, want %q", i, got[i], w)
		}
	}
}

func TestAllowedSPDXLicenses_InSyncWithLicenseAllowed(t *testing.T) {
	// Every license in the returned snapshot must pass LicenseAllowed,
	// AND there must be no LicenseAllowed-true value outside the snapshot.
	// The first direction guards against dropped values; the second
	// against the snapshot ever excluding an allowed value.
	got := AllowedSPDXLicenses()
	for _, l := range got {
		if !LicenseAllowed(l) {
			t.Errorf("snapshot lists %q but LicenseAllowed says no — drift detected", l)
		}
	}
	// Spot-check that a definitely-not-allowed value is rejected (a
	// regression that started returning true for everything would
	// silently break the import gate).
	for _, denied := range []string{"GPL-3.0", "AGPL-3.0", "Proprietary", "", "unknown"} {
		if LicenseAllowed(denied) {
			t.Errorf("LicenseAllowed(%q) = true; want false", denied)
		}
	}
}

func TestAllowedSPDXLicenses_ReturnsCopy(t *testing.T) {
	// Defensive: callers may mutate the returned slice (sort, reverse,
	// append). The function must NOT expose the internal map's keys
	// directly. Mutating the returned slice must not affect subsequent
	// calls. (The current implementation builds a fresh slice; pin
	// that semantic.)
	first := AllowedSPDXLicenses()
	if len(first) == 0 {
		t.Fatal("snapshot empty")
	}
	original := first[0]
	first[0] = "MUTATED"
	second := AllowedSPDXLicenses()
	if second[0] != original {
		t.Errorf("mutating returned slice leaked into next call: second[0] = %q, want %q",
			second[0], original)
	}
}

// ---- LicenseError.Error ----

func TestLicenseError_Error_WithDetectedSPDX(t *testing.T) {
	// Detected non-empty → message mentions the SPDX id alongside the
	// raw input, so an operator can see what was detected and decide
	// whether to override.
	e := &LicenseError{Detected: "GPL-3.0", Raw: "GNU General Public License v3.0"}
	got := e.Error()
	for _, fragment := range []string{"GPL-3.0", "GNU General Public License v3.0", "not on the import allowlist"} {
		if !strings.Contains(got, fragment) {
			t.Errorf("Error() = %q, missing %q", got, fragment)
		}
	}
}

func TestLicenseError_Error_WithoutDetectedSPDX(t *testing.T) {
	// Detected empty → message explicitly says "no SPDX id detected"
	// so the operator knows the rejection wasn't because the SPDX is
	// on the deny-list but because DetectSPDX couldn't infer one.
	e := &LicenseError{Detected: "", Raw: "Complete terms in LICENSE.txt"}
	got := e.Error()
	for _, fragment := range []string{"Complete terms in LICENSE.txt", "no SPDX id detected", "not on the SPDX allowlist"} {
		if !strings.Contains(got, fragment) {
			t.Errorf("Error() = %q, missing %q", got, fragment)
		}
	}
}

func TestLicenseError_Error_EmptyRaw(t *testing.T) {
	// An entirely-empty input still produces a deterministic, non-empty
	// error message so the caller's "wrap with context" pipeline doesn't
	// produce a meaningless "license  is not on the import allowlist"
	// orphan word — but a quoted empty string in the message IS
	// acceptable. Pin the current behavior.
	e := &LicenseError{}
	got := e.Error()
	if got == "" {
		t.Error("zero-value LicenseError.Error() returned empty string; want some deterministic message")
	}
}

func TestLicenseError_ImplementsErrorInterface(t *testing.T) {
	// Compile-time assertion catches a refactor that breaks the
	// Error() receiver signature.
	var _ error = (*LicenseError)(nil)
}
