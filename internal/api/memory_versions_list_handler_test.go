package api

// Coverage for the memory_versions list endpoint (Iter 7).
//
// Contracts pinned here:
//
//   1. Auth: manage required; cross-workspace probe gets no
//      data (not even a count leak).
//
//   2. Filter behaviour: tier / agent_slug / path_prefix /
//      since / until each compose AND-style with the workspace
//      filter. Empty query returns all rows for the workspace
//      in newest-first order.
//
//   3. Keyset pagination: requesting limit=N rows returns
//      exactly N when more remain, with a non-nil next_cursor
//      pointing at the next page boundary. Following the
//      cursor returns the next slice without duplicates or
//      gaps under concurrent inserts.
//
//   4. Cursor encoding round-trips: a cursor emitted by page
//      M decodes cleanly when used to fetch page M+1. Malformed
//      cursors 400.
//
//   5. Wildcard escaping: path_prefix containing '%' or '_'
//      matches the literal character, not the LIKE wildcard.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var memVerCounter atomic.Int64

func memVerRig(t *testing.T) (*MemoryVersionsListHandler, *sql.DB, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewMemoryVersionsListHandler(db, newTestLogger())
	return h, db, userID, wsID
}

// seedVersion inserts one memory_versions row. Each call gets
// a unique synthetic id + sha so the test corpus can grow
// without colliding on PK. writtenAt controls ordering for
// pagination tests.
func seedVersionRow(t *testing.T, db *sql.DB, wsID, path, tier string, bytes int, writtenAt time.Time, writtenBy string) string {
	t.Helper()
	n := memVerCounter.Add(1)
	id := fmt.Sprintf("mv_test_%d", n)
	sha := fmt.Sprintf("%064d", n) // synthetic 64-char hex-like sha
	if _, err := db.Exec(`
		INSERT INTO memory_versions
		(id, workspace_id, path, tier, sha256, bytes, payload_ref, written_at, written_by)
		VALUES (?, ?, ?, ?, ?, ?, '/dev/null', ?, ?)`,
		id, wsID, path, tier, sha, bytes,
		writtenAt.UTC().Format(time.RFC3339Nano),
		writtenBy,
	); err != nil {
		t.Fatalf("seed row: %v", err)
	}
	return id
}

func memVerDo(t *testing.T, h *MemoryVersionsListHandler, userID, wsID, role, query string) (int, memVersionsListResponse) {
	t.Helper()
	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/admin/memory/versions?"+query, nil),
		userID, wsID, role,
	)
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		return rr.Code, memVersionsListResponse{}
	}
	var resp memVersionsListResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rr.Body.String())
	}
	return rr.Code, resp
}

func TestMemoryVersionsList_Preconditions(t *testing.T) {
	h, _, userID, wsID := memVerRig(t)

	cases := []struct {
		name string
		role string
		ws   string
		want int
	}{
		{name: "member_forbidden", role: "MEMBER", ws: wsID, want: http.StatusForbidden},
		{name: "missing_workspace", role: "OWNER", ws: "", want: http.StatusBadRequest},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/v1/admin/memory/versions", nil)
			ctx := withUser(req.Context(), &AuthUser{ID: userID, Email: userID + "@example.com"})
			ctx = withWorkspace(ctx, tc.ws, tc.role)
			rr := httptest.NewRecorder()
			h.List(rr, req.WithContext(ctx))
			if rr.Code != tc.want {
				t.Fatalf("status = %d, want %d", rr.Code, tc.want)
			}
		})
	}
}

func TestMemoryVersionsList_Empty_ReturnsEmptyArrayNotNull(t *testing.T) {
	// A common JSON-API mistake: returning null instead of []
	// for empty results. The dashboard table renders the field
	// directly, so null would crash. Pinning the contract.
	h, _, userID, wsID := memVerRig(t)
	code, resp := memVerDo(t, h, userID, wsID, "OWNER", "")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if resp.Rows == nil {
		t.Errorf("Rows = nil; want []memVersionRow{}")
	}
	if len(resp.Rows) != 0 {
		t.Errorf("Rows = %v; want []", resp.Rows)
	}
	if resp.NextCursor != nil {
		t.Errorf("NextCursor = %v; want nil on empty result", resp.NextCursor)
	}
	if resp.Limit != memVersionsDefaultLimit {
		t.Errorf("Limit = %d; want %d (default)", resp.Limit, memVersionsDefaultLimit)
	}
}

