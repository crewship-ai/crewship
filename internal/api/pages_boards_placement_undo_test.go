package api

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

func placementUndoFixture(t *testing.T) (*PageHandler, string, string, string, string) {
	t.Helper()
	h, _, _, workspace, user, source := artifactFixture(t)
	artifactExpect(t, artifactSave(t, h, workspace, user, "<p>Private source</p>", "source", 0), 201)
	create := pagesGrantRequest(t, "POST", "/", workspace, user, "OWNER", `{"slug":"board","name":"Board","panels":[],"allow_empty":true}`)
	rr := httptest.NewRecorder()
	h.Create(rr, create)
	artifactExpect(t, rr, 201)
	artifactExpect(t, boardCall(t, h, workspace, user, "OWNER", "board", "init", `{"mutation_id":"init"}`), 200)
	rr = boardCall(t, h, workspace, user, "OWNER", "board", "placement", `{"mutation_id":"pin","base_seq":0,"source_page_slug":"report","source_revision":1,"x":10,"y":20,"width":300,"height":200}`)
	artifactExpect(t, rr, 201)
	var result struct {
		ID string `json:"placement_id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return h, workspace, user, source, result.ID
}
func placementDeleteForTest(t *testing.T, h *PageHandler, workspace, user, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := pagesGrantRequest(t, "DELETE", "/", workspace, user, "MEMBER", body)
	r.SetPathValue("slug", "board")
	r.SetPathValue("placementId", id)
	rr := httptest.NewRecorder()
	h.DeleteBoardPlacement(rr, r)
	return rr
}
func TestBoardPlacementUndoOpaqueTombstonesAndABA(t *testing.T) {
	h, workspace, user, _, id := placementUndoFixture(t)
	body := `{"mutation_id":"delete","base_seq":1,"expected_rev":1}`
	rr := placementDeleteForTest(t, h, workspace, user, id, body)
	artifactExpect(t, rr, 200)
	if !strings.Contains(rr.Body.String(), fmt.Sprintf(`"%s":2`, id)) {
		t.Fatal("missing inverse revision receipt: " + rr.Body.String())
	}
	retry := placementDeleteForTest(t, h, workspace, user, id, body)
	artifactExpect(t, retry, 200)
	if retry.Body.String() != rr.Body.String() {
		t.Fatal("delete retry changed receipt")
	}
	rr = boardCall(t, h, workspace, user, "OWNER", "board", "get", "")
	artifactExpect(t, rr, 200)
	var snapshot struct {
		Placements []map[string]any `json:"placements"`
		Tombstones []map[string]any `json:"placement_tombstones"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Placements) != 0 || len(snapshot.Tombstones) != 1 || len(snapshot.Tombstones[0]) != 3 || snapshot.Tombstones[0]["id"] != id || snapshot.Tombstones[0]["placement_rev"] != float64(2) {
		t.Fatalf("tombstone disclosure %+v", snapshot)
	}
	restore := fmt.Sprintf(`{"mutation_id":"restore","base_seq":2,"placement_restores":[{"id":"%s","expected_rev":2}]}`, id)
	artifactExpect(t, boardCall(t, h, workspace, user, "OWNER", "board", "batch", restore), 201)
	artifactExpect(t, boardCall(t, h, workspace, user, "OWNER", "board", "batch", restore), 200)
	artifactExpect(t, placementDeleteForTest(t, h, workspace, user, id, `{"mutation_id":"delete-again","base_seq":3,"expected_rev":3}`), 200)
	// A fresh current board sequence cannot authorize undo of an older tombstone
	// after another writer restored and removed that same placement again.
	stale := fmt.Sprintf(`{"mutation_id":"stale-undo","base_seq":4,"placement_restores":[{"id":"%s","expected_rev":2}]}`, id)
	artifactExpect(t, boardCall(t, h, workspace, user, "OWNER", "board", "batch", stale), 409)
	fresh := fmt.Sprintf(`{"mutation_id":"fresh-undo","base_seq":4,"placement_restores":[{"id":"%s","expected_rev":4}]}`, id)
	artifactExpect(t, boardCall(t, h, workspace, user, "OWNER", "board", "batch", fresh), 201)
}
func TestBoardPlacementUndoRechecksSourceAndIsAtomic(t *testing.T) {
	h, workspace, user, source, id := placementUndoFixture(t)
	artifactExpect(t, placementDeleteForTest(t, h, workspace, user, id, `{"mutation_id":"delete","base_seq":1}`), 200)
	pagesSeedUser(t, h, workspace, "source-owner", "source-owner@example.com", "MEMBER")
	execOrFatal(t, h.db, `UPDATE pages SET owner_user_id='source-owner' WHERE id=?`, source)
	execOrFatal(t, h.db, `UPDATE workspace_members SET role='MEMBER' WHERE workspace_id=? AND user_id=?`, workspace, user)
	// The Board remains this human's, but the independent source Page is private.
	raw := boardAgentElement(t, "new-human", nil)
	restore := fmt.Sprintf(`{"mutation_id":"source-restore","base_seq":2,"upserts":[{"element":%s,"expected_rev":0}],"placement_restores":[{"id":"%s","expected_rev":2}]}`, raw, id)
	artifactExpect(t, boardCall(t, h, workspace, user, "MEMBER", "board", "batch", restore), 403)
	var count int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM page_board_elements WHERE element_id='new-human'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial drawing committed %d %v", count, err)
	}
	pagesGrant(t, h, workspace, "source-owner", "report", fmt.Sprintf(`{"subject_type":"user","subject":"%s","level":"read"}`, userEmailForBoardUndo(t, h, user)))
	artifactExpect(t, boardCall(t, h, workspace, user, "MEMBER", "board", "batch", restore), 201)
}
func userEmailForBoardUndo(t *testing.T, h *PageHandler, user string) string {
	t.Helper()
	var email string
	if err := h.db.QueryRow(`SELECT email FROM users WHERE id=?`, user).Scan(&email); err != nil {
		t.Fatal(err)
	}
	return email
}
func TestBoardPlacementUndoDeletedSourceRemainsGeneric(t *testing.T) {
	h, workspace, user, source, id := placementUndoFixture(t)
	artifactExpect(t, placementDeleteForTest(t, h, workspace, user, id, `{"mutation_id":"delete","base_seq":1}`), 200)
	execOrFatal(t, h.db, `DELETE FROM pages WHERE id=?`, source)
	restore := fmt.Sprintf(`{"mutation_id":"restore","base_seq":2,"placement_restores":[{"id":"%s","expected_rev":2}]}`, id)
	artifactExpect(t, boardCall(t, h, workspace, user, "OWNER", "board", "batch", restore), 201)
	rr := boardCall(t, h, workspace, user, "OWNER", "board", "get", "")
	artifactExpect(t, rr, 200)
	if strings.Contains(rr.Body.String(), `"source"`) || strings.Contains(rr.Body.String(), "Private source") || !strings.Contains(rr.Body.String(), `"x":10`) {
		t.Fatal("deleted source restored data/grant or lost geometry: " + rr.Body.String())
	}
}
func TestBoardPlacementUndoRequiresTombstoneRevision(t *testing.T) {
	h, workspace, user, _, id := placementUndoFixture(t)
	artifactExpect(t, placementDeleteForTest(t, h, workspace, user, id, `{"mutation_id":"delete","base_seq":1}`), 200)
	for _, expected := range []string{"", `,"expected_rev":null`, `,"expected_rev":0`} {
		body := fmt.Sprintf(`{"mutation_id":"bad","base_seq":2,"placement_restores":[{"id":"%s"%s}]}`, id, expected)
		artifactExpect(t, boardCall(t, h, workspace, user, "OWNER", "board", "batch", body), 400)
	}
}
