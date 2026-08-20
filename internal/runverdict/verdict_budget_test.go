package runverdict

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/llm"
)

// The unit half of #1615: GenerateAndEmit is the one place the run_summary
// slot's per-call budget is applied, so that the ad-hoc-run and pipeline-run
// call sites cannot drift on it. Both hand this function a BACKGROUND context,
// which is why "the caller's context bounds it" was never a bound at all.

// budgetProbeProvider reports the deadline its Complete was called with, and
// optionally blocks until that deadline (or the ctx) fires — the difference
// between "a deadline was attached" and "the call is actually cut off by it".
type budgetProbeProvider struct {
	content string
	block   bool

	called      bool
	hadDeadline bool
	remaining   time.Duration
}

func (p *budgetProbeProvider) Complete(ctx context.Context, req llm.Request) (*llm.Response, error) {
	p.called = true
	if dl, ok := ctx.Deadline(); ok {
		p.hadDeadline = true
		p.remaining = time.Until(dl)
	}
	if p.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return &llm.Response{Content: p.content}, nil
}

func (p *budgetProbeProvider) Stream(ctx context.Context, req llm.Request, handler func(llm.StreamEvent) error) (*llm.Response, error) {
	return nil, nil
}

func (p *budgetProbeProvider) Name() string { return "budget-probe" }

// The budget the resolver hands over is the deadline the model call runs
// under, and only that budget — not a constant, and not "whatever the caller
// passed", which at both production call sites is nothing.
func TestGenerateAndEmit_BudgetBoundsTheModelCall(t *testing.T) {
	cases := []struct {
		name         string
		budget       time.Duration
		wantDeadline bool
	}{
		{name: "the slot's budget is attached to the call", budget: 4 * time.Second, wantDeadline: true},
		{name: "a different budget, so the number is read", budget: 9 * time.Second, wantDeadline: true},
		{name: "zero leaves the caller's context in charge", budget: 0, wantDeadline: false},
		{name: "negative is treated as unset, never as an expired deadline", budget: -1 * time.Second, wantDeadline: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := &budgetProbeProvider{content: `{"outcome":"goal_met","verdict":"ok","summary":"ok"}`}
			emitter := &recordingEmitter{}

			// context.Background(), deliberately: it is what
			// internal/api/internal_runs.go and internal/pipeline's verdict
			// hook actually pass.
			if err := GenerateAndEmit(context.Background(), emitter, provider, testModel, tc.budget,
				baseEntry(), multiEntryRun()); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !provider.called {
				t.Fatal("no LLM call was made")
			}
			if provider.hadDeadline != tc.wantDeadline {
				t.Fatalf("call had a deadline = %v, want %v (budget %s)",
					provider.hadDeadline, tc.wantDeadline, tc.budget)
			}
			if !tc.wantDeadline {
				if len(emitter.entries) != 1 {
					t.Errorf("emitted %d entries, want 1 — an unset budget must not cancel the call",
						len(emitter.entries))
				}
				return
			}
			if provider.remaining > tc.budget || provider.remaining < tc.budget/2 {
				t.Errorf("deadline = %s remaining, want ~%s", provider.remaining, tc.budget)
			}
		})
	}
}

// The deadline is enforced, not merely attached: a model that outruns the
// budget is cut off and the verdict is dropped rather than the run's teardown
// waiting on it. This is the property the four Keeper Reviews evaluators got
// in #1601 and run_summary did not.
func TestGenerateAndEmit_OverrunningTheBudgetCutsTheCallOff(t *testing.T) {
	provider := &budgetProbeProvider{block: true}
	emitter := &recordingEmitter{}

	start := time.Now()
	err := GenerateAndEmit(context.Background(), emitter, provider, testModel, 40*time.Millisecond,
		baseEntry(), multiEntryRun())
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("a call that outran its budget returned no error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error = %v, want it to wrap context.DeadlineExceeded", err)
	}
	// Generous ceiling: the point is that it returned at all rather than
	// blocking on a provider that never answers.
	if elapsed > 5*time.Second {
		t.Errorf("returned after %s; the budget did not bound the call", elapsed)
	}
	if len(emitter.entries) != 0 {
		t.Errorf("emitted %d entries after a timed-out call, want 0", len(emitter.entries))
	}
}

// deadlineRecordingEmitter records whether the emit it was handed was itself
// under the model call's deadline.
type deadlineRecordingEmitter struct {
	recordingEmitter
	hadDeadline bool
}

func (e *deadlineRecordingEmitter) Emit(ctx context.Context, entry journal.Entry) (string, error) {
	_, e.hadDeadline = ctx.Deadline()
	return e.recordingEmitter.Emit(ctx, entry)
}

// The budget bounds the model call, not the journal write behind it. A verdict
// that answered just inside its deadline and was then dropped on the way to
// the journal would be the one outcome worse than either bound alone: billed,
// generated, and invisible.
func TestGenerateAndEmit_BudgetDoesNotBoundTheEmit(t *testing.T) {
	provider := &budgetProbeProvider{content: `{"outcome":"goal_met","verdict":"ok","summary":"ok"}`}
	emitter := &deadlineRecordingEmitter{}

	if err := GenerateAndEmit(context.Background(), emitter, provider, testModel, 4*time.Second,
		baseEntry(), multiEntryRun()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !provider.hadDeadline {
		t.Fatal("the model call was not bounded by the budget")
	}
	if emitter.hadDeadline {
		t.Error("the emit inherited the model call's deadline; the budget must cover the call only")
	}
	if len(emitter.entries) != 1 {
		t.Errorf("emitted %d entries, want 1", len(emitter.entries))
	}
}
