package api

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/appengine"
)

type reviewWorkbookBodyReader struct {
	original *strings.Reader
	before   func()
}

func (r *reviewWorkbookBodyReader) Read(p []byte) (int, error) {
	if r.before != nil {
		f := r.before
		r.before = nil
		f()
	}
	return r.original.Read(p)
}
func TestReviewRecoveryWorkbookCreateRechecksMembershipAfterBody(t *testing.T) {
	f := gridcraftInstanceFixture(t)
	f.r.RegisterGridCraft(appengine.Options{Enabled: true}, gridcraftFixtureEngine{})
	body := &reviewWorkbookBodyReader{original: strings.NewReader(`{"slug":"revoked-workbook-create","name":"Revoked"}`), before: func() {
		mustExec(t, f.db, `DELETE FROM workspace_members WHERE workspace_id='ws-old' AND user_id='boss'`)
	}}
	request := httptest.NewRequest("POST", "/api/v1/workbooks?workspace_id=ws-old", body)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+f.boss)
	response := httptest.NewRecorder()
	f.r.ServeHTTP(response, request)
	if body.before != nil {
		t.Fatal("body mutation did not execute after middleware admission")
	}
	var count int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM pages WHERE workspace_id='ws-old' AND slug='revoked-workbook-create'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if response.Code != 403 || count != 0 {
		t.Fatalf("revoked original member created private workbook Page: HTTP=%d rows=%d", response.Code, count)
	}
}

func TestGridCraftCreateRechecksOriginalRightsAfterBody(t *testing.T) {
	for _, change := range []string{"restricted", "capability", "credential"} {
		t.Run(change, func(t *testing.T) {
			f := gridcraftInstanceFixture(t)
			f.r.RegisterGridCraft(appengine.Options{Enabled: true}, gridcraftFixtureEngine{})
			if change == "capability" {
				mustExec(t, f.db, `UPDATE workspace_members SET role='MEMBER',capabilities='["page.create"]' WHERE workspace_id='ws-old' AND user_id='wsadmin'`)
			}
			body := &reviewWorkbookBodyReader{original: strings.NewReader(`{"slug":"original-rights","name":"Rights"}`), before: func() {
				switch change {
				case "restricted":
					mustExec(t, f.db, `UPDATE workspace_members SET role='MEMBER',access_mode='restricted',capabilities='["chat"]' WHERE workspace_id='ws-old' AND user_id='wsadmin'`)
				case "capability":
					mustExec(t, f.db, `UPDATE workspace_members SET capabilities='["chat"]' WHERE workspace_id='ws-old' AND user_id='wsadmin'`)
				case "credential":
					mustExec(t, f.db, `UPDATE cli_tokens SET revoked_at='2026-01-01T00:00:00Z' WHERE token_hash=?`, sha256Hex(f.wsAdmin))
				}
			}}
			request := httptest.NewRequest("POST", "/api/v1/workbooks?workspace_id=ws-old", body)
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Authorization", "Bearer "+f.wsAdmin)
			response := httptest.NewRecorder()
			f.r.ServeHTTP(response, request)
			if body.before != nil {
				t.Fatal("body mutation did not execute after middleware admission")
			}
			wantCode(t, response, 403, "original create rights lost after middleware admission")
			var count int
			if err := f.db.QueryRow(`SELECT COUNT(*) FROM pages WHERE workspace_id='ws-old' AND slug='original-rights'`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("pages=%d err=%v", count, err)
			}
		})
	}
}

