package database

import "testing"

// TestMigrationsAreStrictlyIncreasing guards the core invariant of the
// migration system: versions must be in ascending order with no duplicates.
// If a future PR appends a migration with a stale version or hand-edits an
// existing one to a colliding version, this test fails the build instead of
// letting the runtime collision check trip in production.
//
// Paired with the cross-branch lint in scripts/lint-migrations — that one
// catches the "version already in main got renamed" case, this one catches
// pure-monotonicity bugs visible in a single tree.
func TestMigrationsAreStrictlyIncreasing(t *testing.T) {
	if len(migrations) == 0 {
		t.Fatal("migrations slice is empty")
	}
	for i, m := range migrations {
		if i == 0 {
			if m.version < 1 {
				t.Errorf("migrations[0].version = %d, want >= 1", m.version)
			}
			continue
		}
		prev := migrations[i-1].version
		if m.version <= prev {
			t.Errorf(
				"migrations[%d] (%s, v%d) not strictly greater than previous (%s, v%d)",
				i, m.name, m.version, migrations[i-1].name, prev,
			)
		}
	}
}

// TestMigrationNamesAreUnique catches the "two migrations with the same
// name at different versions" case. Names are used in error messages and
// log lines; duplicates make troubleshooting ambiguous.
func TestMigrationNamesAreUnique(t *testing.T) {
	seen := map[string]int{}
	for _, m := range migrations {
		if other, dup := seen[m.name]; dup {
			t.Errorf("duplicate migration name %q at versions %d and %d",
				m.name, other, m.version)
		}
		seen[m.name] = m.version
	}
}

// TestMigrationsHaveBody guards against an empty migration accidentally
// shipping. A migration must set either `sql` or `fn`. The runner would
// reject this at apply time (empty SQL fails to parse), but catching it
// at build time produces a clearer error and prevents wasting a version
// number on a no-op.
func TestMigrationsHaveBody(t *testing.T) {
	for _, m := range migrations {
		if m.sql == "" && m.fn == nil {
			t.Errorf("migration v%d (%s) has neither sql nor fn", m.version, m.name)
		}
		if m.sql != "" && m.fn != nil {
			t.Errorf("migration v%d (%s) has both sql AND fn; pick one", m.version, m.name)
		}
		if m.name == "" {
			t.Errorf("migration v%d has empty name", m.version)
		}
	}
}
