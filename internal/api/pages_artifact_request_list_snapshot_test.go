package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestArtifactRequestListKeepsBoundedRowsAndCompletionInOneSnapshot(t *testing.T) {
	h, workspace, user, _ := requestFixture(t)
	originalID := createArtifactRequestForTest(t, h, workspace, user, strings.TrimSuffix(requestBody, "}")+`,"result_kind":"reply"}`, 202)
	production := h.db
	execOrFatal(t, production, `UPDATE page_artifact_requests SET created_at='2026-10-13T00:00:00.000Z' WHERE id=?`, originalID)
	clone := func(id, stamp string) {
		t.Helper()
		execOrFatal(t, production, `INSERT INTO page_artifact_requests(id,page_id,human_user_id,chat_id,agent_id,base_rev,submission_id,fingerprint,instruction,snapshot_json,receipt_json,status,created_at,updated_at,result_kind) SELECT ?,page_id,human_user_id,chat_id,agent_id,base_rev,?,'snapshot-fixture','fixture','{}','{}','queued',?,updated_at,'reply' FROM page_artifact_requests WHERE id=?`, id, id, stamp, originalID)
	}
	for i := 0; i < 99; i++ {
		clone(fmt.Sprintf("snapshot-request-%03d", i), "2026-10-13T00:00:00.000Z")
	}
	hook := &reviewQueryHook{match: "SELECT r.id,r.result_kind", nth: 1, run: func() {
		// Commit a new 101st row AND completion after the list selected its100
		// but before the outcome query. A second autocommit read drops the oldest
		// ID and mixes the new reply with the old queued status.
		clone("new-snapshot-request", "2026-10-14T00:00:00.000Z")
		execOrFatal(t, production, `UPDATE page_artifact_requests SET status='succeeded' WHERE id=?`, originalID)
		execOrFatal(t, production, `INSERT INTO page_artifact_request_updates(id,request_id,agent_id,run_id,on_behalf_of_user_id,mutation_id,fingerprint,operation,body,created_at) SELECT 'snapshot-reply',id,agent_id,'snapshot-run',human_user_id,'snapshot-reply','digest','reply','New concurrent reply','2026-10-14T00:00:00.000Z' FROM page_artifact_requests WHERE id=?`, originalID)
	}}
	// Deferred WAL reads allow the interleaved writer to really commit. This
	// exercises snapshot isolation even on a reader that does not block writers.
	// One pool connection also detects accidental h.db calls inside the tx.
	hooked := sql.OpenDB(&reviewHookConnector{dsn: reviewDBPath(t, production) + "?_pragma=busy_timeout(1000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_txlock=deferred", inner: production.Driver(), hook: hook})
	hooked.SetMaxOpenConns(1)
	defer hooked.Close()
	h.db = hooked
	defer func() { h.db = production }()
	request := pagesGrantRequest(t, "GET", "/", workspace, user, "OWNER", "")
	request.SetPathValue("slug", "report")
	ctx, cancel := context.WithTimeout(request.Context(), 5*time.Second)
	defer cancel()
	response := httptest.NewRecorder()
	h.ListArtifactRequests(response, request.WithContext(ctx))
	artifactExpect(t, response, 200)
	if !hook.didFire() {
		t.Fatal("concurrent writer did not run")
	}
	var result struct {
		Requests []artifactRequestWire `json:"requests"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Requests) != 100 {
		t.Fatalf("rows=%d", len(result.Requests))
	}
	found := false
	for _, item := range result.Requests {
		if item.ResultKind != "reply" {
			t.Fatalf("captured request lost typed outcome: id=%s kind=%q", item.ID, item.ResultKind)
		}
		if item.ID == "new-snapshot-request" {
			t.Fatal("concurrent101st row entered old snapshot")
		}
		if item.ID == originalID {
			found = true
			if item.Status != "queued" || item.Reply != nil {
				t.Fatalf("mixed completion snapshot: %+v", item)
			}
		}
	}
	if !found {
		t.Fatal("original captured100th row disappeared")
	}
	var status string
	if err := production.QueryRow(`SELECT status FROM page_artifact_requests WHERE id=?`, originalID).Scan(&status); err != nil || status != "succeeded" {
		t.Fatalf("writer failed: status=%s err=%v", status, err)
	}
	// A later request sees the committed state, rather than a permanently stale
	// application cache or a test hook that merely ignored the writer.
	next := httptest.NewRecorder()
	h.ListArtifactRequests(next, request.WithContext(ctx))
	artifactExpect(t, next, 200)
	if !strings.Contains(next.Body.String(), "new-snapshot-request") {
		t.Fatal("next snapshot missed committed101st row")
	}
}
