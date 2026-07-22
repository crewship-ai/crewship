package update

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// setLatestNightlyListURL points the nightly-channel lookup at a test server
// and returns a restore func, so Check/CheckExplicit's nightly path can be
// exercised end-to-end without hitting the real GitHub API.
func setLatestNightlyListURL(url string) func() {
	prev := latestNightlyListURL
	latestNightlyListURL = url
	return func() { latestNightlyListURL = prev }
}

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
	var incomparable *IncomparableVersionError
	if !errors.As(err, &incomparable) {
		t.Errorf("expected *IncomparableVersionError, got %T: %v", err, err)
	}
}

func TestParseNightlyVersion(t *testing.T) {
	cases := []struct {
		in   string
		date string
		run  int
		ok   bool
	}{
		{"nightly-20260714-r552", "20260714", 552, true},
		{"nightly-20260101-r1", "20260101", 1, true},
		{"  nightly-20260714-r552  ", "20260714", 552, true},
		{"v0.1.0", "", 0, false},
		{"nightly-2026071-r552", "", 0, false},  // short date
		{"nightly-20260714-552", "", 0, false},  // missing 'r'
		{"nightly-20260714-rabc", "", 0, false}, // non-numeric run
		{"not-a-version", "", 0, false},
	}
	for _, c := range cases {
		nv, ok := parseNightlyVersion(c.in)
		if ok != c.ok {
			t.Errorf("parseNightlyVersion(%q) ok = %v, want %v", c.in, ok, c.ok)
			continue
		}
		if ok && (nv.date != c.date || nv.run != c.run) {
			t.Errorf("parseNightlyVersion(%q) = %+v, want date=%s run=%d", c.in, nv, c.date, c.run)
		}
	}
}

func TestCompareNightlyVersion(t *testing.T) {
	older := nightlyVersion{date: "20260714", run: 552}
	newerSameDay := nightlyVersion{date: "20260714", run: 553}
	newerNextDay := nightlyVersion{date: "20260715", run: 1}

	if compareNightlyVersion(older, older) != 0 {
		t.Error("expected equal versions to compare 0")
	}
	if compareNightlyVersion(newerSameDay, older) <= 0 {
		t.Error("expected same-day higher run to compare greater")
	}
	if compareNightlyVersion(older, newerSameDay) >= 0 {
		t.Error("expected same-day lower run to compare less")
	}
	if compareNightlyVersion(newerNextDay, newerSameDay) <= 0 {
		t.Error("expected next-day build to compare greater regardless of run number")
	}
}

// TestFetchLatestNightly_PicksFirstNightlyTag covers the case where the
// releases list interleaves a stable cut ahead of the nightlies (the list is
// newest-first) — the first tag matching nightly-<date>-r<n> must win, not
// simply the first entry.
func TestFetchLatestNightly_PicksFirstNightlyTag(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[
			{"tag_name": "v0.3.0", "html_url": "https://x/stable", "body": "stable release", "draft": false},
			{"tag_name": "nightly-20260722-r010", "html_url": "https://x/n10", "body": "nightly 10", "draft": false},
			{"tag_name": "nightly-20260721-r638", "html_url": "https://x/n638", "body": "nightly 638", "draft": false}
		]`))
	}))
	defer srv.Close()

	tag, notes, url, err := fetchLatestNightly(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("fetchLatestNightly: %v", err)
	}
	if tag != "nightly-20260722-r010" {
		t.Errorf("tag = %q, want nightly-20260722-r010", tag)
	}
	if !strings.Contains(notes, "nightly 10") {
		t.Errorf("notes = %q", notes)
	}
	if url != "https://x/n10" {
		t.Errorf("url = %q", url)
	}
}

func TestFetchLatestNightly_NoneFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"tag_name": "v0.3.0", "html_url": "https://x", "body": "stable", "draft": false}]`))
	}))
	defer srv.Close()

	_, _, _, err := fetchLatestNightly(context.Background(), srv.URL)
	if err == nil {
		t.Error("expected error when no nightly release is present")
	}
}

func TestCheck_NightlyChannel_Newer(t *testing.T) {
	withTempHome(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"tag_name": "nightly-20260722-r010", "html_url": "https://x/n10", "body": "n10", "draft": false}]`))
	}))
	defer srv.Close()
	restore := setLatestNightlyListURL(srv.URL)
	defer restore()

	r, err := Check(context.Background(), "nightly-20260721-r638")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if r == nil {
		t.Fatal("expected a result for a nightly current version")
	}
	if !r.Newer {
		t.Errorf("expected Newer=true, got result %+v", r)
	}
	if r.Latest != "nightly-20260722-r010" {
		t.Errorf("Latest = %q, want nightly-20260722-r010", r.Latest)
	}
}

func TestCheck_NightlyChannel_UpToDate(t *testing.T) {
	withTempHome(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"tag_name": "nightly-20260721-r638", "html_url": "https://x", "body": "current", "draft": false}]`))
	}))
	defer srv.Close()
	restore := setLatestNightlyListURL(srv.URL)
	defer restore()

	r, err := Check(context.Background(), "nightly-20260721-r638")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if r == nil || r.Newer {
		t.Errorf("expected Newer=false for the same nightly build, got %+v", r)
	}
}

func TestCheckExplicit_NightlyChannel(t *testing.T) {
	withTempHome(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"tag_name": "nightly-20260722-r010", "html_url": "https://x/n10", "body": "n10", "draft": false}]`))
	}))
	defer srv.Close()
	restore := setLatestNightlyListURL(srv.URL)
	defer restore()

	r, err := CheckExplicit(context.Background(), "nightly-20260721-r638")
	if err != nil {
		t.Fatalf("CheckExplicit: %v", err)
	}
	if r == nil || !r.Newer {
		t.Errorf("expected a newer nightly result, got %+v", r)
	}
}

func TestCheckExplicit_IncomparableVersion(t *testing.T) {
	withTempHome(t)
	_, err := CheckExplicit(context.Background(), "commit: none")
	var incomparable *IncomparableVersionError
	if !errors.As(err, &incomparable) {
		t.Errorf("expected *IncomparableVersionError, got %T: %v", err, err)
	}
	if !strings.Contains(incomparable.Error(), "local build") {
		t.Errorf("expected a friendly local-build message, got %q", incomparable.Error())
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
		"0.1.0":        "v0.1.0",
		"v0.1.0":       "v0.1.0",
		"  v0.1.0  ":   "v0.1.0",
		"0.1.0-beta.1": "v0.1.0-beta.1",
		"":             "",
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
