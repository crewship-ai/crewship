package backup

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

// Unit-level companion to TestE2E_NullableScopeColumns_RowsSurviveRoundTrip.
// The e2e test proves a row survives; this one pins the two properties of the
// walk that make it survive, on a schema small enough to reason about (#1973).

// newNullablePathDB is the shape the bug lives in. `payloads` can reach a
// workspace two ways:
//
//	producer_run_id → runs → workspaces          (2 hops, NULLABLE)
//	panel_id → panels → pages → workspaces       (3 hops, NOT NULL throughout)
//
// The shortest path is the lossy one, and it is lossy for the ordinary row:
// producer_run_id is NULL for anything not produced by a run.
//
// `tasks` is the tie case: two candidate parents at the SAME depth, one
// nullable and one not, which is where map iteration order used to decide.
func newNullablePathDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/nullpath.db?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`
		CREATE TABLE workspaces (id TEXT PRIMARY KEY);
		CREATE TABLE runs (
			id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL REFERENCES workspaces(id)
		);
		CREATE TABLE jobs (
			id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL REFERENCES workspaces(id)
		);
		CREATE TABLE pages (
			id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL REFERENCES workspaces(id)
		);
		CREATE TABLE panels (
			id TEXT PRIMARY KEY,
			page_id TEXT NOT NULL REFERENCES pages(id)
		);
		CREATE TABLE payloads (
			id TEXT PRIMARY KEY,
			panel_id TEXT NOT NULL REFERENCES panels(id),
			producer_run_id TEXT REFERENCES runs(id)
		);
		CREATE TABLE tasks (
			id TEXT PRIMARY KEY,
			job_id TEXT NOT NULL REFERENCES jobs(id),
			claimed_run_id TEXT REFERENCES runs(id)
		);
		-- No NOT NULL route out at all: whatever it gets is the best
		-- available, and the walk must still answer the same way twice.
		CREATE TABLE hints (
			id TEXT PRIMARY KEY,
			run_id TEXT REFERENCES runs(id),
			job_id TEXT REFERENCES jobs(id)
		);
	`); err != nil {
		t.Fatalf("schema: %v", err)
	}
	return db
}

func filterFor(t *testing.T, scoped []ScopedTable, table string) string {
	t.Helper()
	for _, st := range scoped {
		if st.Name == table {
			f, _ := st.WorkspaceScopeFilter("ws")
			return f
		}
	}
	t.Fatalf("%s was not discovered as workspace-scoped", table)
	return ""
}

// A longer NOT NULL path beats a shorter nullable one. The shorter one is not
// wrong about which workspace a row belongs to — it is wrong about the rows it
// does not mention at all, and those vanish from the bundle without a word.
func TestDiscoverScopedTables_PrefersNotNullPathOverShorterNullableOne(t *testing.T) {
	scoped, err := DiscoverScopedTables(context.Background(), newNullablePathDB(t))
	if err != nil {
		t.Fatalf("discover: %v", err)
	}

	const wantPayloads = `"panel_id" IN (SELECT "id" FROM "panels" WHERE "page_id" IN ` +
		`(SELECT "id" FROM "pages" WHERE "workspace_id" = ?))`
	if got := filterFor(t, scoped, "payloads"); got != wantPayloads {
		t.Errorf("payloads filter:\n  got  %s\n  want %s\n"+
			"the 2-hop route through producer_run_id is shorter and NULL for every "+
			"payload no run produced", got, wantPayloads)
	}

	const wantTasks = `"job_id" IN (SELECT "id" FROM "jobs" WHERE "workspace_id" = ?)`
	if got := filterFor(t, scoped, "tasks"); got != wantTasks {
		t.Errorf("tasks filter:\n  got  %s\n  want %s\n"+
			"jobs and runs are the same distance from workspaces; the NOT NULL one wins",
			got, wantTasks)
	}
}

// The same schema must produce the same filters every time. It did not: two
// consecutive runs reported keeper_requests through credential_id once and
// requesting_agent_id the next, because the reverse-FK adjacency was built by
// ranging over a map. A backup whose contents depend on Go's map seed is a
// backup nobody can reason about — including the one table here that has no
// NOT NULL route and must still answer consistently.
func TestDiscoverScopedTables_IsDeterministicAcrossRuns(t *testing.T) {
	db := newNullablePathDB(t)
	ctx := context.Background()

	baseline := map[string]string{}
	for i := 0; i < 25; i++ {
		scoped, err := DiscoverScopedTables(ctx, db)
		if err != nil {
			t.Fatalf("discover (run %d): %v", i, err)
		}
		for _, st := range scoped {
			filter, _ := st.WorkspaceScopeFilter("ws")
			if i == 0 {
				baseline[st.Name] = filter
				continue
			}
			if baseline[st.Name] != filter {
				t.Fatalf("%s changed filter between runs:\n  run 0: %s\n  run %d: %s",
					st.Name, baseline[st.Name], i, filter)
			}
		}
		if len(scoped) != len(baseline) {
			t.Fatalf("run %d discovered %d tables, run 0 discovered %d",
				i, len(scoped), len(baseline))
		}
	}
	if _, ok := baseline["hints"]; !ok {
		t.Error("hints was not discovered at all — a table whose only routes are " +
			"nullable is still workspace-scoped, and dropping it from discovery " +
			"would take it out of the --replace wipe")
	}
}
