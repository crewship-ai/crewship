package api

// Operator API for cross-run routine state (#1420 follow-up). These pin the
// contract the `crewship routine state` CLI drives: all-buckets by default,
// bucket-scoped mutation, and a 404 (not a silent 200) for a key that isn't
// there — the mistyped-key case that would otherwise send an operator hunting
// in the wrong place.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// seedRoutineState writes directly through the store, mirroring what a run's
// state_write binding does.
func seedRoutineState(t *testing.T, h *PipelineHandler, pipelineID, scheduleID, key, value string) {
	t.Helper()
	st := pipeline.NewRoutineStateStore(h.db)
	if err := st.Write(t.Context(), pipelineID, scheduleID, key, value); err != nil {
		t.Fatalf("seed state %s/%s: %v", scheduleID, key, err)
	}
}

func stateReq(t *testing.T, method, target, body, userID, wsID, slug string) *http.Request {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, target, nil)
	} else {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
	}
	r = withWorkspaceUser(r, userID, wsID, "OWNER")
	r.SetPathValue("slug", slug)
	return r
}

func TestGetState_ReturnsEveryBucketByDefault(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	seedPipelineWithVersions(t, h, wsID, "pln-s1", "state-all", 1)
	seedRoutineState(t, h, "pln-s1", "sched_1", "cursor", "100")
	seedRoutineState(t, h, "pln-s1", "sched_2", "cursor", "200")
	seedRoutineState(t, h, "pln-s1", "", "cursor", "manual")

	rr := httptest.NewRecorder()
	h.GetState(rr, stateReq(t, "GET", "/x", "", userID, wsID, "state-all"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var resp routineStateResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// All three buckets — an operator debugging a stalled routine does not know
	// which schedule owns the stuck cursor, so the default must not hide any.
	if len(resp.Buckets) != 3 {
		t.Fatalf("want 3 buckets, got %d: %+v", len(resp.Buckets), resp.Buckets)
	}
	if resp.Slug != "state-all" {
		t.Errorf("slug = %q", resp.Slug)
	}
}

func TestGetState_ScheduleFilterNarrowsToOneBucket(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	seedPipelineWithVersions(t, h, wsID, "pln-s2", "state-filter", 1)
	seedRoutineState(t, h, "pln-s2", "sched_1", "cursor", "100")
	seedRoutineState(t, h, "pln-s2", "sched_2", "cursor", "200")

	rr := httptest.NewRecorder()
	h.GetState(rr, stateReq(t, "GET", "/x?schedule_id=sched_2", "", userID, wsID, "state-filter"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var resp routineStateResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Buckets) != 1 || resp.Buckets[0].ScheduleID != "sched_2" {
		t.Fatalf("want only sched_2, got %+v", resp.Buckets)
	}
	if resp.Buckets[0].Entries[0].Value != "200" {
		t.Errorf("value = %q, want 200", resp.Buckets[0].Entries[0].Value)
	}
}

func TestGetState_EmptyScheduleParamSelectsTheManualBucket(t *testing.T) {
	// "?schedule_id=" is a legitimate selector (the shared manual/webhook
	// bucket) and must NOT be read as "param absent → all buckets".
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	seedPipelineWithVersions(t, h, wsID, "pln-s3", "state-manual", 1)
	seedRoutineState(t, h, "pln-s3", "sched_1", "cursor", "100")
	seedRoutineState(t, h, "pln-s3", "", "cursor", "manual")

	rr := httptest.NewRecorder()
	h.GetState(rr, stateReq(t, "GET", "/x?schedule_id=", "", userID, wsID, "state-manual"))
	var resp routineStateResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Buckets) != 1 || resp.Buckets[0].ScheduleID != "" {
		t.Fatalf("want only the manual bucket, got %+v", resp.Buckets)
	}
	if resp.Buckets[0].Entries[0].Value != "manual" {
		t.Errorf("value = %q, want manual", resp.Buckets[0].Entries[0].Value)
	}
}

func TestSetState_WritesAndIsReadableBack(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	seedPipelineWithVersions(t, h, wsID, "pln-s4", "state-set", 1)

	put := stateReq(t, "PUT", "/x", `{"value":"2026-07-25","schedule_id":"sched_1"}`, userID, wsID, "state-set")
	put.SetPathValue("key", "cursor")
	rr := httptest.NewRecorder()
	h.SetState(rr, put)
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}

	// Round-trip through the executor's own read path — this is the value the
	// next run will actually see in {{ routine.state.cursor }}.
	got, err := pipeline.NewRoutineStateStore(h.db).Load(t.Context(), "pln-s4", "sched_1")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got["cursor"] != "2026-07-25" {
		t.Errorf("cursor = %q, want 2026-07-25", got["cursor"])
	}
}

func TestSetState_RejectsAnOverlongKey(t *testing.T) {
	// The key is half a primary key and a template-namespace name; an unbounded
	// one is storage the caller gets to define.
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	seedPipelineWithVersions(t, h, wsID, "pln-s8", "state-longkey", 1)

	put := stateReq(t, "PUT", "/x", `{"value":"v"}`, userID, wsID, "state-longkey")
	put.SetPathValue("key", strings.Repeat("k", maxRoutineStateKeyLen+1))
	rr := httptest.NewRecorder()
	h.SetState(rr, put)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
}

func TestSetState_RejectsUnknownRoutine(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	put := stateReq(t, "PUT", "/x", `{"value":"v"}`, userID, wsID, "no-such-routine")
	put.SetPathValue("key", "cursor")
	rr := httptest.NewRecorder()
	h.SetState(rr, put)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rr.Code, rr.Body.String())
	}
}

