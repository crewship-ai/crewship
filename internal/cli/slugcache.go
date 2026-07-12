package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// Workspace slug→CUID disk cache.
//
// When -w/--workspace (or CREWSHIP_WORKSPACE / the config workspace) is a
// human slug rather than a CUID, the client fires a GET /api/v1/workspaces
// preflight to translate it. The in-process cache on Client never amortizes
// that cost: a typical CLI invocation makes exactly one real request, so
// every slug-configured command paid two round-trips instead of one.
//
// This cache persists the mapping across processes with a short TTL. It is
// strictly best-effort — a missing/expired/corrupt cache, an unwritable
// home dir, or CREWSHIP_NO_SLUG_CACHE=1 all fall back to the preflight.
// Definitive misses are never cached (a typo shouldn't stick for 15 min),
// and entries are keyed by server URL so multi-instance setups (dev1/dev2/
// prod profiles) can't leak a CUID across servers.

// slugCacheTTL bounds staleness. Slugs are stable in practice; the TTL
// guards the rare delete-and-recreate of a workspace under the same slug —
// 15 minutes keeps agent loops fast without letting a re-mapped slug point
// at the dead CUID for long.
const slugCacheTTL = 15 * time.Minute

type slugCacheEntry struct {
	ID       string    `json:"id"`
	CachedAt time.Time `json:"cached_at"`
}

func slugCacheDisabled() bool {
	return os.Getenv("CREWSHIP_NO_SLUG_CACHE") != ""
}

// slugCachePath mirrors the update-check cache location (~/.crewship/cache).
func slugCachePath() (string, error) {
	dir, err := DefaultConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "cache", "workspace_slugs.json"), nil
}

func slugCacheKey(server, slug string) string {
	return server + "|" + slug
}

// lookupSlugCache returns the cached CUID for (server, slug), or "" on any
// miss: disabled, unreadable, corrupt, expired, or absent.
func lookupSlugCache(server, slug string) string {
	if slugCacheDisabled() {
		return ""
	}
	path, err := slugCachePath()
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var entries map[string]slugCacheEntry
	if json.Unmarshal(data, &entries) != nil {
		return ""
	}
	e, ok := entries[slugCacheKey(server, slug)]
	if !ok || e.ID == "" || time.Since(e.CachedAt) > slugCacheTTL {
		return ""
	}
	return e.ID
}

// storeSlugCache persists a successful resolution. Best-effort: every error
// is swallowed — the worst outcome is the next process re-runs the preflight.
// The write goes through AtomicFile so concurrent CLI invocations can't
// interleave partial JSON.
func storeSlugCache(server, slug, id string) {
	if slugCacheDisabled() || id == "" {
		return
	}
	path, err := slugCachePath()
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	// Load-modify-write; drop expired entries while we're here so the file
	// doesn't accrete dead servers/slugs forever.
	entries := map[string]slugCacheEntry{}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &entries) // corrupt → start fresh
	}
	for k, e := range entries {
		if time.Since(e.CachedAt) > slugCacheTTL {
			delete(entries, k)
		}
	}
	entries[slugCacheKey(server, slug)] = slugCacheEntry{ID: id, CachedAt: time.Now()}
	data, err := json.Marshal(entries)
	if err != nil {
		return
	}
	f, err := NewAtomicFile(path)
	if err != nil {
		return
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return
	}
	_ = f.Commit()
}
