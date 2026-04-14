package devcontainer

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// newTestLogger returns a silent logger for tests.
func newTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// newMockUpstream spins up an httptest server that emulates both the collection
// index and per-collection manifest endpoints. Returns the server and a
// pointer to a counter that tracks total requests.
func newMockUpstream(t *testing.T) (*httptest.Server, *int64) {
	t.Helper()
	var hits int64
	mux := http.NewServeMux()

	// Collection index — serve YAML with two collections pointing at the same server.
	mux.HandleFunc("/collection-index.yml", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.Header().Set("Content-Type", "text/yaml")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`- name: Mock Features
  maintainer: mock
  contact: mock@example.com
  repository: https://github.com/mock/features
  ociReference: ghcr.io/mock/features
`))
	})

	// Manifest — serve a minimal devcontainer-collection.json.
	mux.HandleFunc("/mock/features/main/devcontainer-collection.json", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"features": [
				{"id": "python", "version": "1.8.0", "name": "Python", "description": "Installs Python"},
				{"id": "node", "version": "1.3.1", "name": "Node.js", "description": "Installs Node.js"}
			]
		}`))
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &hits
}

// rewriteFetcher replaces the upstream URLs inside the fetcher's http client
// with paths relative to the mock server — used by the mem-cache-hit test.
// Instead, we test by stubbing the fetcher's httpClient via a RoundTripper.
type redirectRoundTripper struct {
	base   http.RoundTripper
	target string
}

func (r *redirectRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	// Rewrite raw.githubusercontent.com / collection-index.yml to the mock.
	url := req.URL
	switch {
	case strings.Contains(url.Host, "raw.githubusercontent.com"):
		// /devcontainers/devcontainers.github.io/gh-pages/_data/collection-index.yml
		if strings.Contains(url.Path, "collection-index.yml") {
			newURL := r.target + "/collection-index.yml"
			req2, _ := http.NewRequestWithContext(req.Context(), req.Method, newURL, nil)
			req2.Header = req.Header
			return r.base.RoundTrip(req2)
		}
		// /devcontainers/features/main/devcontainer-collection.json
		newURL := r.target + url.Path
		req2, _ := http.NewRequestWithContext(req.Context(), req.Method, newURL, nil)
		req2.Header = req.Header
		return r.base.RoundTrip(req2)
	case strings.Contains(url.Host, "mockhost"):
		newURL := r.target + url.Path
		req2, _ := http.NewRequestWithContext(req.Context(), req.Method, newURL, nil)
		req2.Header = req.Header
		return r.base.RoundTrip(req2)
	}
	return r.base.RoundTrip(req)
}

func TestCatalogFetcher_UpstreamFetch(t *testing.T) {
	srv, hits := newMockUpstream(t)
	dir := t.TempDir()

	f := NewCatalogFetcher(dir, newTestLogger())
	f.httpClient = &http.Client{
		Timeout:   5 * time.Second,
		Transport: &redirectRoundTripper{base: http.DefaultTransport, target: srv.URL},
	}

	ctx := context.Background()
	if err := f.RefreshCatalog(ctx); err != nil {
		t.Fatalf("RefreshCatalog: %v", err)
	}
	if *hits == 0 {
		t.Fatal("expected upstream hits, got 0")
	}

	entries := f.GetCatalog(ctx)
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}

	// Verify OCI ref construction: major version from "1.8.0" -> "1".
	wantRef := "ghcr.io/mock/features/python:1"
	var found bool
	for _, e := range entries {
		if e.Ref == wantRef {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected ref %q in catalog, got %+v", wantRef, entries)
	}

	// Disk cache should exist.
	if _, err := os.Stat(filepath.Join(dir, featureCatalogFile)); err != nil {
		t.Errorf("expected disk cache file, got err: %v", err)
	}
}

func TestCatalogFetcher_MemCacheHit(t *testing.T) {
	f := NewCatalogFetcher(t.TempDir(), newTestLogger())
	f.memCache = &catalogMemCache{
		entries:   []CatalogEntry{{Ref: "ghcr.io/test/x:1", Name: "X", Category: "tools", Icon: "wrench"}},
		fetchedAt: time.Now(),
	}

	got := f.GetCatalog(context.Background())
	if len(got) != 1 || got[0].Name != "X" {
		t.Errorf("memory cache miss, got %+v", got)
	}
}

func TestCatalogFetcher_DiskCacheFallback(t *testing.T) {
	dir := t.TempDir()
	// Write a fresh disk cache.
	payload := diskCacheFile{
		FetchedAt: time.Now().Add(-1 * time.Hour),
		Entries:   []CatalogEntry{{Ref: "ghcr.io/test/disk:1", Name: "Disk", Category: "tools", Icon: "wrench"}},
	}
	data, _ := json.Marshal(payload)
	if err := os.WriteFile(filepath.Join(dir, featureCatalogFile), data, 0o644); err != nil {
		t.Fatalf("seed disk cache: %v", err)
	}

	f := NewCatalogFetcher(dir, newTestLogger())
	got := f.GetCatalog(context.Background())
	if len(got) != 1 || got[0].Name != "Disk" {
		t.Errorf("expected to load from disk cache, got %+v", got)
	}
}

func TestCatalogFetcher_UpstreamFailFallback(t *testing.T) {
	// No mock server; fetcher will fail to reach collectionIndexURL.
	f := NewCatalogFetcher(t.TempDir(), newTestLogger())
	// Use an http client that immediately errors on all requests.
	f.httpClient = &http.Client{
		Timeout:   100 * time.Millisecond,
		Transport: &failingTransport{},
	}

	if err := f.RefreshCatalog(context.Background()); err == nil {
		t.Fatal("expected error from upstream failure")
	}

	got := f.GetCatalog(context.Background())
	if len(got) == 0 {
		t.Fatal("expected fallback catalog, got empty")
	}
	if len(got) != len(FallbackCatalog) {
		t.Errorf("expected fallback length %d, got %d", len(FallbackCatalog), len(got))
	}
}

type failingTransport struct{}

func (failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, http.ErrServerClosed
}

// --- runtime fetcher tests -------------------------------------------------

func TestRuntimeFetcher_MemCacheHit(t *testing.T) {
	f := NewRuntimeFetcher(t.TempDir(), newTestLogger())
	f.memCache = &runtimeMemCache{
		entries:   []RuntimeCatalogEntry{{Name: "X", Tool: "x", Category: "tools", Icon: "wrench"}},
		fetchedAt: time.Now(),
	}
	got := f.GetRuntimes(context.Background())
	if len(got) != 1 || got[0].Tool != "x" {
		t.Errorf("mem cache miss, got %+v", got)
	}
}

func TestRuntimeFetcher_DiskCacheFallback(t *testing.T) {
	dir := t.TempDir()
	payload := runtimeDiskCache{
		FetchedAt: time.Now(),
		Entries:   []RuntimeCatalogEntry{{Name: "Disk", Tool: "disk", Category: "tools", Icon: "wrench"}},
	}
	data, _ := json.Marshal(payload)
	if err := os.WriteFile(filepath.Join(dir, runtimeCatalogFile), data, 0o644); err != nil {
		t.Fatalf("seed disk cache: %v", err)
	}
	f := NewRuntimeFetcher(dir, newTestLogger())
	got := f.GetRuntimes(context.Background())
	if len(got) != 1 || got[0].Tool != "disk" {
		t.Errorf("expected disk cache entry, got %+v", got)
	}
}

func TestRuntimeFetcher_UpstreamFailFallback(t *testing.T) {
	f := NewRuntimeFetcher(t.TempDir(), newTestLogger())
	f.httpClient = &http.Client{
		Timeout:   100 * time.Millisecond,
		Transport: &failingTransport{},
	}
	if err := f.RefreshRuntimes(context.Background()); err == nil {
		t.Fatal("expected error from upstream failure")
	}
	got := f.GetRuntimes(context.Background())
	if len(got) == 0 {
		t.Error("expected fallback runtimes, got empty")
	}
}

func TestRuntimeFetcher_UpstreamFetch(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`[
			{"name": "node.toml", "path": "registry/node.toml", "type": "file"},
			{"name": "python.toml", "path": "registry/python.toml", "type": "file"},
			{"name": "awscli.toml", "path": "registry/awscli.toml", "type": "file"},
			{"name": "README.md", "path": "registry/README.md", "type": "file"}
		]`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	f := NewRuntimeFetcher(t.TempDir(), newTestLogger())
	f.httpClient = &http.Client{
		Timeout: 5 * time.Second,
		Transport: &redirectTransport{target: srv.URL, host: "api.github.com"},
	}

	if err := f.RefreshRuntimes(context.Background()); err != nil {
		t.Fatalf("RefreshRuntimes: %v", err)
	}
	got := f.GetRuntimes(context.Background())
	if len(got) == 0 {
		t.Fatal("expected entries")
	}
	// Node should be categorized as "languages".
	var nodeEntry *RuntimeCatalogEntry
	for i := range got {
		if got[i].Tool == "node" {
			nodeEntry = &got[i]
			break
		}
	}
	if nodeEntry == nil {
		t.Fatal("node entry missing")
	}
	if nodeEntry.Category != "languages" {
		t.Errorf("node category = %q, want languages", nodeEntry.Category)
	}
	// awscli should be "cloud".
	for _, e := range got {
		if e.Tool == "awscli" && e.Category != "cloud" {
			t.Errorf("awscli category = %q, want cloud", e.Category)
		}
	}
}

// redirectTransport rewrites any request matching `host` to the test server.
type redirectTransport struct {
	target string
	host   string
}

func (rt *redirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if strings.Contains(req.URL.Host, rt.host) {
		newURL := rt.target + req.URL.Path
		req2, _ := http.NewRequestWithContext(req.Context(), req.Method, newURL, nil)
		req2.Header = req.Header
		return http.DefaultTransport.RoundTrip(req2)
	}
	return http.DefaultTransport.RoundTrip(req)
}

func TestParseGitHubRepo(t *testing.T) {
	cases := []struct {
		in        string
		wantOwner string
		wantRepo  string
		wantOK    bool
	}{
		{"https://github.com/devcontainers/features", "devcontainers", "features", true},
		{"github.com/foo/bar.git", "foo", "bar", true},
		{"https://github.com/foo/bar/", "foo", "bar", true},
		{"invalid", "", "", false},
	}
	for _, c := range cases {
		o, r, ok := parseGitHubRepo(c.in)
		if ok != c.wantOK || o != c.wantOwner || r != c.wantRepo {
			t.Errorf("parseGitHubRepo(%q) = (%q, %q, %v), want (%q, %q, %v)",
				c.in, o, r, ok, c.wantOwner, c.wantRepo, c.wantOK)
		}
	}
}

func TestMajorVersion(t *testing.T) {
	cases := map[string]string{
		"1.8.0": "1",
		"2.0":   "2",
		"3":     "3",
		"":      "1",
	}
	for in, want := range cases {
		if got := majorVersion(in); got != want {
			t.Errorf("majorVersion(%q) = %q, want %q", in, got, want)
		}
	}
}
