package api

import (
	"encoding/json"
	"fmt"
	"github.com/crewship-ai/crewship/internal/tsformat"
	"strings"
	"testing"
	"time"
)

func TestArtifactExpiredReceiptsDoNotPermanentlyBlockPublication(t *testing.T) {
	h, _, _, ws, user, page := artifactFixture(t)
	artifactExpect(t, artifactSave(t, h, ws, user, "<h1>Original</h1>", "original", 0), 201)
	execOrFatal(t, h.db, `WITH RECURSIVE n(v) AS (SELECT 1 UNION ALL SELECT v+1 FROM n WHERE v<10000) INSERT INTO page_artifact_mutation_receipts(page_id,mutation_id,revision,request_digest,created_at) SELECT ?,'expired-'||v,1000+v,'digest','2000-01-01T00:00:00.000Z' FROM n`, page)
	artifactExpect(t, artifactSave(t, h, ws, user, "<h1>Updated</h1>", "next", 1), 201)
}

func TestArtifactExpiredDiscussionReceiptsDoNotBlockNewReply(t *testing.T) {
	h, _, _, ws, user, page := artifactFixture(t)
	artifactExpect(t, artifactSave(t, h, ws, user, "<h1>Original</h1>", "original", 0), 201)
	response := artifactDiscussionCreate(t, h, ws, user, "Original comment", "thread", 1)
	artifactExpect(t, response, 201)
	thread := discussionThread(t, response)
	execOrFatal(t, h.db, `WITH RECURSIVE n(v) AS (SELECT 1 UNION ALL SELECT v+1 FROM n WHERE v<10000) INSERT INTO page_artifact_comment_mutations(id,page_id,thread_id,mutation_id,request_digest,actor_user_id,operation,result_version,created_at) SELECT 'expired-'||v,?,?,'expired-'||v,'digest',?,'reply',1,'2000-01-01T00:00:00.000Z' FROM n`, page, thread.ID, user)
	artifactExpect(t, artifactDiscussionCall(t, h, ws, user, "OWNER", "reply", thread.ID, map[string]any{"body": "New reply", "base_version": 1, "mutation_id": "next-reply"}), 201)
	// Thread creation has no version precondition: its original key must survive
	// compaction to prevent an old retry from creating a duplicate thread.
	artifactExpect(t, artifactDiscussionCreate(t, h, ws, user, "Original comment", "thread", 1), 200)
}

func TestArtifactRestoreProvenanceDoesNotPinEverySourceForever(t *testing.T) {
	h, _, clock, ws, user, page := artifactFixture(t)
	artifactExpect(t, artifactSave(t, h, ws, user, "<h1>Original</h1>", "original", 0), 201)
	var sha string
	if err := h.db.QueryRow(`SELECT sha256 FROM page_artifact_revisions WHERE page_id=?`, page).Scan(&sha); err != nil {
		t.Fatal(err)
	}
	for i := 2; i <= 200; i++ {
		execOrFatal(t, h.db, `INSERT INTO page_artifact_revisions(id,page_id,revision,sha256,mutation_id,request_digest,restored_from,created_at) VALUES(?,?,?,?,?,'digest',?,'2000-01-01T00:00:00.000Z')`, fmt.Sprint("restored-", i), page, i, sha, fmt.Sprint("restore-", i), i-1)
	}
	execOrFatal(t, h.db, `UPDATE page_artifacts SET head_rev=200 WHERE page_id=?`, page)
	clock.advance(2 * time.Hour)
	artifactExpect(t, artifactSave(t, h, ws, user, "<h1>New revision</h1>", "next", 200), 201)
}

