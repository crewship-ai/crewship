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
	"regexp"
	"strings"
	"sync"
	"time"
)

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
	catalogMemTTL  = 6 * time.Hour
	catalogDiskTTL = 24 * time.Hour
	catalogFetchTO = 30 * time.Second
	userAgent      = "crewship/1.0"

	featureCatalogFile = "feature-catalog.json"
)

// catalogURL is the upstream HTML page listing published devcontainer
// features. Declared as a var so tests can override it.
var catalogURL = "https://containers.dev/features"

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

// fetchUpstream scrapes the containers.dev/features HTML page and extracts
// OCI refs via regex. The upstream devcontainer-collection.json files are OCI
// artifacts (not git-tracked), so scraping the aggregated HTML page is the
// only stable public source.
func (f *CatalogFetcher) fetchUpstream(ctx context.Context) ([]CatalogEntry, error) {
	ctx, cancel := context.WithTimeout(ctx, catalogFetchTO)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, catalogURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html")

	resp, err := f.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", catalogURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("upstream returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	return extractFeaturesFromHTML(body), nil
}

var featureRefRegex = regexp.MustCompile(`ghcr\.io/[a-zA-Z0-9][a-zA-Z0-9/_.-]*:[0-9]+`)

func extractFeaturesFromHTML(body []byte) []CatalogEntry {
	matches := featureRefRegex.FindAllString(string(body), -1)
	seen := make(map[string]bool, len(matches))
	entries := make([]CatalogEntry, 0, len(matches))
	for _, ref := range matches {
		if seen[ref] {
			continue
		}
		seen[ref] = true
		entries = append(entries, buildCatalogEntryFromRef(ref))
	}
	return entries
}

func buildCatalogEntryFromRef(ref string) CatalogEntry {
	// ghcr.io/devcontainers/features/python:1
	withoutTag := strings.SplitN(ref, ":", 2)[0]
	pathParts := strings.Split(withoutTag, "/")
	id := pathParts[len(pathParts)-1]

	category := inferCategory(withoutTag)
	return CatalogEntry{
		Ref:         ref,
		Name:        prettyName(id),
		Description: "",
		Category:    category,
		Icon:        iconForCategory(category),
		SizeHint:    "",
	}
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

// prettyName converts "aws-cli" -> "AWS CLI", "python" -> "Python", etc.
func prettyName(id string) string {
	specials := map[string]string{
		"aws-cli":                  "AWS CLI",
		"azure-cli":                "Azure CLI",
		"github-cli":               "GitHub CLI",
		"gitlab":                   "GitLab",
		"google-cloud-cli":         "Google Cloud CLI",
		"kubectl-helm-minikube":    "Kubectl, Helm & Minikube",
		"docker-in-docker":         "Docker in Docker",
		"docker-outside-of-docker": "Docker Outside of Docker",
		"node":                     "Node.js",
		"nodejs":                   "Node.js",
		"dotnet":                   ".NET",
		"postgres":                 "PostgreSQL",
		"postgresql":               "PostgreSQL",
		"php":                      "PHP",
		"sshd":                     "SSH Server",
		"oryx":                     "Oryx",
		"nvidia-cuda":              "NVIDIA CUDA",
		"git-lfs":                  "Git LFS",
	}
	if v, ok := specials[id]; ok {
		return v
	}
	parts := strings.FieldsFunc(id, func(r rune) bool {
		return r == '-' || r == '_'
	})
	for i, p := range parts {
		if len(p) > 0 {
			parts[i] = strings.ToUpper(p[:1]) + strings.ToLower(p[1:])
		}
	}
	return strings.Join(parts, " ")
}

// inferCategory categorizes based on the ref path.
func inferCategory(path string) string {
	lower := strings.ToLower(path)

	langPatterns := []string{
		"python", "pypy", "anaconda", "conda", "node", "deno", "bun",
		"go-", "/go:", "golang", "rust", "ruby", "java", "kotlin", "scala",
		"groovy", "clojure", "dotnet", "csharp", "powershell", "/php", "perl",
		"lua", "/r:", "julia", "elixir", "erlang", "ocaml", "haskell", "swift",
		"zig", "crystal", "nim", "dart", "gleam", "v-lang", "hugo",
	}
	for _, p := range langPatterns {
		if strings.Contains(lower, p) {
			return "languages"
		}
	}

	dbPatterns := []string{
		"postgres", "pgcli", "mysql", "mariadb", "redis", "mongo", "cassandra",
		"dragonfly", "dynamodb", "couchdb", "elasticsearch", "meilisearch",
		"duckdb", "sqlite", "cockroach", "surrealdb", "clickhouse", "influxdb",
		"neo4j", "scylla", "minio",
	}
	for _, p := range dbPatterns {
		if strings.Contains(lower, p) {
			return "databases"
		}
	}

	cloudPatterns := []string{
		"aws", "azure", "gcp", "gcloud", "google-cloud", "alibaba", "oci-cli",
		"digitalocean", "do-cli", "pulumi", "terraform", "opentofu", "packer",
		"vault", "consul", "nomad", "ansible", "cloudflare", "fly", "heroku",
		"vercel", "render-cli", "openstack",
	}
	for _, p := range cloudPatterns {
		if strings.Contains(lower, p) {
			return "cloud"
		}
	}

	return "tools"
}

func iconForCategory(cat string) string {
	switch cat {
	case "languages":
		return "code"
	case "cloud":
		return "cloud"
	case "databases":
		return "database"
	default:
		return "wrench"
	}
}
