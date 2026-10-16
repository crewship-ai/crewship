package main

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func resetSlideCommand(t *testing.T) {
	t.Helper()
	old := findSubcommand(rootCmd.Commands(), "slide")
	fresh := newSlideCommand()
	rootCmd.RemoveCommand(old)
	rootCmd.AddCommand(fresh)
	t.Cleanup(func() { rootCmd.RemoveCommand(fresh); rootCmd.AddCommand(old) })
}
func TestSlidesCLIPreservesPageIdentityAndStopsOnConflict(t *testing.T) {
	stub := pageStub(t)
	resetSlideCommand(t)
	body := `{"page_id":"original_page","schema_version":1,"base_rev":7,"mutation_id":"exact_retry","if_order_version":3,"operations":[{"op":"set_notes","slide_id":"stable_slide","if_hash":"` + strings.Repeat("a", 64) + `","notes":"česky\nLF"}]}`
	stub.OnPost("/api/v1/pages/report/slides/patch", func(_ *http.Request, actual []byte) (int, []byte, string) {
		var got, want any
		if json.Unmarshal(actual, &got) != nil || json.Unmarshal([]byte(body), &want) != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("rewrote immutable preconditions: %s", actual)
		}
		return 409, []byte(`{"error":"Slides conflict"}`), "application/json"
	})
	_, err := runPageCLI(t, body, "slide", "patch", "report", "--file", "-")
	if err == nil || !strings.Contains(err.Error(), "Slides conflict") {
		t.Fatalf("conflict hidden: %v", err)
	}
	if len(stub.CallsFor("POST", "/api/v1/pages/report/slides/patch")) != 1 || len(stub.CallsFor("GET", "/api/v1/pages/report/slides")) != 0 {
		t.Fatal("CLI refreshed/retried/rebased conflicting immutable Page request")
	}
}
func TestSlidesCLIReadRoutes(t *testing.T) {
	for _, test := range []struct {
		args []string
		path string
	}{{[]string{"read", "report"}, "/api/v1/pages/report/slides"}, {[]string{"outline", "report"}, "/api/v1/pages/report/slides/outline"}, {[]string{"history", "report"}, "/api/v1/pages/report/slides/history"}, {[]string{"at-rev", "report", "2"}, "/api/v1/pages/report/slides/revisions/2"}} {
		t.Run(test.args[0], func(t *testing.T) {
			stub := pageStub(t)
			resetSlideCommand(t)
			stub.OnGet(test.path, func(_ *http.Request, _ []byte) (int, []byte, string) {
				return 200, []byte(`{"revision":2}`), "application/json"
			})
			if _, err := runPageCLI(t, "", append([]string{"slide"}, test.args...)...); err != nil {
				t.Fatal(err)
			}
			if len(stub.CallsFor("GET", test.path)) != 1 {
				t.Fatal("wrong read route")
			}
		})
	}
}
func TestSlidesCLIRejectsCaseAliasedRequestBeforeTransport(t *testing.T) {
	stub := pageStub(t)
	resetSlideCommand(t)
	body := `{"page_id":"original_page","PAGE_ID":"replacement","schema_version":1,"base_rev":1,"mutation_id":"bad","if_order_version":1,"slide_order":["stable_slide"]}`
	if _, err := runPageCLI(t, body, "slide", "reorder", "report", "--file", "-"); err == nil {
		t.Fatal("accepted aliased identity")
	}
	if len(stub.CallsFor("POST", "/api/v1/pages/report/slides/reorder")) != 0 {
		t.Fatal("sent invalid strict request")
	}
}
