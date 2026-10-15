package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAcceptanceIssueDeliveriesFailed(t *testing.T) {
	calls := make(chan string, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/workspaces" {
			w.Write([]byte(`[{"id":"cworkspacetest000000001","slug":"ws_test","name":"Synthetic"}]`))
			return
		}
		calls <- r.Method + " " + r.URL.RequestURI()
		if r.URL.Query().Get("cursor") == "forbidden" {
			w.WriteHeader(403)
			w.Write([]byte(`{"error":"workspace_admin_required"}`))
			return
		}
		w.Write([]byte(`{"jobs":[{"id":"job-1","kind":"journal","attempts":8,"max_attempts":8,"created_at":"2026-10-14T09:00:00Z","failed_at":"2026-10-14T10:00:00Z","error_code":"delivery_exhausted"}],"has_more":false,"next_cursor":""}`))
	}))
	defer srv.Close()
	out, err := runChatListCLI(t, srv.URL, "issue", "deliveries", "failed", "--limit", "2", "--cursor", "opaque/+==")
	if err != nil || !strings.Contains(out, "delivery_exhausted") {
		t.Fatalf("%v %s", err, out)
	}
	call := <-calls
	if !strings.HasPrefix(call, "GET /api/v1/workspaces/cworkspacetest000000001/issue-mutation-outbox?") || !strings.Contains(call, "limit=2") || !strings.Contains(call, "state=failed") || !strings.Contains(call, "cursor=opaque%2F%2B%3D%3D") {
		t.Fatal(call)
	}
	out, err = runChatListCLI(t, srv.URL, "issue", "deliveries", "failed", "--cursor", "forbidden")
	if err == nil || !strings.Contains(out, "workspace_admin_required") {
		t.Fatalf("%v %s", err, out)
	}
}
