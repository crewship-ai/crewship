package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Public fixtures are outputs of the production reader. Refresh only with
// UPDATE_ISSUE_OUTBOX_FIXTURES=1 while running this test.
func TestFailedIssueMutationJobsAuthorizationPagingAndFixtures(t *testing.T) {
	db := setupTestDB(t)
	user := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, user)
	router := &Router{db: db, logger: newTestLogger()}
	request := func(query string, scopes stringSet) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/api/v1/workspaces/"+ws+"/issue-mutation-outbox"+query, nil)
		req.SetPathValue("workspaceId", ws)
		ctx := withWorkspace(withUser(req.Context(), &AuthUser{ID: user}), ws, "OWNER")
		if scopes != nil {
			ctx = context.WithValue(ctx, ctxTokenScopes, scopes)
		}
		rec := httptest.NewRecorder()
		router.failedIssueMutationJobs(rec, req.WithContext(ctx))
		return rec
	}
	decode := func(rec *httptest.ResponseRecorder) failedIssueMutationPage {
		t.Helper()
		if rec.Code != 200 {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("cacheable diagnostics")
		}
		for _, secret := range []string{"PAYLOAD_SECRET", "TRANSPORT_SECRET", "mission_id", "human_authority", "last_error"} {
			if strings.Contains(rec.Body.String(), secret) {
				t.Fatalf("leaked %s", secret)
			}
		}
		var page failedIssueMutationPage
		if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		return page
	}
	fixture := func(name string, rec *httptest.ResponseRecorder) {
		t.Helper()
		var val any
		if err := json.Unmarshal(rec.Body.Bytes(), &val); err != nil {
			t.Fatal(err)
		}
		data, err := json.MarshalIndent(val, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		data = append(data, '\n')
		path := filepath.Join("testdata", "issue-outbox-failed", name)
		if os.Getenv("UPDATE_ISSUE_OUTBOX_FIXTURES") == "1" {
			if err = os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(path, data, 0644); err != nil {
				t.Fatal(err)
			}
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(want) != string(data) {
			t.Fatalf("fixture %s differs: %s", name, data)
		}
	}
	empty := request("", nil)
	decode(empty)
	fixture("empty.json", empty)
	execOrFatal(t, db, `INSERT INTO workspaces(id,name,slug) VALUES('hidden-outbox-ws','Hidden','hidden-outbox')`)
	for _, row := range []struct{ id, workspace, code, state string }{{"job-1", ws, "invalid_payload", "failed"}, {"job-2", ws, "delivery_exhausted", "failed"}, {"job-3", ws, "authority_revoked", "failed"}, {"job-4", ws, "private unknown error", "failed"}, {"hidden-job", "hidden-outbox-ws", "invalid_payload", "failed"}, {"pending-job", ws, "", "pending"}} {
		execOrFatal(t, db, `INSERT INTO issue_mutation_outbox(id,workspace_id,mission_id,kind,payload,state,attempts,max_attempts,failed_at,failure_code,next_attempt_at,last_error,created_at) VALUES(?,?,'historical-issue','journal','{"secret":"PAYLOAD_SECRET"}',?,8,8,'2026-10-14T10:00:00Z',?,'2026-10-14T09:00:00Z','TRANSPORT_SECRET','2026-10-14T09:00:00Z')`, row.id, row.workspace, row.state, row.code)
	}
	all := request("?state=failed", nil)
	page := decode(all)
	if len(page.Jobs) != 4 || page.HasMore || page.Jobs[2].ErrorCode != "authority_revoked" || page.Jobs[3].ErrorCode != "unknown" {
		t.Fatalf("%+v", page)
	}
	fixture("failed.json", all)
	first := decode(request("?limit=2", nil))
	if !first.HasMore || len(first.Jobs) != 2 || first.NextCursor == "" {
		t.Fatalf("%+v", first)
	}

	for _, cursor := range []failedIssueMutationCursor{
		{Workspace: ws, User: "other-user", Created: "2026-10-14T09:00:00Z", ID: "job-2"},
		{Workspace: "hidden-outbox-ws", User: user, Created: "2026-10-14T09:00:00Z", ID: "hidden-job"},
	} {
		encoded, err := router.encodeFailedIssueMutationCursor(cursor)
		if err != nil {
			t.Fatal(err)
		}
		if rec := request("?cursor="+url.QueryEscape(encoded), nil); rec.Code != 400 {
			t.Fatalf("foreign cursor accepted %d", rec.Code)
		}
	}
	last := decode(request("?limit=2&cursor="+url.QueryEscape(first.NextCursor), nil))
	if last.HasMore || len(last.Jobs) != 2 || last.Jobs[0].ID != "job-3" || last.Jobs[1].ID != "job-4" {
		t.Fatalf("%+v", last)
	}
	for _, q := range []string{"?limit=0", "?limit=101", "?limit=oops", "?state=processing", "?cursor=broken"} {
		if rec := request(q, nil); rec.Code != 400 {
			t.Fatalf("query %s: %d %s", q, rec.Code, rec.Body.String())
		}
	}
	if rec := request("", stringSet{"issues:read": {}}); rec.Code != 403 {
		t.Fatalf("narrow scope %d", rec.Code)
	}
	decode(request("", stringSet{"workspace:admin": {}}))
	execOrFatal(t, db, `UPDATE workspace_members SET role='MEMBER' WHERE id='m1'`)
	denied := request("", nil)
	if denied.Code != 403 {
		t.Fatalf("stale role trusted: %d", denied.Code)
	}
	fixture("forbidden.json", denied)
	execOrFatal(t, db, `UPDATE workspace_members SET role='MEMBER',access_mode='restricted' WHERE id='m1'`)
	if rec := request("", nil); rec.Code != 403 {
		t.Fatalf("restricted %d", rec.Code)
	}
	execOrFatal(t, db, `UPDATE workspace_members SET role='OWNER',access_mode='trusted' WHERE id='m1'`)
	execOrFatal(t, db, `UPDATE users SET suspended_at='2026-10-14T10:00:00Z' WHERE id=?`, user)
	if rec := request("", nil); rec.Code != 403 {
		t.Fatalf("suspended %d", rec.Code)
	}
}
