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

func TestWriteCache_ReadCache_RoundTrip(t *testing.T) {
	tmp := withTempHome(t)

	want := &Result{
		Current:   "v0.1.0",
		Latest:    "v0.2.0",
		Newer:     true,
		URL:       "https://github.com/crewship-ai/crewship/releases/tag/v0.2.0",
		Notes:     "bug fixes",
		CheckedAt: time.Now().UTC().Truncate(time.Second),
	}
	writeCache(want)

	path := filepath.Join(tmp, ".crewship", "cache", "latest_release.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("cache file not written at %s: %v", path, err)
	}

	got := readCache()
	if got == nil {
		t.Fatal("readCache returned nil after writeCache")
	}
	if got.Latest != want.Latest || got.URL != want.URL || got.Notes != want.Notes || !got.Newer {
		t.Errorf("round-trip mismatch: got %+v, want %+v", got, want)
	}
	if !got.CheckedAt.Equal(want.CheckedAt) {
		t.Errorf("CheckedAt = %v, want %v", got.CheckedAt, want.CheckedAt)
	}
}

func TestCacheFile_FallsBackToTempDirWithoutHome(t *testing.T) {
	t.Setenv("HOME", "")
	path, err := cacheFile()
	if err != nil {
		t.Fatalf("cacheFile: %v", err)
	}
	if !strings.HasPrefix(path, os.TempDir()) {
		t.Errorf("path = %q, want fallback under %q", path, os.TempDir())
	}
}

func TestReadCache_CorruptJSONIsNoCache(t *testing.T) {
	tmp := withTempHome(t)
	dir := filepath.Join(tmp, ".crewship", "cache")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "latest_release.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readCache(); got != nil {
		t.Errorf("readCache = %+v, want nil for corrupt file", got)
	}
}

func TestReadCache_MissingFieldsIsNoCache(t *testing.T) {
	tmp := withTempHome(t)
	dir := filepath.Join(tmp, ".crewship", "cache")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Well-formed JSON but no Latest → treated as absent.
	data, _ := json.Marshal(Result{Current: "v0.1.0", CheckedAt: time.Now()})
	if err := os.WriteFile(filepath.Join(dir, "latest_release.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readCache(); got != nil {
		t.Errorf("readCache = %+v, want nil when Latest is empty", got)
	}
}

func TestFetchLatest_Non200Status(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, _, _, err := fetchLatest(context.Background(), srv.URL)
	if err == nil || !strings.Contains(err.Error(), "github API status 500") {
		t.Errorf("err = %v, want 'github API status 500'", err)
	}
}

func TestFetchLatest_DraftOnlyList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"tag_name":"v0.3.0","draft":true}]`))
	}))
	defer srv.Close()

	_, _, _, err := fetchLatest(context.Background(), srv.URL)
	if err == nil || !strings.Contains(err.Error(), "no non-draft release") {
		t.Errorf("err = %v, want 'no non-draft release in list'", err)
	}
}

func TestFetchLatest_SkipsDraftPicksNext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[
			{"tag_name":"v0.4.0-beta.2","draft":true},
			{"tag_name":"v0.4.0-beta.1","draft":false,"html_url":"https://x/v0.4.0-beta.1","body":"beta notes"}
		]`))
	}))
	defer srv.Close()

	tag, notes, htmlURL, err := fetchLatest(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("fetchLatest: %v", err)
	}
	if tag != "v0.4.0-beta.1" {
		t.Errorf("tag = %q, want the first non-draft v0.4.0-beta.1", tag)
	}
	if notes != "beta notes" || htmlURL != "https://x/v0.4.0-beta.1" {
		t.Errorf("notes/url = %q / %q", notes, htmlURL)
	}
}

func TestFetchLatest_UnparseableBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`this is not json`))
	}))
	defer srv.Close()

	_, _, _, err := fetchLatest(context.Background(), srv.URL)
	if err == nil || !strings.Contains(err.Error(), "parse release JSON") {
		t.Errorf("err = %v, want parse error", err)
	}
}

func TestFetchLatest_EmptyTagName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"","html_url":"https://x"}`))
	}))
	defer srv.Close()

	_, _, _, err := fetchLatest(context.Background(), srv.URL)
	if err == nil || !strings.Contains(err.Error(), "empty tag_name") {
		t.Errorf("err = %v, want 'release has empty tag_name'", err)
	}
}

func TestFetchLatest_SendsGitHubTokenWhenSet(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"tag_name":"v0.5.0","html_url":"https://x","body":"n"}`))
	}))
	defer srv.Close()

	t.Setenv("GITHUB_TOKEN", "ghp_testtoken")
	tag, _, _, err := fetchLatest(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("fetchLatest: %v", err)
	}
	if tag != "v0.5.0" {
		t.Errorf("tag = %q, want v0.5.0", tag)
	}
	if gotAuth != "Bearer ghp_testtoken" {
		t.Errorf("Authorization = %q, want Bearer ghp_testtoken", gotAuth)
	}
}

func TestTruncateNotes(t *testing.T) {
	short := "fits in the banner"
	if got := truncateNotes(short); got != short {
		t.Errorf("short notes mutated: %q", got)
	}
	long := strings.Repeat("x", 600)
	got := truncateNotes(long)
	if len(got) != 503 { // 500 + "..."
		t.Errorf("len = %d, want 503", len(got))
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("truncated notes missing ellipsis: %q", got[490:])
	}
}

func TestWriteCache_MkdirFailureIsSilentNoCache(t *testing.T) {
	tmp := withTempHome(t)
	// ~/.crewship exists as a FILE → MkdirAll for the cache dir fails and
	// writeCache must bail out without panicking or leaving a cache.
	if err := os.WriteFile(filepath.Join(tmp, ".crewship"), []byte("blocker"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeCache(&Result{Latest: "v9.9.9", CheckedAt: time.Now()})
	if got := readCache(); got != nil {
		t.Errorf("readCache = %+v, want nil after failed write", got)
	}
}