func TestGridCraftCreateRechecksRightsBeforeCommitAndBeforeIdenticalRetry(t *testing.T) {
	t.Run("after-insert-rollback", func(t *testing.T) {
		f := gridcraftInstanceFixture(t)
		f.r.RegisterGridCraft(appengine.Options{Enabled: true}, gridcraftFixtureEngine{})
		mustExec(t, f.db, `CREATE TRIGGER revoke_workbook_create AFTER INSERT ON pages WHEN NEW.slug='final-create-rights' BEGIN DELETE FROM workspace_members WHERE workspace_id='ws-old' AND user_id='wsadmin'; END`)
		response := f.do(f.wsAdmin, "POST", "/api/v1/workbooks?workspace_id=ws-old", `{"slug":"final-create-rights","name":"Final"}`)
		wantCode(t, response, 403, "SQL-side rights loss must roll back the Page and version")
		var pages, versions, member int
		if err := f.db.QueryRow(`SELECT COUNT(*) FROM pages WHERE slug='final-create-rights'`).Scan(&pages); err != nil {
			t.Fatal(err)
		}
		if err := f.db.QueryRow(`SELECT COUNT(*) FROM page_versions v JOIN pages p ON p.id=v.page_id WHERE p.slug='final-create-rights'`).Scan(&versions); err != nil {
			t.Fatal(err)
		}
		if err := f.db.QueryRow(`SELECT COUNT(*) FROM workspace_members WHERE workspace_id='ws-old' AND user_id='wsadmin'`).Scan(&member); err != nil {
			t.Fatal(err)
		}
		if pages != 0 || versions != 0 || member != 1 {
			t.Fatalf("pages=%d versions=%d member=%d", pages, versions, member)
		}
	})
	t.Run("identical-retry", func(t *testing.T) {
		f := gridcraftInstanceFixture(t)
		f.r.RegisterGridCraft(appengine.Options{Enabled: true}, gridcraftFixtureEngine{})
		const original = `{"slug":"retry-create-rights","name":"Retry"}`
		wantCode(t, f.do(f.wsAdmin, "POST", "/api/v1/workbooks?workspace_id=ws-old", original), 201, "initial create")
		wantCode(t, f.do(f.wsAdmin, "POST", "/api/v1/workbooks?workspace_id=ws-old", original), 200, "authorized identical retry")
		body := &reviewWorkbookBodyReader{original: strings.NewReader(original), before: func() {
			mustExec(t, f.db, `DELETE FROM workspace_members WHERE workspace_id='ws-old' AND user_id='wsadmin'`)
		}}
		request := httptest.NewRequest("POST", "/api/v1/workbooks?workspace_id=ws-old", body)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+f.wsAdmin)
		response := httptest.NewRecorder()
		f.r.ServeHTTP(response, request)
		if body.before != nil {
			t.Fatal("body mutation did not execute after middleware admission")
		}
		wantCode(t, response, 403, "identical retry cannot disclose after original membership loss")
		var count int
		if err := f.db.QueryRow(`SELECT COUNT(*) FROM pages WHERE slug='retry-create-rights'`).Scan(&count); err != nil || count != 1 {
			t.Fatalf("existing pages=%d err=%v", count, err)
		}
	})
}

func TestReviewRecoveryWorkbookCreateRejectsOrdinaryMemberRemovalBeforeWriter(t *testing.T) {
	f := gridcraftInstanceFixture(t)
	f.r.RegisterGridCraft(appengine.Options{Enabled: true}, gridcraftFixtureEngine{})
	var memberID string
	if err := f.db.QueryRow(`SELECT id FROM workspace_members WHERE workspace_id='ws-old' AND user_id='wsadmin'`).Scan(&memberID); err != nil {
		t.Fatal(err)
	}
	body := &reviewWorkbookBodyReader{original: strings.NewReader(`{"slug":"revoked-workbook-create","name":"Revoked"}`), before: func() {
		removed := f.do(f.boss, "DELETE", "/api/v1/workspaces/ws-old/members/"+memberID+"?workspace_id=ws-old", "")
		if removed.Code != 200 {
			t.Fatalf("ordinary member removal failed: HTTP=%d", removed.Code)
		}
	}}
	request := httptest.NewRequest("POST", "/api/v1/workbooks?workspace_id=ws-old", body)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+f.wsAdmin)
	response := httptest.NewRecorder()
	f.r.ServeHTTP(response, request)
	if body.before != nil {
		t.Fatal("ordinary membership removal did not execute after middleware admission")
	}
	var count int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM pages WHERE workspace_id='ws-old' AND slug='revoked-workbook-create'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if response.Code != 403 || count != 0 {
		t.Fatalf("revoked original member created private workbook Page: HTTP=%d rows=%d", response.Code, count)
	}
}
