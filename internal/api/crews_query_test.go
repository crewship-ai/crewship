package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
)

// Behavior lock for CrewHandler.List (#1255 item 2).
//
// The counts in `_count` used to come from two correlated subqueries
// evaluated once per returned row; they now come from two grouped
// aggregates LEFT JOINed onto the page. That rewrite is purely a
// performance change, so this fixture pins the observable contract —
// row set, ordering, response key set and both counts — and must pass
// identically before and after the query change.
//
// Fixture covers the shapes that break naive join rewrites: a crew with
// no agents and no members (COALESCE must yield 0, not drop the row), a
// crew with agents but no members and vice versa (a single join key must
// not gate the other count), a crew with many of both (the join must not
// multiply rows), soft-deleted agents (excluded), a soft-deleted crew
// (excluded), and a crew in a different workspace whose children must
// not leak into this workspace's counts.
func TestCrewListCountsGoldenFixture(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	// Second workspace with its own crew, agents and members — none of it
	// may appear in, or contribute to, the wsID listing.
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws-other', 'Other', 'other')`); err != nil {
		t.Fatalf("insert other workspace: %v", err)
	}

	// Extra users for crew_members rows.
	memberIDs := make([]string, 0, 3)
	for i := 1; i <= 3; i++ {
		id := fmt.Sprintf("u-%d", i)
		if _, err := db.Exec(`INSERT INTO users (id, email, full_name) VALUES (?, ?, ?)`,
			id, fmt.Sprintf("u%d@example.com", i), fmt.Sprintf("User %d", i)); err != nil {
			t.Fatalf("insert user %s: %v", id, err)
		}
		memberIDs = append(memberIDs, id)
	}

	addMembers := func(crewID string, n int) {
		t.Helper()
		for i := 0; i < n; i++ {
			if _, err := db.Exec(`INSERT INTO crew_members (id, crew_id, user_id, role) VALUES (?, ?, ?, 'MEMBER')`,
				fmt.Sprintf("cm-%s-%d", crewID, i), crewID, memberIDs[i]); err != nil {
				t.Fatalf("insert crew_member %s/%d: %v", crewID, i, err)
			}
		}
	}
	addAgents := func(crewID, ws string, live, deleted int) {
		t.Helper()
		for i := 0; i < live+deleted; i++ {
			id := fmt.Sprintf("ag-%s-%d", crewID, i)
			seedAgentRow(t, db, id, ws, crewID, id, id, "WORKER")
			if i >= live {
				if _, err := db.Exec(`UPDATE agents SET deleted_at = '2026-01-01T00:00:00Z' WHERE id = ?`, id); err != nil {
					t.Fatalf("soft-delete agent %s: %v", id, err)
				}
			}
		}
	}
	// created_at is second-precision in production rows; stamp distinct
	// values so the ORDER BY created_at DESC, id DESC window is stable.
	stamp := func(crewID, ts string) {
		t.Helper()
		if _, err := db.Exec(`UPDATE crews SET created_at = ? WHERE id = ?`, ts, crewID); err != nil {
			t.Fatalf("stamp crew %s: %v", crewID, err)
		}
	}

	seedCrewRow(t, db, "crew-empty", wsID, "Empty", "empty")
	stamp("crew-empty", "2026-01-01T00:00:01Z")

	seedCrewRow(t, db, "crew-agents", wsID, "Agents Only", "agents-only")
	addAgents("crew-agents", wsID, 2, 1) // 1 soft-deleted → not counted
	stamp("crew-agents", "2026-01-01T00:00:02Z")

	seedCrewRow(t, db, "crew-members", wsID, "Members Only", "members-only")
	addMembers("crew-members", 2)
	stamp("crew-members", "2026-01-01T00:00:03Z")

	seedCrewRow(t, db, "crew-full", wsID, "Full", "full")
	addAgents("crew-full", wsID, 3, 1)
	addMembers("crew-full", 3)
	stamp("crew-full", "2026-01-01T00:00:04Z")

	// Soft-deleted crew — excluded from the listing even though it has
	// live children.
	seedCrewRow(t, db, "crew-gone", wsID, "Gone", "gone")
	addAgents("crew-gone", wsID, 2, 0)
	stamp("crew-gone", "2026-01-01T00:00:05Z")
	if _, err := db.Exec(`UPDATE crews SET deleted_at = '2026-01-02T00:00:00Z' WHERE id = 'crew-gone'`); err != nil {
		t.Fatalf("soft-delete crew: %v", err)
	}

	// Foreign workspace — must not leak.
	seedCrewRow(t, db, "crew-other", "ws-other", "Other", "other")
	addAgents("crew-other", "ws-other", 5, 0)
	addMembers("crew-other", 3)
	stamp("crew-other", "2026-01-01T00:00:06Z")

	h := NewCrewHandler(db, newTestLogger())
	req := httptest.NewRequest("GET", "/api/v1/crews", nil)
	req = req.WithContext(withWorkspace(req.Context(), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.List(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}

	var raw []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode body: %v (body=%s)", err, rr.Body.String())
	}

	// Ordering: created_at DESC, so newest workspace crew first.
	wantIDs := []string{"crew-full", "crew-members", "crew-agents", "crew-empty"}
	gotIDs := make([]string, 0, len(raw))
	for _, row := range raw {
		gotIDs = append(gotIDs, row["id"].(string))
	}
	if len(gotIDs) != len(wantIDs) {
		t.Fatalf("crew ids = %v, want %v", gotIDs, wantIDs)
	}
	for i := range wantIDs {
		if gotIDs[i] != wantIDs[i] {
			t.Fatalf("crew ids = %v, want %v", gotIDs, wantIDs)
		}
	}

	wantCounts := map[string][2]int{
		"crew-full":    {3, 3},
		"crew-members": {0, 2},
		"crew-agents":  {2, 0},
		"crew-empty":   {0, 0},
	}
	for _, row := range raw {
		id := row["id"].(string)
		counts, ok := row["_count"].(map[string]any)
		if !ok {
			t.Fatalf("crew %s: _count missing or wrong type: %#v", id, row["_count"])
		}
		gotAgents := int(counts["agents"].(float64))
		gotMembers := int(counts["members"].(float64))
		want := wantCounts[id]
		if gotAgents != want[0] || gotMembers != want[1] {
			t.Errorf("crew %s counts = (agents=%d, members=%d), want (agents=%d, members=%d)",
				id, gotAgents, gotMembers, want[0], want[1])
		}
		if got := row["workspace_id"].(string); got != wsID {
			t.Errorf("crew %s workspace_id = %q, want %q", id, got, wsID)
		}
	}

	// Response shape lock: the exact JSON key set of a fully-populated row.
	// crewResponse omitempty fields are absent when NULL, so use crew-empty
	// (all optional columns NULL) as the minimal shape.
	wantKeys := []string{
		"_count", "allow_private_endpoints", "allowed_domains", "avatar_style",
		"color", "container_cpus", "container_memory_mb", "container_ttl_hours",
		"created_at", "description", "icon", "id", "issue_prefix",
		"max_ephemeral_agents", "name", "network_mode", "slug", "updated_at",
		"workspace_id",
	}
	gotKeys := make([]string, 0, len(raw[3]))
	for k := range raw[3] {
		gotKeys = append(gotKeys, k)
	}
	sort.Strings(gotKeys)
	if len(gotKeys) != len(wantKeys) {
		t.Fatalf("crew-empty keys = %v, want %v", gotKeys, wantKeys)
	}
	for i := range wantKeys {
		if gotKeys[i] != wantKeys[i] {
			t.Fatalf("crew-empty keys = %v, want %v", gotKeys, wantKeys)
		}
	}

	// allowed_domains is always an array, never null.
	if _, ok := raw[3]["allowed_domains"].([]any); !ok {
		t.Errorf("allowed_domains = %#v, want []", raw[3]["allowed_domains"])
	}
}

