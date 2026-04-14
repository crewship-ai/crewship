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
	"testing"
	"time"
)

// newTestLogger returns a silent logger for tests.
func newTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestCatalogFetcher_UpstreamFetch_FromHTML(t *testing.T) {
	html := `<html><body>
<table>
<tr><td><code>ghcr.io/devcontainers/features/python:1</code></td></tr>
<tr><td><code>ghcr.io/devcontainers/features/node:1</code></td></tr>
<tr><td><code>ghcr.io/devcontainers-contrib/features/bun:1</code></td></tr>
<tr><td><code>ghcr.io/iterative/features/dvc:1</code></td></tr>
</table>
</body></html>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(html))
	}))
	defer srv.Close()

	origURL := catalogURL
	catalogURL = srv.URL
	defer func() { catalogURL = origURL }()

	tmpDir := t.TempDir()
	f := NewCatalogFetcher(tmpDir, newTestLogger())

	entries, err := f.fetchUpstream(context.Background())
	if err != nil {
		t.Fatalf("fetchUpstream: %v", err)
	}
	if len(entries) != 4 {
		t.Errorf("expected 4 entries, got %d", len(entries))
	}

	var pythonEntry *CatalogEntry
	for i := range entries {
		if strings.Contains(entries[i].Ref, "python") {
			pythonEntry = &entries[i]
			break
		}
	}
	if pythonEntry == nil {
		t.Fatal("python entry not found")
	}
	if pythonEntry.Category != "languages" {
		t.Errorf("expected languages category, got %s", pythonEntry.Category)
	}
	if pythonEntry.Name != "Python" {
		t.Errorf("expected name Python, got %s", pythonEntry.Name)
	}
}

func TestCatalogFetcher_UpstreamFail_Fallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	origURL := catalogURL
	catalogURL = srv.URL
	defer func() { catalogURL = origURL }()

	tmpDir := t.TempDir()
	f := NewCatalogFetcher(tmpDir, newTestLogger())

	_, err := f.fetchUpstream(context.Background())
	if err == nil {
		t.Error("expected error on 500 response")
	}

	entries := f.GetCatalog(context.Background())
	if len(entries) == 0 {
		t.Error("expected fallback entries, got 0")
	}
}

func TestCatalogFetcher_RefreshWritesDiskCache(t *testing.T) {
	html := `<code>ghcr.io/devcontainers/features/python:1</code>
<code>ghcr.io/devcontainers/features/node:1</code>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(html))
	}))
	defer srv.Close()

	origURL := catalogURL
	catalogURL = srv.URL
	defer func() { catalogURL = origURL }()

	dir := t.TempDir()
	f := NewCatalogFetcher(dir, newTestLogger())
	if err := f.RefreshCatalog(context.Background()); err != nil {
		t.Fatalf("RefreshCatalog: %v", err)
	}
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
	f := NewCatalogFetcher(t.TempDir(), newTestLogger())
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

func TestPrettyName(t *testing.T) {
	tests := map[string]string{
		"python":                "Python",
		"aws-cli":               "AWS CLI",
		"github-cli":            "GitHub CLI",
		"docker-in-docker":      "Docker in Docker",
		"kubectl-helm-minikube": "Kubectl, Helm & Minikube",
		"nvidia-cuda":           "NVIDIA CUDA",
		"node":                  "Node.js",
		"postgres":              "PostgreSQL",
	}
	for id, want := range tests {
		if got := prettyName(id); got != want {
			t.Errorf("prettyName(%q) = %q, want %q", id, got, want)
		}
	}
}

func TestInferCategory(t *testing.T) {
	tests := map[string]string{
		"ghcr.io/devcontainers/features/python":            "languages",
		"ghcr.io/devcontainers/features/node":              "languages",
		"ghcr.io/devcontainers/features/aws-cli":           "cloud",
		"ghcr.io/devcontainers/features/terraform":         "cloud",
		"ghcr.io/devcontainers-contrib/features/dragonfly": "databases",
		"ghcr.io/devcontainers/features/github-cli":        "tools",
		"ghcr.io/devcontainers/features/common-utils":      "tools",
	}
	for path, want := range tests {
		if got := inferCategory(path); got != want {
			t.Errorf("inferCategory(%q) = %q, want %q", path, got, want)
		}
	}
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
		Timeout:   5 * time.Second,
		Transport: &redirectTransport{target: srv.URL, host: "api.github.com"},
	}

	if err := f.RefreshRuntimes(context.Background()); err != nil {
		t.Fatalf("RefreshRuntimes: %v", err)
	}
	got := f.GetRuntimes(context.Background())
	if len(got) == 0 {
		t.Fatal("expected entries")
	}
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
