package pipeline

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// stubPreflight records what it was asked and answers with a fixed verdict.
//
// Guarded, because the scheduler calls this from several goroutines at once:
// TestPipelineScheduler_Tick_ConcurrentTicksDoNotDoubleFire fires concurrent
// ticks on purpose, and each lands in Executor.Run → Preflight.Check. The
// unsynchronised append raced under -race (two goroutines growing one slice),
// and the failure surfaced intermittently — a real race in the harness, not a
// flake, and it reads as a production bug in the scheduler until you follow the
// stack down to this line.
//
// Readers take the mutex too: `snapshot` rather than touching `seen` directly,
// so a test that reads while another tick is still in flight cannot race the
// writer either.
type stubPreflight struct {
	mu   sync.Mutex
	err  error
	seen []PreflightRequest
}

func (s *stubPreflight) Check(_ context.Context, req PreflightRequest) error {
	s.mu.Lock()
	s.seen = append(s.seen, req)
	err := s.err
	s.mu.Unlock()
	return err
}

// snapshot copies what has been recorded so far. The copy is what makes an
// assertion on the result safe to hold while the scheduler is still running.
func (s *stubPreflight) snapshot() []PreflightRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]PreflightRequest(nil), s.seen...)
}

// The bypass, pinned. A call_pipeline target that the dispatch gates refuse
// must not execute — before this the in-process path went straight to runDSL
// and a routine that could not be run directly could still be run by being
// called.
func TestExecutor_CallPipeline_RefusedByPreflight(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	runner := newMockRunner()

	child := fakePipeline(t, "gated-child",
		`{"dsl_version":"1.0","name":"gated-child","steps":[`+
			`{"id":"c1","type":"agent_run","agent_slug":"cagent","prompt":"1"}]}`,
		"crew_child", "agent_lead")

	gate := &stubPreflight{err: fmt.Errorf("%w: routine requires credential of type \"api_key\" not present in the vault", ErrRunPreflightBlocked)}
	exec := NewExecutor(store, resolver, runner, nil).WithRunPreflight(gate)
	exec.WithPipelineResolver(pipeResolverFn(func(_ context.Context, _, slug string) (*Pipeline, error) {
		if slug == "gated-child" {
			return child, nil
		}
		return nil, ErrNotFound
	}))

	parent := &DSL{
		Name:  "gating-parent",
		Steps: []Step{{ID: "callChild", Type: StepCallPipeline, PipelineSlug: "gated-child"}},
	}
	res, err := exec.RunDefinition(context.Background(), parent, RunInput{
		WorkspaceID: "ws_test", AuthorCrewID: "crew_a", Mode: ModeRun,
	})
	if err != nil {
		t.Fatalf("Run returned transport error: %v", err)
	}
	if res.Status != "FAILED" {
		t.Fatalf("expected FAILED when preflight refuses, got %s (%s)", res.Status, res.ErrorMessage)
	}
	if !strings.Contains(res.ErrorMessage, "api_key") {
		t.Errorf("the gate's reason must reach the run error, got %q", res.ErrorMessage)
	}
	if len(runner.calls) != 0 {
		t.Errorf("child dispatched despite the refusal: %d agent calls", len(runner.calls))
	}

	// The subject is the TARGET's author crew, not the caller's: a routine
	// runs in its author's crew, so its author's connected integrations and
	// installed tools are what decide whether it can work.
	if len(gate.snapshot()) != 1 {
		t.Fatalf("preflight consulted %d times, want 1", len(gate.snapshot()))
	}
	got := gate.snapshot()[0]
	if got.AuthorCrewID != "crew_child" {
		t.Errorf("AuthorCrewID = %q, want crew_child (the target's author, not the invoker)", got.AuthorCrewID)
	}
	if got.PipelineSlug != "gated-child" || got.WorkspaceID != "ws_test" {
		t.Errorf("preflight request mis-populated: %+v", got)
	}
	if got.DSL == nil || got.DSL.Name != "gated-child" {
		t.Errorf("preflight must receive the TARGET's parsed definition, got %+v", got.DSL)
	}
}

// A permitted target still runs — the gate must bound dispatch, not forbid it.
func TestExecutor_CallPipeline_AllowedByPreflight(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	runner := newMockRunner()

	child := fakePipeline(t, "open-child",
		`{"dsl_version":"1.0","name":"open-child","steps":[`+
			`{"id":"c1","type":"agent_run","agent_slug":"cagent","prompt":"1"}]}`,
		"crew_child", "agent_lead")

	gate := &stubPreflight{}
	exec := NewExecutor(store, resolver, runner, nil).WithRunPreflight(gate)
	exec.WithPipelineResolver(pipeResolverFn(func(_ context.Context, _, slug string) (*Pipeline, error) {
		if slug == "open-child" {
			return child, nil
		}
		return nil, ErrNotFound
	}))

	res, err := exec.RunDefinition(context.Background(), &DSL{
		Name:  "open-parent",
		Steps: []Step{{ID: "callChild", Type: StepCallPipeline, PipelineSlug: "open-child"}},
	}, RunInput{WorkspaceID: "ws_test", AuthorCrewID: "crew_a", Mode: ModeRun})
	if err != nil {
		t.Fatalf("Run returned transport error: %v", err)
	}
	if res.Status != "COMPLETED" {
		t.Fatalf("expected COMPLETED when preflight allows, got %s (%s)", res.Status, res.ErrorMessage)
	}
}

