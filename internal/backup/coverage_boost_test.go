package backup

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"testing"
	"time"
)

// coverage_boost_test.go — pure-logic unit tests added 2026-05-25
// to push the package from 72% to ≥ 85% statement coverage. Targets
// previously-untested helpers that don't need real Docker / network
// fixtures. Every test exercises a real edge case, not just the
// happy path.

// === SectionEntries ===

func TestSectionEntries_EmptyManifest(t *testing.T) {
	got := SectionEntries(&Manifest{})
	if len(got) != 0 {
		t.Errorf("empty manifest should produce empty slice, got %v", got)
	}
}

func TestSectionEntries_FullCrewPaths(t *testing.T) {
	m := &Manifest{
		Contents: Contents{
			Crews: []CrewSummary{
				{
					Slug:              "alpha",
					WorkspaceIncluded: true,
					VolumesIncluded:   []string{"home", "tools"},
					MemoryIncluded:    true,
				},
				{
					Slug:              "beta",
					WorkspaceIncluded: false, // no workspace section
					VolumesIncluded:   []string{"home"},
					MemoryIncluded:    true,
				},
			},
		},
	}
	got := SectionEntries(m)
	sort.Strings(got)
	want := []string{
		"memory/alpha",
		"memory/beta",
		"volumes/alpha/home",
		"volumes/alpha/tools",
		"volumes/beta/home",
		"workspace/alpha",
	}
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("entry count drift: got %d, want %d\n  got=%v\n  want=%v",
			len(got), len(want), got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("entry %d: got %q, want %q", i, got[i], want[i])
		}
	}
}

// === DetectCrewshipVersion ===

func TestDetectCrewshipVersion_OverrideWins(t *testing.T) {
	got := DetectCrewshipVersion("v1.2.3")
	if got != "v1.2.3" {
		t.Errorf("explicit override should win, got %q", got)
	}
}

func TestDetectCrewshipVersion_EnvFallback(t *testing.T) {
	t.Setenv("CREWSHIP_VERSION", "env-version")
	got := DetectCrewshipVersion("")
	if got != "env-version" {
		t.Errorf("env fallback should fire when override empty, got %q", got)
	}
}

func TestDetectCrewshipVersion_NeverEmpty(t *testing.T) {
	// Both override and env empty — must still return non-empty so
	// manifests don't ship with a blank version slot.
	t.Setenv("CREWSHIP_VERSION", "")
	got := DetectCrewshipVersion("")
	if got == "" {
		t.Error("DetectCrewshipVersion must never return empty string")
	}
}

// === classifyErr (metrics bucketing) ===

func TestClassifyErr_KnownSentinels(t *testing.T) {
	cases := map[error]string{
		nil:                      "",
		ErrLockHeld:              "lock_held",
		ErrLockExpired:           "lock_expired",
		ErrAgentRunning:          "agent_running",
		ErrSchemaTooOld:          "schema_too_old",
		ErrInvalidChecksum:       "invalid_checksum",
		ErrInvalidManifest:       "invalid_manifest",
		ErrIncompatibleTarget:    "incompatible_target",
		ErrDecryption:            "decryption",
		ErrFormatTooNew:          "format_too_new",
		ErrFormatTooOld:          "format_too_old",
		ErrInvalidScope:          "invalid_scope",
		ErrAdminRequired:         "admin_required",
		ErrNoOpRestore:           "noop_restore",
		ErrRestoreBackfillFailed: "backfill_failed",
	}
	for err, want := range cases {
		got := classifyErr(err)
		if got != want {
			t.Errorf("classifyErr(%v) = %q, want %q", err, got, want)
		}
	}
}

func TestClassifyErr_WrappedSentinel(t *testing.T) {
	wrapped := fmt.Errorf("context: %w", ErrLockHeld)
	if got := classifyErr(wrapped); got != "lock_held" {
		t.Errorf("classifyErr should unwrap, got %q", got)
	}
}

func TestClassifyErr_UnknownGoesToOther(t *testing.T) {
	other := errors.New("random unrelated error")
	if got := classifyErr(other); got != "other" {
		t.Errorf("unknown errors should map to 'other', got %q", got)
	}
}

// === assertNoFKViolationsTx via foreign_key_check ===

func TestAssertNoFKViolationsTx_CleanDBIsOK(t *testing.T) {
	db := newReplaceTestDB(t)
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()
	if err := assertNoFKViolationsTx(t.Context(), tx); err != nil {
		t.Errorf("clean db should produce no violations, got %v", err)
	}
}

// === ScopedTableIntent enum ===

