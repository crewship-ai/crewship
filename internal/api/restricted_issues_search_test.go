package api

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/access"
)

func TestRestrictedIssuesSearchPreservesProjectAudience(t *testing.T) {
	db := setupTestDB(t)
	owner, ws := seedInviter(t, db)
	execOrFatal(t, db, `INSERT INTO users(id,email) VALUES('search-a','search-a@example.test'),('search-b','search-b@example.test')`)
	execOrFatal(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('search-ma',?,'search-a','VIEWER'),('search-mb',?,'search-b','VIEWER')`, ws, ws)
	execOrFatal(t, db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES('search-crew',?,'Crew','search-crew')`, ws)
	seedAgentRow(t, db, "search-agent", ws, "search-crew", "Worker", "search-worker", "AGENT")
	for _, id := range []string{"search-pa", "search-pb", "search-private"} {
		execOrFatal(t, db, `INSERT INTO projects(id,workspace_id,name,slug) VALUES(?,?,?,?)`, id, ws, id, id)
	}
	for _, row := range []struct{ id, project, identifier, title, description, kind string }{
		{"search-a1", "search-pa", "CLIENT-1", "Žlutý Release %_ freeze", "HIDDEN_BODY_CANARY", "issue"},
		{"search-a2", "search-pa", "CLIENT-2", "Release second", "Brief", "issue"},
		{"search-a3", "search-pa", "CLIENT-3", "Other request", "Release hidden body", "issue"},
		{"search-b1", "search-pb", "OTHER-1", "Release %_ foreign", "Other audience", "issue"},
		{"search-secret", "search-private", "SECRET-1", "PRIVATE_TITLE_CANARY", "Release private", "issue"},
	} {
		execOrFatal(t, db, `INSERT INTO missions(id,workspace_id,crew_id,lead_agent_id,trace_id,identifier,title,description,status,mission_type,project_id) VALUES(?,?,'search-crew','search-agent',?,?,?,?, 'TODO',?,?)`, row.id, ws, row.id+"-trace", row.identifier, row.title, row.description, row.kind, row.project)
	}
	// Keep pagination deterministic regardless of insert timestamp resolution.
	execOrFatal(t, db, `UPDATE missions SET created_at='2026-10-15T00:00:00Z' WHERE workspace_id=?`, ws)
	execOrFatal(t, db, `UPDATE missions SET created_at='2026-10-16T00:00:00Z' WHERE id='search-a2'`)
	store := access.Store{DB: db}
	for _, row := range []struct{ user, project string }{{"search-a", "search-pa"}, {"search-b", "search-pb"}} {
		member, err := store.Membership(t.Context(), row.user, ws)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = store.Replace(t.Context(), owner, row.user, ws, "restricted", member, []access.Right{{Kind: "project", ID: row.project, Operation: "list"}}); err != nil {
			t.Fatal(err)
		}
	}
	router, err := NewRouter(db, "synthetic-shared-search-secret-long-enough", newTestLogger())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	// Audience/SQL regressions exercise separate requests, not limiter exhaustion.
	searchClock := time.Now()
	router.sharedSearch.now = func() time.Time { searchClock = searchClock.Add(time.Minute); return searchClock }
	tokens := map[string]string{}
	for _, user := range []string{"search-a", "search-b"} {
		tokens[user] = mintTokenFor(t, db, user, user+"-"+strings.Repeat("0", 40))
	}
	base := "/api/v1/workspaces/" + ws + "/restricted-issues"
	request := func(user, path string, status int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", "Bearer "+tokens[user])
		req.Header.Set("X-Workspace-ID", ws)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		if rr.Code != status {
			t.Fatalf("%s %s: status %d, want %d: %s", user, path, rr.Code, status, rr.Body.String())
		}
		return rr
	}
	for _, tc := range []struct {
		name, user, q, extra, total string
		ids                         []string
	}{
		{"case-insensitive title", "search-a", "rElEaSe", "", "2", []string{"search-a1", "search-a2"}},
		{"Unicode literal", "search-a", "Žlutý", "", "1", []string{"search-a1"}},
		{"Unicode bound", "search-a", strings.Repeat("Ž", 256), "", "0", nil},
		{"literal wildcards", "search-a", "%_", "", "1", []string{"search-a1"}},
		{"identifier", "search-a", "client-3", "", "1", []string{"search-a3"}},
		{"hidden descriptions not searched", "search-a", "HIDDEN_BODY_CANARY", "", "0", nil},
		{"foreign titles absent from count", "search-a", "PRIVATE_TITLE_CANARY", "", "0", nil},
		{"second audience", "search-b", "%_", "", "1", []string{"search-b1"}},
		{"literal SQL syntax", "search-a", "' OR 1=1 --", "", "0", nil},
		{"trimmed query", "search-a", "  client-1  ", "", "1", []string{"search-a1"}},
		{"page total", "search-a", "release", "&limit=1&offset=1", "2", []string{"search-a1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := request(tc.user, base+"?q="+url.QueryEscape(tc.q)+tc.extra, 200)
			var items []restrictedIssue
			if err := json.Unmarshal(rr.Body.Bytes(), &items); err != nil {
				t.Fatal(err)
			}
			if rr.Header().Get("X-Total-Count") != tc.total || len(items) != len(tc.ids) {
				t.Fatalf("total=%s, rows=%+v, want total=%s ids=%v", rr.Header().Get("X-Total-Count"), items, tc.total, tc.ids)
			}
			expected := map[string]bool{}
			for _, id := range tc.ids {
				expected[id] = true
			}
			for _, item := range items {
				if !expected[item.ID] || item.Description != nil || item.CanRead {
					t.Fatalf("unexpected projection %+v", item)
				}
			}
		})
	}
	request("search-a", base+"?q=release&project_id=search-pb", 404)
	request("search-a", base+"?q="+strings.Repeat("x", 257), 400)
	request("search-a", base+"?q=%FF", 400)
	request("search-a", base+"?q="+url.QueryEscape(strings.Repeat("Ž", 257)), 400)
	request("search-a", base+"/search-a1?q=", 400)
	request("search-a", base+"/search-secret?q=release", 400)

	assertEmpty := func(label string) {
		t.Helper()
		for _, suffix := range []string{"", "?q=Release", "?q=%25_"} {
			rr := request("search-a", base+suffix, 200)
			if strings.TrimSpace(rr.Body.String()) != "[]" || rr.Header().Get("X-Total-Count") != "0" {
				t.Fatalf("%s: rows=%s count=%s", label, rr.Body.String(), rr.Header().Get("X-Total-Count"))
			}
		}
	}
	execOrFatal(t, db, `UPDATE workspace_members SET role='MEMBER' WHERE workspace_id=? AND user_id=?`, ws, owner)
	assertEmpty("demoted issuer")
	execOrFatal(t, db, `UPDATE workspace_members SET role='OWNER' WHERE workspace_id=? AND user_id=?`, ws, owner)
	execOrFatal(t, db, `UPDATE users SET suspended_at='2026-10-15' WHERE id=?`, owner)
	assertEmpty("suspended issuer")
	execOrFatal(t, db, `UPDATE users SET suspended_at=NULL WHERE id=?`, owner)
	for _, operations := range [][]string{{"read"}, {"read", "contribute"}} {
		member, err := store.Membership(t.Context(), "search-a", ws)
		if err != nil {
			t.Fatal(err)
		}
		var rights []access.Right
		for _, operation := range operations {
			rights = append(rights, access.Right{Kind: "project", ID: "search-pa", Operation: operation})
		}
		if _, err := store.Replace(t.Context(), owner, "search-a", ws, "restricted", member, rights); err != nil {
			t.Fatal(err)
		}
		assertEmpty("read/contribute without list")
		request("search-a", base+"/search-a1", 200) // Read remains independently usable.
	}
	member, err := store.Membership(t.Context(), "search-a", ws)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Replace(t.Context(), owner, "search-a", ws, "restricted", member, nil); err != nil {
		t.Fatal(err)
	}
	rr := request("search-a", base+"?q=Release", 200)
	if strings.TrimSpace(rr.Body.String()) != "[]" || rr.Header().Get("X-Total-Count") != "0" {
		t.Fatalf("revoked search=%s count=%s", rr.Body.String(), rr.Header().Get("X-Total-Count"))
	}
}

func TestRestrictedIssueSearchPlanStartsFromLiveGrants(t *testing.T) {
	db := setupTestDB(t)
	rows, err := db.Query(`EXPLAIN QUERY PLAN `+restrictedIssueAudienceSQL+`SELECT COUNT(*)`+restrictedIssueFromSQL+restrictedIssueSearchSQL,
		"member", "list", "workspace", "", "", "release", "release", "release")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	materialized, indexedIssues := false, false
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(detail, "MATERIALIZE shared_projects") {
			materialized = true
		}
		if strings.Contains(detail, "SEARCH m USING INDEX idx_mission_project (project_id=?)") {
			indexedIssues = true
		}
		if strings.Contains(detail, "SCAN m") || strings.Contains(detail, "idx_mission_workspace") || strings.Contains(detail, "idx_missions_ws_created") {
			t.Fatalf("search scans issues outside granted projects: %s", detail)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !materialized || !indexedIssues {
		t.Fatalf("plan did not materialize live grants before indexed project lookup: materialized=%t indexed=%t", materialized, indexedIssues)
	}
}
