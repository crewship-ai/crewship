package pipeline

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestApprovalWait_DocumentedTimeoutAlias(t *testing.T) {
	for _, tc := range []struct {
		name             string
		step, wait, want int
	}{
		{"nested timeout", 0, 5, 5},
		{"step timeout", 7, 0, 7},
		{"explicit step takes precedence", 7, 5, 7},
		{"store default", 0, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dsl, err := Parse([]byte(fmt.Sprintf(`{"name":"wait-timeout","steps":[{"id":"gate","type":"wait","timeout_seconds":%d,"wait":{"kind":"approval","approval_prompt":"Approve fixture","timeout_sec":%d}}]}`, tc.step, tc.wait)))
			if err != nil {
				t.Fatal(err)
			}
			wp := &fakeWaitpointStore{waitApprove: true}
			exec := &Executor{waitpoints: wp}
			if _, _, _, err := exec.runWaitStep(context.Background(), dsl.Steps[0], emptyRender(), RunInput{}, "run-alias", 0); err != nil {
				t.Fatal(err)
			}
			if wp.lastCreateReq.TimeoutSec != tc.want {
				t.Fatalf("approval timeout = %d, want %d", wp.lastCreateReq.TimeoutSec, tc.want)
			}
		})
	}
}

// These runs use the SQL stores and park normally; no agent step is executed.
func TestWaitTimeoutAlias_PersistedDeadline(t *testing.T) {
	for _, kind := range []string{"approval", "event"} {
		t.Run(kind, func(t *testing.T) {
			db := openFactoryTestDB(t)
			defer db.Close()
			deps := fullExecutorDeps(t, db, newMockRunner())
			deps.RunVerdict = nil
			exec := NewWiredExecutor(deps)
			definition := fmt.Sprintf(`{"name":"nested-deadline","agentless":true,"steps":[{"id":"gate","type":"wait","wait":{"kind":%q,"approval_prompt":"Approve fixture","event_type":"fixture.approved","timeout_sec":5}}]}`, kind)
			p := saveResumePipeline(t, deps.Store, "nested-deadline", definition)
			started := time.Now()
			result, err := exec.Run(context.Background(), RunInput{PipelineID: p.ID, WorkspaceID: "ws_test", Mode: ModeRun})
			if err != nil || result.Status != "WAITING" {
				t.Fatalf("park: %+v %v", result, err)
			}
			var raw string
			query := `SELECT timeout_at FROM pipeline_signal_waits WHERE run_id=?`
			if kind == "approval" {
				query = `SELECT timeout_at FROM pipeline_waitpoints WHERE pipeline_run_id=?`
			}
			if err := db.QueryRow(query, result.RunID).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			deadline, err := time.Parse(time.RFC3339Nano, raw)
			if err != nil {
				t.Fatal(err)
			}
			if deadline.Before(started.Add(5*time.Second)) || deadline.After(time.Now().Add(5*time.Second)) {
				t.Fatalf("persisted %s deadline %s does not honor nested five-second timeout", kind, raw)
			}
		})
	}
}

func TestWaitTimeoutAlias_DatetimeExpires(t *testing.T) {
	definition := fmt.Sprintf(`{"name":"date-timeout","steps":[{"id":"gate","type":"wait","wait":{"kind":"datetime","until":%q,"timeout_sec":1}}]}`, time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
	dsl, err := Parse([]byte(definition))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	started := time.Now()
	_, _, _, err = (&Executor{}).runWaitStep(ctx, dsl.Steps[0], emptyRender(), RunInput{}, "date-alias", 0)
	if err != context.DeadlineExceeded || time.Since(started) >= 2*time.Second {
		t.Fatalf("nested datetime timeout: elapsed=%s err=%v", time.Since(started), err)
	}
}