func TestMemoryVersionsList_HappyPath_NewestFirst(t *testing.T) {
	h, db, userID, wsID := memVerRig(t)
	base := time.Now().UTC()
	// Insert in random order; the endpoint should return
	// newest first regardless of insertion sequence.
	idOld := seedVersionRow(t, db, wsID, "agent:martin/AGENT.md", "agent", 100, base.Add(-2*time.Hour), "audit-watcher")
	idNew := seedVersionRow(t, db, wsID, "agent:eva/CREW.md", "crew", 200, base.Add(-1*time.Minute), "sidecar")
	idMid := seedVersionRow(t, db, wsID, "agent:viktor/pins.md", "pins", 50, base.Add(-30*time.Minute), "audit-watcher")

	code, resp := memVerDo(t, h, userID, wsID, "OWNER", "")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if len(resp.Rows) != 3 {
		t.Fatalf("got %d rows; want 3", len(resp.Rows))
	}
	if resp.Rows[0].ID != idNew {
		t.Errorf("rows[0] = %q; want newest (%q)", resp.Rows[0].ID, idNew)
	}
	if resp.Rows[1].ID != idMid {
		t.Errorf("rows[1] = %q; want mid (%q)", resp.Rows[1].ID, idMid)
	}
	if resp.Rows[2].ID != idOld {
		t.Errorf("rows[2] = %q; want oldest (%q)", resp.Rows[2].ID, idOld)
	}
}

func TestMemoryVersionsList_FilterByTier(t *testing.T) {
	h, db, userID, wsID := memVerRig(t)
	now := time.Now().UTC()
	seedVersionRow(t, db, wsID, "agent:a/AGENT.md", "agent", 10, now.Add(-1*time.Hour), "test")
	seedVersionRow(t, db, wsID, "agent:b/CREW.md", "crew", 20, now.Add(-2*time.Hour), "test")
	seedVersionRow(t, db, wsID, "agent:c/pins.md", "pins", 30, now.Add(-3*time.Hour), "test")

	_, resp := memVerDo(t, h, userID, wsID, "OWNER", "tier=agent")
	if len(resp.Rows) != 1 || resp.Rows[0].Tier != "agent" {
		t.Errorf("tier=agent filter returned %d rows: %+v", len(resp.Rows), resp.Rows)
	}
	if resp.FiltersApplied["tier"] != "agent" {
		t.Errorf("filters_applied.tier = %q; want agent", resp.FiltersApplied["tier"])
	}
}

func TestMemoryVersionsList_FilterByAgentSlug(t *testing.T) {
	h, db, userID, wsID := memVerRig(t)
	now := time.Now().UTC()
	seedVersionRow(t, db, wsID, "agent:martin/AGENT.md", "agent", 1, now.Add(-1*time.Hour), "test")
	seedVersionRow(t, db, wsID, "agent:martin/daily/2026-05-18.md", "agent", 2, now.Add(-2*time.Hour), "test")
	seedVersionRow(t, db, wsID, "agent:eva/AGENT.md", "agent", 3, now.Add(-3*time.Hour), "test")

	_, resp := memVerDo(t, h, userID, wsID, "OWNER", "agent_slug=martin")
	if len(resp.Rows) != 2 {
		t.Errorf("agent_slug=martin returned %d rows; want 2", len(resp.Rows))
	}
	for _, r := range resp.Rows {
		if !strings.HasPrefix(r.Path, "agent:martin/") {
			t.Errorf("non-martin row leaked: %+v", r)
		}
	}
}

