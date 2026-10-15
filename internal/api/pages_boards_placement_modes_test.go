package api

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

func placementModeBatch(id, mutation, mode string, seq, rev, source int64) string {
	return fmt.Sprintf(`{"mutation_id":%q,"base_seq":%d,"placement_modes":[{"id":%q,"expected_rev":%d,"mode":%q,"source_revision":%d}]}`, mutation, seq, id, rev, mode, source)
}
func TestBoardPlacementFollowLatestExplicitCASAndFixedUndo(t *testing.T) {
	h, ws, user, _, id := placementUndoFixture(t)
	artifactExpect(t, artifactSave(t, h, ws, user, "<p>Second</p>", "second", 1), 201)
	w := boardCall(t, h, ws, user, "OWNER", "board", "get", "")
	artifactExpect(t, w, 200)
	if !strings.Contains(w.Body.String(), `"mode":"fixed"`) || !strings.Contains(w.Body.String(), `"revision":1`) {
		t.Fatal(w.Body.String())
	}
	body := placementModeBatch(id, "follow", "follow_latest", 1, 1, 1)
	artifactExpect(t, boardCall(t, h, ws, user, "OWNER", "board", "batch", body), 201)
	artifactExpect(t, boardCall(t, h, ws, user, "OWNER", "board", "batch", body), 200)
	artifactExpect(t, artifactSave(t, h, ws, user, "<p>Third</p>", "third", 2), 201)
	w = boardCall(t, h, ws, user, "OWNER", "board", "get", "")
	artifactExpect(t, w, 200)
	var state struct {
		Seq        int64 `json:"seq"`
		Placements []struct {
			Rev    int64  `json:"placement_rev"`
			Mode   string `json:"mode"`
			Source struct {
				Revision int64 `json:"revision"`
				Pinned   int64 `json:"pinned_revision"`
			} `json:"source"`
		} `json:"placements"`
	}
	json.Unmarshal(w.Body.Bytes(), &state)
	if state.Seq != 2 || state.Placements[0].Rev != 2 || state.Placements[0].Source.Revision != 3 || state.Placements[0].Source.Pinned != 1 {
		t.Fatal(w.Body.String())
	}
	artifactExpect(t, boardCall(t, h, ws, user, "OWNER", "board", "batch", placementModeBatch(id, "stale", "fixed", 2, 1, 1)), 409)
	artifactExpect(t, boardCall(t, h, ws, user, "OWNER", "board", "batch", placementModeBatch(id, "undo", "fixed", 2, 2, 1)), 201)
	w = boardCall(t, h, ws, user, "OWNER", "board", "get", "")
	artifactExpect(t, w, 200)
	if !strings.Contains(w.Body.String(), `"revision":1`) || !strings.Contains(w.Body.String(), `"mode":"fixed"`) {
		t.Fatal(w.Body.String())
	}
}
func TestBoardPlacementModeSourceACLAndAtomicity(t *testing.T) {
	h, ws, user, source, id := placementUndoFixture(t)
	pagesSeedUser(t, h, ws, "mode-editor", "mode-editor@example.invalid", "MEMBER")
	pagesGrant(t, h, ws, user, "board", `{"subject_type":"user","subject":"mode-editor@example.invalid","level":"write"}`)
	artifactExpect(t, boardCall(t, h, ws, "mode-editor", "MEMBER", "board", "batch", placementModeBatch(id, "forbidden", "follow_latest", 1, 1, 1)), 403)
	w := boardCall(t, h, ws, "mode-editor", "MEMBER", "board", "get", "")
	artifactExpect(t, w, 200)
	if strings.Contains(w.Body.String(), `"source"`) || strings.Contains(w.Body.String(), source) {
		t.Fatal("source metadata leaked")
	}
	pagesGrant(t, h, ws, user, "report", `{"subject_type":"user","subject":"mode-editor@example.invalid","level":"read"}`)
	artifactExpect(t, boardCall(t, h, ws, "mode-editor", "MEMBER", "board", "batch", placementModeBatch(id, "allowed", "follow_latest", 1, 1, 1)), 201)
	execOrFatal(t, h.db, `DELETE FROM page_grants WHERE page_id=? AND subject_type='user' AND subject_id='mode-editor'`, source)
	artifactExpect(t, boardCall(t, h, ws, "mode-editor", "MEMBER", "board", "batch", placementModeBatch(id, "revoked", "fixed", 2, 2, 1)), 403)
	w = boardCall(t, h, ws, "mode-editor", "MEMBER", "board", "get", "")
	artifactExpect(t, w, 200)
	if strings.Contains(w.Body.String(), `"source"`) {
		t.Fatal("revoked source metadata leaked")
	}
	artifactExpect(t, boardCall(t, h, ws, user, "OWNER", "board", "batch", placementModeBatch(id, "missing-revision", "fixed", 2, 2, 999)), 409)
	invalid := strings.TrimSuffix(placementModeBatch(id, "atomic", "fixed", 2, 2, 1), "}") + `,"placement_deletes":[{"id":"missing","expected_rev":1}]}`
	artifactExpect(t, boardCall(t, h, ws, user, "OWNER", "board", "batch", invalid), 409)
	var mode string
	var rev int
	if err := h.db.QueryRow(`SELECT mode,placement_rev FROM page_board_artifact_placements WHERE id=?`, id).Scan(&mode, &rev); err != nil || mode != "follow_latest" || rev != 2 {
		t.Fatalf("partial mutation %s %d %v", mode, rev, err)
	}
	execOrFatal(t, h.db, `DELETE FROM pages WHERE id=?`, source)
	w = boardCall(t, h, ws, user, "OWNER", "board", "get", "")
	artifactExpect(t, w, 200)
	if strings.Contains(w.Body.String(), `"source"`) {
		t.Fatal("deletedsource leaked")
	}
	artifactExpect(t, boardCall(t, h, ws, user, "OWNER", "board", "batch", placementModeBatch(id, "deleted", "fixed", 2, 2, 1)), 403)
}

