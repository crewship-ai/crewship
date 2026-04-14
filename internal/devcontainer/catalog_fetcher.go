package devcontainer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// CollectionEntry describes one devcontainer feature collection as listed in
// the upstream collection-index.yml.
type CollectionEntry struct {
	Name         string `yaml:"name" json:"name"`
	Maintainer   string `yaml:"maintainer" json:"maintainer"`
	Contact      string `yaml:"contact" json:"contact"`
	Repository   string `yaml:"repository" json:"repository"`
	OCIReference string `yaml:"ociReference" json:"ociReference"`
}

// CatalogFetcher fetches and caches the devcontainer feature catalog with a
// three-tier cache: memory (6h), disk (24h), and an embedded fallback.
type CatalogFetcher struct {
	httpClient *http.Client
	cacheDir   string
	logger     *slog.Logger

	mu       sync.RWMutex
	memCache *catalogMemCache
}

type catalogMemCache struct {
	entries   []CatalogEntry
	fetchedAt time.Time
}

type diskCacheFile struct {
	FetchedAt time.Time      `json:"fetched_at"`
	Entries   []CatalogEntry `json:"entries"`
}

const (
	catalogMemTTL      = 6 * time.Hour
	catalogDiskTTL     = 24 * time.Hour
	catalogFetchTO     = 30 * time.Second
	catalogPerItemTO   = 5 * time.Second
	catalogWorkers     = 10
	collectionIndexURL = "https://raw.githubusercontent.com/devcontainers/devcontainers.github.io/gh-pages/_data/collection-index.yml"
	userAgent          = "crewship/1.0"

	featureCatalogFile = "feature-catalog.json"
)

// NewCatalogFetcher constructs a fetcher. cacheDir is created on first write.
func NewCatalogFetcher(cacheDir string, logger *slog.Logger) *CatalogFetcher {
	if logger == nil {
		logger = slog.Default()
	}
	return &CatalogFetcher{
		httpClient: &http.Client{Timeout: catalogFetchTO},
		cacheDir:   cacheDir,
		logger:     logger,
	}
}

// GetCatalog returns the current feature catalog. Memory cache first, then
// disk cache, then the embedded fallback. It never triggers a network fetch;
// call RefreshCatalog (typically at startup or from a scheduler) to refresh.
func (f *CatalogFetcher) GetCatalog(ctx context.Context) []CatalogEntry {
	f.mu.RLock()
	if f.memCache != nil && time.Since(f.memCache.fetchedAt) < catalogMemTTL {
		entries := f.memCache.entries
		f.mu.RUnlock()
		return entries
	}
	f.mu.RUnlock()

	// Try disk cache.
	if entries, fetchedAt, err := f.readDiskCache(); err == nil {
		if time.Since(fetchedAt) < catalogDiskTTL {
			f.mu.Lock()
			f.memCache = &catalogMemCache{entries: entries, fetchedAt: fetchedAt}
			f.mu.Unlock()
			return entries
		}
	}

	// Fallback to embedded list.
	out := make([]CatalogEntry, len(FallbackCatalog))
	copy(out, FallbackCatalog)
	return out
}

// RefreshCatalog forces a network fetch and updates both caches.
// If the fetch fails, the existing caches are left untouched.
func (f *CatalogFetcher) RefreshCatalog(ctx context.Context) error {
	entries, err := f.fetchUpstream(ctx)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return errors.New("upstream fetch returned zero entries")
	}

	now := time.Now()
	f.mu.Lock()
	f.memCache = &catalogMemCache{entries: entries, fetchedAt: now}
	f.mu.Unlock()

	if err := f.writeDiskCache(entries, now); err != nil {
		f.logger.Warn("write feature catalog disk cache", "error", err)
	}
	f.logger.Info("feature catalog refreshed", "entries", len(entries))
	return nil
}

