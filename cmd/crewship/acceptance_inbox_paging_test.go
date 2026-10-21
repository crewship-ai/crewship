package main

// Acceptance for server-side Inbox paging, search and filters, driven through
// the BUILT BINARY against a stub that pages by cursor: the CLI must send the
// filter parameters as given, follow next_cursor (never offset) with --all,
// hand the cursor back on a single page, and pass the same filters to counts.

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

func TestAcceptance_InboxList_CursorQueryAndFilters(t *testing.T) {
	var mu sync.Mutex
	var queries []url.Values
	var countsQueries []url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/inbox":
			queries = append(queries, r.URL.Query())
			switch r.URL.Query().Get("cursor") {
			case "":
				_, _ = w.Write([]byte(`{"rows":[{"id":"i3","kind":"waitpoint","title":"third","state":"unread"},{"id":"i2","kind":"waitpoint","title":"second","state":"unread"}],"count":2,"unread_count":3,"has_more":true,"next_cursor":"CUR1"}`))
			case "CUR1":
				_, _ = w.Write([]byte(`{"rows":[{"id":"i1","kind":"waitpoint","title":"first","state":"unread"}],"count":1,"unread_count":3,"has_more":false}`))
			default:
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"invalid cursor"}`))
			}
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/inbox/counts":
			countsQueries = append(countsQueries, r.URL.Query())
			_, _ = w.Write([]byte(`{"total":3,"unread":3,"spaces":{"action":3,"updates":0,"history":0},"by_kind":{},"by_attention":{"decision":3},"by_deadline":{"none":3}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"detail":"no stub"}`))
		}
	}))
	defer srv.Close()

	filters := []string{"--query", "deploy", "--space", "action", "--unread", "--attention", "decision",
		"--deadline", "soon", "--crew", "crew-1", "--agent", "riley"}

	// --all follows next_cursor and never sends offset.
	out, err := runInboxReadStateCLI(t, srv.URL, append([]string{"inbox", "list", "--state", "all", "--limit", "2", "--all"}, filters...)...)
	if err != nil {
		t.Fatalf("inbox list --all: %v\n%s", err, out)
	}
	for _, want := range []string{"i3", "i2", "i1"} {
		if !strings.Contains(out, want) {
			t.Errorf("--all output missing %s:\n%s", want, out)
		}
	}
	mu.Lock()
	if len(queries) != 2 {
		t.Fatalf("list requests = %d, want 2", len(queries))
	}
	for i, q := range queries {
		for k, want := range map[string]string{"q": "deploy", "space": "action", "unread": "true", "attention": "decision",
			"deadline": "soon", "crew": "crew-1", "agent": "riley"} {
			if q.Get(k) != want {
				t.Errorf("request %d: %s = %q, want %q", i, k, q.Get(k), want)
			}
		}
		if q.Has("offset") {
			t.Errorf("request %d sent offset=%q; a cursor walk must not", i, q.Get("offset"))
		}
	}
	if queries[0].Has("cursor") || queries[1].Get("cursor") != "CUR1" {
		t.Errorf("cursors = %q then %q, want none then CUR1", queries[0].Get("cursor"), queries[1].Get("cursor"))
	}
	queries = nil
	mu.Unlock()

	// A single page hands the cursor back; --cursor resumes from it.
	out, err = runInboxReadStateCLI(t, srv.URL, "inbox", "list", "--limit", "2")
	if err != nil {
		t.Fatalf("inbox list: %v\n%s", err, out)
	}
	if !strings.Contains(out, "--cursor CUR1") {
		t.Errorf("single page output does not offer the cursor:\n%s", out)
	}
	out, err = runInboxReadStateCLI(t, srv.URL, "inbox", "list", "--cursor", "CUR1")
	if err != nil || !strings.Contains(out, "i1") || strings.Contains(out, "--cursor") {
		t.Errorf("inbox list --cursor CUR1 = %v\n%s", err, out)
	}

	// counts takes the same filters.
	out, err = runInboxReadStateCLI(t, srv.URL, append([]string{"inbox", "counts"}, filters...)...)
	if err != nil {
		t.Fatalf("inbox counts: %v\n%s", err, out)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(countsQueries) != 1 || countsQueries[0].Get("q") != "deploy" || countsQueries[0].Get("attention") != "decision" || countsQueries[0].Get("unread") != "true" {
		t.Errorf("counts query = %v, want the filters forwarded", countsQueries)
	}
}