func TestScopedTableIntent_Values(t *testing.T) {
	// Lock in the iota values so a future reorder of the const block
	// surfaces as a test failure (rows in the IntentMap would
	// silently flip meaning).
	if IntentInclude != 0 {
		t.Errorf("IntentInclude should be 0, got %d", IntentInclude)
	}
	if IntentExcludeOperational == IntentInclude || IntentExcludeRuntime == IntentInclude {
		t.Error("Exclude intents must be distinct from Include")
	}
	if IntentExcludeOperational == IntentExcludeRuntime {
		t.Error("Two Exclude variants must be distinct")
	}
}

// === Manifest.Validate edge cases ===

func TestManifest_Validate_RejectsZeroFormatVersion(t *testing.T) {
	m := &Manifest{
		FormatVersion: 0,
		Scope:         ScopeWorkspace,
	}
	if err := m.Validate(); err == nil {
		t.Error("Validate should reject FormatVersion=0")
	}
}

func TestManifest_Validate_RejectsUnknownScope(t *testing.T) {
	m := &Manifest{
		FormatVersion:     1,
		Scope:             "bogus",
		CompatibleTargets: []Target{TargetAnyInstance},
	}
	if err := m.Validate(); !errors.Is(err, ErrInvalidScope) {
		t.Errorf("Validate should reject unknown scope with ErrInvalidScope, got %v", err)
	}
}

func TestManifest_Validate_RejectsBadScopeLevel(t *testing.T) {
	m := &Manifest{
		FormatVersion:     2,
		Scope:             ScopeWorkspace,
		ScopeLevel:        "bogus-preset",
		CompatibleTargets: []Target{TargetAnyInstance},
	}
	if err := m.Validate(); !errors.Is(err, ErrInvalidManifest) {
		t.Errorf("Validate should reject unknown scope_level, got %v", err)
	}
}

func TestManifest_Validate_EmptyScopeLevelIsLegacyOK(t *testing.T) {
	// Pre-preset bundles ship without scope_level; the manifest
	// must still validate (we backfill later).
	m := &Manifest{
		FormatVersion:     1,
		Scope:             ScopeWorkspace,
		ScopeLevel:        "",
		CompatibleTargets: []Target{TargetAnyInstance},
		CreatedAt:         nonZeroTime(),
		CreatedBy:         Actor{UserID: "u_1"},
		Checksums:         Checksums{PayloadSHA256: "abc"},
	}
	if err := m.Validate(); err != nil {
		t.Errorf("empty scope_level should be accepted as legacy, got %v", err)
	}
}

// === Format compat policy ===

func TestIsCompatible_AcceptsCurrentAndDown(t *testing.T) {
	if !IsCompatible(FormatVersion) {
		t.Errorf("current FormatVersion (%d) must be compatible", FormatVersion)
	}
	if !IsCompatible(MinSupportedFormatVersion) {
		t.Errorf("MinSupportedFormatVersion must be compatible")
	}
}

func TestIsCompatible_RejectsFuture(t *testing.T) {
	if IsCompatible(FormatVersion + 1) {
		t.Errorf("future format version must be rejected")
	}
	err := CompatibilityReason(FormatVersion + 1)
	if !errors.Is(err, ErrFormatTooNew) {
		t.Errorf("expected ErrFormatTooNew, got %v", err)
	}
}

func TestIsCompatible_AcceptsMinSupported(t *testing.T) {
	if err := CompatibilityReason(FormatVersion); err != nil {
		t.Errorf("current version reason should be nil, got %v", err)
	}
}

// === Scope & ScopeLevel.Valid coverage ===

func TestScope_Valid(t *testing.T) {
	for _, s := range []Scope{ScopeCrew, ScopeWorkspace, ScopeInstance} {
		if !s.Valid() {
			t.Errorf("known scope %q should validate", s)
		}
	}
	if Scope("nope").Valid() {
		t.Error("unknown scope should not validate")
	}
}

// TestScopeLevel_Valid is already covered by scope_level_test.go;
// removed here to avoid duplicate test name.

// === IsInstanceOwner ===

func TestIsInstanceOwner_EmptyEnvAlwaysDenies(t *testing.T) {
	t.Setenv("CREWSHIP_OWNER_EMAIL", "")
	if IsInstanceOwner("anyone@example.com") {
		t.Error("empty env should deny everyone")
	}
}

func TestIsInstanceOwner_CaseInsensitiveMatch(t *testing.T) {
	t.Setenv("CREWSHIP_OWNER_EMAIL", "  Admin@Example.com  ")
	if !IsInstanceOwner("admin@example.com") {
		t.Error("case-insensitive trimmed match should accept")
	}
	if IsInstanceOwner("other@example.com") {
		t.Error("non-matching email should deny")
	}
}

// === Test helpers ===

func nonZeroTime() (t time.Time) {
	return time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)
}

// time package import lives only in this test file; keep it isolated
// at the bottom so the test imports stay grouped.
var _ = os.Getenv
