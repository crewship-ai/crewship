package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

// memVersionCounter generates a unique synthetic memory_versions id
// per insert so two rows with the same sha (legitimate test
// scenario — content-identical re-writes) don't collide on the
// PRIMARY KEY. Production IDs are CUID-style; for tests a monotonic
// counter is enough and keeps the assertions readable.
var memVersionCounter atomic.Int64

// memStatsRig builds a freshly-migrated test DB, a workspace, and
// returns the handler. Memory_versions rows are inserted by the
// individual tests so each assertion controls its own corpus.
func memStatsRig(t *testing.T) (*MemoryStatsHandler, *sql.DB, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	return NewMemoryStatsHandler(db, logger), db, userID, wsID
}

// seedVersion inserts one memory_versions row with the supplied
// shape. payload_ref is a synthetic /dev/null marker because the
// stats handler never opens blobs; only the row counters matter.
func seedVersion(t *testing.T, db *sql.DB, wsID, path, tier, sha string, bytes int, writtenAt time.Time) {
	t.Helper()
	id := fmt.Sprintf("mv_test_%d_%s", memVersionCounter.Add(1), sha[:8])
	if _, err := db.Exec(`
		INSERT INTO memory_versions
		(id, workspace_id, path, tier, sha256, bytes, payload_ref, written_at, written_by)
		VALUES (?, ?, ?, ?, ?, ?, '/dev/null', ?, 'test')`,
		id, wsID, path, tier, sha, bytes,
		writtenAt.UTC().Format(time.RFC3339Nano),
	); err != nil {
		t.Fatalf("seed version: %v", err)
	}
}

// memStatsRespond runs the handler and returns the decoded body. Test
// helper rather than inlined so the auth wiring + JSON decode stay
// centralised.
func memStatsRespond(t *testing.T, h *MemoryStatsHandler, userID, wsID, role string) (int, memoryStatsResponse) {
	t.Helper()
	req := withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/admin/memory/stats", nil), userID, wsID, role)
	rr := httptest.NewRecorder()
	h.Stats(rr, req)
	if rr.Code != http.StatusOK {
		return rr.Code, memoryStatsResponse{}
	}
	var resp memoryStatsResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rr.Body.String())
	}
	return rr.Code, resp
}

// ── Role gate ────────────────────────────────────────────────────────

func TestMemoryStats_MemberRole_Returns403(t *testing.T) {
	// canRole("manage") gates this — MEMBER must not see workspace-
	// wide memory numbers (path names alone can leak project
	// structure). Regression guard for the role boundary.
	h, _, userID, wsID := memStatsRig(t)
	code, _ := memStatsRespond(t, h, userID, wsID, "MEMBER")
	if code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", code)
	}
}

