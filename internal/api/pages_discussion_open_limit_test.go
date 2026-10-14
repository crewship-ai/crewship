package api

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDiscussionOpenLimit_ResolveReleasesCapacityAndReopenChecksIt(t *testing.T) {
	h, _, clock, ws, user, page := artifactFixture(t)
	kind := "artifact"
	artifactExpect(t, artifactSave(t, h, ws, user, "<h1>Original</h1>", "first", 0), 201)
	call := func(operation, id string, payload any) *httptest.ResponseRecorder {
		return artifactDiscussionCall(t, h, ws, user, "OWNER", operation, id, payload)
	}
	create := map[string]any{"body": "Preserve original discussion", "mutation_id": "original"}
	create["anchor"] = artifactCommentAnchor{SchemaVersion: 1, Type: "document"}
	create["revision"] = 1
	rr := call("create", "", create)
	artifactExpect(t, rr, 201)
	type threadResult struct {
		ID       string                `json:"id"`
		Status   string                `json:"status"`
		Version  int64                 `json:"version"`
		Comments []artifactCommentWire `json:"comments"`
	}
	decode := func(rr *httptest.ResponseRecorder) threadResult {
		var v threadResult
		if err := json.Unmarshal(rr.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	original := decode(rr)
	columns := "page_id,anchor_json,created_by_user_id,created_at,updated_at"
	columns += ",revision"
	table := "page_" + kind + "_threads"
	execOrFatal(t, h.db, fmt.Sprintf(`WITH RECURSIVE n(v) AS(SELECT 1 UNION ALL SELECT v+1 FROM n WHERE v<99) INSERT INTO %s(id,%s) SELECT 'quota-'||v,%s FROM n CROSS JOIN %s WHERE id=?`, table, columns, columns, table), original.ID)
	clock.advance(2 * time.Hour)
	create["mutation_id"] = "replacement"
	artifactExpect(t, call("create", "", create), 409)
	artifactExpect(t, call("resolve", original.ID, map[string]any{"resolved": true, "base_version": 1, "mutation_id": "resolve-original"}), 200)
	rr = call("create", "", create)
	artifactExpect(t, rr, 201)
	replacement := decode(rr)
	artifactExpect(t, call("resolve", original.ID, map[string]any{"resolved": false, "base_version": 2, "mutation_id": "reopen-original"}), 409)
	artifactExpect(t, call("resolve", replacement.ID, map[string]any{"resolved": true, "base_version": 1, "mutation_id": "resolve-replacement"}), 200)
	rr = call("resolve", original.ID, map[string]any{"resolved": false, "base_version": 2, "mutation_id": "reopen-original"})
	artifactExpect(t, rr, 200)
	reopened := decode(rr)
	if reopened.Status != "open" || reopened.Version != 3 || len(reopened.Comments) != 1 || reopened.Comments[0].Body != "Preserve original discussion" {
		t.Fatalf("lost original discussion: %+v", reopened)
	}
	var count int
	if err := h.db.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE page_id=?", table), page).Scan(&count); err != nil || count != 101 {
		t.Fatalf("historical threads removed: count=%d err=%v", count, err)
	}
	rr = call("list", "", nil)
	artifactExpect(t, rr, 200)
	var list struct {
		Threads []threadResult `json:"threads"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Threads) != 100 {
		t.Fatalf("list omitted open threads: %d", len(list.Threads))
	}
	for _, thread := range list.Threads {
		if thread.Status != "open" {
			t.Fatal("resolved history displaced an open thread")
		}
	}
	create["mutation_id"] = "original"
	rr = call("create", "", create)
	artifactExpect(t, rr, 200)
	if decode(rr).ID != original.ID {
		t.Fatal("creation retry duplicated historical thread")
	}
	// Retained creation identities must not become a second lifetime cap.
	mutations := "page_" + kind + "_comment_mutations"
	execOrFatal(t, h.db, fmt.Sprintf(`WITH RECURSIVE n(v) AS(SELECT 1 UNION ALL SELECT v+1 FROM n WHERE v<10000) INSERT INTO %s(id,page_id,thread_id,mutation_id,request_digest,actor_user_id,operation,result_version,created_at) SELECT 'old-create-'||v,?,?,'old-create-'||v,'digest',?,'create',1,'2000-01-01T00:00:00.000Z' FROM n`, mutations), page, original.ID, user)
	artifactExpect(t, call("resolve", original.ID, map[string]any{"resolved": true, "base_version": 3, "mutation_id": "resolve-original-again"}), 200)
	create["mutation_id"] = "beyond-lifetime-receipts"
	artifactExpect(t, call("create", "", create), 201)
}
