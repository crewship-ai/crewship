package api

// Pages — the limits half of §10b.3, proved where it actually lands.
//
// The unit tests in internal/pages own the arithmetic (what the floor is, when
// a bucket refills, what Retry-After should say). What only a test with a
// database can show is the part the PRD is emphatic about: that the floor is
// enforced BY THE WRITE, so it holds "regardless of how many processes are
// serving". A limiter that lives only in a process is a limiter that multiplies
// by the replica count.

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/pages"
)

// panelRowID resolves a panel's primary key, which is what the ring and the
// floor are keyed by (the author-chosen panel_id is only unique per page).
func panelRowID(t *testing.T, h *PageHandler, slug, panelID string) string {
	t.Helper()
	var id string
	if err := h.db.QueryRow(`
		SELECT p.id FROM page_panels p
		JOIN pages g ON g.id = p.page_id
		WHERE g.slug = ? AND p.panel_id = ?`, slug, panelID).Scan(&id); err != nil {
		t.Fatalf("resolve panel row id: %v", err)
	}
	return id
}

func ringSize(t *testing.T, h *PageHandler, panelRow string) int {
	t.Helper()
	var n int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM page_panel_data WHERE panel_id = ?`, panelRow).Scan(&n); err != nil {
		t.Fatalf("count ring rows: %v", err)
	}
	return n
}

// TestPagesPush_TheFloorLeavesOneRowNotTwo is §10b.3's second layer.
//
// "Panel push limits must therefore also be enforced where the write lands — a
// cheap produced_at check against the panel's minimum interval, in the same
// transaction — so the floor holds regardless of how many processes are
// serving."
//
// The token bucket cannot be what refuses the second push here: its burst is 30
// and this is push number two. The only thing standing between these two
// requests is the condition attached to the INSERT.
func TestPagesPush_TheFloorLeavesOneRowNotTwo(t *testing.T) {
	h, _, clock, wsID, userID := newPagesFixture(t)
	pagesCreate(t, h, wsID, userID, "fleet-201")
	row := panelRowID(t, h, "fleet-201", "sluzby")

	if rr := pagesPush(t, h, wsID, userID, "OWNER", "fleet-201", "sluzby", pagesStatusPayload); rr.Code != http.StatusOK {
		t.Fatalf("first push: status = %d, want 200, body: %s", rr.Code, rr.Body.String())
	}

	rr := pagesPush(t, h, wsID, userID, "OWNER", "fleet-201", "sluzby", pagesStatusPayload)
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("second push inside the minimum interval: status = %d, want 429, body: %s",
			rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Retry-After"); got == "" {
		t.Error("429 without Retry-After — §10b.3 asks for both, and a producer with no wait retries immediately")
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("429 body is not JSON: %v", err)
	}
	if scope, _ := body["scope"].(string); scope != string(pages.ScopePanel) {
		t.Errorf("scope = %q, want %q", scope, pages.ScopePanel)
	}

	if n := ringSize(t, h, row); n != 1 {
		t.Fatalf("the ring holds %d rows after two pushes inside the minimum interval, want 1 — "+
			"a refused push that still writes is not a limit", n)
	}

	// And the floor opens exactly when it says it does.
	clock.advance(pages.ConfiguredPushLimits().MinInterval())
	if rr := pagesPush(t, h, wsID, userID, "OWNER", "fleet-201", "sluzby", pagesStatusPayload); rr.Code != http.StatusOK {
		t.Fatalf("push one interval later: status = %d, want 200, body: %s", rr.Code, rr.Body.String())
	}
	if n := ringSize(t, h, row); n != 2 {
		t.Fatalf("the ring holds %d rows, want 2 — the floor is refusing pushes it should admit", n)
	}
}

// TestPagesPush_RingHonoursWorkspaceRetention proves the age cut reads
// workspaces.page_retention_days rather than a compiled-in 7 days (§10b.3,
// following the run_retention_days convention).
//
// The push itself is what applies it: the ring is bounded by the write that
// grows it, not by a sweep that might not run.
func TestPagesPush_RingHonoursWorkspaceRetention(t *testing.T) {
	h, _, clock, wsID, userID := newPagesFixture(t)
	pagesCreate(t, h, wsID, userID, "fleet-201")
	row := panelRowID(t, h, "fleet-201", "sluzby")

	// Two payloads already in the ring: one outside a one-day window, one
	// inside it. Both are inside the 7-day default, so the default keeps them.
	seed := func(seq int64, age time.Duration) {
		t.Helper()
		if _, err := h.db.Exec(`
			INSERT INTO page_panel_data (panel_id, seq, payload_json, produced_at, producer_run_id, state)
			VALUES (?, ?, ?, ?, NULL, 'ok')`,
			row, seq, pagesStatusPayload, clock.now.Add(-age).UTC().Format(time.RFC3339)); err != nil {
			t.Fatalf("seed ring row: %v", err)
		}
	}
	seed(1, 48*time.Hour)
	seed(2, 12*time.Hour)

	if _, err := h.db.Exec(`UPDATE workspaces SET page_retention_days = 1 WHERE id = ?`, wsID); err != nil {
		t.Fatalf("set page_retention_days: %v", err)
	}

	if rr := pagesPush(t, h, wsID, userID, "OWNER", "fleet-201", "sluzby", pagesStatusPayload); rr.Code != http.StatusOK {
		t.Fatalf("push: status = %d, want 200, body: %s", rr.Code, rr.Body.String())
	}

	// The 48-hour payload is outside the workspace's window and goes; the
	// 12-hour one stays; the push itself stays. Under the instance default all
	// three would survive, which is what makes this a test of the column.
	var kept []int64
	rows, err := h.db.Query(`SELECT seq FROM page_panel_data WHERE panel_id = ? ORDER BY seq`, row)
	if err != nil {
		t.Fatalf("read ring: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var seq int64
		if err := rows.Scan(&seq); err != nil {
			t.Fatalf("scan seq: %v", err)
		}
		kept = append(kept, seq)
	}
	if len(kept) != 2 || kept[0] != 2 || kept[1] != 3 {
		t.Fatalf("ring holds %v, want [2 3]: seq 1 is 48h old and the workspace keeps one day", kept)
	}
}

// The newest payload survives the age cut whatever the workspace sets, so a
// producer dead for eight days still renders as "stale, last value <date>"
// rather than "never produced" (§9b.4 rows 2 and 4). A per-workspace window is
// exactly the setting that would break this if it were applied naively.
func TestPagesPush_RetentionNeverDeletesTheLastKnownValue(t *testing.T) {
	h, _, clock, wsID, userID := newPagesFixture(t)
	pagesCreate(t, h, wsID, userID, "fleet-201")
	row := panelRowID(t, h, "fleet-201", "sluzby")

	if _, err := h.db.Exec(`UPDATE workspaces SET page_retention_days = 1 WHERE id = ?`, wsID); err != nil {
		t.Fatalf("set page_retention_days: %v", err)
	}
	if rr := pagesPush(t, h, wsID, userID, "OWNER", "fleet-201", "sluzby", pagesStatusPayload); rr.Code != http.StatusOK {
		t.Fatalf("push: status = %d, want 200", rr.Code)
	}

	// The producer dies. Eight days later something else on the page pushes,
	// which runs this panel's eviction... except that eviction only runs for
	// the panel being written, so read the panel back instead: the state is
	// what a viewer would see.
	clock.advance(8 * 24 * time.Hour)
	if n := ringSize(t, h, row); n != 1 {
		t.Fatalf("ring holds %d rows, want 1", n)
	}
	doc := pagesGet(t, h, wsID, userID, "OWNER", "fleet-201")
	panel := pagesPanel(t, doc, "sluzby")
	if state, _ := panel["state"].(string); state != string(pages.StateStale) {
		t.Errorf("panel state after eight silent days = %q, want %q — the last known value is what makes "+
			"the difference between 'stale since' and 'never produced'", state, pages.StateStale)
	}
}