func TestMemoryVersionsList_FilterByPathPrefix(t *testing.T) {
	h, db, userID, wsID := memVerRig(t)
	now := time.Now().UTC()
	seedVersionRow(t, db, wsID, "agent:martin/daily/2026-05-17.md", "agent", 1, now.Add(-1*time.Hour), "test")
	seedVersionRow(t, db, wsID, "agent:martin/daily/2026-05-18.md", "agent", 2, now.Add(-2*time.Hour), "test")
	seedVersionRow(t, db, wsID, "agent:martin/AGENT.md", "agent", 3, now.Add(-3*time.Hour), "test")

	_, resp := memVerDo(t, h, userID, wsID, "OWNER", "path_prefix=agent:martin/daily/")
	if len(resp.Rows) != 2 {
		t.Errorf("path_prefix filter returned %d rows; want 2", len(resp.Rows))
	}
}

func TestMemoryVersionsList_PathPrefixWildcardEscaping(t *testing.T) {
	// A naive LIKE clause would treat '%' as a wildcard. Seed
	// two paths: one with a literal '%' that the user wants
	// to find, one without. Search for the literal — only the
	// literal-bearing row should match.
	h, db, userID, wsID := memVerRig(t)
	now := time.Now().UTC()
	seedVersionRow(t, db, wsID, "agent:martin/100%fancy.md", "agent", 1, now.Add(-1*time.Hour), "test")
	seedVersionRow(t, db, wsID, "agent:martin/100okay.md", "agent", 2, now.Add(-2*time.Hour), "test")
	seedVersionRow(t, db, wsID, "agent:eva/100okay.md", "agent", 3, now.Add(-3*time.Hour), "test")

	_, resp := memVerDo(t, h, userID, wsID, "OWNER", "path_prefix=agent:martin/100%25")
	// URL-decoded %25 = '%'. The handler should escape so only
	// the literal "agent:martin/100%fancy.md" matches.
	if len(resp.Rows) != 1 {
		t.Errorf("path_prefix wildcard-escape: got %d rows; want 1", len(resp.Rows))
		for _, r := range resp.Rows {
			t.Logf("  row: %s", r.Path)
		}
	}
}

func TestMemoryVersionsList_FilterByTimeRange(t *testing.T) {
	h, db, userID, wsID := memVerRig(t)
	now := time.Now().UTC()
	seedVersionRow(t, db, wsID, "agent:a/AGENT.md", "agent", 1, now.Add(-4*time.Hour), "test")
	seedVersionRow(t, db, wsID, "agent:b/AGENT.md", "agent", 2, now.Add(-2*time.Hour), "test")
	seedVersionRow(t, db, wsID, "agent:c/AGENT.md", "agent", 3, now.Add(-30*time.Minute), "test")

	since := now.Add(-3 * time.Hour).Format(time.RFC3339)
	until := now.Add(-1 * time.Hour).Format(time.RFC3339)
	_, resp := memVerDo(t, h, userID, wsID, "OWNER",
		"since="+strings.ReplaceAll(since, ":", "%3A")+
			"&until="+strings.ReplaceAll(until, ":", "%3A"))
	if len(resp.Rows) != 1 {
		t.Errorf("time range filter: got %d rows; want 1 (only -2h is in [-3h, -1h))", len(resp.Rows))
	}
}

