package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"runtime"
	"testing"
)

// userAgentPattern matches "crewship/<version> (<goos>/<goarch>)" — the one
// header format this package is allowed to emit. Anything beyond
// version/os/arch (hostname, username, machine ID) would fail this pattern
// by construction, since the parens only ever contain GOOS/GOARCH.
var userAgentPattern = regexp.MustCompile(`^crewship/\S+ \(` + regexp.QuoteMeta(runtime.GOOS+"/"+runtime.GOARCH) + `\)$`)

// TestUserAgentFormat: a configured version renders literally into the
// header, and the whole string matches the documented format.
func TestUserAgentFormat(t *testing.T) {
	resetSkewState(t, "1.2.3")
	got := UserAgent()
	if !userAgentPattern.MatchString(got) {
		t.Errorf("UserAgent() = %q, want to match %s", got, userAgentPattern)
	}
	want := "crewship/1.2.3 (" + runtime.GOOS + "/" + runtime.GOARCH + ")"
	if got != want {
		t.Errorf("UserAgent() = %q, want %q", got, want)
	}
}

// TestUserAgentEmptyVersionFallsBackToDev: a dev build (no ldflags version,
// so SetClientVersion sees "") must report "crewship/dev (...)" — not
// "crewship/ (...)", which would be a confusing, malformed header.
func TestUserAgentEmptyVersionFallsBackToDev(t *testing.T) {
	resetSkewState(t, "")
	got := UserAgent()
	want := "crewship/dev (" + runtime.GOOS + "/" + runtime.GOARCH + ")"
	if got != want {
		t.Errorf("UserAgent() = %q, want %q", got, want)
	}
}

// TestClientSendsUserAgent covers an ordinary request built via Client.Do
// (e.g. c.Get) — the common path every CLI command uses.
func TestClientSendsUserAgent(t *testing.T) {
	resetSkewState(t, "0.9.3")
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.WriteHeader(200)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "tok", "")
	resp, err := c.Get("/api/v1/agents")
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	resp.Body.Close()

	want := "crewship/0.9.3 (" + runtime.GOOS + "/" + runtime.GOARCH + ")"
	if gotUA != want {
		t.Errorf("User-Agent = %q, want %q", gotUA, want)
	}
}

// TestWorkspaceSlugPreflightSendsUserAgent covers the SECOND request-building
// call site in client.go: resolveWorkspaceSlug builds its own
// http.NewRequestWithContext (line ~343) instead of going through
// Client.NewRequest, so the header has to be set there too — this test
// would have failed before that call site was covered even though
// TestClientSendsUserAgent passed.
func TestWorkspaceSlugPreflightSendsUserAgent(t *testing.T) {
	resetSkewState(t, "0.9.3")
	t.Setenv("HOME", t.TempDir()) // isolate the slug disk cache
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/workspaces" {
			gotUA = r.Header.Get("User-Agent")
			json.NewEncoder(w).Encode([]map[string]string{
				{"id": "cuid_resolved_id_12345678", "slug": "my-slug"},
			})
			return
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "tok", "my-slug")
	resp, err := c.Get("/api/v1/agents")
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	resp.Body.Close()

	want := "crewship/0.9.3 (" + runtime.GOOS + "/" + runtime.GOARCH + ")"
	if gotUA != want {
		t.Errorf("workspace preflight User-Agent = %q, want %q", gotUA, want)
	}
}
