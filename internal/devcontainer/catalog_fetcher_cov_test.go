package devcontainer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewCatalogFetcher_NilLoggerDefaults(t *testing.T) {
	t.Parallel()

	f := NewCatalogFetcher(t.TempDir(), nil)
	if f.logger == nil {
		t.Fatal("nil logger must be replaced with slog.Default()")
	}
	if f.httpClient == nil || f.httpClient.Timeout != catalogFetchTO {
		t.Errorf("httpClient not configured with fetch timeout: %+v", f.httpClient)
	}
}

func TestWriteDiskCache_NoCacheDirIsNoop(t *testing.T) {
	t.Parallel()

	f := NewCatalogFetcher("", newTestLogger())
	if err := f.writeDiskCache([]CatalogEntry{{Ref: "ghcr.io/x/y:1"}}, time.Now()); err != nil {
		t.Fatalf("writeDiskCache with empty cacheDir should be a no-op, got %v", err)
	}
	if _, _, err := f.readDiskCache(); err == nil {
		t.Error("readDiskCache with empty cacheDir must error")
	}
}

func TestWriteDiskCache_MkdirError(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	blocker := filepath.Join(base, "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// cacheDir nests under a regular file → MkdirAll must fail.
	f := NewCatalogFetcher(filepath.Join(blocker, "sub"), newTestLogger())
	err := f.writeDiskCache([]CatalogEntry{{Ref: "ghcr.io/x/y:1"}}, time.Now())
	if err == nil || !strings.Contains(err.Error(), "creating cache dir") {
		t.Errorf("expected creating-cache-dir error, got %v", err)
	}
}

func TestWriteDiskCache_CreateTempError(t *testing.T) {
	t.Parallel()
	if os.Getuid() == 0 {
		t.Skip("permission checks are bypassed for root")
	}

	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil { // read+exec, no write
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	f := NewCatalogFetcher(dir, newTestLogger())
	err := f.writeDiskCache([]CatalogEntry{{Ref: "ghcr.io/x/y:1"}}, time.Now())
	if err == nil || !strings.Contains(err.Error(), "creating catalog cache tmp") {
		t.Errorf("expected tmp-file creation error, got %v", err)
	}
}

func TestWriteDiskCache_RenameError(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	// Make the destination path an existing non-empty directory: the final
	// os.Rename cannot replace it and must fail.
	dst := filepath.Join(dir, featureCatalogFile)
	if err := os.MkdirAll(filepath.Join(dst, "occupied"), 0o755); err != nil {
		t.Fatal(err)
	}
	f := NewCatalogFetcher(dir, newTestLogger())
	err := f.writeDiskCache([]CatalogEntry{{Ref: "ghcr.io/x/y:1"}}, time.Now())
	if err == nil || !strings.Contains(err.Error(), "renaming catalog cache") {
		t.Errorf("expected rename error, got %v", err)
	}
	// The temp file must have been cleaned up.
	files, _ := os.ReadDir(dir)
	for _, fi := range files {
		if strings.Contains(fi.Name(), ".tmp") {
			t.Errorf("leftover temp file %s after failed rename", fi.Name())
		}
	}
}

func TestWriteDiskCache_ReadDiskCache_RoundTrip(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	f := NewCatalogFetcher(dir, newTestLogger())
	entries := []CatalogEntry{
		{Ref: "ghcr.io/x/python:1", Name: "Python", Category: "languages", Icon: "code"},
		{Ref: "ghcr.io/x/terraform:1", Name: "Terraform", Category: "cloud", Icon: "cloud"},
	}
	fetchedAt := time.Now().Add(-30 * time.Minute)
	if err := f.writeDiskCache(entries, fetchedAt); err != nil {
		t.Fatalf("writeDiskCache: %v", err)
	}

	got, gotAt, err := f.readDiskCache()
	if err != nil {
		t.Fatalf("readDiskCache: %v", err)
	}
	if len(got) != 2 || got[0].Ref != entries[0].Ref || got[1].Name != "Terraform" {
		t.Errorf("entries round trip mismatch: %+v", got)
	}
	if !gotAt.Equal(fetchedAt) {
		t.Errorf("fetchedAt = %v, want %v", gotAt, fetchedAt)
	}
	// No stray tmp files left behind.
	files, _ := os.ReadDir(dir)
	for _, fi := range files {
		if strings.Contains(fi.Name(), ".tmp") {
			t.Errorf("leftover temp file %s", fi.Name())
		}
	}
}

func TestReadDiskCache_InvalidJSON(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, featureCatalogFile), []byte("{garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := NewCatalogFetcher(dir, newTestLogger())
	_, _, err := f.readDiskCache()
	if err == nil || !strings.Contains(err.Error(), "unmarshaling catalog cache") {
		t.Errorf("expected unmarshal error, got %v", err)
	}
}

func TestGetCatalog_ExpiredDiskCacheFallsBack(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	f := NewCatalogFetcher(dir, newTestLogger())
	stale := time.Now().Add(-(catalogDiskTTL + time.Hour))
	if err := f.writeDiskCache([]CatalogEntry{{Ref: "ghcr.io/x/old:1", Name: "Old"}}, stale); err != nil {
		t.Fatal(err)
	}

	got := f.GetCatalog(context.Background())
	if len(got) != len(FallbackCatalog) {
		t.Fatalf("expected embedded fallback (%d entries), got %d", len(FallbackCatalog), len(got))
	}
	for _, e := range got {
		if e.Ref == "ghcr.io/x/old:1" {
			t.Error("stale disk cache entry leaked into result")
		}
	}
}

func TestRefreshCatalog_ZeroEntriesError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html>no feature refs in here</html>"))
	}))
	defer srv.Close()

	origURL := catalogURL
	catalogURL = srv.URL
	defer func() { catalogURL = origURL }()

	f := NewCatalogFetcher(t.TempDir(), newTestLogger())
	err := f.RefreshCatalog(context.Background())
	if err == nil || !strings.Contains(err.Error(), "zero entries") {
		t.Errorf("expected zero-entries error, got %v", err)
	}
	if f.memCache != nil {
		t.Error("failed refresh must not populate the memory cache")
	}
}

