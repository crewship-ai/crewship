package pipeline

import (
	"database/sql"
	"sort"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/testutil"
)

// newPendingDB builds the pending_runs table by hand instead of running the
// migrations, so the fast tests in this package can use an in-memory DB without
// paying for the whole schema. That trade is fine; the hazard it creates is not.
//
// A column added to the migration and not to the fixture makes every store test
// pass against a table production does not have — and the reverse, a column in
// the fixture that no migration creates, makes them pass against a table that
// does not exist anywhere. Both failure modes are silent, and the second is
// worse because the tests look MORE thorough.
//
// This branch added chain_origin to both, and noticed only because the INSERT
// blew up at runtime. Nothing would have caught a column that is merely READ.
//
// So: pin the fixture against the real migrated schema. This is the only test
// here that pays for migrations, and it pays once.
func TestPendingRunsFixtureMatchesTheMigratedSchema(t *testing.T) {
	real := columnsOf(t, testutil.MigratedSQLDB(t), "pending_runs")
	fixture := columnsOf(t, newPendingDB(t), "pending_runs")

	missing := difference(real, fixture)
	if len(missing) > 0 {
		t.Errorf("the fixture is missing %d column(s) the migrations create: %s\n"+
			"Every store test in this package is running against a table production does not "+
			"have. Add them to newPendingDB.", len(missing), strings.Join(missing, ", "))
	}

	invented := difference(fixture, real)
	if len(invented) > 0 {
		t.Errorf("the fixture has %d column(s) no migration creates: %s\n"+
			"Tests are asserting against a table that exists nowhere else, which reads as "+
			"coverage and is not.", len(invented), strings.Join(invented, ", "))
	}
}

func columnsOf(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		t.Fatalf("pragma_table_info(%s): %v", table, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if len(out) == 0 {
		t.Fatalf("table %q has no columns — it does not exist in this database", table)
	}
	sort.Strings(out)
	return out
}

// difference returns the members of a that are not in b.
func difference(a, b []string) []string {
	in := make(map[string]bool, len(b))
	for _, s := range b {
		in[s] = true
	}
	var out []string
	for _, s := range a {
		if !in[s] {
			out = append(out, s)
		}
	}
	return out
}

// The phase is separate from status; legacy rows and the run-store fixtures
// must allow NULL without inventing evidence that setup already completed.
func TestRunStartPhaseFixturesMatchMigration(t *testing.T) {
	_, runs := openRunsTestDB(t)
	defer runs.Close()
	resume := openResumeTestDB(t)
	defer resume.Close()
	for name, db := range map[string]*sql.DB{
		"migrated": testutil.MigratedSQLDB(t), "runs": runs, "resume": resume,
	} {
		t.Run(name, func(t *testing.T) {
			var kind string
			var notNull int
			var defaultValue sql.NullString
			err := db.QueryRow(`SELECT type,"notnull",dflt_value FROM pragma_table_info('pipeline_runs') WHERE name='start_phase'`).Scan(&kind, &notNull, &defaultValue)
			if err != nil {
				t.Fatal(err)
			}
			if kind != "TEXT" || notNull != 0 || defaultValue.Valid {
				t.Fatalf("phase must be nullable TEXT without a default: %s %d %+v", kind, notNull, defaultValue)
			}
		})
	}
}