// TestCrewListPaginationWithCounts locks that the counts stay attached to
// the right crew across a LIMIT/OFFSET window — the failure mode a join
// that multiplies rows would produce.
func TestCrewListPaginationWithCounts(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("crew-p%d", i)
		seedCrewRow(t, db, id, wsID, id, id)
		if _, err := db.Exec(`UPDATE crews SET created_at = ? WHERE id = ?`,
			fmt.Sprintf("2026-01-01T00:00:0%dZ", i), id); err != nil {
			t.Fatalf("stamp %s: %v", id, err)
		}
		// crew-pN gets N live agents.
		for j := 0; j < i; j++ {
			aid := fmt.Sprintf("ag-%s-%d", id, j)
			seedAgentRow(t, db, aid, wsID, id, aid, aid, "WORKER")
		}
	}

	h := NewCrewHandler(db, newTestLogger())
	req := httptest.NewRequest("GET", "/api/v1/crews?limit=2&offset=1", nil)
	req = req.WithContext(withWorkspace(req.Context(), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.List(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var got []crewResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2 (body=%s)", len(got), rr.Body.String())
	}
	// DESC order: p4, p3, p2, p1, p0 → offset 1, limit 2 = p3, p2.
	if got[0].ID != "crew-p3" || got[0].Count.Agents != 3 {
		t.Errorf("row 0 = %s/%d, want crew-p3/3", got[0].ID, got[0].Count.Agents)
	}
	if got[1].ID != "crew-p2" || got[1].Count.Agents != 2 {
		t.Errorf("row 1 = %s/%d, want crew-p2/2", got[1].ID, got[1].Count.Agents)
	}
}
