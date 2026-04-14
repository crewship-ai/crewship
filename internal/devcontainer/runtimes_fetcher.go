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
)

// RuntimeFetcher fetches and caches the mise runtime/tool catalog.
type RuntimeFetcher struct {
	httpClient *http.Client
	cacheDir   string
	logger     *slog.Logger

	mu       sync.RWMutex
	memCache *runtimeMemCache
}

type runtimeMemCache struct {
	entries   []RuntimeCatalogEntry
	fetchedAt time.Time
}

type runtimeDiskCache struct {
	FetchedAt time.Time             `json:"fetched_at"`
	Entries   []RuntimeCatalogEntry `json:"entries"`
}

const (
	miseRegistryListURL = "https://api.github.com/repos/jdx/mise/contents/registry"

	runtimeCatalogFile = "runtime-catalog.json"
)

// NewRuntimeFetcher constructs a fetcher.
func NewRuntimeFetcher(cacheDir string, logger *slog.Logger) *RuntimeFetcher {
	if logger == nil {
		logger = slog.Default()
	}
	return &RuntimeFetcher{
		httpClient: &http.Client{Timeout: catalogFetchTO},
		cacheDir:   cacheDir,
		logger:     logger,
	}
}

// GetRuntimes returns the runtime catalog. Memory cache first, then disk,
// then the embedded fallback. Does not trigger a network fetch.
func (f *RuntimeFetcher) GetRuntimes(ctx context.Context) []RuntimeCatalogEntry {
	f.mu.RLock()
	if f.memCache != nil && time.Since(f.memCache.fetchedAt) < catalogMemTTL {
		entries := f.memCache.entries
		f.mu.RUnlock()
		return entries
	}
	f.mu.RUnlock()

	if entries, fetchedAt, err := f.readDiskCache(); err == nil {
		if time.Since(fetchedAt) < catalogDiskTTL {
			f.mu.Lock()
			f.memCache = &runtimeMemCache{entries: entries, fetchedAt: fetchedAt}
			f.mu.Unlock()
			return entries
		}
	}

	out := make([]RuntimeCatalogEntry, len(FallbackRuntimeCatalog))
	copy(out, FallbackRuntimeCatalog)
	return out
}

// RefreshRuntimes forces a network fetch and updates both caches.
func (f *RuntimeFetcher) RefreshRuntimes(ctx context.Context) error {
	entries, err := f.fetchUpstream(ctx)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return errors.New("upstream fetch returned zero entries")
	}

	// Merge: prefer our curated fallback metadata (versions/icons/descriptions)
	// where we have it, otherwise keep the bare upstream entry.
	merged := mergeRuntimeEntries(entries, FallbackRuntimeCatalog)

	now := time.Now()
	f.mu.Lock()
	f.memCache = &runtimeMemCache{entries: merged, fetchedAt: now}
	f.mu.Unlock()

	if err := f.writeDiskCache(merged, now); err != nil {
		f.logger.Warn("write runtime catalog disk cache", "error", err)
	}
	f.logger.Info("runtime catalog refreshed", "entries", len(merged))
	return nil
}

// githubContentItem is the JSON shape returned by the GitHub contents API.
type githubContentItem struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Type string `json:"type"`
}

func (f *RuntimeFetcher) fetchUpstream(ctx context.Context) ([]RuntimeCatalogEntry, error) {
	ctx, cancel := context.WithTimeout(ctx, catalogFetchTO)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, miseRegistryListURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/vnd.github+json")
	if tok := os.Getenv("GITHUB_TOKEN"); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := f.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("http %d for mise registry list", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
	if err != nil {
		return nil, err
	}

	var items []githubContentItem
	if err := json.Unmarshal(body, &items); err != nil {
		return nil, fmt.Errorf("parse registry list: %w", err)
	}

	out := make([]RuntimeCatalogEntry, 0, len(items))
	for _, it := range items {
		if it.Type != "file" || !strings.HasSuffix(it.Name, ".toml") {
			continue
		}
		tool := strings.TrimSuffix(it.Name, ".toml")
		if tool == "" {
			continue
		}
		cat := CategorizeRuntime(tool)
		out = append(out, RuntimeCatalogEntry{
			Name:     prettifyToolName(tool),
			Tool:     tool,
			Category: cat,
			Icon:     RuntimeIcon(tool, cat),
		})
	}
	return out, nil
}

// mergeRuntimeEntries overlays fallback metadata onto entries with the same
// Tool key, so the UI gets versions/descriptions we know about without losing
// the 900+ upstream tool names.
func mergeRuntimeEntries(upstream, fallback []RuntimeCatalogEntry) []RuntimeCatalogEntry {
	byTool := make(map[string]RuntimeCatalogEntry, len(upstream))
	for _, e := range upstream {
		byTool[e.Tool] = e
	}
	for _, fb := range fallback {
		cur, ok := byTool[fb.Tool]
		if !ok {
			// Upstream doesn't know this tool — include the fallback entry
			// anyway so the UI still offers it.
			byTool[fb.Tool] = fb
			continue
		}
		if cur.Name == "" || cur.Name == prettifyToolName(cur.Tool) {
			cur.Name = fb.Name
		}
		if cur.Description == "" {
			cur.Description = fb.Description
		}
		if cur.Icon == "" {
			cur.Icon = fb.Icon
		}
		if len(cur.Versions) == 0 {
			cur.Versions = fb.Versions
		}
		if cur.DefaultVersion == "" {
			cur.DefaultVersion = fb.DefaultVersion
		}
		if len(cur.Backends) == 0 {
			cur.Backends = fb.Backends
		}
		byTool[fb.Tool] = cur
	}

	out := make([]RuntimeCatalogEntry, 0, len(byTool))
	for _, e := range byTool {
		out = append(out, e)
	}
	return out
}

// prettifyToolName turns "aws-cli" into "Aws Cli". The merge step replaces
// this with curated names for known tools.
func prettifyToolName(tool string) string {
	parts := strings.FieldsFunc(tool, func(r rune) bool {
		return r == '-' || r == '_'
	})
	for i, p := range parts {
		if p == "" {
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, " ")
}

func (f *RuntimeFetcher) readDiskCache() ([]RuntimeCatalogEntry, time.Time, error) {
	if f.cacheDir == "" {
		return nil, time.Time{}, errors.New("no cache dir configured")
	}
	data, err := os.ReadFile(filepath.Join(f.cacheDir, runtimeCatalogFile))
	if err != nil {
		return nil, time.Time{}, err
	}
	var f1 runtimeDiskCache
	if err := json.Unmarshal(data, &f1); err != nil {
		return nil, time.Time{}, err
	}
	return f1.Entries, f1.FetchedAt, nil
}

func (f *RuntimeFetcher) writeDiskCache(entries []RuntimeCatalogEntry, fetchedAt time.Time) error {
	if f.cacheDir == "" {
		return nil
	}
	if err := os.MkdirAll(f.cacheDir, 0o755); err != nil {
		return err
	}
	payload := runtimeDiskCache{FetchedAt: fetchedAt, Entries: entries}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	dst := filepath.Join(f.cacheDir, runtimeCatalogFile)
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}
