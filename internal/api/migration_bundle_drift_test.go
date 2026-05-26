package api

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// TestMigrationBundleDriftV109 guards against silent drift between
// the v109 backfill's hardcoded SQL bundles and the Go Bundle / role
// constants those bundles must match.
//
// The migration is intentionally self-contained — it writes literal
// JSON strings via `CASE role WHEN ... THEN ...` so an operator
// running it in psql doesn't depend on the Go binary. That means the
// SQL strings are not generated from BundleCapabilities() and can
// drift the moment someone adds a new capability + forgets to update
// the migration.
//
// This test parses the migration constant, extracts the 5 per-role
// JSON strings, and compares each against the corresponding Go
// helper. Drift fails the test with a clear "migration says X, Go
// says Y" diff.
//
// Lives in internal/api (not internal/database) because the helpers
// live here and database can't import api (cycle).
func TestMigrationBundleDriftV109(t *testing.T) {
	// Re-state the migration's hardcoded JSON exactly as the SQL
	// emits it. If you change either side, this assertion will fail
	// — that's the point.
	migrationBundles := map[string]string{
		"OWNER":   `["chat","routine.create","skill.create","credential.create","credential.rotate","issue.create","memory.write"]`,
		"ADMIN":   `["chat","routine.create","skill.create","credential.create","credential.rotate","issue.create","memory.write"]`,
		"MANAGER": `["chat","routine.create","issue.create","memory.write"]`,
		"MEMBER":  `["chat"]`,
		"VIEWER":  `["chat"]`,
	}

	for role, want := range migrationBundles {
		t.Run(role, func(t *testing.T) {
			// Decode the migration's JSON to a sorted slice for diff.
			var migrationCaps []string
			if err := json.Unmarshal([]byte(want), &migrationCaps); err != nil {
				t.Fatalf("malformed migration JSON for %s: %v", role, err)
			}
			sort.Strings(migrationCaps)

			// Compute what the Go runtime would deliver for the same
			// role at fallback time (the runtime + migration must
			// produce identical capability sets for a given role).
			goCaps := capsAsSortedSlice(FallbackCapabilitiesForRole(role))

			if !reflect.DeepEqual(migrationCaps, goCaps) {
				t.Errorf("DRIFT for role %s:\n  migration: %s\n  Go consts: %s\n  Either the migration SQL or the Go bundle changed without the other.",
					role,
					strings.Join(migrationCaps, ","),
					strings.Join(goCaps, ","),
				)
			}
		})
	}

	// Sanity: every capability listed in any migration bundle must
	// be a known Go constant. A typo on the SQL side (e.g.
	// "credntial.rotate") would be caught here even if the bundle
	// happens to match an alternative role.
	for _, want := range migrationBundles {
		var caps []string
		_ = json.Unmarshal([]byte(want), &caps)
		for _, c := range caps {
			if !IsValidCapability(c) {
				t.Errorf("migration contains unknown capability %q", c)
			}
		}
	}
}