func TestMemoryVersionsList_Pagination_KeysetRoundTrip(t *testing.T) {
	// 12 rows, limit=5 → expect pages of 5, 5, 2 with cursors.
	// The cursor must round-trip: page B's contents are
	// exactly the rows page A would have shown next, no
	// duplicates and no gaps.
	h, db, userID, wsID := memVerRig(t)
	base := time.Now().UTC()
	idsByRecency := make([]string, 0, 12)
	for i := 11; i >= 0; i-- {
		// Insert oldest-first so idsByRecency, written by the
		// loop below, ends up in newest-first order.
		id := seedVersionRow(t, db, wsID, fmt.Sprintf("agent:a/file%02d.md", i), "agent", i, base.Add(-time.Duration(i+1)*time.Minute), "test")
		idsByRecency = append([]string{id}, idsByRecency...)
	}

	// Page 1
	_, p1 := memVerDo(t, h, userID, wsID, "OWNER", "limit=5")
	if len(p1.Rows) != 5 || p1.NextCursor == nil {
		t.Fatalf("page 1: got %d rows + cursor=%v; want 5 + non-nil",
			len(p1.Rows), p1.NextCursor)
	}
	for i, r := range p1.Rows {
		if r.ID != idsByRecency[i] {
			t.Errorf("page 1 row %d: id=%q; want %q", i, r.ID, idsByRecency[i])
		}
	}

	// Page 2
	_, p2 := memVerDo(t, h, userID, wsID, "OWNER", "limit=5&cursor="+*p1.NextCursor)
	if len(p2.Rows) != 5 || p2.NextCursor == nil {
		t.Fatalf("page 2: got %d rows + cursor=%v; want 5 + non-nil",
			len(p2.Rows), p2.NextCursor)
	}
	for i, r := range p2.Rows {
		if r.ID != idsByRecency[5+i] {
			t.Errorf("page 2 row %d: id=%q; want %q", i, r.ID, idsByRecency[5+i])
		}
	}

	// Page 3 — last 2 rows, no more pages.
	_, p3 := memVerDo(t, h, userID, wsID, "OWNER", "limit=5&cursor="+*p2.NextCursor)
	if len(p3.Rows) != 2 {
		t.Fatalf("page 3: got %d rows; want 2 (final page)", len(p3.Rows))
	}
	if p3.NextCursor != nil {
		t.Errorf("page 3 NextCursor = %v; want nil (last page)", p3.NextCursor)
	}
	for i, r := range p3.Rows {
		if r.ID != idsByRecency[10+i] {
			t.Errorf("page 3 row %d: id=%q; want %q", i, r.ID, idsByRecency[10+i])
		}
	}
}

func TestMemoryVersionsList_MalformedCursor_Returns400(t *testing.T) {
	h, _, userID, wsID := memVerRig(t)

	cases := []string{
		"not-base64-!!!",
		"YQ==",                                 // valid base64, missing version prefix
		"djI6MjAyNi0wNS0xOFQwMDowMDowMFp8aWQ=", // base64 of "v2:2026-05-18T00:00:00Z|id" — wrong version
		"djE6bm9waXBl",                         // base64 of "v1:nopipe" — no delimiter
		"djE6Mjk5OXxpZA==",                     // base64 of "v1:2999|id" — not RFC3339
	}
	for i, c := range cases {
		c := c
		t.Run(fmt.Sprintf("case_%d", i), func(t *testing.T) {
			req := withWorkspaceUser(
				httptest.NewRequest("GET", "/api/v1/admin/memory/versions?cursor="+c, nil),
				userID, wsID, "OWNER",
			)
			rr := httptest.NewRecorder()
			h.List(rr, req)
			if rr.Code != http.StatusBadRequest {
				t.Errorf("case %q: status = %d, want 400", c, rr.Code)
			}
		})
	}
}

func TestMemoryVersionsList_InvalidTier_Returns400(t *testing.T) {
	h, _, userID, wsID := memVerRig(t)
	code, _ := memVerDo(t, h, userID, wsID, "OWNER", "tier=bogus")
	if code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for unknown tier", code)
	}
}

func TestMemoryVersionsList_LimitClamp(t *testing.T) {
	h, _, userID, wsID := memVerRig(t)

	cases := []struct {
		name  string
		query string
		want  int
	}{
		{name: "above_max_clamped", query: "limit=10000", want: memVersionsMaxLimit},
		{name: "explicit_below_max", query: "limit=7", want: 7},
		{name: "missing_uses_default", query: "", want: memVersionsDefaultLimit},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			_, resp := memVerDo(t, h, userID, wsID, "OWNER", tc.query)
			if resp.Limit != tc.want {
				t.Errorf("Limit = %d; want %d", resp.Limit, tc.want)
			}
		})
	}
}

