package database

import (
	"fmt"
	"time"
)

// Migration version numbering — why new migrations look like a date.
//
// Versions v1..v169 were allocated sequentially: the author took the next
// free integer. That works until two branches are open at once, at which
// point both authors take the SAME next free integer and neither notices
// until one of them merges. CI catches the collision before it reaches main
// (scripts/lint-migrations fingerprints each entry against the base ref), so
// the schema never forks — but the fix is to renumber the loser, and
// renumbering is a one-way door for any database that already APPLIED the
// migration under its old number. That database now disagrees with the
// binary about what v168 means, the collision guard in Migrate refuses to
// start, and the pre-migration snapshot is no help because it carries the
// same ledger. It happened to dev3 on 2026-07-27.
//
// Sequential numbering makes collisions the default outcome of ordinary
// parallel work. Timestamps make them require two authors to generate a
// migration in the same second. Rails, Django and Flyway all landed here for
// the same reason.
//
// So: everything above the legacy ceiling is a UTC timestamp, YYYYMMDDHHMMSS.
//
//	{version: 20260728143000, name: "add_widget_flag", sql: migrationAddWidgetFlag},
//
// Generate one with `date -u +%Y%m%d%H%M%S`. Strict ascending order still
// holds (enforced by TestMigrationsAreStrictlyIncreasing), which for
// timestamps means chronological order — so append, never insert.
//
// Nothing below the ceiling may change. Those numbers are applied in
// databases we do not control.
const legacySequentialCeiling = 169

// minTimestampVersion is the smallest 14-digit number, i.e. the floor for
// "this is a YYYYMMDDHHMMSS stamp and not somebody typing 170".
const minTimestampVersion = 10000000000000

// Bounds for the timestamp form. The lower bound is the day this scheme
// landed; the upper is far enough out to be irrelevant and near enough to
// catch a fat-fingered extra digit.
var (
	timestampSchemeEpoch = time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	timestampSchemeLimit = time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)
)

// validateMigrationVersion enforces the two-era numbering scheme. Returns nil
// for a legal version, a descriptive error otherwise.
func validateMigrationVersion(v int) error {
	if v < 1 {
		return fmt.Errorf("version %d is not positive", v)
	}
	if v <= legacySequentialCeiling {
		// Historical block. Immutable, and nothing new may be added to it:
		// the whole point is that the next free small integer is no longer
		// something two branches can race for.
		return nil
	}
	if v < minTimestampVersion {
		return fmt.Errorf(
			"version %d sits above the legacy ceiling (v%d) but is not a YYYYMMDDHHMMSS timestamp — "+
				"new migrations are timestamped so two branches cannot claim the same number; "+
				"generate one with `date -u +%%Y%%m%%d%%H%%M%%S`",
			v, legacySequentialCeiling)
	}

	ts, err := time.Parse("20060102150405", fmt.Sprintf("%014d", v))
	if err != nil {
		return fmt.Errorf(
			"version %d is not a valid YYYYMMDDHHMMSS timestamp: %w — "+
				"generate one with `date -u +%%Y%%m%%d%%H%%M%%S`", v, err)
	}
	if ts.Before(timestampSchemeEpoch) {
		return fmt.Errorf(
			"version %d parses as %s, before the timestamp scheme began (%s) — "+
				"a version in that range would sort among the legacy sequential migrations",
			v, ts.Format(time.RFC3339), timestampSchemeEpoch.Format("2006-01-02"))
	}
	if !ts.Before(timestampSchemeLimit) {
		return fmt.Errorf("version %d parses as %s, which is implausibly far in the future — "+
			"check for an extra digit", v, ts.Format(time.RFC3339))
	}
	return nil
}
