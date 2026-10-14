package api

import (
	"fmt"
	"github.com/crewship-ai/crewship/internal/tsformat"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/pages"
)

func seedStorageBoardPin(t *testing.T, h *PageHandler, ws, user, source string, revision int) {
	t.Helper()
	execOrFatal(t, h.db, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('storage-board',?,'storage-board','Storage board',?,'{}')`, ws, user)
	execOrFatal(t, h.db, `INSERT INTO page_boards(page_id,seq,format) VALUES('storage-board',1,?)`, pages.BoardFormat)
	execOrFatal(t, h.db, `INSERT INTO page_board_artifact_placements(id,board_page_id,source_page_id,source_revision,x,y,width,height,updated_seq,author_user_id) VALUES('storage-placement','storage-board',?,?,0,0,300,200,1,?)`, source, revision, user)
}

func TestBoardArtifactPinProtectsRevisionUntilBoardDeleted(t *testing.T) {
	testBoardStoredPinRetention(t, "fixed")
}
func TestBoardFollowLatestBaselineProtectsRevisionUntilBoardDeleted(t *testing.T) {
	testBoardStoredPinRetention(t, "follow_latest")
}
func testBoardStoredPinRetention(t *testing.T, mode string) {
	h, _, _, ws, user, page := artifactFixture(t)
	execOrFatal(t, h.db, `INSERT INTO page_artifacts(page_id,head_rev) VALUES(?,200)`, page)
	for i := 1; i <= 200; i++ {
		execOrFatal(t, h.db, `INSERT INTO page_artifact_revisions(id,page_id,revision,sha256,mutation_id,request_digest,created_at) VALUES(?,?,?,'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',?,'digest','2020-01-01T00:00:00.000Z')`, fmt.Sprint("board-pin-", i), page, i, fmt.Sprint("board-pin-", i))
	}
	seedStorageBoardPin(t, h, ws, user, page, 1)
	execOrFatal(t, h.db, `UPDATE page_board_artifact_placements SET mode=? WHERE id='storage-placement'`, mode)
	tx, err := h.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = reserveArtifactRevision(t.Context(), tx, page); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var n int
	if err = h.db.QueryRow(`SELECT COUNT(*) FROM page_artifact_revisions WHERE page_id=? AND revision=1`, page).Scan(&n); err != nil || n != 1 {
		t.Fatalf("board pin pruned: %d %v", n, err)
	}
	if err = h.db.QueryRow(`SELECT COUNT(*) FROM page_artifact_revisions WHERE page_id=? AND revision=2`, page).Scan(&n); err != nil || n != 0 {
		t.Fatalf("oldest unpinned not pruned: %d %v", n, err)
	}
	execOrFatal(t, h.db, `DELETE FROM pages WHERE id='storage-board'`)
	// Refill the reserved slot; deleting the board releases only its source pin.
	execOrFatal(t, h.db, `INSERT INTO page_artifact_revisions(id,page_id,revision,sha256,mutation_id,request_digest,created_at) VALUES('refill',?,201,'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','refill','digest','2020-01-01T00:00:00.000Z')`, page)
	execOrFatal(t, h.db, `UPDATE page_artifacts SET head_rev=201 WHERE page_id=?`, page)
	tx, err = h.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = reserveArtifactRevision(t.Context(), tx, page); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(`SELECT COUNT(*) FROM page_artifact_revisions WHERE page_id=? AND revision=1`, page).Scan(&n); err != nil || n != 0 {
		t.Fatalf("deleted board kept pin: %d %v", n, err)
	}
}

func TestBoardDeletedArtifactPlaceholderDoesNotRetainBlob(t *testing.T) {
	h, _, _, ws, user, page := artifactFixture(t)
	artifactExpect(t, artifactSave(t, h, ws, user, "<h1>Pinned board source</h1>", "source", 0), 201)
	seedStorageBoardPin(t, h, ws, user, page, 1)
	if n, err := reclaimAttachmentBlobs(t.Context(), h.db, h.artifactStoragePath, ws); err != nil || n != 0 {
		t.Fatalf("live source collected: %d %v", n, err)
	}
	execOrFatal(t, h.db, `DELETE FROM pages WHERE id=?`, page)
	var placeholders int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM page_board_artifact_placements WHERE id='storage-placement' AND source_page_id IS NULL AND source_revision IS NULL`).Scan(&placeholders); err != nil || placeholders != 1 {
		t.Fatalf("generic placeholder lost: %d %v", placeholders, err)
	}
	if n, err := reclaimAttachmentBlobs(t.Context(), h.db, h.artifactStoragePath, ws); err != nil || n != 1 {
		t.Fatalf("deleted artifact blob retained: %d %v", n, err)
	}
}

func TestBoardRecentRequestHistoryPinsSourceAndResultRevisions(t *testing.T) {
	for _, kind := range []string{"source", "result"} {
		t.Run(kind, func(t *testing.T) {
			h, _, _, ws, user, page := artifactFixture(t)
			execOrFatal(t, h.db, `INSERT INTO page_artifacts(page_id,head_rev) VALUES(?,200)`, page)
			for i := 1; i <= 200; i++ {
				execOrFatal(t, h.db, `INSERT INTO page_artifact_revisions(id,page_id,revision,sha256,mutation_id,request_digest,created_at) VALUES(?,?,?,'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',?,'digest','2020-01-01T00:00:00.000Z')`, fmt.Sprint("request-pin-", i), page, i, fmt.Sprint("request-pin-", i))
			}
			seedStorageBoardPin(t, h, ws, user, page, 1)
			execOrFatal(t, h.db, `DELETE FROM page_board_artifact_placements`)
			execOrFatal(t, h.db, `INSERT INTO page_board_requests(id,workspace_id,board_page_id,original_board_id,human_user_id,kind,base_seq,submission_id,fingerprint,instruction,selected_element_ids_json,selected_placement_ids_json,snapshot_json,target_json,dialect,converter_version,receipt_json,status,created_at,updated_at) VALUES('pin-job',?,'storage-board','storage-board',?,'artifact',1,'job','fingerprint','instruction','[]','[]','{}','{}','','','','succeeded','2020-01-01T00:00:00.000Z',?)`, ws, user, tsformat.Format(time.Now()))
			if kind == "source" {
				execOrFatal(t, h.db, `INSERT INTO page_board_request_sources(id,request_id,placement_id,source_page_id,source_revision) VALUES('job-source','pin-job','deleted-placement',?,1)`, page)
			} else {
				execOrFatal(t, h.db, `UPDATE page_board_requests SET result_page_id=?,result_revision=1 WHERE id='pin-job'`, page)
			}
			tx, err := h.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if err = reserveArtifactRevision(t.Context(), tx, page); err != nil {
				t.Fatal(err)
			}
			var n int
			if err = tx.QueryRow(`SELECT COUNT(*) FROM page_artifact_revisions WHERE page_id=? AND revision=1`, page).Scan(&n); err != nil || n != 1 {
				t.Fatalf("history pin lost: %d %v", n, err)
			}
			if err = tx.QueryRow(`SELECT COUNT(*) FROM page_artifact_revisions WHERE page_id=? AND revision=2`, page).Scan(&n); err != nil || n != 0 {
				t.Fatalf("unpinned revision retained: %d %v", n, err)
			}
		})
	}
}
