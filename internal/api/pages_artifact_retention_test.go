package api

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func TestArtifactRetentionPrunesUnpinnedAndKeepsRetryReceipts(t *testing.T) {
	h, _, _, _, _, page := artifactFixture(t)
	execOrFatal(t, h.db, `INSERT INTO page_artifacts(page_id,head_rev) VALUES(?,200)`, page)
	for i := 1; i <= 200; i++ {
		execOrFatal(t, h.db, `INSERT INTO page_artifact_revisions(id,page_id,revision,sha256,mutation_id,request_digest,created_at) VALUES(?,?,?,'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',?,'fingerprint','2020-01-01T00:00:00.000Z')`, fmt.Sprint("retain-", i), page, i, fmt.Sprint("mutation-", i))
		execOrFatal(t, h.db, `INSERT INTO page_artifact_mutation_receipts(page_id,mutation_id,revision,request_digest,created_at) VALUES(?,?,?,'fingerprint','2020-01-01T00:00:00.000Z')`, page, fmt.Sprint("mutation-", i), i)
	}
	// The durable restore receipt pins source even after its output is gone.
	execOrFatal(t, h.db, `UPDATE page_artifact_mutation_receipts SET restored_from=1 WHERE page_id=? AND revision=199`, page)
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
		t.Fatalf("restore pin lost %d %v", n, err)
	}
	if err = tx.QueryRow(`SELECT COUNT(*) FROM page_artifact_revisions WHERE page_id=? AND revision=2`, page).Scan(&n); err != nil || n != 0 {
		t.Fatalf("oldest unpinned retained %d %v", n, err)
	}
	if err = tx.QueryRow(`SELECT COUNT(*) FROM page_artifact_mutation_receipts WHERE page_id=? AND revision=2`, page).Scan(&n); err != nil || n != 1 {
		t.Fatalf("retry receipt lost %d %v", n, err)
	}
}

func TestArtifactRetention_RestoreRetryAfterOutputPruned(t *testing.T) {
	h, _, clock, ws, user, page := artifactFixture(t)
	artifactExpect(t, artifactSave(t, h, ws, user, "<p>original restore source</p>", "original", 0), 201)
	restoreBody := `{"revision":1,"base_rev":1,"mutation_id":"restore-retry"}`
	artifactExpect(t, artifactCall(t, h, ws, user, "OWNER", "POST", "/restore", restoreBody), 201)
	var sha string
	if err := h.db.QueryRow(`SELECT sha256 FROM page_artifact_revisions WHERE page_id=? AND revision=1`, page).Scan(&sha); err != nil {
		t.Fatal(err)
	}
	// Grow history without spending an hour of wall time or weakening quotas.
	for i := 3; i <= 200; i++ {
		execOrFatal(t, h.db, `INSERT INTO page_artifact_revisions(id,page_id,revision,sha256,mutation_id,request_digest,created_at) VALUES(?,?,?,?,?,'historical','2000-01-01T00:00:00.000Z')`, fmt.Sprint("restore-historical-", i), page, i, sha, fmt.Sprint("historical-", i))
		execOrFatal(t, h.db, `INSERT INTO page_artifact_mutation_receipts(page_id,mutation_id,revision,request_digest,created_at) VALUES(?,?,?,'historical','2000-01-01T00:00:00.000Z')`, page, fmt.Sprint("historical-", i), i)
	}
	execOrFatal(t, h.db, `UPDATE page_artifacts SET head_rev=200 WHERE page_id=?`, page)
	clock.advance(2 * time.Hour)
	artifactExpect(t, artifactSave(t, h, ws, user, "<p>new head</p>", "after-retention", 200), 201)
	artifactExpect(t, artifactCall(t, h, ws, user, "OWNER", "GET", "?revision=2", ""), 404)
	artifactExpect(t, artifactCall(t, h, ws, user, "OWNER", "GET", "?revision=1", ""), 200)
	retry := artifactCall(t, h, ws, user, "OWNER", "POST", "/restore", restoreBody)
	artifactExpect(t, retry, 200)
	var got struct {
		Revision int64 `json:"revision"`
		Head     int64 `json:"head_rev"`
	}
	if err := json.Unmarshal(retry.Body.Bytes(), &got); err != nil || got.Revision != 2 || got.Head != 201 {
		t.Fatalf("restore retry duplicated/lost: %s %v", retry.Body.String(), err)
	}
}