func TestBoardFollowLatestJobFreezesResolvedSourceRevision(t *testing.T) {
	h, ws, user, board, _, _ := boardJobFixture(t)
	execOrFatal(t, h.db, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('follow-job-source',?,'follow-job-source','Source',?,'{}')`, ws, user)
	execOrFatal(t, h.db, `INSERT INTO page_artifacts(page_id,head_rev) VALUES('follow-job-source',2)`)
	for i := 1; i <= 3; i++ {
		execOrFatal(t, h.db, `INSERT INTO page_artifact_revisions(id,page_id,revision,sha256,mutation_id,request_digest,created_at) VALUES(?,'follow-job-source',?,'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',?,'digest','2020-01-01T00:00:00.000000000Z')`, fmt.Sprint("follow-job-", i), i, fmt.Sprint("follow-job-", i))
	}
	execOrFatal(t, h.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('follow-job-source','agent','artifact-agent','read',?)`, user)
	execOrFatal(t, h.db, `INSERT INTO page_board_artifact_placements(id,board_page_id,source_page_id,source_revision,x,y,width,height,updated_seq,mode) VALUES('follow-job-pin',?,'follow-job-source',1,0,0,100,100,0,'follow_latest')`, board)
	body := strings.Replace(boardDiagramRequest, `"submission_id":"diagram-request"`, `"submission_id":"follow-job"`, 1)
	body = strings.Replace(body, `"selected_placement_ids":[]`, `"selected_placement_ids":["follow-job-pin"]`, 1)
	w := boardJobCall(t, h, ws, user, "", body, h.CreateBoardRequest)
	artifactExpect(t, w, 202)
	var result struct {
		ID string `json:"id"`
	}
	json.Unmarshal(w.Body.Bytes(), &result)
	execOrFatal(t, h.db, `UPDATE page_artifacts SET head_rev=3 WHERE page_id='follow-job-source'`)
	artifactExpect(t, boardJobCall(t, h, ws, user, "", body, h.CreateBoardRequest), 202)
	var revision int
	if err := h.db.QueryRow(`SELECT source_revision FROM page_board_request_sources WHERE request_id=?`, result.ID).Scan(&revision); err != nil || revision != 2 {
		t.Fatalf("job source drifted to new head %d %v", revision, err)
	}
}

