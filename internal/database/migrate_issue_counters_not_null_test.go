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
// and the backup scoper — which reaches this table's workspace ONLY through
// crew_id, there being no workspace_id column — was filtering on a column the
// schema said could be absent. A filter on a nullable column omits every row
// where it is NULL, silently (#1973).
//
// This is the migration that makes the declaration true.

var issueCountersCounter atomic.Int64

func issueCountersNotNullVersion() int {
	for _, m := range migrations {
		if m.name == "issue_counters_crew_not_null" {
			return m.version
		}
	}
	return 0
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

func issueCountersCrewIDIsNotNull(t *testing.T, db *sql.DB, ctx context.Context) bool {
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
		if name == "crew_id" {
			return notnull == 1
		}
	}
	t.Fatal("issue_counters has no crew_id column")
	return false
}

// A fresh install must carry the constraint, because that is what every tool
// asking the schema — the backup scoper included — reads.
func TestIssueCountersCrewNotNull_FreshInstall(t *testing.T) {
	db, ctx, logger := openIssueCountersDB(t)
	if err := Migrate(ctx, db, logger); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if !issueCountersCrewIDIsNotNull(t, db, ctx) {
		t.Error("issue_counters.crew_id is still nullable on a fresh install — " +
			"PRIMARY KEY is not NOT NULL in SQLite, and the backup scoper reads " +
			"PRAGMA table_info, not the developer's intent (#1973)")
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO issue_counters (crew_id, next_number) VALUES (NULL, 7)`); err == nil {
		t.Error("a NULL crew_id counter was accepted; the constraint is not doing anything")
	} else if !strings.Contains(err.Error(), "NOT NULL") {
		t.Errorf("NULL crew_id rejected for the wrong reason: %v", err)
	}
}

// The upgrade path is where the risk is: an install that already has counters
// must keep every one that names a live crew, with its number intact, because
// next_number is what stops a restored crew from re-issuing identifiers it has
// already used.
func TestIssueCountersCrewNotNull_UpgradeKeepsLiveCounters(t *testing.T) {
	db, ctx, logger := openIssueCountersDB(t)

	version := issueCountersNotNullVersion()
	if version == 0 {
		t.Fatal("no migration named `issue_counters_crew_not_null` in the registry")
	}
	if err := applyMigrationsUpTo(ctx, db, version-1, logger); err != nil {
		t.Fatalf("migrate to the version before the rebuild: %v", err)
	}
	if issueCountersCrewIDIsNotNull(t, db, ctx) {
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

	if !issueCountersCrewIDIsNotNull(t, db, ctx) {
		t.Error("issue_counters.crew_id is still nullable after the upgrade")
	}
	var next int
	if err := db.QueryRowContext(ctx,
		`SELECT next_number FROM issue_counters WHERE crew_id = 'crew_ic'`).Scan(&next); err != nil {
		t.Fatalf("live counter did not survive the rebuild: %v", err)
	}
	if next != 137 {
		t.Errorf("next_number = %d, want 137 — the rebuild reset a live counter, which "+
			"makes the crew re-issue identifiers it already used", next)
	}
	var nulls int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM issue_counters WHERE crew_id IS NULL`).Scan(&nulls); err != nil {
		t.Fatalf("count null counters: %v", err)
	}
	if nulls != 0 {
		t.Errorf("%d counter(s) with a NULL crew_id survived the rebuild", nulls)
	}
	// The FK must come across with the table, or the rebuild has traded one
	// silent hole for another.
	if _, err := db.ExecContext(ctx,
		`INSERT INTO issue_counters (crew_id, next_number) VALUES ('crew_gone', 1)`); err == nil {
		t.Error("a counter for a non-existent crew was accepted; the rebuilt table lost its foreign key")
	}
}