func TestRefreshCatalog_SucceedsEvenWhenDiskWriteFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<code>ghcr.io/devcontainers/features/python:1</code>`))
	}))
	defer srv.Close()

	origURL := catalogURL
	catalogURL = srv.URL
	defer func() { catalogURL = origURL }()

	base := t.TempDir()
	blocker := filepath.Join(base, "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := NewCatalogFetcher(filepath.Join(blocker, "sub"), newTestLogger())
	if err := f.RefreshCatalog(context.Background()); err != nil {
		t.Fatalf("RefreshCatalog must tolerate disk-cache write failure, got %v", err)
	}
	got := f.GetCatalog(context.Background())
	if len(got) != 1 || got[0].Ref != "ghcr.io/devcontainers/features/python:1" {
		t.Errorf("memory cache should hold the fetched entry, got %+v", got)
	}
}

func TestFetchUpstream_BadURL(t *testing.T) {
	origURL := catalogURL
	catalogURL = "://not-a-url"
	defer func() { catalogURL = origURL }()

	f := NewCatalogFetcher(t.TempDir(), newTestLogger())
	if _, err := f.fetchUpstream(context.Background()); err == nil {
		t.Error("expected request-construction error for malformed URL")
	}
}

func TestExtractFeaturesFromHTML_Dedupes(t *testing.T) {
	t.Parallel()

	body := []byte(`<code>ghcr.io/devcontainers/features/python:1</code>
<code>ghcr.io/devcontainers/features/python:1</code>
<code>ghcr.io/devcontainers/features/node:2</code>`)
	entries := extractFeaturesFromHTML(body)
	if len(entries) != 2 {
		t.Fatalf("expected 2 deduped entries, got %d: %+v", len(entries), entries)
	}
	if entries[0].Ref != "ghcr.io/devcontainers/features/python:1" {
		t.Errorf("first entry = %q", entries[0].Ref)
	}
}

func TestIconForCategory_AllCategories(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"languages": "code",
		"cloud":     "cloud",
		"databases": "database",
		"tools":     "wrench",
		"":          "wrench",
	}
	for cat, want := range tests {
		if got := iconForCategory(cat); got != want {
			t.Errorf("iconForCategory(%q) = %q, want %q", cat, got, want)
		}
	}
}

func TestBuildCatalogEntryFromRef_CategoryAndIcon(t *testing.T) {
	t.Parallel()

	e := buildCatalogEntryFromRef("ghcr.io/devcontainers/features/terraform:1")
	if e.Category != "cloud" {
		t.Errorf("category = %q, want cloud", e.Category)
	}
	if e.Icon != "cloud" {
		t.Errorf("icon = %q, want cloud", e.Icon)
	}
	if e.Name != "Terraform" {
		t.Errorf("name = %q, want Terraform", e.Name)
	}

	db := buildCatalogEntryFromRef("ghcr.io/itsmechlark/features/postgres:1")
	if db.Category != "databases" || db.Icon != "database" || db.Name != "PostgreSQL" {
		t.Errorf("postgres entry = %+v", db)
	}
}
