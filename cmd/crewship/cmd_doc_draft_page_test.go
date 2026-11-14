package main

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// `crewship doc draft view --page <id>` names one Document the run reaches
// through its chat scope (#5317 step 5.3). The server decides; the CLI only
// carries the id, and only on the view.
func TestProtectedDocDraftViewPage(t *testing.T) {
	guardCLIState(t)
	flagFormat = "json"
	t.Setenv("CREWSHIP_AGENT_TOKEN", "agtv2.aaaa")
	var queries []string
	view := func(args ...string) error {
		cmd := newDocDraftCommandWithTransport(docCreateTransport(func(r *http.Request) (*http.Response, error) {
			queries = append(queries, r.URL.RawQuery)
			if r.Method != "GET" || r.URL.Path != "/api/v1/documents/draft" {
				t.Fatalf("view went to %s %s", r.Method, r.URL)
			}
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"page_id":"attached","mode":"read"}`)), Request: r}, nil
		}))
		cmd.SetArgs(append([]string{"view"}, args...))
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		return cmd.Execute()
	}
	if err := view("--page", "attached"); err != nil {
		t.Fatal(err)
	}
	if err := view(); err != nil {
		t.Fatal(err)
	}
	if len(queries) != 2 || queries[0] != "page_id=attached" || queries[1] != "" {
		t.Fatalf("queries %v", queries)
	}
	if err := view("--page", ""); err == nil {
		t.Fatal("an empty --page was sent")
	}
	if len(queries) != 2 {
		t.Fatalf("an empty page reached the transport: %v", queries)
	}
	// Only the view selects a page.
	apply := newDocDraftCommandWithTransport(nil)
	apply.SetArgs([]string{"apply", "--file", "-", "--page", "attached"})
	apply.SetIn(strings.NewReader("{}"))
	apply.SetOut(io.Discard)
	apply.SetErr(io.Discard)
	if err := apply.Execute(); err == nil || !strings.Contains(err.Error(), "unknown flag") {
		t.Fatalf("apply accepted --page: %v", err)
	}
}

// CREWSHIP_SIDECAR_URL selects the loopback sidecar, as for `mooring agent`;
// anything but a plain loopback http origin is refused before any request.
func TestProtectedDocDraftSidecarURLIsLoopbackOnly(t *testing.T) {
	guardCLIState(t)
	flagFormat = "json"
	t.Setenv("CREWSHIP_AGENT_TOKEN", "agtv2.aaaa")
	for _, bad := range []string{"https://127.0.0.1:9119", "http://example.test:9119", "http://127.0.0.1:9119/x", "http://127.0.0.1:9119?a=b", "http://user@127.0.0.1:9119"} {
		t.Setenv("CREWSHIP_SIDECAR_URL", bad)
		calls := 0
		cmd := newDocDraftCommandWithTransport(docCreateTransport(func(*http.Request) (*http.Response, error) { calls++; return nil, nil }))
		cmd.SetArgs([]string{"view"})
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		if err := cmd.Execute(); err == nil || calls != 0 {
			t.Fatalf("%s accepted", bad)
		}
	}
	t.Setenv("CREWSHIP_SIDECAR_URL", "http://127.0.0.1:41234")
	var host string
	cmd := newDocDraftCommandWithTransport(docCreateTransport(func(r *http.Request) (*http.Response, error) {
		host = r.URL.Host
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`)), Request: r}, nil
	}))
	cmd.SetArgs([]string{"view"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err != nil || host != "127.0.0.1:41234" {
		t.Fatalf("selected sidecar not used: %q %v", host, err)
	}
}
