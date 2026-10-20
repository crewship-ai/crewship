package main

// Acceptance for `crewship inbox counts`, driven through the BUILT BINARY: one
// GET /api/v1/inbox/counts (no list walk), and the server's numbers printed
// as given, in table and JSON form.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestAcceptance_InboxCounts(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.Method+" "+r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/inbox/counts" {
			_, _ = w.Write([]byte(`{"total":6,"unread":4,
				"spaces":{"action":2,"updates":3,"history":1},
				"by_kind":{"waitpoint":{"action":2,"updates":0,"history":0},
				           "failed_run":{"action":0,"updates":3,"history":1}}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"detail":"no stub"}`))
	}))
	defer srv.Close()

	out, err := runInboxReadStateCLI(t, srv.URL, "inbox", "counts")
	if err != nil {
		t.Fatalf("inbox counts: %v\n%s", err, out)
	}
	for _, want := range []string{"6 items · 4 unread", "to handle", "waitpoint", "failed_run"} {
		if !strings.Contains(out, want) {
			t.Errorf("table output missing %q:\n%s", want, out)
		}
	}

	out, err = runInboxReadStateCLI(t, srv.URL, "inbox", "counts", "--format", "json")
	if err != nil {
		t.Fatalf("inbox counts --format json: %v\n%s", err, out)
	}
	if !strings.Contains(out, `"action": 2`) || !strings.Contains(out, `"history": 1`) {
		t.Errorf("JSON output = %s", out)
	}

	mu.Lock()
	defer mu.Unlock()
	counts := 0
	for _, p := range paths {
		switch p {
		case "GET /api/v1/inbox/counts":
			counts++
		case "GET /api/v1/workspaces":
			// workspace resolution, not inbox traffic
		default:
			t.Errorf("unexpected request %q: counts must not walk the list", p)
		}
	}
	if counts != 2 {
		t.Errorf("counts requests = %d, want 2 (one per invocation); all: %v", counts, paths)
	}
}