// dry_run is exempt on the HTTP side (it touches no vault and asserts no
// status). The two doors must agree about the edges too, or they are two
// doors again.
func TestExecutor_CallPipeline_DryRunSkipsPreflight(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()

	child := fakePipeline(t, "dry-child",
		`{"dsl_version":"1.0","name":"dry-child","steps":[`+
			`{"id":"c1","type":"agent_run","agent_slug":"cagent","prompt":"1"}]}`,
		"crew_child", "agent_lead")

	gate := &stubPreflight{err: errors.New("must not be consulted")}
	exec := NewExecutor(store, resolver, newMockRunner(), nil).WithRunPreflight(gate)
	exec.WithPipelineResolver(pipeResolverFn(func(_ context.Context, _, slug string) (*Pipeline, error) {
		if slug == "dry-child" {
			return child, nil
		}
		return nil, ErrNotFound
	}))

	if _, err := exec.RunDefinition(context.Background(), &DSL{
		Name:  "dry-parent",
		Steps: []Step{{ID: "callChild", Type: StepCallPipeline, PipelineSlug: "dry-child"}},
	}, RunInput{WorkspaceID: "ws_test", AuthorCrewID: "crew_a", Mode: ModeDryRun}); err != nil {
		t.Fatalf("dry-run returned transport error: %v", err)
	}
	if len(gate.snapshot()) != 0 {
		t.Errorf("preflight consulted on a dry run (%d times) — it must be exempt, as on the HTTP path", len(gate.snapshot()))
	}
}

// The THIRD door. RunPreflight's own doc says the operation "had two doors and
// only one was guarded"; the composition substrate added another. An
// automation-fired run enters through PendingRunDispatcher and goes straight
// to Executor.Run, which never consulted the gates — so a routine whose
// integrations, resources or credentials a user cannot satisfy by hand could
// start itself the moment a rule matched.
//
// Guarded at Run rather than in the dispatcher on purpose: fixing the
// dispatcher would guard door three and leave door four to whoever adds it.
// Run already documents itself as the top-level chokepoint and carries the
// governance status gate for exactly this reason.
func TestExecutor_Run_RefusedByPreflight(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()

	in := validSaveInput("gated-top")
	in.DefinitionJSON = `{"dsl_version":"1.0","name":"gated-top","steps":[` +
		`{"id":"s1","type":"agent_run","agent_slug":"agent_lead","prompt":"1"}]}`
	p, serr := store.Save(context.Background(), in)
	if serr != nil {
		t.Fatalf("save: %v", serr)
	}

	gate := &stubPreflight{err: fmt.Errorf("%w: routine requires an integration the crew has not connected", ErrRunPreflightBlocked)}
	exec := NewExecutor(store, resolver, newMockRunner(), nil).WithRunPreflight(gate)

	_, err := exec.Run(context.Background(), RunInput{
		WorkspaceID: "ws_test", PipelineID: p.ID, Mode: ModeRun,
	})
	if !errors.Is(err, ErrRunPreflightBlocked) {
		t.Fatalf("err = %v, want it to wrap ErrRunPreflightBlocked — a top-level dispatch that "+
			"skips the gates lets an automation start a routine nobody can start by hand", err)
	}
	if len(gate.snapshot()) != 1 {
		t.Fatalf("preflight consulted %d times, want 1", len(gate.snapshot()))
	}
	// The AUTHOR crew is the subject, not the invoker: a routine runs in its
	// author's crew, so its author's connections decide whether it can work.
	if got := gate.snapshot()[0].AuthorCrewID; got != "crew_a" {
		t.Errorf("AuthorCrewID = %q, want crew_a", got)
	}
	if gate.snapshot()[0].DSL == nil {
		t.Error("the gates need the parsed definition to see what the routine declares")
	}
}

// dry_run previews and touches no vault. Gating it would refuse a preview of
// the very routine an operator is trying to diagnose, and the HTTP path is
// exempt for the same reason — two doors that disagree about the edges are two
// doors again.
func TestExecutor_Run_DryRunIsNotGated(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()

	in := validSaveInput("gated-dry")
	in.DefinitionJSON = `{"dsl_version":"1.0","name":"gated-dry","steps":[` +
		`{"id":"s1","type":"agent_run","agent_slug":"agent_lead","prompt":"1"}]}`
	p, serr := store.Save(context.Background(), in)
	if serr != nil {
		t.Fatalf("save: %v", serr)
	}

	gate := &stubPreflight{err: fmt.Errorf("%w: blocked", ErrRunPreflightBlocked)}
	exec := NewExecutor(store, resolver, newMockRunner(), nil).WithRunPreflight(gate)

	if _, err := exec.Run(context.Background(), RunInput{
		WorkspaceID: "ws_test", PipelineID: p.ID, Mode: ModeDryRun,
	}); errors.Is(err, ErrRunPreflightBlocked) {
		t.Error("dry_run was refused by the dispatch gates; it previews and asserts nothing")
	}
	if len(gate.snapshot()) != 0 {
		t.Errorf("preflight consulted %d times on a dry run, want 0", len(gate.snapshot()))
	}
}
