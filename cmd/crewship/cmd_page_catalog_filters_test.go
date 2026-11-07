package main

import (
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func TestPageCLICatalogFacetFiltersEncodeRepeatedValues(t *testing.T) {
	stub := pageStub(t)
	stub.OnGet("/api/v1/pages", func(r *http.Request, _ []byte) (int, []byte, string) {
		want := url.Values{"q": {"Žluťoučký & launch?"}, "kind": {"project", "word", "slides", "design"}, "access": {"private", "open"}, "version": {"shared", "unshared"}, "owner": {"me", "user/cuser123", "crew/ops"}, "folder": {"~unfiled"}, "empty": {"exclude"}, "sort": {"name"}, "limit": {"7"}, "offset": {"14"}, "test_items": {"include"}, "archived": {"only"}, "audience": {"all"}}
		got := r.URL.Query()
		got.Del("workspace_id")
		if !reflect.DeepEqual(got, want) {
			t.Errorf("query=%v, want %v", got, want)
		}
		if strings.Contains(r.URL.RawQuery, "Žluťoučký") || !strings.Contains(r.URL.RawQuery, "%26") {
			t.Errorf("query not URL encoded: %s", r.URL.RawQuery)
		}
		return 200, []byte(`[]`), "application/json"
	})
	_, err := runPageCLI(t, "", "page", "list", "-q", "Žluťoučký & launch?", "--kind", "project,word", "--kind", "slides,design", "--access", "private,open", "--version-state", "shared", "--version-state", "unshared", "--owner", "me,user/cuser123", "--owner", "crew/ops", "--folder", "~unfiled", "--empty", "exclude", "--sort", "name", "--limit", "7", "--offset", "14", "--test-items", "include", "--archived", "only", "--audience", "all", "--format", "json")
	if err != nil {
		t.Fatal(err)
	}
	if len(stub.CallsFor("GET", "/api/v1/pages")) != 1 {
		t.Fatal("catalog request was not sent once")
	}
}

func TestPageCLICatalogLegacyListSendsNoExtraFilters(t *testing.T) {
	stub := pageStub(t)
	stub.OnGet("/api/v1/pages", func(r *http.Request, _ []byte) (int, []byte, string) {
		query := r.URL.Query()
		query.Del("workspace_id")
		if len(query) != 0 {
			t.Errorf("legacy query changed: %v", query)
		}
		return 200, []byte(`[{"slug":"legacy-page","name":"Legacy","owner":"user/u1"}]`), "application/json"
	})
	out, err := runPageCLI(t, "", "page", "list", "--format", "json")
	if err != nil || !strings.HasPrefix(strings.TrimSpace(out), "[") || !strings.Contains(out, "legacy-page") {
		t.Fatalf("legacy array output=%q error=%v", out, err)
	}
}

func TestPageCLICatalogKnownInvalidFiltersSendNoRequest(t *testing.T) {
	guardCLIState(t)
	for _, args := range [][]string{{"--kind", "unknown"}, {"--empty", "yes"}, {"--kind", "Design"}, {"--kind", "project,Word"}, {"--kind", ""}, {"--access", "edit"}, {"--version-state", "latest"}, {"--sort", "descending"}, {"--owner", "Ada Lovelace"}, {"--owner", "user/"}, {"--owner", "crew:"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			resetCLIState()
			stub := pageStub(t)
			if _, err := runPageCLI(t, "", append([]string{"page", "list"}, args...)...); err == nil || !strings.Contains(err.Error(), strings.TrimPrefix(args[0], "--")) {
				t.Fatalf("invalid catalog filter did not identify %s: %v", args[0], err)
			}
			if len(stub.Calls()) != 0 {
				t.Fatalf("invalid filters reached transport: %v", stub.Calls())
			}
		})
	}
}