func TestMemoryStats_MissingWorkspace_Returns400(t *testing.T) {
	// Workspace context comes from middleware; an OWNER without a
	// workspace context (test mocks, broken middleware ordering)
	// must 400 with a clear message rather than returning a
	// workspace-less default response.
	h, _, userID, _ := memStatsRig(t)
	req := httptest.NewRequest("GET", "/api/v1/admin/memory/stats", nil)
	// User context, no workspace context.
	ctx := withUser(req.Context(), &AuthUser{ID: userID, Email: userID + "@example.com"})
	ctx = withWorkspace(ctx, "", "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Stats(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

// ── Empty workspace ───────────────────────────────────────────────────

func TestMemoryStats_EmptyWorkspace_ReturnsZeroes(t *testing.T) {
	// New workspace with no memory_versions rows: counters must be
	// 0, byte sum 0, oldest/newest empty strings (NOT the SQLite
	// NULL leakage that would happen if we forgot sql.NullString).
	h, _, userID, wsID := memStatsRig(t)
	code, resp := memStatsRespond(t, h, userID, wsID, "OWNER")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if resp.WorkspaceID != wsID {
		t.Errorf("workspace_id echo = %q, want %q", resp.WorkspaceID, wsID)
	}
	if resp.Totals.Versions != 0 || resp.Totals.Bytes != 0 || resp.Totals.Blobs != 0 {
		t.Errorf("totals not zero: %+v", resp.Totals)
	}
	if resp.Totals.OldestAt != "" || resp.Totals.NewestAt != "" {
		t.Errorf("oldest/newest leaked NULL: %q / %q", resp.Totals.OldestAt, resp.Totals.NewestAt)
	}
	if len(resp.ByTier) != 0 {
		t.Errorf("by_tier should be empty slice; got %v", resp.ByTier)
	}
}

// ── Totals ────────────────────────────────────────────────────────────

func TestMemoryStats_Totals_AggregatesCorrectly(t *testing.T) {
	// Two rows with the same sha (re-write of identical content) +
	// one distinct row → versions=3, blobs=2 (sha-distinct count).
	// SUM(bytes) covers the totals.bytes contract.
	h, db, userID, wsID := memStatsRig(t)
	now := time.Now().UTC()
	seedVersion(t, db, wsID, "agent:m/AGENT.md", "agent",
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 100, now.Add(-2*time.Hour))
	seedVersion(t, db, wsID, "agent:m/AGENT.md", "agent",
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 100, now.Add(-1*time.Hour)) // same sha
	seedVersion(t, db, wsID, "agent:m/daily/2026-05-17.md", "agent",
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", 200, now)

	code, resp := memStatsRespond(t, h, userID, wsID, "OWNER")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if resp.Totals.Versions != 3 {
		t.Errorf("versions = %d, want 3", resp.Totals.Versions)
	}
	if resp.Totals.Bytes != 400 {
		t.Errorf("bytes = %d, want 400", resp.Totals.Bytes)
	}
	if resp.Totals.Blobs != 2 {
		t.Errorf("blobs = %d, want 2 (distinct shas)", resp.Totals.Blobs)
	}
	if resp.Totals.OldestAt == "" || resp.Totals.NewestAt == "" {
		t.Errorf("timestamps empty: oldest=%q newest=%q", resp.Totals.OldestAt, resp.Totals.NewestAt)
	}
}

// ── by_tier ───────────────────────────────────────────────────────────

func TestMemoryStats_ByTier_GroupsAndSorts(t *testing.T) {
	// Three tiers seeded; response must enumerate each once with
	// the right counts. Tier order is alphabetical (stable for UI).
	h, db, userID, wsID := memStatsRig(t)
	now := time.Now().UTC()
	seedVersion(t, db, wsID, "agent:m/AGENT.md", "agent", "1111111111111111111111111111111111111111111111111111111111111111", 100, now)
	seedVersion(t, db, wsID, "agent:m/AGENT.md", "agent", "2222222222222222222222222222222222222222222222222222222222222222", 50, now)
	seedVersion(t, db, wsID, "crew/CREW.md", "crew", "3333333333333333333333333333333333333333333333333333333333333333", 200, now)
	seedVersion(t, db, wsID, "agent:m/learned-2026-05-17.md", "learned", "4444444444444444444444444444444444444444444444444444444444444444", 80, now)

	_, resp := memStatsRespond(t, h, userID, wsID, "OWNER")
	if len(resp.ByTier) != 3 {
		t.Fatalf("by_tier len = %d, want 3 (agent, crew, learned)", len(resp.ByTier))
	}
	// Lock the alphabetical ordering — the UI sorts off this.
	want := []memoryStatsByTier{
		{Tier: "agent", Versions: 2, Bytes: 150},
		{Tier: "crew", Versions: 1, Bytes: 200},
		{Tier: "learned", Versions: 1, Bytes: 80},
	}
	for i, w := range want {
		got := resp.ByTier[i]
		if got != w {
			t.Errorf("by_tier[%d] = %+v, want %+v", i, got, w)
		}
	}
}

// ── by_agent ─────────────────────────────────────────────────────────

func TestMemoryStats_ByAgent_ExtractsSlugFromCanonicalPath(t *testing.T) {
	// Path convention "agent:<slug>/<rel>" must parse into the
	// slug. Crew-tier paths (no agent: prefix) collapse under the
	// empty-string bucket so the UI can render them as "shared".
	h, db, userID, wsID := memStatsRig(t)
	now := time.Now().UTC()
	seedVersion(t, db, wsID, "agent:martin/AGENT.md", "agent", "1111111111111111111111111111111111111111111111111111111111111111", 100, now)
	seedVersion(t, db, wsID, "agent:martin/daily/2026-05-17.md", "agent", "2222222222222222222222222222222222222222222222222222222222222222", 250, now)
	seedVersion(t, db, wsID, "agent:nela/AGENT.md", "agent", "3333333333333333333333333333333333333333333333333333333333333333", 75, now)
	seedVersion(t, db, wsID, "crew/CREW.md", "crew", "4444444444444444444444444444444444444444444444444444444444444444", 500, now) // no agent: prefix

	_, resp := memStatsRespond(t, h, userID, wsID, "OWNER")
	got := map[string]memoryStatsByAgent{}
	for _, row := range resp.ByAgent {
		got[row.AgentSlug] = row
	}
	if got["martin"].Versions != 2 || got["martin"].Bytes != 350 {
		t.Errorf("martin = %+v, want versions=2 bytes=350", got["martin"])
	}
	if got["nela"].Versions != 1 || got["nela"].Bytes != 75 {
		t.Errorf("nela = %+v, want versions=1 bytes=75", got["nela"])
	}
	if got[""].Versions != 1 || got[""].Bytes != 500 {
		t.Errorf("'' (shared) = %+v, want versions=1 bytes=500", got[""])
	}
}

// ── Tenant isolation ────────────────────────────────────────────────

func TestMemoryStats_CrossWorkspaceIsolation(t *testing.T) {
	// Seed memory rows in workspace A AND workspace B; verify the
	// caller's stats return ONLY their workspace's data. A bleed
	// here is a SOC-2 / EU AI Act observability flaw.
	h, db, userID, wsA := memStatsRig(t)
	now := time.Now().UTC()
	wsB := "ws_other_tenant"
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Other', 'other')`, wsB); err != nil {
		t.Fatalf("seed ws B: %v", err)
	}
	seedVersion(t, db, wsA, "agent:m/AGENT.md", "agent",
		"1111111111111111111111111111111111111111111111111111111111111111", 100, now)
	seedVersion(t, db, wsB, "agent:m/AGENT.md", "agent",
		"2222222222222222222222222222222222222222222222222222222222222222", 500, now)
	seedVersion(t, db, wsB, "agent:m/AGENT.md", "agent",
		"3333333333333333333333333333333333333333333333333333333333333333", 700, now)

	_, resp := memStatsRespond(t, h, userID, wsA, "OWNER")
	if resp.Totals.Versions != 1 || resp.Totals.Bytes != 100 {
		t.Errorf("workspace A stats leaked B's rows: %+v", resp.Totals)
	}
}

// ── compile guard ────────────────────────────────────────────────────

// memStatsCompileGuard ensures the response struct keeps the JSON
// keys the dashboard reads. A field rename (e.g. ByTier → Tiers)
// would compile but break the UI; downstream snapshot tests would
// catch it but only after a deploy. This package-level reference
// holds the line at build time.
var memStatsCompileGuard = func() {
	_ = memoryStatsResponse{
		WorkspaceID: "",
		Totals:      memoryStatsTotals{Versions: 0, Bytes: 0, Blobs: 0},
		ByTier:      nil,
		ByAgent:     nil,
	}
}

// underscore use so the compile guard isn't flagged unused if a
// future refactor reshuffles the file.
var _ = context.Background
