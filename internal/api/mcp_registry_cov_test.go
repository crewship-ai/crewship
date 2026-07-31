package api

// Coverage tests for the MCP registry HTTP handlers (List, Search, Sync) in
// mcp_registry.go. The existing mcp_registry_test.go exercises SyncMCPRegistry
// (the upstream fetch + upsert), so these focus on the handler surface that it
// leaves uncovered: pagination, the ?trust_tier=/?featured= filters and their
// 400 paths, the search LIKE/ranking branches, and the Sync auth + cooldown
// branches.
//
// The live remote-registry fetch inside Sync's accepted (202) path is NOT
// exercised here: Sync spawns a detached goroutine that calls SyncMCPRegistry,
// which is already covered by mcp_registry_test.go against an httptest fixture.
// We assert only the synchronous 202 / 403 / 429 branches — but we do join
// that goroutine before the test unwinds (see syncDoneLogger), because it
// reads package state the test's cleanups put back.

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// syncDoneLogger returns a logger to hand MCPRegistryHandler plus a wait
// function that blocks until the detached goroutine Sync spawns has run to
// completion.
//
// Sync's accepted path is fire-and-forget: it starts a goroutine that calls
// SyncMCPRegistry and returns 202 immediately, so the goroutine outlives the
// test unless the test joins it. It reads the package-level mcpRegistryURL
// (mcp_registry.go), which every registry test swaps and restores from
// t.Cleanup — under -race that restore is a write concurrent with the
// goroutine's read, and it also lets the goroutine write to a *sql.DB that
// setupTestDB's own cleanup is closing.
//
// The goroutine has no exported join point, but it does have a terminal log
// record on both outcomes: SyncMCPRegistry logs "MCP registry sync complete"
// as its last statement on success, and Sync's closure logs "manual MCP
// registry sync failed" after it returns an error. Neither is emitted before
// the last read of mcpRegistryURL, so signalling on either one is a real
// happens-before edge between the goroutine and the test's cleanups.
func syncDoneLogger(t *testing.T) (*slog.Logger, func()) {
	t.Helper()
	signal := &syncDoneSignal{done: make(chan struct{})}
	h := &syncDoneHandler{
		Handler: slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}),
		signal:  signal,
	}
	wait := func() {
		t.Helper()
		select {
		case <-signal.done:
		case <-time.After(30 * time.Second):
			t.Fatal("background registry sync did not finish within 30s")
		}
	}
	return slog.New(h), wait
}

// syncDoneSignal is the shared close-once channel behind every clone a
// syncDoneHandler hands out from WithAttrs/WithGroup.
type syncDoneSignal struct {
	once sync.Once
	done chan struct{}
}

// syncDoneHandler closes the signal the first time it sees a record marking
// the end of a background registry sync, then discards the record. Handle
// runs on the detached goroutine, hence the sync.Once.
//
// Enabled must say yes at every level: the terminal success record is logged
// at Info, and the embedded discard handler is pinned to Error, so deferring
// to it would drop the record before Handle ever saw it.
type syncDoneHandler struct {
	slog.Handler
	signal *syncDoneSignal
}

func (h *syncDoneHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *syncDoneHandler) Handle(ctx context.Context, r slog.Record) error {
	if r.Message == "MCP registry sync complete" || r.Message == "manual MCP registry sync failed" {
		h.signal.once.Do(func() { close(h.signal.done) })
	}
	if !h.Handler.Enabled(ctx, r.Level) {
		return nil
	}
	return h.Handler.Handle(ctx, r)
}

func (h *syncDoneHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &syncDoneHandler{Handler: h.Handler.WithAttrs(attrs), signal: h.signal}
}

func (h *syncDoneHandler) WithGroup(name string) slog.Handler {
	return &syncDoneHandler{Handler: h.Handler.WithGroup(name), signal: h.signal}
}