func TestBoardModeUndoPinsOldBaselineUntilReceiptExpiry(t *testing.T) {
	h, ws, user, source, id := placementUndoFixture(t)
	artifactExpect(t, artifactSave(t, h, ws, user, "<p>Second</p>", "second", 1), 201)
	artifactExpect(t, boardCall(t, h, ws, user, "OWNER", "board", "batch", placementModeBatch(id, "new-baseline", "fixed", 1, 1, 2)), 201)
	for i := 3; i <= 200; i++ {
		execOrFatal(t, h.db, `INSERT INTO page_artifact_revisions(id,page_id,revision,sha256,mutation_id,request_digest,created_at) VALUES(?,?,?,'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',?,'digest','2020-01-01T00:00:00.000000000Z')`, fmt.Sprint("mode-pressure-", i), source, i, fmt.Sprint("mode-pressure-", i))
	}
	execOrFatal(t, h.db, `UPDATE page_artifacts SET head_rev=200 WHERE page_id=?`, source)
	tx, err := h.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = reserveArtifactRevisionAt(t.Context(), tx, source, h.clock.Now()); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var n int
	if err = h.db.QueryRow(`SELECT COUNT(*) FROM page_artifact_revisions WHERE page_id=? AND revision=1`, source).Scan(&n); err != nil || n != 1 {
		t.Fatalf("undo source pruned %d %v", n, err)
	}
	artifactExpect(t, boardCall(t, h, ws, user, "OWNER", "board", "batch", placementModeBatch(id, "undo-baseline", "follow_latest", 2, 2, 1)), 201)
	h.clock.(*pagesFakeClock).now = h.clock.Now().Add(25 * time.Hour)
	execOrFatal(t, h.db, `INSERT INTO page_artifact_revisions(id,page_id,revision,sha256,mutation_id,request_digest,created_at) VALUES('mode-refill',?,201,'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','mode-refill','digest','2020-01-01T00:00:00.000000000Z')`, source)
	execOrFatal(t, h.db, `UPDATE page_artifacts SET head_rev=201 WHERE page_id=?`, source)
	tx, err = h.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = reserveArtifactRevisionAt(t.Context(), tx, source, h.clock.Now()); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(`SELECT COUNT(*) FROM page_artifact_revisions WHERE page_id=? AND revision=2`, source).Scan(&n); err != nil || n != 0 {
		t.Fatalf("expired baseline pin remains %d %v", n, err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	// No Board write ran before the artifact reservation: expiry must release
	// pins even while the Board is idle, then normal compaction removes metadata.
	artifactExpect(t, boardCall(t, h, ws, user, "OWNER", "board", "batch", fmt.Sprintf(`{"mutation_id":"after-window","base_seq":3,"placement_updates":[{"id":%q,"expected_rev":3,"x":11,"y":21,"width":300,"height":200}]}`, id)), 201)
	if err = h.db.QueryRow(`SELECT COUNT(*) FROM page_board_source_history`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("undo pins did not expire %d %v", n, err)
	}

}

func TestBoardModeUndoHistoryCapacityCountsOnlyNewReferences(t *testing.T) {
	h, ws, user, source, id := placementUndoFixture(t)
	artifactExpect(t, artifactSave(t, h, ws, user, "<p>Second</p>", "second", 1), 201)
	var board, stamp string
	if err := h.db.QueryRow(`SELECT board_page_id FROM page_board_artifact_placements WHERE id=?`, id).Scan(&board); err != nil {
		t.Fatal(err)
	}
	stamp = tsformat.Format(h.clock.Now())
	execOrFatal(t, h.db, `WITH RECURSIVE n(x) AS(SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<99) INSERT INTO page_board_artifact_placements(id,board_page_id,source_page_id,source_revision,x,y,width,height,updated_seq,deleted_at) SELECT 'history-placement-'||x,?,?,1,0,0,100,100,1,? FROM n`, board, source, stamp)
	execOrFatal(t, h.db, `WITH RECURSIVE n(x) AS(SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<100) INSERT INTO page_board_mutations(board_page_id,mutation_id,fingerprint,result_seq,created_at) SELECT ?,'history-receipt-'||x,'digest',1,? FROM n`, board, stamp)
	execOrFatal(t, h.db, `INSERT INTO page_board_source_history(board_page_id,mutation_id,placement_id,source_page_id,source_revision) SELECT m.board_page_id,m.mutation_id,p.id,p.source_page_id,p.source_revision FROM page_board_mutations m JOIN page_board_artifact_placements p ON p.board_page_id=m.board_page_id WHERE m.board_page_id=? AND m.mutation_id LIKE 'history-receipt-%'`, board)
	w := boardCall(t, h, ws, user, "OWNER", "board", "batch", placementModeBatch(id, "full-history", "fixed", 1, 1, 2))
	artifactExpect(t, w, 409)
	if !strings.Contains(w.Body.String(), `"code":"history_capacity"`) || !strings.Contains(w.Body.String(), tsformat.Format(h.clock.Now().Add(24*time.Hour))) {
		t.Fatal(w.Body.String())
	}
	// Same-baseline mode changes allocate no history references and remain usable.
	artifactExpect(t, boardCall(t, h, ws, user, "OWNER", "board", "batch", placementModeBatch(id, "no-new-reference", "follow_latest", 1, 1, 1)), 201)
	h.clock.(*pagesFakeClock).now = h.clock.Now().Add(25 * time.Hour)
	artifactExpect(t, boardCall(t, h, ws, user, "OWNER", "board", "batch", placementModeBatch(id, "after-expiry", "fixed", 2, 2, 2)), 201)
}
