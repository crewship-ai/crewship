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

// newAncestorNullDB is the hazard a first-hop-only check misses: `leaf` has two
// NOT NULL foreign keys, so whichever one is chosen the leading column looks
// clean — but one of the parents was itself reached through a nullable column,
// and a filter that goes that way drops every leaf whose p1 has no workspace.
// Nullability has to be counted over the WHOLE path or it is not counted.
func newAncestorNullDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/ancestor.db?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`
		CREATE TABLE workspaces (id TEXT PRIMARY KEY);
		CREATE TABLE roots (
			id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL REFERENCES workspaces(id)
		);
		-- Two hops from workspaces, but one of them is nullable.
		CREATE TABLE p1 (
			id TEXT PRIMARY KEY,
			workspace_id TEXT REFERENCES workspaces(id)
		);
		-- Three hops from workspaces, and every one of them is NOT NULL.
		CREATE TABLE p2 (
			id TEXT PRIMARY KEY,
			root_id TEXT NOT NULL REFERENCES roots(id)
		);
		CREATE TABLE leaf (
			id TEXT PRIMARY KEY,
			p1_id TEXT NOT NULL REFERENCES p1(id),
			p2_id TEXT NOT NULL REFERENCES p2(id)
		);
	`); err != nil {
		t.Fatalf("schema: %v", err)
	}
	return db
}

func TestDiscoverScopedTables_CountsNullableHopsAcrossTheWholePath(t *testing.T) {
	scoped, err := DiscoverScopedTables(context.Background(), newAncestorNullDB(t))
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	const want = `"p2_id" IN (SELECT "id" FROM "p2" WHERE "root_id" IN ` +
		`(SELECT "id" FROM "roots" WHERE "workspace_id" = ?))`
	if got := filterFor(t, scoped, "leaf"); got != want {
		t.Errorf("leaf filter:\n  got  %s\n  want %s\n"+
			"the p1 route is one hop shorter and its leading column is NOT NULL, so a "+
			"check that stops at the first hop calls it clean — but p1.workspace_id is "+
			"nullable and every leaf under an unscoped p1 falls out of the bundle", got, want)
	}
}

// A table with its own foreign key into workspaces is anchored on that column
// even when a NOT NULL route to some other workspace-scoped parent exists,
// because DumpWorkspace scopes such a table by `workspace_id = ?` and a
// --replace DELETE that disagreed would remove rows the bundle never carried.
// credential_audit is the live case: nullable workspace_id, NOT NULL
// credential_id.
func TestDiscoverScopedTables_AnchorsOnItsOwnWorkspaceFK(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/anchor.db?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`
		CREATE TABLE workspaces (id TEXT PRIMARY KEY);
		CREATE TABLE creds (
			id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL REFERENCES workspaces(id)
		);
		CREATE TABLE audit (
			id TEXT PRIMARY KEY,
			workspace_id TEXT REFERENCES workspaces(id) ON DELETE CASCADE,
			cred_id TEXT NOT NULL REFERENCES creds(id) ON DELETE CASCADE
		);
	`); err != nil {
		t.Fatalf("schema: %v", err)
	}
	scoped, err := DiscoverScopedTables(context.Background(), db)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	const want = `"workspace_id" = ?`
	if got := filterFor(t, scoped, "audit"); got != want {
		t.Errorf("audit filter:\n  got  %s\n  want %s\n"+
			"DumpWorkspace short-circuits any table carrying workspace_id to that column; "+
			"discovery choosing another route makes the --replace DELETE wider than the "+
			"bundle that replaces it", got, want)
	}
}

// Where two paths are otherwise equal, the CASCADE one wins. page_panels is the
// live case: NOT NULL to its page (CASCADE) and NOT NULL to its owning crew
// (RESTRICT), both two hops out. The page is the parent the row cannot outlive,
// and scoping by containment keeps a page and its panels in the same bundle
// without depending on an API invariant about where a crew may live.
func TestDiscoverScopedTables_PrefersTheCascadingParent(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/cascade.db?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`
		CREATE TABLE workspaces (id TEXT PRIMARY KEY);
		CREATE TABLE crews (
			id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL REFERENCES workspaces(id)
		);
		CREATE TABLE pages (
			id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL REFERENCES workspaces(id)
		);
		-- owner_crew_id sorts BEFORE page_id, so the alphabetical last resort
		-- would take it. ON DELETE is the reason it does not.
		CREATE TABLE panels (
			id TEXT PRIMARY KEY,
			owner_crew_id TEXT NOT NULL REFERENCES crews(id) ON DELETE RESTRICT,
			page_id TEXT NOT NULL REFERENCES pages(id) ON DELETE CASCADE
		);
	`); err != nil {
		t.Fatalf("schema: %v", err)
	}
	scoped, err := DiscoverScopedTables(context.Background(), db)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	const want = `"page_id" IN (SELECT "id" FROM "pages" WHERE "workspace_id" = ?)`
	if got := filterFor(t, scoped, "panels"); got != want {
		t.Errorf("panels filter:\n  got  %s\n  want %s\n"+
			"a panel is deleted with its page, not with the crew that owns it", got, want)
	}
}
