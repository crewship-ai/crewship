package api

// Topic-scoped signal delivery over HTTP.
//
// POST /pipeline-runs/{runId}/signal needs a run id, so only a caller that
// already knows which run to wake can use it. An internal event source never
// does: "this mission changed status" identifies a workspace and an event,
// not a set of parked runs. POST /workspaces/{workspaceId}/signals is the
// run_id-less door — deliver by topic, wake everything parked on it.
//
// These tests drive the real handler end-to-end (park two runs, deliver once,
// watch both finish) because the property that matters is not "rows flipped"
// but "every parked run resumed" — the un-park is a separate step after
// delivery and is exactly the part a run_id-keyed design got for free.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

const topicEventWaitDSL = `{
  "dsl_version": "1.0",
  "name": "topic-wait",
  "steps": [
    {"id": "gate", "type": "wait", "wait": {"kind": "event", "event_type": "mission.status_change"}, "timeout_seconds": 3600}
  ]
}`

// seedTopicPipeline inserts the wait:event routine into a workspace.
func seedTopicPipeline(t *testing.T, h *PipelineHandler, pipelineID, wsID string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := h.db.Exec(`
		INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash, created_at, updated_at, last_test_run_at)
		VALUES (?, ?, 'topic-wait', 'topic-wait', ?, 'hash', ?, ?, ?)`,
		pipelineID, wsID, topicEventWaitDSL, now, now, now); err != nil {
		t.Fatalf("seed pipeline %s: %v", pipelineID, err)
	}
}

// parkRun starts the wait:event routine and asserts it actually parked.
func parkRun(t *testing.T, h *PipelineHandler, pipelineID, wsID string) string {
	t.Helper()
	res, err := h.newExecutor().Run(context.Background(), pipeline.RunInput{
		PipelineID:  pipelineID,
		WorkspaceID: wsID,
		Mode:        pipeline.ModeRun,
	})
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	if res.Status != "WAITING" {
		t.Fatalf("status = %q, want WAITING (top-level wait:event must park)", res.Status)
	}
	return res.RunID
}

func postTopicSignal(t *testing.T, h *PipelineHandler, wsID, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/workspaces/"+wsID+"/signals", strings.NewReader(body))
	req = withWorkspaceCtx(req, wsID)
	rr := httptest.NewRecorder()
	h.SignalWorkspace(rr, req)
	return rr
}

func awaitRunStatus(t *testing.T, runStore *pipeline.RunStore, runID string) *pipeline.RunRecord {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var rec *pipeline.RunRecord
	var err error
	for time.Now().Before(deadline) {
		rec, err = runStore.Get(context.Background(), runID)
		if err != nil {
			t.Fatalf("get run %s: %v", runID, err)
		}
		if rec.Status == "completed" || rec.Status == "failed" {
			return rec
		}
		time.Sleep(10 * time.Millisecond)
	}
	return rec
}

func newTopicSignalHandler(t *testing.T) (*PipelineHandler, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewPipelineHandler(db, testLogger(), nil, nil)
	h.SetRunner(&stubRunner{output: "unused"})
	h.SetRunStore(pipeline.NewRunStore(db))
	h.SetSignalRegistry(pipeline.NewSignalRegistry())
	return h, wsID
}