func TestPageRequestRetentionKeepsActiveAndWaitingJobsAndReleasesOldPreviews(t *testing.T) {
	h, ws, user, _, _, _ := boardJobFixture(t)
	ids := map[string]string{}
	for _, status := range []string{"cancelled", "running", "ready_to_insert", "succeeded"} {
		body := boardRequestWithPreview(t, strings.Replace(boardDiagramRequest, "diagram-request", status, 1), boardPreviewPNG(t, 20, 20))
		w := boardJobCall(t, h, ws, user, "", body, h.CreateBoardRequest)
		artifactExpect(t, w, 202)
		var out struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		ids[status] = out.ID
		execOrFatal(t, h.db, `UPDATE page_board_requests SET status=?,updated_at='2000-01-01T00:00:00.000Z' WHERE id=?`, status, out.ID)
	}
	tx, err := h.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = compactPageRequestHistory(t.Context(), tx, ws, time.Now()); err != nil {
		t.Fatal(err)
	}
	for status, id := range ids {
		var n int
		if err = tx.QueryRow(`SELECT COUNT(*) FROM page_board_request_previews WHERE request_id=?`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		want := 0
		if status == "running" || status == "ready_to_insert" {
			want = 1
		}
		if n != want {
			t.Fatalf("%s preview count %d want %d", status, n, want)
		}
	}
}

func TestBoardUnusedFilesExpireAfterReplayAndUndoWindow(t *testing.T) {
	h, _, clock, ws, user, page := artifactFixture(t)
	artifactExpect(t, boardCall(t, h, ws, user, "OWNER", "report", "init", `{"mutation_id":"init"}`), 200)
	w := boardFileUpload(t, h, ws, user, "report", "unused", 0, boardRaster(t, false))
	artifactExpect(t, w, 201)
	file := boardFileID(t, w)
	// No scene references this import. It must remain for upload retries first.
	compact := func() {
		tx, err := h.db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if err = h.compactBoardHistory(t.Context(), tx, page); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	compact()
	artifactExpect(t, boardFileRead(t, h, ws, user, "report", file), 200)
	clock.advance(25 * time.Hour)
	compact()
	artifactExpect(t, boardFileRead(t, h, ws, user, "report", file), 404)
}

func TestBoardImageRetirementKeepsCurrentSceneAndRecentUndo(t *testing.T) {
	h, _, clock, ws, user, page := artifactFixture(t)
	artifactExpect(t, boardCall(t, h, ws, user, "OWNER", "report", "init", `{"mutation_id":"init"}`), 200)
	w := boardFileUpload(t, h, ws, user, "report", "image", 0, boardRaster(t, false))
	artifactExpect(t, w, 201)
	file := boardFileID(t, w)
	var element map[string]any
	if err := json.Unmarshal(boardAPIElement(t), &element); err != nil {
		t.Fatal(err)
	}
	element["id"] = "saved-image"
	element["type"] = "image"
	element["fileId"] = file
	element["status"] = "saved"
	element["scale"] = []int{1, 1}
	element["crop"] = nil
	raw, _ := json.Marshal(element)
	artifactExpect(t, boardCall(t, h, ws, user, "OWNER", "report", "batch", fmt.Sprintf(`{"mutation_id":"place","base_seq":1,"upserts":[{"expected_rev":0,"element":%s}]}`, raw)), 201)
	clock.advance(25 * time.Hour)
	compact := func() {
		tx, err := h.db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if err = h.compactBoardHistory(t.Context(), tx, page); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	compact()
	artifactExpect(t, boardFileRead(t, h, ws, user, "report", file), 200)
	var revision int64
	if err := h.db.QueryRow(`SELECT element_rev FROM page_board_elements WHERE board_page_id=? AND element_id='saved-image'`, page).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	artifactExpect(t, boardCall(t, h, ws, user, "OWNER", "report", "batch", fmt.Sprintf(`{"mutation_id":"remove","base_seq":2,"deletes":[{"id":"saved-image","expected_rev":%d}]}`, revision)), 201)
	clock.advance(time.Hour)
	compact()
	artifactExpect(t, boardFileRead(t, h, ws, user, "report", file), 200)
	clock.advance(24 * time.Hour)
	compact()
	artifactExpect(t, boardFileRead(t, h, ws, user, "report", file), 404)
}

func TestPageRetentionWorkerReleasesDetachedTerminalPreviewForGC(t *testing.T) {
	h, ws, user, page, _, _ := boardJobFixture(t)
	body := boardRequestWithPreview(t, strings.Replace(boardDiagramRequest, "diagram-request", "retention-preview", 1), boardPreviewPNG(t, 20, 20))
	w := boardJobCall(t, h, ws, user, "", body, h.CreateBoardRequest)
	artifactExpect(t, w, 202)
	var out struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	execOrFatal(t, h.db, `UPDATE page_board_requests SET status='cancelled',updated_at='2000-01-01T00:00:00.000Z' WHERE id=?`, out.ID)
	execOrFatal(t, h.db, `DELETE FROM pages WHERE id=?`, page)
	if n, err := reclaimAttachmentBlobs(t.Context(), h.db, h.artifactStoragePath, ws); err != nil || n != 0 {
		t.Fatalf("retained preview collected: %d %v", n, err)
	}
	if _, _, err := h.cleanupPageRetention(t.Context(), "", "", time.Now()); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM page_board_requests WHERE id=?`, out.ID).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("detached history remains: %d %v", remaining, err)
	}
	if n, err := reclaimAttachmentBlobs(t.Context(), h.db, h.artifactStoragePath, ws); err != nil || n != 1 {
		t.Fatalf("expired preview not released to GC: %d %v", n, err)
	}
}

func TestCompletedArtifactRequestsCannotSaturateRevisionRetention(t *testing.T) {
	h, ws, user, page := requestFixture(t)
	var sha string
	if err := h.db.QueryRow(`SELECT sha256 FROM page_artifact_revisions WHERE page_id=? AND revision=1`, page).Scan(&sha); err != nil {
		t.Fatal(err)
	}
	for rev := 2; rev <= 200; rev++ {
		execOrFatal(t, h.db, `INSERT INTO page_artifact_revisions(id,page_id,revision,sha256,mutation_id,request_digest,created_at) VALUES(?,?,?,?,?,'digest','2000-01-01T00:00:00Z')`, fmt.Sprint("completed-rev-", rev), page, rev, sha, fmt.Sprint("completed-rev-", rev))
	}
	execOrFatal(t, h.db, `UPDATE page_artifacts SET head_rev=200 WHERE page_id=?`, page)
	execOrFatal(t, h.db, `WITH RECURSIVE n(v) AS(SELECT 1 UNION ALL SELECT v+1 FROM n WHERE v<200) INSERT INTO page_artifact_requests(id,page_id,human_user_id,chat_id,agent_id,base_rev,submission_id,fingerprint,instruction,snapshot_json,receipt_json,status,result_revision,created_at,updated_at) SELECT 'completed-'||v,?,?,'artifact-chat','artifact-agent',v,'completed-'||v,'digest','done','{}','{}','succeeded',v,?,? FROM n`, page, user, tsformat.Format(time.Now()), tsformat.Format(time.Now()))
	artifactExpect(t, artifactSave(t, h, ws, user, "<h1>Next ordinary update</h1>", "after-completed-history", 200), 201)
	var count int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM page_artifact_requests WHERE page_id=?`, page).Scan(&count); err != nil || count != 200 {
		t.Fatalf("publication discarded request history: %d %v", count, err)
	}
}

func TestTerminalRequestHistoryCapacityKeepsRecentRetriesAndActiveJobs(t *testing.T) {
	h, ws, _, _, _, id := boardJobFixture(t)
	old := tsformat.Format(time.Now().Add(-48 * time.Hour))
	recent := tsformat.Format(time.Now())
	for i := 0; i < 210; i++ {
		stamp := old
		if i >= 205 {
			stamp = recent
		}
		clone := fmt.Sprint("capacity-history-", i)
		execOrFatal(t, h.db, `INSERT INTO page_board_requests(id,workspace_id,board_page_id,original_board_id,human_user_id,agent_id,chat_id,kind,base_seq,submission_id,fingerprint,instruction,selected_element_ids_json,selected_placement_ids_json,snapshot_json,target_json,dialect,converter_version,receipt_json,status,created_at,updated_at) SELECT ?,workspace_id,board_page_id,original_board_id,human_user_id,agent_id,chat_id,kind,base_seq,?,fingerprint,instruction,selected_element_ids_json,selected_placement_ids_json,snapshot_json,target_json,dialect,converter_version,receipt_json,'cancelled',?,? FROM page_board_requests WHERE id=?`, clone, clone, stamp, stamp, id)
	}
	for i := 0; i < 3; i++ {
		tx, err := h.db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if err = compactPageRequestHistory(t.Context(), tx, ws, time.Now()); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	var total, fresh, active int
	if err := h.db.QueryRow(`SELECT COUNT(*),COALESCE(SUM(updated_at=?),0),COALESCE(SUM(status='queued'),0) FROM page_board_requests`, recent).Scan(&total, &fresh, &active); err != nil {
		t.Fatal(err)
	}
	if total != 101 || fresh != 5 || active != 1 {
		t.Fatalf("history count=%d fresh=%d active=%d", total, fresh, active)
	}
}

func TestRequestRetentionPreservesCurrentDiagramSourceAndRecentUndo(t *testing.T) {
	h, workspace, user, page, token, id := boardJobFixture(t)
	execOrFatal(t, h.db, `UPDATE page_board_requests SET status='running',run_id='artifact-run' WHERE id=?`, id)
	artifactExpect(t, boardJobAgentCall(t, h, workspace, token, id, `{"mutation_id":"source","source":"flowchart TD\nA[Hello] --> B[World]","dialect":"flowchart-v1","converter_version":"2.2.2"}`, h.PublishBoardRequestMermaid), 201)
	var hash string
	if err := h.db.QueryRow(`SELECT source_hash FROM page_board_requests WHERE id=?`, id).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"mutation_id": "insert", "base_seq": 0, "source_hash": hash, "converter_version": "2.2.2", "upserts": []map[string]any{{"element": boardAgentElement(t, "converted", nil), "expected_rev": 0}}})
	artifactExpect(t, boardJobCall(t, h, workspace, user, id, string(raw), h.InsertBoardRequest), 201)
	now := h.clock.Now()
	execOrFatal(t, h.db, `UPDATE page_board_requests SET updated_at=? WHERE id=?`, tsformat.Format(now.Add(-100*24*time.Hour)), id)
	compact := func(at time.Time, want int) {
		tx, err := h.db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if err = compactPageRequestHistory(t.Context(), tx, workspace, at); err != nil {
			t.Fatal(err)
		}
		var n int
		if err = tx.QueryRow(`SELECT COUNT(*) FROM page_board_requests WHERE id=? AND mermaid_source!=''`, id).Scan(&n); err != nil || n != want {
			t.Fatalf("diagram source count=%d want=%d err=%v", n, want, err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	compact(now, 1)
	var revision int64
	if err := h.db.QueryRow(`SELECT element_rev FROM page_board_elements WHERE board_page_id=? AND element_id='converted'`, page).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	artifactExpect(t, boardCall(t, h, workspace, user, "OWNER", "report", "batch", fmt.Sprintf(`{"mutation_id":"remove-diagram","base_seq":1,"deletes":[{"id":"converted","expected_rev":%d}]}`, revision)), 201)
	compact(now.Add(time.Hour), 1)
	compact(now.Add(25*time.Hour), 0)
}
