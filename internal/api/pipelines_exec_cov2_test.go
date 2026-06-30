package api

// Second coverage pass for pipelines_exec.go: Run/DryRun/ListRuns/
// ListRunRecords load + executor + query error branches, ApproveWaitpoint's
// generic completion failure, and ListPendingWaitpoints' query failure.
//
// DB errors are forced surgically: db.Close() for first-query failures,
// ALTER TABLE ... RENAME for mid-handler failures, and an invalid stored
// definition_json for executor failures.

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

func covPE2Req(t *testing.T, method, target, body, userID, wsID, slug string) *http.Request {
	t.Helper()
	var req *http.Request
	if body != "" {
		r := strings.NewReader(body)
		req = httptest.NewRequest(method, target, r)
		req.ContentLength = int64(len(body))
	} else {
		req = httptest.NewRequest(method, target, nil)
	}
	if slug != "" {
		req.SetPathValue("slug", slug)
	}
	return withWorkspaceUser(req, userID, wsID, "OWNER")
}

func TestPE2_Run_UnknownSlug404(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	h.SetRunner(&stubRunner{output: "ok"})
	rr := httptest.NewRecorder()
	h.Run(rr, covPE2Req(t, "POST", "/x", "", userID, wsID, "no-such-pipeline"))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rr.Code, rr.Body.String())
	}
}

func TestPE2_Run_LoadDBError500(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	h.SetRunner(&stubRunner{output: "ok"})
	h.db.Close()
	rr := httptest.NewRecorder()
	h.Run(rr, covPE2Req(t, "POST", "/x", "", userID, wsID, "p"))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestPE2_Run_ExecutorError500(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	h.SetRunner(&stubRunner{output: "ok"})
	seedPipelineRowDef(t, h.db, wsID, "pipe-pe2-bad", "pe2-bad", `this is not json`)
	rr := httptest.NewRecorder()
	h.Run(rr, covPE2Req(t, "POST", "/x", `{"inputs":{},"tier_override":"warp-drive","triggered_via":"carrier-pigeon"}`, userID, wsID, "pe2-bad"))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "Failed to start pipeline run") {
		t.Errorf("body = %q", rr.Body.String())
	}
}

func TestPE2_DryRun_LoadDBError500(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	h.db.Close()
	rr := httptest.NewRecorder()
	h.DryRun(rr, covPE2Req(t, "POST", "/x", "", userID, wsID, "p"))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

// A stored definition that no longer parses is handled best-effort by dry_run:
// it returns a 200 report with manifest null (rather than 500-ing the preview),
// so the UI can still render "this routine's definition is corrupt". See the
// dry_run manifest contract in TestPipelineDryRun_MalformedDefinition_ManifestNull.
func TestPE2_DryRun_UnparseableDefinition_BestEffort200(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	seedPipelineRowDef(t, h.db, wsID, "pipe-pe2-dry", "pe2-dry", `also not json`)
	rr := httptest.NewRecorder()
	h.DryRun(rr, covPE2Req(t, "POST", "/x", "", userID, wsID, "pe2-dry"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (best-effort report); body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"manifest":null`) {
		t.Errorf("manifest should be null for an unparseable definition; body=%s", rr.Body.String())
	}
}

// ---- ListRuns ----

func TestPE2_ListRuns_LoadDBError500(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	h.db.Close()
	rr := httptest.NewRecorder()
	h.ListRuns(rr, covPE2Req(t, "GET", "/x", "", userID, wsID, "p"))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestPE2_ListRuns_QueryError500(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	seedPipelineRowDef(t, h.db, wsID, "pipe-pe2-lr", "pe2-lr", agentlessProbeDef)
	if _, err := h.db.Exec(`ALTER TABLE journal_entries RENAME TO journal_entries_hidden_pe2`); err != nil {
		t.Fatalf("rename journal_entries: %v", err)
	}
	t.Cleanup(func() { _, _ = h.db.Exec(`ALTER TABLE journal_entries_hidden_pe2 RENAME TO journal_entries`) })

	rr := httptest.NewRecorder()
	h.ListRuns(rr, covPE2Req(t, "GET", "/x?limit=10&include_steps=1", "", userID, wsID, "pe2-lr"))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

// ---- ListRunRecords ----

func TestPE2_ListRunRecords_LoadDBError500(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	h.SetRunStore(pipeline.NewRunStore(h.db))
	h.db.Close()
	rr := httptest.NewRecorder()
	h.ListRunRecords(rr, covPE2Req(t, "GET", "/x", "", userID, wsID, "p"))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestPE2_ListRunRecords_StoreQueryError500(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	h.SetRunStore(pipeline.NewRunStore(h.db))
	seedPipelineRowDef(t, h.db, wsID, "pipe-pe2-rr", "pe2-rr", agentlessProbeDef)
	if _, err := h.db.Exec(`ALTER TABLE pipeline_runs RENAME TO pipeline_runs_hidden_pe2`); err != nil {
		t.Fatalf("rename pipeline_runs: %v", err)
	}
	t.Cleanup(func() { _, _ = h.db.Exec(`ALTER TABLE pipeline_runs_hidden_pe2 RENAME TO pipeline_runs`) })

	rr := httptest.NewRecorder()
	h.ListRunRecords(rr, covPE2Req(t, "GET", "/x?limit=5", "", userID, wsID, "pe2-rr"))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestPE2_ListRunRecords_EmptyOK(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	h.SetRunStore(pipeline.NewRunStore(h.db))
	seedPipelineRowDef(t, h.db, wsID, "pipe-pe2-ok", "pe2-ok", agentlessProbeDef)
	rr := httptest.NewRecorder()
	h.ListRunRecords(rr, covPE2Req(t, "GET", "/x?limit=5&status=COMPLETED", "", userID, wsID, "pe2-ok"))
	if rr.Code != http.StatusOK || strings.TrimSpace(rr.Body.String()) != "[]" {
		t.Fatalf("status = %d body=%q, want 200 []", rr.Code, rr.Body.String())
	}
}

// ---- waitpoints ----

func TestPE2_ApproveWaitpoint_GenericError500(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	stub := &stubApproverWaitpoints{completeReturnGenericErr: errors.New("waitpoint table on fire")}
	h.SetWaitpointStore(stub)

	body := `{"approved":false,"comment":"nope"}`
	req := covPE2Req(t, "POST", "/x", body, userID, wsID, "")
	req.SetPathValue("token", "tok-pe2")
	rr := httptest.NewRecorder()
	h.ApproveWaitpoint(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestPE2_ListPendingWaitpoints_QueryError500(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	if _, err := h.db.Exec(`ALTER TABLE pipeline_waitpoints RENAME TO pipeline_waitpoints_hidden_pe2`); err != nil {
		t.Fatalf("rename pipeline_waitpoints: %v", err)
	}
	t.Cleanup(func() { _, _ = h.db.Exec(`ALTER TABLE pipeline_waitpoints_hidden_pe2 RENAME TO pipeline_waitpoints`) })

	rr := httptest.NewRecorder()
	h.ListPendingWaitpoints(rr, covPE2Req(t, "GET", "/x", "", userID, wsID, ""))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}
