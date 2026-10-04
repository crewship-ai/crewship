package pipeline

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

// Corrected-reading reproducer (not product code). A sweeper worker owns the
// resumed run through its whole downstream execution, so eight long resumes
// occupy every worker and an unrelated expired wait is never enforced.

type starvationBlockingCodeRunner struct{ started atomic.Int32 }

func (r *starvationBlockingCodeRunner) RunCode(ctx context.Context, _ CodeRunRequest) (CodeRunResult, error) {
	r.started.Add(1)
	<-ctx.Done()
	return CodeRunResult{}, ctx.Err()
}

func runStarvation(t *testing.T, longDSL string, deliver bool) {
	db := openFactoryTestDB(t)
	defer db.Close()
	deps := fullExecutorDeps(t, db, newMockRunner())
	deps.RunVerdict = nil
	// This regression exercises expiry fairness, not websocket capture. The
	// factory's counter fake is not safe for eight concurrent retry emitters.
	deps.WS = nil
	blocker := &starvationBlockingCodeRunner{}
	deps.CodeRunner = blocker
	exec := NewWiredExecutor(deps)
	long := saveResumePipeline(t, deps.Store, "long-resume", longDSL)
	free := saveResumePipeline(t, deps.Store, "free-expiry2", eventWaitDSL)
	ctx := context.Background()
	for i := 0; i < 9; i++ {
		pid := long.ID
		if i == 8 {
			pid = free.ID
		}
		res, err := exec.Run(ctx, RunInput{PipelineID: pid, WorkspaceID: "ws_test", Mode: ModeRun, RunIDOverride: fmt.Sprintf("starve-%02d", i)})
		if err != nil || res.Status != "WAITING" {
			t.Fatalf("park %d: %+v %v", i, res, err)
		}
	}
	if deliver {
		for i := 0; i < 8; i++ {
			if ok, err := exec.signalWaits.Deliver(ctx, fmt.Sprintf("starve-%02d", i), "go", "p"); err != nil || !ok {
				t.Fatalf("deliver %d: %v %v", i, ok, err)
			}
		}
		if _, err := db.Exec(`UPDATE pipeline_signal_waits SET timeout_at=? WHERE run_id='starve-08'`, tsformat.Format(time.Now().Add(-time.Minute))); err != nil {
			t.Fatal(err)
		}
	} else if _, err := db.Exec(`UPDATE pipeline_signal_waits SET timeout_at=?`, tsformat.Format(time.Now().Add(-time.Minute))); err != nil {
		t.Fatal(err)
	}
	stop := StartEventWaitSweeper(ctx, db, exec, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), 10*time.Millisecond)
	defer stop()
	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		rec, err := deps.RunStore.Get(ctx, "starve-08")
		if err != nil {
			t.Fatal(err)
		}
		if rec.Status == RunStatusFailed {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	rec, _ := deps.RunStore.Get(ctx, "starve-08")
	t.Fatalf("unrelated expired wait not enforced after 1.5s: status=%s step=%s (blocking code steps started=%d)", rec.Status, rec.CurrentStepID, blocker.started.Load())
}

// Delivered-but-unresumed signals (API resume lost to crash/restart, or the
// sweeper winning the race) followed by a long downstream step.
func TestCorrectedReader_LongDeliveredResumesStarveExpiry(t *testing.T) {
	runStarvation(t, `{"dsl_version":"1.0","name":"long-resume","steps":[{"id":"gate","type":"wait","wait":{"kind":"event","event_type":"go"}},{"id":"work","type":"code","code":{"runtime":"cel","code":"true"}}]}`, true)
}

// Expired waits whose author-chosen retry backoff (max_ms is unbounded) keeps
// each sweeper worker sleeping between instant re-failures.
func TestCorrectedReader_TimeoutRetryBackoffStarvesExpiry(t *testing.T) {
	runStarvation(t, `{"dsl_version":"1.0","name":"long-resume","steps":[{"id":"gate","type":"wait","wait":{"kind":"event","event_type":"go"},"timeout_seconds":60,"retry":{"max_attempts":3,"backoff":{"min_ms":30000,"max_ms":30000,"factor":1,"jitter":false}}}]}`, false)
}
