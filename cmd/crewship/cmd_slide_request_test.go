package main

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestSlidesRequestCLIPreservesOriginalAdmissionAndStopsOnConflict(t *testing.T) {
	stub := pageStub(t)
	resetSlideCommand(t)
	body := `{"page_id":"original_page","type_id":"slides","schema_version":1,"base_rev":7,"submission_id":"original_submission","chat_id":"private_chat","instruction":"Uprav výběr","selection":{"selection_schema_version":1,"kind":"text_range","slide_id":"slide_one","element_id":"text_one","element_hash":"` + strings.Repeat("a", 64) + `","from":0,"to":1,"quote":"ě"}}`
	path := "/api/v1/pages/report/slides/requests"
	stub.OnPost(path, func(_ *http.Request, actual []byte) (int, []byte, string) {
		var got, want any
		if json.Unmarshal(actual, &got) != nil || json.Unmarshal([]byte(body), &want) != nil || !reflect.DeepEqual(got, want) {
			t.Error("changed immutable selection/base/Page", string(actual))
		}
		return 409, []byte(`{"error":"Original Page or selection changed"}`), "application/json"
	})
	if _, err := runPageCLI(t, body, "slide", "request", "create", "report", "--file", "-"); err == nil || !strings.Contains(err.Error(), "Original Page") {
		t.Fatal("conflict hidden", err)
	}
	if len(stub.CallsFor("POST", path)) != 1 || len(stub.CallsFor("GET", path)) != 0 || len(stub.CallsFor("GET", "/api/v1/pages/report/slides")) != 0 {
		t.Fatal("admission conflict refreshed original baseline")
	}
}

func TestSlidesRequestCLIReadPaginationAndStrictCancellation(t *testing.T) {
	stub := pageStub(t)
	resetSlideCommand(t)
	path := "/api/v1/pages/report/slides/requests"
	stub.OnGet(path, func(r *http.Request, _ []byte) (int, []byte, string) {
		if r.URL.Query().Get("limit") != "10" || r.URL.Query().Get("cursor") != "original-cursor" {
			t.Error("request pagination changed", r.URL)
		}
		return 200, []byte(`{"items":[],"next_cursor":"","watermark":0,"has_more":false}`), "application/json"
	})
	if _, err := runPageCLI(t, "", "slide", "request", "list", "report", "--limit", "10", "--cursor", "original-cursor"); err != nil {
		t.Fatal(err)
	}
	if _, err := runPageCLI(t, `{"page_id":"original_page","PAGE_ID":"other","schema_version":1,"mutation_id":"cancel","if_version":1}`, "slide", "request", "cancel", "report", "request", "--file", "-"); err == nil {
		t.Fatal("case alias cancellation accepted")
	}
	if len(stub.CallsFor("POST", path+"/request/cancel")) != 0 {
		t.Fatal("invalid cancellation transported")
	}
}
