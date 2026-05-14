package update

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// withTempHome redirects HOME so cacheFile() writes into a per-test directory
// instead of polluting the real ~/.crewship.
func withTempHome(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	// macOS also honors $HOME but some systems read XDG_CACHE_HOME first;
	// keep both consistent for safety.
	t.Setenv("XDG_CACHE_HOME", filepath.Join(tmp, ".cache"))
	return tmp
}

func TestCheck_SkipDevVersion(t *testing.T) {
	withTempHome(t)
	r, err := Check(context.Background(), "dev")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if r != nil {
		t.Errorf("expected nil result for dev, got %+v", r)
	}
}

func TestCheck_SkipEmptyVersion(t *testing.T) {
	withTempHome(t)
	r, err := Check(context.Background(), "")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if r != nil {
		t.Errorf("expected nil result for empty, got %+v", r)
	}
}

func TestCheck_EnvOptOut(t *testing.T) {
	withTempHome(t)
	t.Setenv("CREWSHIP_SKIP_UPDATE_CHECK", "1")
	r, err := Check(context.Background(), "v0.1.0")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if r != nil {
		t.Errorf("expected nil result under opt-out, got %+v", r)
	}
}

func TestCheck_InvalidVersion(t *testing.T) {
	withTempHome(t)
	_, err := Check(context.Background(), "not-a-version")
	if err == nil {
		t.Error("expected error for invalid version")
	}
}

// TestCheck_FromCache verifies that a fresh cache file is used in preference
// to the network. We write a synthetic cache entry pointing at a non-existent
// "latest" and confirm Check returns it without hitting any HTTP endpoint.
// (No HTTP server is wired up, so any network call would fail and surface
// an error.)
func TestCheck_FromCache(t *testing.T) {
	withTempHome(t)
	path, err := cacheFile()
	if err != nil {
		t.Fatalf("cacheFile: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cached := Result{
		Current:   "v0.1.0",
		Latest:    "v0.1.5",
		Newer:     true,
		CheckedAt: time.Now().UTC(),
	}
	data, _ := json.Marshal(cached)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write cache: %v", err)
	}
	r, err := Check(context.Background(), "v0.1.0")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if r == nil || r.Latest != "v0.1.5" {
		t.Fatalf("expected cached v0.1.5, got %+v", r)
	}
	if !r.Newer {
		t.Error("expected Newer=true")
	}
}

// TestCheck_RecomputesNewerAgainstCurrent guards a subtle bug class: the
// cache stores `Newer` from when the check ran, but the operator may have
// upgraded since. If the cached "latest" equals the new local version,
// `Newer` must report false on the next read instead of stale true.
func TestCheck_RecomputesNewerAgainstCurrent(t *testing.T) {
	withTempHome(t)
	path, err := cacheFile()
	if err != nil {
		t.Fatalf("cacheFile: %v", err)
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	cached := Result{
		Current:   "v0.1.0",
		Latest:    "v0.1.5",
		Newer:     true,
		CheckedAt: time.Now().UTC(),
	}
	data, _ := json.Marshal(cached)
	_ = os.WriteFile(path, data, 0o600)

	// Pretend the user just upgraded to v0.1.5.
	r, err := Check(context.Background(), "v0.1.5")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if r == nil {
		t.Fatal("expected cached result, got nil")
	}
	if r.Newer {
		t.Errorf("Newer should be false when current matches cached latest, got true")
	}
}

// TestFetchLatest_StableEndpoint stands up a fake GitHub-Releases endpoint
// returning a single-release JSON object (the shape the /releases/latest
// route returns) and verifies parsing.
func TestFetchLatest_StableEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"tag_name": "v0.2.0",
			"html_url": "https://github.com/crewship-ai/crewship/releases/tag/v0.2.0",
			"body": "Lots of changes.",
			"draft": false
		}`))
	}))
	defer srv.Close()

	tag, notes, url, err := fetchLatest(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("fetchLatest: %v", err)
	}
	if tag != "v0.2.0" {
		t.Errorf("tag = %q, want v0.2.0", tag)
	}
	if !strings.Contains(notes, "Lots of changes") {
		t.Errorf("notes = %q", notes)
	}
	if !strings.HasPrefix(url, "https://github.com") {
		t.Errorf("url = %q", url)
	}
}

// TestFetchLatest_PrereleaseEndpoint exercises the array-shape response from
// /releases?per_page=N, which we use when the local build is itself a
// pre-release.
func TestFetchLatest_PrereleaseEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[
			{"tag_name": "v0.1.0-beta.3", "html_url": "https://x/y", "body": "beta3", "draft": false},
			{"tag_name": "v0.1.0-beta.2", "html_url": "https://x/y2", "body": "beta2", "draft": false}
		]`))
	}))
	defer srv.Close()

	tag, _, _, err := fetchLatest(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("fetchLatest: %v", err)
	}
	if tag != "v0.1.0-beta.3" {
		t.Errorf("tag = %q, want v0.1.0-beta.3", tag)
	}
}

// TestFetchLatest_404IsSoftError covers the bootstrapping case where no
// release has been cut yet: the API returns 404. We want a controlled
// "no published release" error rather than a panic or noisy banner.
func TestFetchLatest_404IsSoftError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	_, _, _, err := fetchLatest(context.Background(), srv.URL)
	if err == nil || !strings.Contains(err.Error(), "no published release") {
		t.Errorf("expected 'no published release' error, got %v", err)
	}
}

func TestNormalizeVersion(t *testing.T) {
	cases := map[string]string{
		"0.1.0":         "v0.1.0",
		"v0.1.0":        "v0.1.0",
		"  v0.1.0  ":    "v0.1.0",
		"0.1.0-beta.1":  "v0.1.0-beta.1",
		"":              "",
	}
	for in, want := range cases {
		if got := normalizeVersion(in); got != want {
			t.Errorf("normalizeVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFormatBanner(t *testing.T) {
	if got := FormatBanner(nil); got != "" {
		t.Errorf("nil result should produce empty banner, got %q", got)
	}
	if got := FormatBanner(&Result{Newer: false}); got != "" {
		t.Errorf("non-newer result should produce empty banner, got %q", got)
	}
	got := FormatBanner(&Result{
		Current: "v0.1.0", Latest: "v0.1.1", Newer: true,
		URL: "https://example.com/release",
	})
	if !strings.Contains(got, "v0.1.0") || !strings.Contains(got, "v0.1.1") {
		t.Errorf("banner missing version info: %q", got)
	}
	if !strings.Contains(got, "brew upgrade") {
		t.Errorf("banner missing brew hint: %q", got)
	}
}