func TestSignalWorkspace_WakesEveryRunParkedOnTheTopic(t *testing.T) {
	h, wsID := newTopicSignalHandler(t)
	seedTopicPipeline(t, h, "pln_topic", wsID)

	runA := parkRun(t, h, "pln_topic", wsID)
	runB := parkRun(t, h, "pln_topic", wsID)

	rr := postTopicSignal(t, h, wsID, `{"event_type":"mission.status_change","payload":"issue-closed"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		OK        bool     `json:"ok"`
		Delivered int      `json:"delivered"`
		RunIDs    []string `json:"run_ids"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Delivered != 2 {
		t.Errorf("delivered = %d, want 2 (both parked runs); run_ids=%v", resp.Delivered, resp.RunIDs)
	}
	got := map[string]bool{}
	for _, id := range resp.RunIDs {
		got[id] = true
	}
	if !got[runA] || !got[runB] {
		t.Errorf("run_ids = %v, want both %s and %s", resp.RunIDs, runA, runB)
	}

	runStore := pipeline.NewRunStore(h.db)
	for _, runID := range []string{runA, runB} {
		rec := awaitRunStatus(t, runStore, runID)
		if rec.Status != "completed" {
			t.Fatalf("run %s final status = %q (error=%q), want completed — a topic delivery must un-park every run it woke",
				runID, rec.Status, rec.ErrorMessage)
		}
		outputs, err := runStore.GetStepOutputs(context.Background(), runID)
		if err != nil {
			t.Fatalf("get step outputs: %v", err)
		}
		if outputs["gate"] != "issue-closed" {
			t.Errorf("run %s gate output = %q, want issue-closed", runID, outputs["gate"])
		}
	}
}

func TestSignalWorkspace_DoesNotWakeAnotherWorkspacesRun(t *testing.T) {
	h, wsA := newTopicSignalHandler(t)
	wsB := "ws-other"
	if _, err := h.db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Other', 'other')`, wsB); err != nil {
		t.Fatalf("seed other workspace: %v", err)
	}
	seedTopicPipeline(t, h, "pln_a", wsA)
	seedTopicPipeline(t, h, "pln_b", wsB)

	runA := parkRun(t, h, "pln_a", wsA)
	runB := parkRun(t, h, "pln_b", wsB)

	rr := postTopicSignal(t, h, wsA, `{"event_type":"mission.status_change","payload":"mine"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Delivered int      `json:"delivered"`
		RunIDs    []string `json:"run_ids"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Delivered != 1 || len(resp.RunIDs) != 1 || resp.RunIDs[0] != runA {
		t.Fatalf("run_ids = %v (delivered=%d), want only %s — a topic must not cross the workspace fence",
			resp.RunIDs, resp.Delivered, runA)
	}

	runStore := pipeline.NewRunStore(h.db)
	if rec := awaitRunStatus(t, runStore, runA); rec.Status != "completed" {
		t.Fatalf("own-workspace run status = %q, want completed", rec.Status)
	}
	rec, err := runStore.Get(context.Background(), runB)
	if err != nil {
		t.Fatalf("get foreign run: %v", err)
	}
	if rec.Status != "waiting" {
		t.Errorf("foreign-workspace run status = %q, want waiting (it must still be parked)", rec.Status)
	}
}

func TestSignalWorkspace_UnwatchedTopicIs200NotError(t *testing.T) {
	h, wsID := newTopicSignalHandler(t)
	seedTopicPipeline(t, h, "pln_topic", wsID)
	parkRun(t, h, "pln_topic", wsID)

	rr := postTopicSignal(t, h, wsID, `{"event_type":"nobody.listens","payload":"x"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — an internal event nobody waits on is normal, not a 404; body=%s",
			rr.Code, rr.Body.String())
	}
	var resp struct {
		OK        bool     `json:"ok"`
		Delivered int      `json:"delivered"`
		RunIDs    []string `json:"run_ids"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.OK || resp.Delivered != 0 || len(resp.RunIDs) != 0 {
		t.Errorf("response = %+v, want ok=true delivered=0 run_ids=[]", resp)
	}
}

func TestSignalWorkspace_RequiresEventType(t *testing.T) {
	h, wsID := newTopicSignalHandler(t)
	if rr := postTopicSignal(t, h, wsID, `{"payload":"x"}`); rr.Code != http.StatusBadRequest {
		t.Errorf("missing event_type = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	if rr := postTopicSignal(t, h, wsID, `not json`); rr.Code != http.StatusBadRequest {
		t.Errorf("invalid body = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
}
