package main

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestSlideCommentsCLIPreservesAnchorAndConflict(t *testing.T) {
	stub := pageStub(t)
	resetSlideCommand(t)
	body := `{"page_id":"original_page","schema_version":1,"base_rev":7,"mutation_id":"exact_comment","anchor":{"anchor_schema_version":1,"kind":"element","slide_id":"slide_one","element_id":"text_one","revision":7},"body":"Česky\nLF"}`
	path := "/api/v1/pages/report/slides/comments"
	stub.OnPost(path, func(_ *http.Request, actual []byte) (int, []byte, string) {
		var got, want any
		if json.Unmarshal(actual, &got) != nil || json.Unmarshal([]byte(body), &want) != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("immutable anchor changed: %s", actual)
		}
		return 409, []byte(`{"error":"comment conflict"}`), "application/json"
	})
	_, err := runPageCLI(t, body, "slide", "comments", "create", "report", "--file", "-")
	if err == nil || !strings.Contains(err.Error(), "comment conflict") {
		t.Fatal(err)
	}
	if len(stub.CallsFor("POST", path)) != 1 || len(stub.CallsFor("GET", path)) != 0 {
		t.Fatal("refreshed or replayed conflict")
	}
}
func TestSlideCommentsCLIStrictInput(t *testing.T) {
	stub := pageStub(t)
	resetSlideCommand(t)
	_, err := runPageCLI(t, `{"page_id":"page","schema_version":1,"mutation_id":"resolve","if_version":2,"resolved":null}`, "slide", "comments", "resolve", "report", "thread", "--file", "-")
	if err == nil {
		t.Fatal("null resolution accepted")
	}
	if len(stub.CallsFor("POST", "/api/v1/pages/report/slides/comments/thread/resolve")) != 0 {
		t.Fatal("invalid resolution transported")
	}
}
func TestSlideCommentsCLIReadPagination(t *testing.T) {
	stub := pageStub(t)
	resetSlideCommand(t)
	path := "/api/v1/pages/report/slides/comments/thread/history"
	stub.OnGet(path, func(r *http.Request, _ []byte) (int, []byte, string) {
		if r.URL.Query().Get("limit") != "10" || r.URL.Query().Get("cursor") != "opaque-cursor" {
			t.Error("pagination changed", r.URL)
		}
		return 200, []byte(`{"items":[],"next_cursor":"","watermark":0,"has_more":false,"can_comment":false,"can_resolve":false}`), "application/json"
	})
	if _, err := runPageCLI(t, "", "slide", "comments", "history", "report", "thread", "--limit", "10", "--cursor", "opaque-cursor"); err != nil {
		t.Fatal(err)
	}
	if len(stub.CallsFor("GET", path)) != 1 {
		t.Fatal("wrong history route")
	}
}
