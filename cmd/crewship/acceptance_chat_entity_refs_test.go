package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAcceptance_ChatEntityRefsAndOperationGet(t *testing.T) {
	calls := make(chan string, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/workspaces" {
			_, _ = w.Write([]byte(`[{"id":"cworkspacetest000000001","slug":"ws_test","name":"Synthetic"}]`))
			return
		}
		calls <- r.Method + " " + r.URL.RequestURI()
		switch r.URL.Path {
		case "/api/v1/chats/c_1/entity-refs":
			_, _ = w.Write([]byte(`{"reset":true,"next_cursor":"opaque-next","snapshot_id":"snapshot-1","snapshot_complete":true,"has_more":false,"refs":[]}`))
		case "/api/v1/operations/op_1":
			_, _ = w.Write([]byte(`{"id":"op_1","status":"completed","kind":"issue","verb":"created","entity":{"id":"issue-1","identifier":"ENG-1","workspace_id":"ws_test"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"Not found"}`))
		}
	}))
	defer srv.Close()
	out, err := runChatListCLI(t, srv.URL, "chat", "refs", "c_1", "--limit", "2", "--cursor", "opaque/+==")
	if err != nil {
		t.Fatalf("chat refs: %v %s", err, out)
	}
	if !strings.Contains(out, "opaque-next") || !strings.Contains(out, "snapshot_complete") {
		t.Fatal(out)
	}
	call := <-calls
	if !strings.HasPrefix(call, "GET /api/v1/chats/c_1/entity-refs?") || !strings.Contains(call, "cursor=opaque%2F%2B%3D%3D") || !strings.Contains(call, "limit=2") {
		t.Fatal(call)
	}
	out, err = runChatListCLI(t, srv.URL, "operation", "get", "op_1")
	if err != nil {
		t.Fatalf("operation get: %v %s", err, out)
	}
	if !strings.Contains(out, "ENG-1") {
		t.Fatal(out)
	}
	if call = <-calls; !strings.HasPrefix(call, "GET /api/v1/operations/op_1") {
		t.Fatal(call)
	}
	out, err = runChatListCLI(t, srv.URL, "operation", "get", "missing")
	if err == nil || !strings.Contains(out, "Not found") {
		t.Fatalf("authorization failure: %v %s", err, out)
	}
}
