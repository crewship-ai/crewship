package main

import (
	"net/http"
	"testing"
)

func TestPageDiscussionPaginationCLI(t *testing.T) {
	for _, kind := range []string{"artifact", "board"} {
		t.Run(kind, func(t *testing.T) {
			stub := pageStub(t)
			resetPageArtifactCommand(t)
			path := "/api/v1/pages/report/" + kind + "/threads"
			stub.On("GET", path, func(r *http.Request, _ []byte) (int, []byte, string) {
				q := r.URL.Query()
				if q.Get("status") != "resolved" || q.Get("limit") != "25" || q.Get("cursor") != "opaque+/=" {
					t.Errorf("query=%v", q)
				}
				return 200, []byte(`{"threads":[],"next_cursor":"next"}`), "application/json"
			})
			verb := "list"
			if kind == "board" {
				verb = "show"
			}
			if out, err := runPageCLI(t, "", "page", kind, "threads", verb, "report", "--status", "resolved", "--limit", "25", "--cursor", "opaque+/="); err != nil {
				t.Fatalf("%v %s", err, out)
			}
			if len(stub.CallsFor("GET", path)) != 1 {
				t.Fatal("missing request")
			}
		})
	}
}