// fetchUpstream downloads the collection index and builds a flat list of
// catalog entries by pulling each collection's devcontainer-collection.json.
func (f *CatalogFetcher) fetchUpstream(ctx context.Context) ([]CatalogEntry, error) {
	ctx, cancel := context.WithTimeout(ctx, catalogFetchTO)
	defer cancel()

	collections, err := f.fetchCollectionIndex(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetch collection index: %w", err)
	}
	if len(collections) == 0 {
		return nil, errors.New("empty collection index")
	}

	// Fan-out with bounded concurrency.
	type result struct {
		entries []CatalogEntry
		err     error
		name    string
	}

	jobs := make(chan CollectionEntry)
	results := make(chan result)

	var wg sync.WaitGroup
	for i := 0; i < catalogWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for c := range jobs {
				entries, err := f.fetchCollectionFeatures(ctx, c)
				results <- result{entries: entries, err: err, name: c.Name}
			}
		}()
	}

	go func() {
		for _, c := range collections {
			select {
			case <-ctx.Done():
				close(jobs)
				return
			case jobs <- c:
			}
		}
		close(jobs)
	}()

	go func() {
		wg.Wait()
		close(results)
	}()

	var out []CatalogEntry
	for r := range results {
		if r.err != nil {
			f.logger.Debug("collection fetch failed", "collection", r.name, "error", r.err)
			continue
		}
		out = append(out, r.entries...)
	}

	// Deduplicate by Ref (in case multiple collections share features).
	seen := make(map[string]bool, len(out))
	uniq := out[:0]
	for _, e := range out {
		if seen[e.Ref] {
			continue
		}
		seen[e.Ref] = true
		uniq = append(uniq, e)
	}
	return uniq, nil
}

// fetchCollectionIndex downloads and parses collection-index.yml.
func (f *CatalogFetcher) fetchCollectionIndex(ctx context.Context) ([]CollectionEntry, error) {
	body, err := f.httpGet(ctx, collectionIndexURL)
	if err != nil {
		return nil, err
	}
	// The YAML file is a top-level list, or a map with a `collections` key —
	// accept both forms defensively.
	var direct []CollectionEntry
	if err := yaml.Unmarshal(body, &direct); err == nil && len(direct) > 0 {
		return direct, nil
	}
	var wrapper struct {
		Collections []CollectionEntry `yaml:"collections"`
	}
	if err := yaml.Unmarshal(body, &wrapper); err != nil {
		return nil, fmt.Errorf("parse collection index yaml: %w", err)
	}
	return wrapper.Collections, nil
}

// collectionManifest mirrors the devcontainer-collection.json format.
type collectionManifest struct {
	Features []struct {
		ID          string `json:"id"`
		Version     string `json:"version"`
		Name        string `json:"name"`
		Description string `json:"description"`
	} `json:"features"`
}

// fetchCollectionFeatures fetches devcontainer-collection.json for a single
// collection via raw.githubusercontent.com and maps each feature to a
// CatalogEntry.
func (f *CatalogFetcher) fetchCollectionFeatures(ctx context.Context, c CollectionEntry) ([]CatalogEntry, error) {
	owner, repo, ok := parseGitHubRepo(c.Repository)
	if !ok {
		return nil, fmt.Errorf("unparseable repository %q", c.Repository)
	}

	itemCtx, cancel := context.WithTimeout(ctx, catalogPerItemTO)
	defer cancel()

	var manifestBytes []byte
	var lastErr error
	for _, branch := range []string{"main", "master"} {
		url := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s/devcontainer-collection.json", owner, repo, branch)
		body, err := f.httpGet(itemCtx, url)
		if err == nil {
			manifestBytes = body
			break
		}
		lastErr = err
	}
	if manifestBytes == nil {
		return nil, fmt.Errorf("fetch manifest: %w", lastErr)
	}

	var manifest collectionManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}

	ociBase := strings.TrimSuffix(c.OCIReference, "/")
	if ociBase == "" {
		return nil, errors.New("missing OCI reference")
	}

	out := make([]CatalogEntry, 0, len(manifest.Features))
	for _, feat := range manifest.Features {
		if feat.ID == "" {
			continue
		}
		majorVer := majorVersion(feat.Version)
		ref := fmt.Sprintf("%s/%s:%s", ociBase, feat.ID, majorVer)

		name := feat.Name
		if name == "" {
			name = feat.ID
		}
		category := inferFeatureCategory(feat.ID, feat.Name, feat.Description)
		icon := featureIcon(feat.ID, category)

		out = append(out, CatalogEntry{
			Ref:         ref,
			Name:        name,
			Description: feat.Description,
			Category:    category,
			Icon:        icon,
			SizeHint:    "",
		})
	}
	return out, nil
}

