package api

import (
	"fmt"
	"github.com/crewship-ai/crewship/internal/tsformat"
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
