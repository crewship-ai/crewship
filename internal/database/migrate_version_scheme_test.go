package database

import (
	"math"
	"strings"
	"testing"
)

// The validator is tested with synthetic values rather than only against the
// real slice: at the moment this lands there are no timestamped migrations
// yet, so a slice-only test would pass without exercising a single rule.
func TestValidateMigrationVersion(t *testing.T) {
	cases := []struct {
		name    string
		version int
		wantErr string // substring; "" means must be accepted
	}{
		{"legacy floor", 1, ""},
		{"legacy ceiling", legacySequentialCeiling, ""},
		{"zero", 0, "not positive"},
		{"negative", -5, "not positive"},

		// The regression this whole scheme exists to prevent: somebody
		// reaches for the next free small integer out of habit.
		{"next sequential integer", legacySequentialCeiling + 1, "not a YYYYMMDDHHMMSS timestamp"},
		{"a few past the ceiling", 200, "not a YYYYMMDDHHMMSS timestamp"},
		{"almost a timestamp", 2026072814300, "not a YYYYMMDDHHMMSS timestamp"}, // 13 digits

		{"valid stamp", 20260728143000, ""},
		{"valid stamp, later", 20301231235959, ""},

		{"month 13", 20261328143000, "not a valid YYYYMMDDHHMMSS timestamp"},
		{"day 32", 20260732143000, "not a valid YYYYMMDDHHMMSS timestamp"},
		{"hour 25", 20260728253000, "not a valid YYYYMMDDHHMMSS timestamp"},
		{"before the scheme began", 20200101000000, "before the timestamp scheme began"},
		// A 15-digit number fails at parse ("extra text"), which names the
		// problem well enough; the far-future bound is what catches a stamp
		// that is well-formed but absurd.
		{"extra digit", 202607281430000, "not a valid YYYYMMDDHHMMSS timestamp"},
		{"well-formed but absurd", 21000101000000, "implausibly far in the future"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateMigrationVersion(tc.version)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("validateMigrationVersion(%d) = %v, want nil", tc.version, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("validateMigrationVersion(%d) = nil, want error containing %q", tc.version, tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("validateMigrationVersion(%d) = %q, want it to mention %q", tc.version, err, tc.wantErr)
			}
		})
	}
}

// Every migration actually in the tree obeys the scheme. This is the gate a
// future PR trips when it appends {version: 170, …}.
func TestEveryMigrationFollowsTheVersionScheme(t *testing.T) {
	for _, m := range migrations {
		if err := validateMigrationVersion(m.version); err != nil {
			t.Errorf("migration %q: %v", m.name, err)
		}
	}
}

// The legacy block is closed. Nothing may be added to it — a new small
// integer is exactly the collision this scheme removes — and the ceiling
// must keep matching reality, so bumping it requires deliberately editing
// this test too.
func TestLegacyBlockIsClosed(t *testing.T) {
	var highestLegacy, legacyCount int
	for _, m := range migrations {
		if m.version <= legacySequentialCeiling {
			legacyCount++
			if m.version > highestLegacy {
				highestLegacy = m.version
			}
		}
	}
	if highestLegacy != legacySequentialCeiling {
		t.Errorf("highest legacy migration is v%d but the ceiling is v%d — "+
			"if a sequential migration was added, it should have been timestamped; "+
			"if one was removed, the ceiling stays put (those numbers are applied in "+
			"databases we do not control)", highestLegacy, legacySequentialCeiling)
	}
	if legacyCount != legacySequentialCeiling {
		t.Errorf("legacy block holds %d migrations for %d version numbers — "+
			"the sequential era had no gaps", legacyCount, legacySequentialCeiling)
	}
}

// A timestamp version needs 64-bit ints. The release matrix is amd64/arm64
// only, so this holds; the test exists so that adding a 32-bit target fails
// here rather than silently truncating a version number at runtime.
func TestPlatformCanHoldTimestampVersions(t *testing.T) {
	if math.MaxInt < 99999999999999 {
		t.Fatalf("int tops out at %d — a YYYYMMDDHHMMSS migration version does not fit "+
			"and would wrap into a number that collides with the legacy block", math.MaxInt)
	}
}