// httpGet issues a GET with the configured client and returns the body for
// 2xx responses.
func (f *CatalogFetcher) httpGet(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json, text/yaml, text/plain, */*")
	resp, err := f.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("http %d for %s", resp.StatusCode, url)
	}
	// Cap body to 10 MB.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return nil, err
	}
	return body, nil
}

// readDiskCache reads the on-disk catalog file.
func (f *CatalogFetcher) readDiskCache() ([]CatalogEntry, time.Time, error) {
	if f.cacheDir == "" {
		return nil, time.Time{}, errors.New("no cache dir configured")
	}
	data, err := os.ReadFile(filepath.Join(f.cacheDir, featureCatalogFile))
	if err != nil {
		return nil, time.Time{}, err
	}
	var f1 diskCacheFile
	if err := json.Unmarshal(data, &f1); err != nil {
		return nil, time.Time{}, err
	}
	return f1.Entries, f1.FetchedAt, nil
}

// writeDiskCache writes the catalog to disk atomically.
func (f *CatalogFetcher) writeDiskCache(entries []CatalogEntry, fetchedAt time.Time) error {
	if f.cacheDir == "" {
		return nil
	}
	if err := os.MkdirAll(f.cacheDir, 0o755); err != nil {
		return err
	}
	payload := diskCacheFile{FetchedAt: fetchedAt, Entries: entries}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	dst := filepath.Join(f.cacheDir, featureCatalogFile)
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// --- helpers ---------------------------------------------------------------

// parseGitHubRepo extracts owner/repo from a GitHub URL.
func parseGitHubRepo(raw string) (owner, repo string, ok bool) {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	s = strings.TrimPrefix(s, "github.com/")
	s = strings.TrimSuffix(s, ".git")
	s = strings.TrimSuffix(s, "/")
	parts := strings.SplitN(s, "/", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// majorVersion returns the first segment of a semver-like version.
func majorVersion(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return "1"
	}
	if i := strings.Index(v, "."); i > 0 {
		return v[:i]
	}
	return v
}

// inferFeatureCategory guesses a category from feature id/name/description.
func inferFeatureCategory(id, name, desc string) string {
	s := strings.ToLower(id + " " + name + " " + desc)
	languages := []string{"python", "node", "golang", " go ", "rust", "ruby", "java", "php", "deno", "bun", "dotnet", ".net", "kotlin", "scala", "elixir", "erlang", "swift", "zig", "crystal", "haskell", "ocaml", "dart", "flutter", "lua"}
	for _, k := range languages {
		if strings.Contains(s, k) {
			return "languages"
		}
	}
	databases := []string{"postgres", "mysql", "mariadb", "redis", "mongodb", "sqlite", "cassandra", "clickhouse"}
	for _, k := range databases {
		if strings.Contains(s, k) {
			return "databases"
		}
	}
	cloud := []string{"aws", "azure", "gcloud", "gcp", "terraform", "pulumi", "kubectl", "helm", "kubernetes", "doctl", "flyctl", "cloudflare"}
	for _, k := range cloud {
		if strings.Contains(s, k) {
			return "cloud"
		}
	}
	return "tools"
}

// featureIcon returns a lucide icon hint for the given feature id/category.
func featureIcon(id, category string) string {
	id = strings.ToLower(id)
	switch {
	case strings.Contains(id, "node"):
		return "hexagon"
	case strings.Contains(id, "python"):
		return "code"
	case strings.Contains(id, "go"):
		return "arrow-right"
	case strings.Contains(id, "rust"):
		return "cog"
	case strings.Contains(id, "docker"):
		return "container"
	case strings.Contains(id, "kubectl") || strings.Contains(id, "helm"):
		return "ship"
	case strings.Contains(id, "github"):
		return "github"
	case strings.Contains(id, "terraform") || strings.Contains(id, "pulumi"):
		return "blocks"
	}
	switch category {
	case "languages":
		return "code"
	case "databases":
		return "database"
	case "cloud":
		return "cloud"
	default:
		return "wrench"
	}
}
