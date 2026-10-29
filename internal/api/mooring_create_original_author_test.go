package api

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/appengine"
)

func TestReviewRecoveryGenericCreateRetryRechecksMembershipAfterBody(t *testing.T) {
	f := gridcraftInstanceFixture(t)
	f.r.RegisterGridCraft(appengine.Options{Enabled: true}, gridcraftFixtureEngine{})
	const original = `{"app_id":"hello","slug":"generic-retry-original","name":"Original"}`
	wantCode(t, f.do(f.wsAdmin, "POST", "/api/v1/mooring/documents?workspace_id=ws-old", original), 201, "authorized initial generic create")
	wantCode(t, f.do(f.wsAdmin, "POST", "/api/v1/mooring/documents?workspace_id=ws-old", original), 200, "authorized identical generic retry")
	body := &reviewWorkbookBodyReader{original: strings.NewReader(original), before: func() {
		mustExec(t, f.db, `DELETE FROM workspace_members WHERE workspace_id='ws-old' AND user_id='wsadmin'`)
	}}
	request := httptest.NewRequest("POST", "/api/v1/mooring/documents?workspace_id=ws-old", body)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+f.wsAdmin)
	response := httptest.NewRecorder()
	f.r.ServeHTTP(response, request)
	if body.before != nil {
		t.Fatal("original body mutation did not run")
	}
	if response.Code != 404 {
		t.Fatalf("generic identical retry bypassed original current membership: HTTP=%d", response.Code)
	}
	var pages, documents int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM pages WHERE workspace_id='ws-old' AND slug='generic-retry-original'`).Scan(&pages); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM mooring_documents d JOIN pages p ON p.id=d.page_id WHERE p.workspace_id='ws-old' AND p.slug='generic-retry-original'`).Scan(&documents); err != nil {
		t.Fatal(err)
	}
	if pages != 1 || documents != 1 {
		t.Fatalf("identical retry changed existing document: pages=%d documents=%d", pages, documents)
	}
}