func TestDeleteStateKey_RemovesOnlyThatBucketsKey(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	seedPipelineWithVersions(t, h, wsID, "pln-s5", "state-del", 1)
	seedRoutineState(t, h, "pln-s5", "sched_1", "cursor", "100")
	seedRoutineState(t, h, "pln-s5", "sched_2", "cursor", "200")

	del := stateReq(t, "DELETE", "/x?schedule_id=sched_1", "", userID, wsID, "state-del")
	del.SetPathValue("key", "cursor")
	rr := httptest.NewRecorder()
	h.DeleteStateKey(rr, del)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	// The sibling schedule keeps its own cursor.
	got, err := pipeline.NewRoutineStateStore(h.db).Load(t.Context(), "pln-s5", "sched_2")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got["cursor"] != "200" {
		t.Errorf("sched_2 cursor = %q, want it untouched at 200", got["cursor"])
	}
}

func TestDeleteStateKey_404sAMistypedKey(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	seedPipelineWithVersions(t, h, wsID, "pln-s6", "state-typo", 1)
	seedRoutineState(t, h, "pln-s6", "", "cursor", "100")

	del := stateReq(t, "DELETE", "/x", "", userID, wsID, "state-typo")
	del.SetPathValue("key", "curser") // typo
	rr := httptest.NewRecorder()
	h.DeleteStateKey(rr, del)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 so the typo is visible; body=%s", rr.Code, rr.Body.String())
	}
	// …and the real key is still there.
	got, _ := pipeline.NewRoutineStateStore(h.db).Load(t.Context(), "pln-s6", "")
	if got["cursor"] != "100" {
		t.Errorf("a failed delete must not touch state, got %+v", got)
	}
}

func TestClearState_IsBucketScoped(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	seedPipelineWithVersions(t, h, wsID, "pln-s7", "state-clear", 1)
	seedRoutineState(t, h, "pln-s7", "sched_1", "cursor", "100")
	seedRoutineState(t, h, "pln-s7", "sched_1", "alpha", "a")
	seedRoutineState(t, h, "pln-s7", "sched_2", "cursor", "200")

	rr := httptest.NewRecorder()
	h.ClearState(rr, stateReq(t, "DELETE", "/x?schedule_id=sched_1", "", userID, wsID, "state-clear"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Removed int64 `json:"removed"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Removed != 2 {
		t.Errorf("removed = %d, want 2", resp.Removed)
	}
	// Clearing one schedule must not make every OTHER schedule reprocess.
	got, _ := pipeline.NewRoutineStateStore(h.db).Load(t.Context(), "pln-s7", "sched_2")
	if got["cursor"] != "200" {
		t.Errorf("sched_2 bucket should survive, got %+v", got)
	}
}
