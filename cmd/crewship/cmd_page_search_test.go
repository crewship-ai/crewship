package main

import (
	"net/http"
	"testing"
)

func TestPageSearchCLIExactQueryAndOpaqueContinuation(t *testing.T) {
	stub := pageStub(t)
	const path = "/api/v1/pages/search/content"
	stub.OnGet(path, func(r *http.Request, _ []byte) (int, []byte, string) {
		if r.URL.Query().Get("q") != "needle & second" || r.URL.Query().Get("cursor") != "opaque+/==" || r.URL.Query().Get("limit") != "2" || r.URL.Query().Get("workspace_id") == "" {
			t.Errorf("incorrect search query: %v", r.URL.Query())
		}
		return 200, []byte(`{"scope":"native-docs-slides","matches":[],"has_more":false}`), "application/json"
	})
	_, err := runPageCLI(t, "", "page", "search", "needle & second", "--cursor", "opaque+/==", "--limit", "2")
	if err != nil {
		t.Fatal(err)
	}
	if len(stub.Calls()) != 1 || len(stub.CallsFor("GET", path)) != 1 {
		t.Fatalf("unexpected autopaging/fallback: %v", stub.Calls())
	}
}
func TestPageSearchCLIRejectsInvalidContinuationWithoutRequest(t *testing.T) {
	stub := pageStub(t)
	for _, args := range [][]string{{"page", "search", "needle", "--limit", "0"}, {"page", "search", "needle", "--limit", "51"}, {"page", "search", "needle", "--cursor", ""}} {
		if _, err := runPageCLI(t, "", args...); err == nil {
			t.Fatalf("invalid arguments accepted: %v", args)
		}
	}
	if len(stub.Calls()) != 0 {
		t.Fatalf("invalid arguments made network requests: %v", stub.Calls())
	}
}