// covMRSeed inserts a single mcp_registry_servers row with the given id/name,
// trust_tier and featured flag. Only the columns the handlers filter/sort on
// are parameterised; the rest fall back to schema defaults.
func covMRSeed(t *testing.T, db *sql.DB, id, name, displayName, description, category, trustTier string, featured bool) {
	t.Helper()
	feat := 0
	if featured {
		feat = 1
	}
	_, err := db.Exec(`INSERT INTO mcp_registry_servers
		(id, name, display_name, description, category, trust_tier, is_featured)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, name, displayName, description, category, trustTier, feat)
	if err != nil {
		t.Fatalf("seed registry row %s: %v", id, err)
	}
}

// covMRDecode reads a recorder body into a generic map.
func covMRDecode(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("decode response: %v (body=%s)", err, body)
	}
	return m
}

// covMRServerCount returns len(resp["servers"]).
func covMRServerCount(t *testing.T, m map[string]any) int {
	t.Helper()
	servers, ok := m["servers"].([]any)
	if !ok {
		t.Fatalf("response missing servers array: %v", m)
	}
	return len(servers)
}

func covMRHandler(t *testing.T) (*MCPRegistryHandler, *sql.DB) {
	t.Helper()
	db := setupTestDB(t)
	return NewMCPRegistryHandler(db, newTestLogger()), db
}

// --- List ---

func TestCovMRListEmpty(t *testing.T) {
	h, _ := covMRHandler(t)
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)

	req := withWorkspaceUser(httptest.NewRequest(http.MethodGet, "/api/v1/mcp-registry", nil), userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.List(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}
	m := covMRDecode(t, rec.Body.Bytes())
	if covMRServerCount(t, m) != 0 {
		t.Errorf("expected empty servers, got %v", m["servers"])
	}
	if total, _ := m["total"].(float64); total != 0 {
		t.Errorf("total: got %v, want 0", m["total"])
	}
	if limit, _ := m["limit"].(float64); limit != 50 {
		t.Errorf("default limit: got %v, want 50", m["limit"])
	}
}

func TestCovMRListHappyAndFeaturedFirst(t *testing.T) {
	h, db := covMRHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	covMRSeed(t, db, "z-srv", "z-srv", "Zebra", "last alphabetically", "tools", "community", false)
	covMRSeed(t, db, "a-srv", "a-srv", "Alpha", "first alphabetically", "tools", "community", false)
	covMRSeed(t, db, "feat-srv", "feat-srv", "Featured", "should sort first", "tools", "anthropic", true)

	req := withWorkspaceUser(httptest.NewRequest(http.MethodGet, "/api/v1/mcp-registry", nil), userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.List(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}
	m := covMRDecode(t, rec.Body.Bytes())
	if covMRServerCount(t, m) != 3 {
		t.Fatalf("expected 3 servers, got %d", covMRServerCount(t, m))
	}
	if total, _ := m["total"].(float64); total != 3 {
		t.Errorf("total: got %v, want 3", m["total"])
	}
	servers := m["servers"].([]any)
	first := servers[0].(map[string]any)
	if first["name"] != "feat-srv" {
		t.Errorf("featured row should sort first, got %v", first["name"])
	}
	// Among non-featured: alphabetical.
	if servers[1].(map[string]any)["name"] != "a-srv" {
		t.Errorf("expected a-srv second, got %v", servers[1].(map[string]any)["name"])
	}
}

func TestCovMRListTrustTierFilter(t *testing.T) {
	h, db := covMRHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	covMRSeed(t, db, "anth", "anth", "Anthropic One", "", "", "anthropic", false)
	covMRSeed(t, db, "comm", "comm", "Community One", "", "", "community", false)

	req := withWorkspaceUser(httptest.NewRequest(http.MethodGet, "/api/v1/mcp-registry?trust_tier=anthropic", nil), userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.List(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}
	m := covMRDecode(t, rec.Body.Bytes())
	if covMRServerCount(t, m) != 1 {
		t.Fatalf("expected 1 anthropic server, got %d", covMRServerCount(t, m))
	}
	if m["servers"].([]any)[0].(map[string]any)["name"] != "anth" {
		t.Errorf("wrong server returned: %v", m["servers"])
	}
	if total, _ := m["total"].(float64); total != 1 {
		t.Errorf("filtered total: got %v, want 1", m["total"])
	}
}

func TestCovMRListFeaturedFilter(t *testing.T) {
	h, db := covMRHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	covMRSeed(t, db, "f1", "f1", "Feat", "", "", "community", true)
	covMRSeed(t, db, "n1", "n1", "Plain", "", "", "community", false)

	cases := []struct {
		q    string
		want int
	}{
		{"featured=true", 1},
		{"featured=1", 1},
		{"featured=false", 1},
		{"featured=0", 1},
	}
	for _, c := range cases {
		req := withWorkspaceUser(httptest.NewRequest(http.MethodGet, "/api/v1/mcp-registry?"+c.q, nil), userID, wsID, "OWNER")
		rec := httptest.NewRecorder()
		h.List(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status: got %d, want 200", c.q, rec.Code)
		}
		m := covMRDecode(t, rec.Body.Bytes())
		if got := covMRServerCount(t, m); got != c.want {
			t.Errorf("%s: got %d servers, want %d", c.q, got, c.want)
		}
	}
}

func TestCovMRListInvalidFilters(t *testing.T) {
	h, _ := covMRHandler(t)
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)

	for _, q := range []string{"trust_tier=bogus", "featured=maybe"} {
		req := withWorkspaceUser(httptest.NewRequest(http.MethodGet, "/api/v1/mcp-registry?"+q, nil), userID, wsID, "OWNER")
		rec := httptest.NewRecorder()
		h.List(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: got %d, want 400", q, rec.Code)
		}
	}
}

func TestCovMRListPaginationClamp(t *testing.T) {
	h, db := covMRHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	covMRSeed(t, db, "p1", "p1", "P1", "", "", "community", false)
	covMRSeed(t, db, "p2", "p2", "P2", "", "", "community", false)
	covMRSeed(t, db, "p3", "p3", "P3", "", "", "community", false)

	// limit beyond max (200) clamps to 200; offset negative clamps to 0;
	// a small explicit limit + offset paginates.
	req := withWorkspaceUser(httptest.NewRequest(http.MethodGet, "/api/v1/mcp-registry?limit=1&offset=1", nil), userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.List(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}
	m := covMRDecode(t, rec.Body.Bytes())
	if covMRServerCount(t, m) != 1 {
		t.Errorf("limit=1 should return 1 row, got %d", covMRServerCount(t, m))
	}
	if total, _ := m["total"].(float64); total != 3 {
		t.Errorf("total ignores pagination: got %v, want 3", m["total"])
	}
	if off, _ := m["offset"].(float64); off != 1 {
		t.Errorf("offset echoed: got %v, want 1", m["offset"])
	}

	// Over-max limit clamps to 200 (still returns all 3 rows).
	req2 := withWorkspaceUser(httptest.NewRequest(http.MethodGet, "/api/v1/mcp-registry?limit=9999", nil), userID, wsID, "OWNER")
	rec2 := httptest.NewRecorder()
	h.List(rec2, req2)
	m2 := covMRDecode(t, rec2.Body.Bytes())
	if lim, _ := m2["limit"].(float64); lim != 200 {
		t.Errorf("over-max limit should clamp to 200, got %v", m2["limit"])
	}
}

// --- Search ---

func TestCovMRSearchEmptyDelegatesToList(t *testing.T) {
	h, db := covMRHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	covMRSeed(t, db, "s1", "s1", "One", "", "", "community", false)
	covMRSeed(t, db, "s2", "s2", "Two", "", "", "community", false)

	// Blank q must fall through to List (no "query" key in the response).
	req := withWorkspaceUser(httptest.NewRequest(http.MethodGet, "/api/v1/mcp-registry/search?q=%20%20", nil), userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.Search(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}
	m := covMRDecode(t, rec.Body.Bytes())
	if _, hasQuery := m["query"]; hasQuery {
		t.Errorf("blank-q search should delegate to List (no query key): %v", m)
	}
	if covMRServerCount(t, m) != 2 {
		t.Errorf("expected 2 servers from delegated List, got %d", covMRServerCount(t, m))
	}
}

func TestCovMRSearchMatchAndRanking(t *testing.T) {
	h, db := covMRHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	// "github" matches the name on one row and only the description on
	// another. The name match must rank first per the CASE WHEN ordering.
	covMRSeed(t, db, "desc-only", "desc-only", "Desc Only", "talks to github repos", "", "community", false)
	covMRSeed(t, db, "github-srv", "github-srv", "Git Hub", "version control", "", "community", false)
	covMRSeed(t, db, "unrelated", "unrelated", "Unrelated", "nothing here", "", "community", false)

	req := withWorkspaceUser(httptest.NewRequest(http.MethodGet, "/api/v1/mcp-registry/search?q=github", nil), userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.Search(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}
	m := covMRDecode(t, rec.Body.Bytes())
	if m["query"] != "github" {
		t.Errorf("query echoed: got %v, want github", m["query"])
	}
	if covMRServerCount(t, m) != 2 {
		t.Fatalf("expected 2 matches, got %d", covMRServerCount(t, m))
	}
	if total, _ := m["total"].(float64); total != 2 {
		t.Errorf("search total: got %v, want 2", m["total"])
	}
	first := m["servers"].([]any)[0].(map[string]any)
	if first["name"] != "github-srv" {
		t.Errorf("name match should rank first, got %v", first["name"])
	}
}

func TestCovMRSearchWithTrustTierFilter(t *testing.T) {
	h, db := covMRHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	covMRSeed(t, db, "tool-anth", "tool-anth", "Tool A", "a useful tool", "", "anthropic", false)
	covMRSeed(t, db, "tool-comm", "tool-comm", "Tool C", "a useful tool", "", "community", false)

	req := withWorkspaceUser(httptest.NewRequest(http.MethodGet, "/api/v1/mcp-registry/search?q=tool&trust_tier=anthropic", nil), userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.Search(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}
	m := covMRDecode(t, rec.Body.Bytes())
	if covMRServerCount(t, m) != 1 {
		t.Fatalf("expected 1 filtered match, got %d", covMRServerCount(t, m))
	}
	if m["servers"].([]any)[0].(map[string]any)["name"] != "tool-anth" {
		t.Errorf("wrong filtered match: %v", m["servers"])
	}
	if total, _ := m["total"].(float64); total != 1 {
		t.Errorf("filtered search total: got %v, want 1", m["total"])
	}
}

func TestCovMRSearchInvalidFilter(t *testing.T) {
	h, _ := covMRHandler(t)
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)

	req := withWorkspaceUser(httptest.NewRequest(http.MethodGet, "/api/v1/mcp-registry/search?q=x&trust_tier=nope", nil), userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.Search(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("invalid trust_tier in search: got %d, want 400", rec.Code)
	}
}

// --- Sync (synchronous branches only; background fetch is covered elsewhere) ---

func TestCovMRSyncForbiddenRole(t *testing.T) {
	h, db := covMRHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	// MEMBER lacks the "manage" capability → 403, no sync attempted.
	req := withWorkspaceUser(httptest.NewRequest(http.MethodPost, "/api/v1/mcp-registry/sync", nil), userID, wsID, "MEMBER")
	rec := httptest.NewRecorder()
	h.Sync(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-manage role: got %d, want 403", rec.Code)
	}
	if h.lastSync.Load() != 0 {
		t.Errorf("forbidden sync must not set lastSync, got %d", h.lastSync.Load())
	}
}

func TestCovMRSyncAcceptedThenCooldown(t *testing.T) {
	db := setupTestDB(t)
	logger, waitForSync := syncDoneLogger(t)
	h := NewMCPRegistryHandler(db, logger)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	// Point the background fetch at an unroutable URL so the detached
	// goroutine fails fast and never touches the live registry. We only
	// assert the synchronous 202 here; the goroutine's error is logged and
	// ignored.
	//
	prev := mcpRegistryURL
	mcpRegistryURL = "http://127.0.0.1:0/never"
	t.Cleanup(func() { mcpRegistryURL = prev })

	req := withWorkspaceUser(httptest.NewRequest(http.MethodPost, "/api/v1/mcp-registry/sync", nil), userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.Sync(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("first sync: got %d, want 202", rec.Code)
	}
	// The 202 means the detached goroutine is now live and reading
	// mcpRegistryURL. Join it before anything else unwinds: cleanups run
	// LIFO, so registering here puts the wait ahead of both the restore
	// above and setupTestDB's db.Close.
	t.Cleanup(waitForSync)
	m := covMRDecode(t, rec.Body.Bytes())
	if m["status"] != "sync_started" {
		t.Errorf("status: got %v, want sync_started", m["status"])
	}
	if h.lastSync.Load() == 0 {
		t.Errorf("accepted sync should record lastSync")
	}

	// Immediate second call is inside the 1-hour cooldown → 429.
	req2 := withWorkspaceUser(httptest.NewRequest(http.MethodPost, "/api/v1/mcp-registry/sync", nil), userID, wsID, "OWNER")
	rec2 := httptest.NewRecorder()
	h.Sync(rec2, req2)
	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("second sync within cooldown: got %d, want 429", rec2.Code)
	}
	m2 := covMRDecode(t, rec2.Body.Bytes())
	if _, ok := m2["error"]; !ok {
		t.Errorf("cooldown response should carry an error message: %v", m2)
	}
}
