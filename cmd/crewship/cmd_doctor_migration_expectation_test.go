//go:build !clionly

package main

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/database"
)

// #1645 asked that doctor's migration warning be able to "name both sides".
// It could not name even one: the expected version was a hand-bumped
// constant that stopped being bumped at v85, roughly eighty migrations ago.
// Every healthy install was therefore told its schema was "newer than the CLI
// knows about" — the exact false alarm that makes a real one unreadable.
//
// The expectation has to be derived from the migration registry compiled into
// this same binary, so it cannot go stale again.
func TestCheckDBMigrationVersion_ExpectsWhatThisBinaryCanApply(t *testing.T) {
	dd := tempDataDir(t)
	ctx := context.Background()

	db, err := sql.Open("sqlite", dd.DatabasePath())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE _migrations (version INTEGER)"); err != nil {
		t.Fatal(err)
	}
	setVersion := func(v int) {
		t.Helper()
		if _, err := db.Exec("DELETE FROM _migrations"); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec("INSERT INTO _migrations (version) VALUES (?)", v); err != nil {
			t.Fatal(err)
		}
	}

	latest := database.MaxKnownMigrationVersion()
	if latest <= 85 {
		t.Fatalf("MaxKnownMigrationVersion()=%d — at or below the stale hard-coded 85, so this test could not distinguish the bug from the fix", latest)
	}

	// A DB migrated by THIS binary is up to date, and must say so.
	setVersion(latest)
	r := checkDBMigrationVersion(ctx)
	if r.status != "PASS" {
		t.Errorf("a DB at the binary's own latest (v%d) reported %s: %+v", latest, r.status, r)
	}
	if !strings.Contains(r.detail, fmt.Sprintf("v%d (latest)", latest)) {
		t.Errorf("detail %q does not name v%d as latest", r.detail, latest)
	}

	// One past it is genuinely a newer server having migrated the DB.
	setVersion(latest + 1)
	r = checkDBMigrationVersion(ctx)
	if r.status != "WARN" || !strings.Contains(r.detail, "newer than CLI knows about") {
		t.Errorf("a DB one past the binary's ceiling should warn: %+v", r)
	}
	// And the warning must name the CLI's side of the comparison, not just
	// the DB's — that is the "both sides" the issue asked for.
	if !strings.Contains(r.detail, fmt.Sprintf("v%d", latest)) {
		t.Errorf("detail %q does not name the version this CLI expects (v%d)", r.detail, latest)
	}
}
