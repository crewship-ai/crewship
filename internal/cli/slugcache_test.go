package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The workspace slug→CUID disk cache removes the extra /workspaces preflight
// round-trip that every `crewship <cmd> -w my-slug` invocation used to pay —
// the in-process cache never helped because a typical CLI run makes exactly
// one real request. The cache is best-effort: any miss, expiry, corruption,
// or disabled state falls back to the normal preflight.

func newSlugServer(t *testing.T, listCalls *int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/workspaces" {
			*listCalls++
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[{"id":"cabcdefghijklmnopqrst","slug":"alpha"}]`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestSlugCacheSkipsPreflightAcrossProcesses(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CREWSHIP_NO_SLUG_CACHE", "")
	listCalls := 0
	srv := newSlugServer(t, &listCalls)

	// First "process": resolves via preflight and populates the disk cache.
	c1 := NewClient(srv.URL, "", "alpha")
	if resp, err := c1.Get("/api/v1/agents"); err != nil {
		t.Fatalf("first resolve: %v", err)
	} else {
		resp.Body.Close()
	}
	if listCalls != 1 {
		t.Fatalf("first client: %d preflights, want 1", listCalls)
	}

	// Second "process" (fresh Client = fresh in-memory cache): must hit the
	// disk cache instead of re-running the preflight.
	c2 := NewClient(srv.URL, "", "alpha")
	if resp, err := c2.Get("/api/v1/agents"); err != nil {
		t.Fatalf("second resolve: %v", err)
	} else {
		resp.Body.Close()
	}
	if listCalls != 1 {
		t.Errorf("second client re-ran the preflight (%d calls) — disk cache not used", listCalls)
	}
}

func TestSlugCacheExpires(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CREWSHIP_NO_SLUG_CACHE", "")
	listCalls := 0
	srv := newSlugServer(t, &listCalls)

	c1 := NewClient(srv.URL, "", "alpha")
	if resp, err := c1.Get("/api/v1/agents"); err != nil {
		t.Fatalf("first resolve: %v", err)
	} else {
		resp.Body.Close()
	}

	// Age the cache entry past the TTL by rewriting its timestamp.
	path := filepath.Join(home, ".crewship", "cache", "workspace_slugs.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cache file not written: %v", err)
	}
	var entries map[string]slugCacheEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		t.Fatalf("parse cache file: %v", err)
	}
	for k, e := range entries {
		e.CachedAt = time.Now().Add(-2 * slugCacheTTL)
		entries[k] = e
	}
	aged, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, aged, 0o600); err != nil {
		t.Fatalf("rewrite cache: %v", err)
	}

	c2 := NewClient(srv.URL, "", "alpha")
	if resp, err := c2.Get("/api/v1/agents"); err != nil {
		t.Fatalf("second resolve: %v", err)
	} else {
		resp.Body.Close()
	}
	if listCalls != 2 {
		t.Errorf("expired entry must re-run the preflight; got %d calls, want 2", listCalls)
	}
}

func TestSlugCacheKeyedByServer(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CREWSHIP_NO_SLUG_CACHE", "")
	listCallsA, listCallsB := 0, 0
	srvA := newSlugServer(t, &listCallsA)
	srvB := newSlugServer(t, &listCallsB)

	cA := NewClient(srvA.URL, "", "alpha")
	if resp, err := cA.Get("/api/v1/agents"); err != nil {
		t.Fatalf("server A resolve: %v", err)
	} else {
		resp.Body.Close()
	}
	// Same slug, different server: must NOT reuse server A's cached CUID.
	cB := NewClient(srvB.URL, "", "alpha")
	if resp, err := cB.Get("/api/v1/agents"); err != nil {
		t.Fatalf("server B resolve: %v", err)
	} else {
		resp.Body.Close()
	}
	if listCallsB != 1 {
		t.Errorf("server B preflights = %d, want 1 (cache must be keyed by server)", listCallsB)
	}
}

func TestSlugCacheCorruptFileFallsBack(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CREWSHIP_NO_SLUG_CACHE", "")
	listCalls := 0
	srv := newSlugServer(t, &listCalls)

	path := filepath.Join(home, ".crewship", "cache", "workspace_slugs.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	c := NewClient(srv.URL, "", "alpha")
	if resp, err := c.Get("/api/v1/agents"); err != nil {
		t.Fatalf("corrupt cache must not fail resolution: %v", err)
	} else {
		resp.Body.Close()
	}
	if listCalls != 1 {
		t.Errorf("preflights = %d, want 1", listCalls)
	}
}

func TestSlugCacheDisabledByEnv(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CREWSHIP_NO_SLUG_CACHE", "1")
	listCalls := 0
	srv := newSlugServer(t, &listCalls)

	for i := 0; i < 2; i++ {
		c := NewClient(srv.URL, "", "alpha")
		if resp, err := c.Get("/api/v1/agents"); err != nil {
			t.Fatalf("resolve %d: %v", i, err)
		} else {
			resp.Body.Close()
		}
	}
	if listCalls != 2 {
		t.Errorf("with CREWSHIP_NO_SLUG_CACHE=1 every fresh client must preflight; got %d calls, want 2", listCalls)
	}
}
