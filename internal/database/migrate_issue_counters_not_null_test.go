package database

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"

	_ "modernc.org/sqlite"
)

// issue_counters said `crew_id TEXT PRIMARY KEY` and everybody read that as
// "required". SQLite does not: a rowid table's PRIMARY KEY column accepts NULL
// unless it is INTEGER PRIMARY KEY, so `PRAGMA table_info` answered notnull=0
// and the backup scoper — which reached this table's workspace ONLY through
// crew_id, there being no workspace_id column — was filtering on a column the
// schema said could be absent. A filter on a nullable column omits every row
// where it is NULL, silently (#1973).
//
// #1797 then re-keyed the table to (workspace_id, prefix), because the counter
// fed a per-WORKSPACE identifier namespace while counting per crew. crew_id is
// gone with that rebuild, and the hazard above is gone with it in the strongest
// available way: the table now carries workspace_id as a NOT NULL column of its
// own, so the scoper has no foreign key to traverse and no nullable column to
// traverse it through. These tests assert #1973's guarantee in the shape the
// table has today — the guarantee did not change, its implementation did.

var issueCountersCounter atomic.Int64

func issueCountersNotNullVersion() int {
	m, ok := migrationByName("issue_counters_crew_not_null")
	if !ok {
		return 0
	}
	return m.version
}

func openIssueCountersDB(t *testing.T) (*sql.DB, context.Context, *slog.Logger) {
	t.Helper()
	name := fmt.Sprintf("crewship-issue-counters-%d", issueCountersCounter.Add(1))
	db, err := sql.Open("sqlite",
		fmt.Sprintf("file:%s?mode=memory&cache=shared&_pragma=foreign_keys(ON)", name))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil))
}

// issueCountersColumnIsNotNull reports whether issue_counters declares col, and
// whether that declaration is NOT NULL — read from PRAGMA table_info, which is
// the answer every tool that asks the schema gets, the backup scoper included.
func issueCountersColumnIsNotNull(t *testing.T, db *sql.DB, ctx context.Context, col string) (present, notNull bool) {
	t.Helper()
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(issue_counters)`)
	if err != nil {
		t.Fatalf("table_info: %v", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			cid, notnull, pk int
			name, ctype      string
			dflt             sql.NullString
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan table_info: %v", err)
		}
		if name == col {
			return true, notnull == 1
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("table_info rows: %v", err)
	}
	return false, false
}

// A fresh install must carry the constraint, because that is what every tool
// asking the schema — the backup scoper included — reads. Since #1797 the
// column carrying it is workspace_id, and crew_id is not there at all.
func TestIssueCountersCrewNotNull_FreshInstall(t *testing.T) {
	db, ctx, logger := openIssueCountersDB(t)
	if err := Migrate(ctx, db, logger); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	if present, _ := issueCountersColumnIsNotNull(t, db, ctx, "crew_id"); present {
		t.Error("issue_counters still has a crew_id column — #1797 re-keyed this table " +
			"to (workspace_id, prefix); a counter row belongs to a prefix within a " +
			"workspace, which is not one crew")
	}
	for _, col := range []string{"workspace_id", "prefix"} {
		present, notNull := issueCountersColumnIsNotNull(t, db, ctx, col)
		if !present {
			t.Fatalf("issue_counters has no %s column", col)
		}
		if !notNull {
			t.Errorf("issue_counters.%s is nullable — PRIMARY KEY is not NOT NULL in SQLite, "+
				"and the backup scoper reads PRAGMA table_info, not the developer's intent (#1973)", col)
		}
	}

	if _, err := db.ExecContext(ctx,
		`INSERT INTO issue_counters (workspace_id, prefix, next_number) VALUES (NULL, 'ENG', 7)`); err == nil {
		t.Error("a NULL workspace_id counter was accepted; the constraint is not doing anything")
	} else if !strings.Contains(err.Error(), "NOT NULL") {
		t.Errorf("NULL workspace_id rejected for the wrong reason: %v", err)
	}
}

// The upgrade path is where the risk is: an install that already has counters
// must keep every one that names a live crew, with its number intact, because
// next_number is what stops a restored crew from re-issuing identifiers it has
// already used. The fixture starts before the #1973 rebuild and runs both it
// and #1797's re-key, so the number has to survive two table rebuilds.
func TestIssueCountersCrewNotNull_UpgradeKeepsLiveCounters(t *testing.T) {
	db, ctx, logger := openIssueCountersDB(t)

	version := issueCountersNotNullVersion()
	if version == 0 {
		t.Fatal("no migration named `issue_counters_crew_not_null` in the registry")
	}
	if err := applyMigrationsUpTo(ctx, db, version-1, logger); err != nil {
		t.Fatalf("migrate to the version before the rebuild: %v", err)
	}
	if _, notNull := issueCountersColumnIsNotNull(t, db, ctx, "crew_id"); notNull {
		t.Fatal("the pre-migration schema already declares crew_id NOT NULL — " +
			"this fixture no longer reproduces the upgrade the migration exists for")
	}

	mustExec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("seed %q: %v", query, err)
		}
	}
	mustExec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws_ic','WS','ws-ic')`)
	mustExec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew_ic','ws_ic','Eng','eng')`)
	mustExec(`INSERT INTO issue_counters (crew_id, next_number) VALUES ('crew_ic', 137)`)
	// The row the old schema quietly permitted. It names no crew, therefore no
	// workspace, and nothing reads it — but it must not wedge the upgrade.
	mustExec(`INSERT INTO issue_counters (crew_id, next_number) VALUES (NULL, 4)`)

	if err := Migrate(ctx, db, logger); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	if _, notNull := issueCountersColumnIsNotNull(t, db, ctx, "workspace_id"); !notNull {
		t.Error("issue_counters.workspace_id is nullable after the upgrade")
	}
	// The crew sets no issue_prefix, so its effective prefix is the first three
	// letters of the slug upper-cased — ENG, exactly as the allocator derives it.
	var next int
	if err := db.QueryRowContext(ctx,
		`SELECT next_number FROM issue_counters WHERE workspace_id = 'ws_ic' AND prefix = 'ENG'`).Scan(&next); err != nil {
		t.Fatalf("live counter did not survive the rebuilds: %v", err)
	}
	if next != 137 {
		t.Errorf("next_number = %d, want 137 — the rebuild reset a live counter, which "+
			"makes the crew re-issue identifiers it already used", next)
	}
	var total int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM issue_counters`).Scan(&total); err != nil {
		t.Fatalf("count counters: %v", err)
	}
	if total != 1 {
		t.Errorf("%d counter rows survived, want 1 — the crewless row has no workspace and "+
			"no prefix, so it cannot be carried into the re-keyed table", total)
	}
	// The FK must come across with the table, or the rebuild has traded one
	// silent hole for another. It points at workspaces now, not crews.
	if _, err := db.ExecContext(ctx,
		`INSERT INTO issue_counters (workspace_id, prefix, next_number) VALUES ('ws_gone', 'GON', 1)`); err == nil {
		t.Error("a counter for a non-existent workspace was accepted; the rebuilt table lost its foreign key")
	}
}
