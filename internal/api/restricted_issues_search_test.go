package api

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

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
