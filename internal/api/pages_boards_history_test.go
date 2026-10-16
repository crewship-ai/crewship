package api

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

func TestBoardHistoryCompactionPreventsABAAndExpiredReplay(t *testing.T) {
	h, _, clock, ws, user, page := artifactFixture(t)
	artifactExpect(t, boardCall(t, h, ws, user, "OWNER", "report", "init", `{"mutation_id":"init"}`), 200)
	create := boardAgentBatch(t, "first", 0, 0, boardAgentElement(t, "a", nil))
	w := boardCall(t, h, ws, user, "OWNER", "report", "batch", create)
	artifactExpect(t, w, 201)
	var original map[string]any
	json.Unmarshal(w.Body.Bytes(), &original)
	comment := boardDiscussionCall(t, h, ws, user, "create", "", map[string]any{"anchor": map[string]any{"schema_version": 1, "type": "board-element", "board_seq": 1, "element_id": "a", "element_rev": 1}, "body": "Original anchor", "mutation_id": "discussion"})
	artifactExpect(t, comment, 201)
	artifactExpect(t, boardCall(t, h, ws, user, "OWNER", "report", "batch", `{"mutation_id":"delete","base_seq":1,"upserts":[],"deletes":[{"id":"a","expected_rev":1}]}`), 201)
	// Simulate receipts restored from a backup predating the audit ledger.
	execOrFatal(t, h.db, `DELETE FROM page_board_write_audit WHERE board_page_id=?`, page)
	clock.now = clock.now.Add(25 * time.Hour)
	artifactExpect(t, boardCall(t, h, ws, user, "OWNER", "report", "batch", boardAgentBatch(t, "fresh", 2, 0, boardAgentElement(t, "b", nil))), 201)
	var n int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM page_board_elements WHERE board_page_id=? AND element_id='a'`, page).Scan(&n); err != nil || n != 0 {
		t.Fatalf("expired tombstone remains %d %v", n, err)
	}
	w = boardCall(t, h, ws, user, "OWNER", "report", "batch", create)
	artifactExpect(t, w, 409)
	if !strings.Contains(w.Body.String(), `"history_expired"`) {
		t.Fatal(w.Body.String())
	}
	w = boardCall(t, h, ws, user, "OWNER", "report", "batch", boardAgentBatch(t, "intentional-new", 3, 0, boardAgentElement(t, "a", map[string]any{"x": 999})))
	artifactExpect(t, w, 201)
	if !strings.Contains(w.Body.String(), `"element_revs":{"a":4}`) {
		t.Fatal(w.Body.String())
	}
	artifactExpect(t, boardCall(t, h, ws, user, "OWNER", "report", "batch", boardAgentBatch(t, "old-undo", 4, 2, boardAgentElement(t, "a", nil))), 409)
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM page_board_write_audit WHERE board_page_id=?`, page).Scan(&n); err != nil || n != 5 {
		t.Fatalf("audit pruned/duplicated: %d %v", n, err)
	}
	var anchor string
	if err := h.db.QueryRow(`SELECT anchor_json FROM page_board_threads WHERE page_id=?`, page).Scan(&anchor); err != nil || !strings.Contains(anchor, `"element_rev":1`) {
		t.Fatalf("comment reanchored %s %v", anchor, err)
	}
	if original["undo_expires_at"] == nil {
		t.Fatal("missing server expiry")
	}
}
func TestBoardHistoryAuditAtomicFailureAndRetryDeadline(t *testing.T) {
	h, _, clock, ws, user, page := artifactFixture(t)
	artifactExpect(t, boardCall(t, h, ws, user, "OWNER", "report", "init", `{"mutation_id":"init"}`), 200)
	execOrFatal(t, h.db, `CREATE TRIGGER fail_board_audit BEFORE INSERT ON page_board_write_audit WHEN NEW.mutation_id='fail' BEGIN SELECT RAISE(ABORT,'audit unavailable');END`)
	artifactExpect(t, boardCall(t, h, ws, user, "OWNER", "report", "batch", boardAgentBatch(t, "fail", 0, 0, boardAgentElement(t, "a", nil))), 500)
	var seq int
	if err := h.db.QueryRow(`SELECT seq FROM page_boards WHERE page_id=?`, page).Scan(&seq); err != nil || seq != 0 {
		t.Fatalf("unaudited write committed %d %v", seq, err)
	}
	body := boardAgentBatch(t, "success", 0, 0, boardAgentElement(t, "a", nil))
	first := boardCall(t, h, ws, user, "OWNER", "report", "batch", body)
	artifactExpect(t, first, 201)
	var before map[string]any
	json.Unmarshal(first.Body.Bytes(), &before)
	clock.now = clock.now.Add(time.Hour)
	retry := boardCall(t, h, ws, user, "OWNER", "report", "batch", body)
	artifactExpect(t, retry, 200)
	var after map[string]any
	json.Unmarshal(retry.Body.Bytes(), &after)
	if before["undo_expires_at"] != after["undo_expires_at"] {
		t.Fatal("retry extended undo")
	}
	if fmt.Sprint(before["element_revs"]) != fmt.Sprint(after["element_revs"]) {
		t.Fatal("retry changed revision proof")
	}
}

func TestBoardHistoryCapacityRecoversAfterWindow(t *testing.T) {
	h, _, clock, ws, user, page := artifactFixture(t)
	artifactExpect(t, boardCall(t, h, ws, user, "OWNER", "report", "init", `{"mutation_id":"init"}`), 200)
	execOrFatal(t, h.db, `UPDATE page_boards SET seq=1 WHERE page_id=?`, page)
	for i := 0; i < 1000; i++ {
		execOrFatal(t, h.db, `INSERT INTO page_board_elements(board_page_id,element_id,element_rev,element_json,updated_seq,deleted_at) VALUES(?,?,1,NULL,1,?)`, page, fmt.Sprint("old-", i), tsformat.Format(clock.now))
	}
	body := boardAgentBatch(t, "after-window", 1, 0, boardAgentElement(t, "fresh", nil))
	w := boardCall(t, h, ws, user, "OWNER", "report", "batch", body)
	artifactExpect(t, w, 409)
	if !strings.Contains(w.Body.String(), `"code":"history_capacity"`) || !strings.Contains(w.Body.String(), tsformat.Format(clock.now.Add(24*time.Hour))) {
		t.Fatal(w.Body.String())
	}
	clock.now = clock.now.Add(25 * time.Hour)
	artifactExpect(t, boardCall(t, h, ws, user, "OWNER", "report", "batch", body), 201)
	var n int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM page_board_elements WHERE board_page_id=?`, page).Scan(&n); err != nil || n != 1 {
		t.Fatalf("capacity did not recover %d %v", n, err)
	}
}
