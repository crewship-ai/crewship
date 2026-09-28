package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

func TestBulkReplayUsesCurrentOperatorAuthority(t *testing.T) {
	h, db, operatorID, wsID := runsHandlerRig(t)
	h.SetRunner(&stubRunner{output: "ok"})
	store := pipeline.NewRunStore(db)
	h.SetRunStore(store)
	seedVersionedPipeline(t, db, wsID, "bulk_replay_operator", "bulk-replay-operator")
	seedRunRow(t, db, wsID, "bulk_replay_operator", "bulk-replay-operator", "original_failed", "failed")

	req := withWorkspaceUser(httptest.NewRequest(http.MethodPost, "/bulk_replay",
		strings.NewReader(`{"run_ids":["original_failed"]}`)), operatorID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.BulkReplayRuns(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("bulk replay status %d: %s", rr.Code, rr.Body.String())
	}
	var response struct {
		Results []struct {
			NewRunID string `json:"new_run_id"`
			Error    string `json:"error"`
		} `json:"results"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil || len(response.Results) != 1 || response.Results[0].NewRunID == "" {
		t.Fatalf("bulk replay response=%+v err=%v body=%s", response, err, rr.Body.String())
	}
	rec, err := store.Get(t.Context(), response.Results[0].NewRunID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.InvokingUserID != operatorID || rec.InvocationAuthority != pipeline.RoutineBatchAuthority {
		t.Fatalf("bulk replay authority = user %q, policy %q; want operator %q with batch authority",
			rec.InvokingUserID, rec.InvocationAuthority, operatorID)
	}
}

func TestReplayRejectsOperatorRevokedAfterRequestAuthentication(t *testing.T) {
	h, db, operatorID, wsID := runsHandlerRig(t)
	h.SetRunner(&stubRunner{output: "must not run"})
	store := pipeline.NewRunStore(db)
	h.SetRunStore(store)
	seedVersionedPipeline(t, db, wsID, "revoked_replay", "revoked-replay")
	seedRunRow(t, db, wsID, "revoked_replay", "revoked-replay", "original", "failed")

	// The request was authenticated while the operator was OWNER. A role
	// change before execution must win over that stale context snapshot.
	req := withWorkspaceUser(httptest.NewRequest(http.MethodPost, "/replay", nil), operatorID, wsID, "OWNER")
	req.SetPathValue("runId", "original")
	if _, err := db.Exec(`UPDATE workspace_members SET role='MEMBER' WHERE workspace_id=? AND user_id=?`, wsID, operatorID); err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	h.ReplayRun(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("revoked replay status %d, want 403: %s", rr.Code, rr.Body.String())
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pipeline_runs WHERE pipeline_id='revoked_replay'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("revoked operator started replay: %d run rows", count)
	}
}
