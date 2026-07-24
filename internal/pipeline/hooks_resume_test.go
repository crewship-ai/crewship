package pipeline

import (
	"context"
	"sync/atomic"
	"testing"
)

// #1428 (2.7) — a resumed run must still fire after_all (on COMPLETED) and
// on_failure (on FAILED) exactly once. Previously runHooksAround short-
// circuited on in.resume and skipped ALL hooks, so a resumed run never
// released credentials / announced its failure. Only before_all is
// resume-gated.
func TestRunHooksAround_ResumeRunsTeardownHooks(t *testing.T) {
	ctx := context.Background()

	t.Run("after_all fires on resumed COMPLETED", func(t *testing.T) {
		cr := &recordingCodeRunner{}
		e := &Executor{codeRunner: cr}
		body := func() (*RunResult, error) {
			return &RunResult{RunID: "r", Status: "COMPLETED"}, nil
		}
		in := RunInput{PipelineID: "p", resume: true, dsl: &DSL{Hooks: &RoutineHooks{
			// before_all must be skipped on resume (would error if run).
			BeforeAll: &Step{ID: "pre", Type: StepCode, Code: &CodeStep{Runtime: "cel", Code: "missing.var > 1"}},
			AfterAll:  &Step{ID: "post", Type: StepCode, Code: &CodeStep{Runtime: "cel", Code: "1 > 0"}},
		}}}
		res, err := e.runHooksAround(ctx, in, "r", "slug", body)
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if res.Status != "COMPLETED" {
			t.Fatalf("status: %q", res.Status)
		}
		if n := atomic.LoadInt32(&cr.calls); n != 1 {
			t.Errorf("after_all ran %d times on resume, want exactly 1 (before_all must be skipped)", n)
		}
	})

	t.Run("on_failure fires on resumed FAILED", func(t *testing.T) {
		cr := &recordingCodeRunner{}
		e := &Executor{codeRunner: cr}
		body := func() (*RunResult, error) {
			return &RunResult{RunID: "r", Status: "FAILED", FailedAtStep: "s1"}, nil
		}
		in := RunInput{PipelineID: "p", resume: true, dsl: &DSL{Hooks: &RoutineHooks{
			OnFailure: &Step{ID: "of", Type: StepCode, Code: &CodeStep{Runtime: "cel", Code: "1 > 0"}},
		}}}
		res, err := e.runHooksAround(ctx, in, "r", "slug", body)
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if res.Status != "FAILED" {
			t.Fatalf("status: %q", res.Status)
		}
		if n := atomic.LoadInt32(&cr.calls); n != 1 {
			t.Errorf("on_failure ran %d times on resume, want exactly 1", n)
		}
	})
}
