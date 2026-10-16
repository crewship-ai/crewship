package main

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestPageResolveCLIUsesIdentityWithoutSlugLookup(t *testing.T) {
	stub := pageStub(t)
	const path = "/api/v1/pages/resolve/id/stable-page-id"
	stub.OnGet(path, func(_ *http.Request, _ []byte) (int, []byte, string) {
		return 200, []byte(`{"page_id":"stable-page-id","slug":"renamed-page"}`), "application/json"
	})
	output, err := runPageCLI(t, "", "page", "resolve", "stable-page-id")
	if err != nil {
		t.Fatal(err)
	}
	var identity map[string]string
	if err := json.Unmarshal([]byte(output), &identity); err != nil || identity["slug"] != "renamed-page" || identity["page_id"] != "stable-page-id" {
		t.Fatalf("identity=%q error=%v", output, err)
	}
	if len(stub.Calls()) != 1 || len(stub.CallsFor("GET", path)) != 1 {
		t.Fatalf("unexpected lookup/fallback: %v", stub.Calls())
	}
}
