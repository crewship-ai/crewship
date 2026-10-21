package api

import (
	"database/sql"
	"database/sql/driver"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/testutil"
)

func TestSharedSearchBothSQLPlansAndDeniedKeyIsolation(t *testing.T) {
	fixture := testutil.MigratedDB(t)
	token := seedSearchBenchmark(t, fixture.DB, 1, 1, 100)
	trace := &searchTraceConnector{path: fixture.Path()}
	db := sql.OpenDB(trace)
	t.Cleanup(func() { db.Close() })
	router, err := NewRouter(db, "synthetic-search-plan-secret-32-characters", newTestLogger())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	now := time.Now()
	router.sharedSearch.now = func() time.Time { return now }
	request := func(suffix string, want int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest("GET", "/api/v1/workspaces/search-bench-ws/restricted-issues"+suffix, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Workspace-ID", "search-bench-ws")
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		if rr.Code != want {
			t.Fatalf("%s status%d want%d: %s", suffix, rr.Code, want, rr.Body.String())
		}
		return rr
	}
	request("", 200)
	plans := 0
	trace.plans.Range(func(key, value any) bool {
		plans++
		args := []any{}
		for _, v := range value.([]driver.NamedValue) {
			args = append(args, v.Value)
		}
		rows, err := db.QueryContext(t.Context(), "EXPLAIN QUERY PLAN "+key.(string), args...)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		materialized, indexed := false, false
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			t.Log(detail)
			materialized = materialized || strings.Contains(detail, "MATERIALIZE shared_projects")
			indexed = indexed || strings.Contains(detail, "SEARCH m USING INDEX idx_mission_project (project_id=?)")
			if strings.Contains(detail, "SCAN m") || strings.Contains(detail, "idx_mission_workspace") || strings.Contains(detail, "idx_missions_ws_created") {
				t.Fatalf("query searches outside granted projects: %s", detail)
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if !materialized || !indexed {
			t.Fatal("missing audience-first indexed issue plan")
		}
		return true
	})
	if plans != 2 {
		t.Fatalf("captured %d issue statements want COUNT and rows", plans)
	}
	for i := 0; i < 4; i++ {
		request("?q=common", 200)
	}
	before := request("?q=changed&offset=1000000", 429)
	queries := trace.issues.Load()
	for _, path := range []string{"?project_id=p1", "?project_id=missing", "?q=%FF", "?limit=501"} {
		want := 404
		if strings.Contains(path, "q=") || strings.Contains(path, "limit=") {
			want = 400
		}
		request(path, want)
	}
	if len(router.sharedSearch.accounts[sharedSearchIdentity("search-bench-user", "search-bench-ws")].audiences) != 1 {
		t.Fatal("invalid/hidden audience allocated bucket")
	}
	execOrFatal(t, db, `DELETE FROM missions WHERE project_id='p1'`)
	after := request("?q=another", 429)
	if trace.issues.Load() != queries {
		t.Fatal("rate refusal executed issue SQL")
	}
	if before.Body.String() != after.Body.String() || before.Header().Get("Retry-After") != after.Header().Get("Retry-After") {
		t.Fatal("hidden issue count changed limiter response")
	}
}