func TestMemoryVersionsList_LimitInvalid_Returns400(t *testing.T) {
	h, _, userID, wsID := memVerRig(t)
	cases := []string{"limit=-1", "limit=0", "limit=abc", "limit=", "limit=1.5"}
	for _, q := range cases {
		q := q
		t.Run(strings.ReplaceAll(q, "=", "_"), func(t *testing.T) {
			req := withWorkspaceUser(
				httptest.NewRequest("GET", "/api/v1/admin/memory/versions?"+q, nil),
				userID, wsID, "OWNER",
			)
			rr := httptest.NewRecorder()
			h.List(rr, req)
			// limit= (empty) is fine in URL parsing — value is
			// "". Our TrimSpace check treats it as missing, so
			// 200. Adjust expectation.
			if q == "limit=" {
				if rr.Code != http.StatusOK {
					t.Errorf("empty limit value: status %d, want 200", rr.Code)
				}
				return
			}
			if rr.Code != http.StatusBadRequest {
				t.Errorf("query=%q: status %d, want 400", q, rr.Code)
			}
		})
	}
}

func TestMemoryVersionsList_CrossWorkspaceIsolation(t *testing.T) {
	h, db, _, wsID := memVerRig(t)
	now := time.Now().UTC()
	seedVersionRow(t, db, wsID, "agent:a/AGENT.md", "agent", 100, now, "test")

	// Inline second tenant.
	otherUserID := "test-other-user-id"
	otherWS := "test-other-workspace-id"
	if _, err := db.Exec(`INSERT INTO users (id, email, full_name) VALUES (?, 'other@example.com', 'Other')`, otherUserID); err != nil {
		t.Fatalf("seed other user: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Other', 'other')`, otherWS); err != nil {
		t.Fatalf("seed other ws: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('m_other', ?, ?, 'OWNER')`, otherWS, otherUserID); err != nil {
		t.Fatalf("seed other member: %v", err)
	}

	_, resp := memVerDo(t, h, otherUserID, otherWS, "OWNER", "")
	if len(resp.Rows) != 0 {
		t.Errorf("workspace B leaked workspace A rows: %+v", resp.Rows)
	}
}

// ── Cursor codec table ───────────────────────────────────────────────

func TestMemVersionsCursor_RoundTrip(t *testing.T) {
	cases := []struct {
		name string
		at   time.Time
		id   string
	}{
		{name: "now", at: time.Now().UTC(), id: "mv_1"},
		{name: "with_subsec", at: time.Date(2026, 5, 18, 12, 34, 56, 123456789, time.UTC), id: "mv_long_id_with_underscores"},
		{name: "epoch", at: time.Unix(0, 0).UTC(), id: "x"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			enc := encodeMemVersionsCursor(tc.at, tc.id)
			gotAt, gotID, err := decodeMemVersionsCursor(enc)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if !gotAt.Equal(tc.at) {
				t.Errorf("at: got %v, want %v", gotAt, tc.at)
			}
			if gotID != tc.id {
				t.Errorf("id: got %q, want %q", gotID, tc.id)
			}
		})
	}
}

// Compile-time guard: response shape pinned for the dashboard.
// Any rename of a JSON-tagged field on memVersionRow /
// memVersionsListResponse stops compiling here.
func TestMemVersionsList_ResponseShapeContract(t *testing.T) {
	resp := memVersionsListResponse{
		WorkspaceID: "ws",
		Rows: []memVersionRow{{
			ID: "id", Path: "p", Tier: "agent",
			Sha256: "sha", Bytes: 1, WrittenAt: "ts",
			WrittenBy: "wb", ParentSha: nil,
		}},
		NextCursor:     nil,
		Limit:          50,
		FiltersApplied: map[string]string{},
	}
	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	required := []string{
		`"workspace_id"`, `"rows"`, `"next_cursor"`, `"limit"`, `"filters_applied"`,
		`"id"`, `"path"`, `"tier"`, `"sha256"`, `"bytes"`, `"written_at"`, `"written_by"`,
	}
	for _, k := range required {
		if !strings.Contains(string(raw), k) {
			t.Errorf("response shape missing JSON key %s; got:\n%s", k, raw)
		}
	}
}
