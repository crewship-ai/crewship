package pipeline

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// runner_wait.go — Executor.runWaitStep.
//
// Three flavours of wait step:
//   - datetime: sleep until RFC3339 Until
//   - approval: park on a WaitpointStore-issued token
//   - event:    Phase-2 placeholder → must fail loudly
//
// Each branch carries error-message shape contracts that downstream
// telemetry / inbox UI greps on. A regression in any one would
// silently change the meaning of pipeline output strings or hide
// real failures behind misleading errors.
//
// The waitpoints unit tests cover the store; here we cover only
// the step orchestration in runWaitStep.
// ---------------------------------------------------------------------------

// fakeWaitpointStore satisfies WaitpointStore with programmable
// behaviour per test. CreateApproval returns createErr or token+nil;
// WaitFor returns approved/waitErr.
type fakeWaitpointStore struct {
	mu sync.Mutex

	createCalls   int
	createToken   string
	createErr     error
	lastCreateReq WaitpointApprovalRequest

	waitCalls   int
	waitToken   string
	waitApprove bool
	waitErr     error
}

func (f *fakeWaitpointStore) CreateApproval(_ context.Context, req WaitpointApprovalRequest) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createCalls++
	f.lastCreateReq = req
	if f.createErr != nil {
		return "", f.createErr
	}
	if f.createToken == "" {
		f.createToken = "tok-fake"
	}
	return f.createToken, nil
}

func (f *fakeWaitpointStore) WaitFor(_ context.Context, token string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.waitCalls++
	f.waitToken = token
	return f.waitApprove, f.waitErr
}

// emptyRender returns a RenderContext with empty namespaces so
// `Render(template, ctx)` substitutes missing refs to "".
func emptyRender() RenderContext {
	return RenderContext{
		Inputs:      map[string]any{},
		StepOutputs: map[string]string{},
		Env:         map[string]string{},
	}
}

func waitStepReq(kind string) Step {
	return Step{
		ID:   "wait_x",
		Type: StepWait,
		Wait: &WaitStep{Kind: kind},
	}
}

// ---- nil body / unknown kind ----

func TestRunWaitStep_NilWaitBody_Errors(t *testing.T) {
	// Defensive: a wait-type step without a Wait body is malformed.
	// Surfacing the error here keeps the executor's deferred timing
	// + journal emit semantics consistent for malformed steps.
	e := &Executor{}
	step := Step{ID: "wait_nil", Type: StepWait, Wait: nil}
	out, cost, _, err := e.runWaitStep(context.Background(), step, emptyRender(), RunInput{}, "run-1")
	if err == nil {
		t.Fatal("expected error on nil Wait body")
	}
	if !strings.Contains(err.Error(), "missing body") {
		t.Errorf("err = %v, want \"missing body\" marker", err)
	}
	if out != "" || cost != 0 {
		t.Errorf("out/cost should be zero values on error; got out=%q cost=%v", out, cost)
	}
}

func TestRunWaitStep_UnknownKind_Errors(t *testing.T) {
	// Unrecognised kind falls through the switch. Pin the
	// "unknown kind" message + that the offending kind value is
	// quoted in the error (operator triage signal).
	e := &Executor{}
	step := waitStepReq("flux-capacitor")
	_, _, _, err := e.runWaitStep(context.Background(), step, emptyRender(), RunInput{}, "run-1")
	if err == nil {
		t.Fatal("expected error on unknown kind")
	}
	if !strings.Contains(err.Error(), "unknown kind") {
		t.Errorf("err = %v, want \"unknown kind\" marker", err)
	}
	if !strings.Contains(err.Error(), `"flux-capacitor"`) {
		t.Errorf("err = %v, must quote the offending kind for triage", err)
	}
}

// ---- event kind ----

func TestRunWaitStep_EventKind_NotImplementedYet(t *testing.T) {
	// Source: Phase-2 event waits return a clear "not yet implemented"
	// instead of silently hanging. Pin so a regression that returned
	// nil ("succeeded with no wait") doesn't let pipelines unknowingly
	// skip event gates.
	e := &Executor{}
	step := waitStepReq("event")
	_, _, _, err := e.runWaitStep(context.Background(), step, emptyRender(), RunInput{}, "run-1")
	if err == nil {
		t.Fatal("expected \"not yet implemented\" error on event kind")
	}
	if !strings.Contains(err.Error(), "not yet implemented") {
		t.Errorf("err = %v, want \"not yet implemented\"", err)
	}
}

// ---- datetime kind ----

func TestRunWaitStep_Datetime_PastReturnsImmediately(t *testing.T) {
	// Past Until → "waited:datetime:past" with no sleep. Authors use
	// past timestamps as a "skip this wait if input data is stale"
	// idiom — pin so a regression to "always sleep ≥0" breaks that.
	e := &Executor{}
	step := waitStepReq("datetime")
	step.Wait.Until = "2020-01-01T00:00:00Z"

	out, _, _, err := e.runWaitStep(context.Background(), step, emptyRender(), RunInput{}, "run-1")
	if err != nil {
		t.Fatalf("past datetime: %v", err)
	}
	if out != "waited:datetime:past" {
		t.Errorf("out = %q, want \"waited:datetime:past\"", out)
	}
}

func TestRunWaitStep_Datetime_FutureSleepsThenReturns(t *testing.T) {
	// Future Until → sleep until then, return "waited:datetime"
	// (no ":past" suffix — pin the distinct output so downstream
	// templates can tell which branch fired).
	e := &Executor{}
	step := waitStepReq("datetime")
	step.Wait.Until = time.Now().Add(50 * time.Millisecond).UTC().Format(time.RFC3339Nano)

	start := time.Now()
	out, _, _, err := e.runWaitStep(context.Background(), step, emptyRender(), RunInput{}, "run-1")
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("future datetime: %v", err)
	}
	if out != "waited:datetime" {
		t.Errorf("out = %q, want \"waited:datetime\" (no :past suffix on the future branch)", out)
	}
	if elapsed < 40*time.Millisecond {
		t.Errorf("elapsed = %v, want ≥ 40ms (the future wait must actually sleep)", elapsed)
	}
}

func TestRunWaitStep_Datetime_ContextCancelInterrupts(t *testing.T) {
	// Mid-sleep ctx cancel returns ctx.Err() (NOT a wrapped
	// "wait step ... wait" error — the ctx is treated as a clean
	// cancellation, not a parse/store failure).
	e := &Executor{}
	step := waitStepReq("datetime")
	step.Wait.Until = time.Now().Add(10 * time.Second).UTC().Format(time.RFC3339Nano)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, _, _, err := e.runWaitStep(ctx, step, emptyRender(), RunInput{}, "run-1")
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want errors.Is(err, context.Canceled)", err)
	}
	if elapsed := time.Since(start); elapsed > 1*time.Second {
		t.Errorf("elapsed = %v, ctx cancel should have unblocked the sleep within ~30ms (10s wait would be intolerable)", elapsed)
	}
}

func TestRunWaitStep_Datetime_UnparseableUntil_WrapsError(t *testing.T) {
	// Bad Until → wrapped error with "parse until" prefix and the
	// offending value quoted. Operator can grep the failing string.
	e := &Executor{}
	step := waitStepReq("datetime")
	step.Wait.Until = "this is not a date"

	_, _, _, err := e.runWaitStep(context.Background(), step, emptyRender(), RunInput{}, "run-1")
	if err == nil {
		t.Fatal("expected parse error")
	}
	if !strings.Contains(err.Error(), "parse until") {
		t.Errorf("err = %v, want \"parse until\" prefix", err)
	}
	if !strings.Contains(err.Error(), `"this is not a date"`) {
		t.Errorf("err = %v, must quote the offending value for triage", err)
	}
}

func TestRunWaitStep_Datetime_UntilFromInputTemplate(t *testing.T) {
	// Source: `untilRaw := Render(step.Wait.Until, parentRender)` —
	// authors can pass `{{ inputs.deadline }}` and the renderer
	// substitutes before parse. Pin the integration so a regression
	// that bypassed Render would break templated waits.
	e := &Executor{}
	step := waitStepReq("datetime")
	step.Wait.Until = "{{ inputs.deadline }}"

	render := emptyRender()
	render.Inputs["deadline"] = "2020-01-01T00:00:00Z" // past → immediate return

	out, _, _, err := e.runWaitStep(context.Background(), step, render, RunInput{}, "run-1")
	if err != nil {
		t.Fatalf("templated past Until: %v", err)
	}
	if out != "waited:datetime:past" {
		t.Errorf("out = %q, want \"waited:datetime:past\" (template should have rendered to past timestamp)", out)
	}
}

// ---- approval kind ----

func TestRunWaitStep_Approval_NoStoreWired_ContextCancelExits(t *testing.T) {
	// Source: when WaitpointStore is nil, the function parks for 60s
	// or until ctx.Done. Cancelling fast lets us validate the branch
	// without waiting a real minute.
	e := &Executor{} // no waitpoints
	step := waitStepReq("approval")
	step.Wait.ApprovalPrompt = "Approve?"

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, _, _, err := e.runWaitStep(ctx, step, emptyRender(), RunInput{}, "run-1")
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want errors.Is(err, context.Canceled)", err)
	}
	if time.Since(start) > 1*time.Second {
		t.Errorf("no-store ctx cancel took > 1s; the 60s timer must yield to ctx.Done")
	}
}

func TestRunWaitStep_Approval_HappyPath(t *testing.T) {
	// CreateApproval returns a token; WaitFor returns approved=true
	// → output "waited:approval:approved". Pin the exact string —
	// downstream templates can match on the ":approved" suffix to
	// distinguish from denied (where the function returns an error,
	// no output).
	store := &fakeWaitpointStore{createToken: "tok-happy", waitApprove: true}
	e := &Executor{waitpoints: store}
	step := waitStepReq("approval")
	step.Wait.ApprovalPrompt = "Ship it?"

	out, _, _, err := e.runWaitStep(context.Background(), step,
		emptyRender(),
		RunInput{WorkspaceID: "ws-1", InvokingCrewID: "crew-x"},
		"run-42")
	if err != nil {
		t.Fatalf("approval happy: %v", err)
	}
	if out != "waited:approval:approved" {
		t.Errorf("out = %q, want \"waited:approval:approved\"", out)
	}

	// Verify CreateApproval got the full context (workspace, pipeline
	// run id, step id, prompt, invoking crew, timeout).
	got := store.lastCreateReq
	if got.WorkspaceID != "ws-1" {
		t.Errorf("CreateApproval WorkspaceID = %q, want \"ws-1\"", got.WorkspaceID)
	}
	if got.PipelineRunID != "run-42" {
		t.Errorf("CreateApproval PipelineRunID = %q, want \"run-42\"", got.PipelineRunID)
	}
	if got.StepID != "wait_x" {
		t.Errorf("CreateApproval StepID = %q, want \"wait_x\"", got.StepID)
	}
	if got.Prompt != "Ship it?" {
		t.Errorf("CreateApproval Prompt = %q, want \"Ship it?\"", got.Prompt)
	}
	if got.InvokingCrewID != "crew-x" {
		t.Errorf("CreateApproval InvokingCrewID = %q, want \"crew-x\"", got.InvokingCrewID)
	}
	if store.waitToken != "tok-happy" {
		t.Errorf("WaitFor token = %q, want \"tok-happy\" (must use what CreateApproval returned)", store.waitToken)
	}
}

func TestRunWaitStep_Approval_Denied_ReturnsError(t *testing.T) {
	// approved=false → "(approval) denied" error. Pin the exact
	// marker — the executor catches it as a step failure but the
	// audit timeline groups all denied approvals by this string.
	store := &fakeWaitpointStore{waitApprove: false}
	e := &Executor{waitpoints: store}
	step := waitStepReq("approval")

	_, _, _, err := e.runWaitStep(context.Background(), step, emptyRender(), RunInput{}, "run-1")
	if err == nil {
		t.Fatal("expected error on denied approval")
	}
	if !strings.Contains(err.Error(), "(approval) denied") {
		t.Errorf("err = %v, want \"(approval) denied\" marker", err)
	}
}

func TestRunWaitStep_Approval_CreateError_WrapsWithCreateApproval(t *testing.T) {
	// Store CreateApproval failure → wrapped with "create approval"
	// prefix. Pin the prefix so an operator log line tells them which
	// stage (create vs wait) failed.
	store := &fakeWaitpointStore{createErr: errors.New("db went away")}
	e := &Executor{waitpoints: store}
	step := waitStepReq("approval")

	_, _, _, err := e.runWaitStep(context.Background(), step, emptyRender(), RunInput{}, "run-1")
	if err == nil {
		t.Fatal("expected error from CreateApproval failure")
	}
	if !strings.Contains(err.Error(), "create approval") {
		t.Errorf("err = %v, want \"create approval\" stage marker", err)
	}
	if !strings.Contains(err.Error(), "db went away") {
		t.Errorf("err = %v, must wrap the original (errors.Is should reach it)", err)
	}
}

func TestRunWaitStep_Approval_WaitForError_WrapsWithWait(t *testing.T) {
	// Store WaitFor failure → wrapped with "wait" prefix. Distinct
	// from "create approval" so triage knows which side faulted.
	store := &fakeWaitpointStore{waitErr: errors.New("listener torn down")}
	e := &Executor{waitpoints: store}
	step := waitStepReq("approval")

	_, _, _, err := e.runWaitStep(context.Background(), step, emptyRender(), RunInput{}, "run-1")
	if err == nil {
		t.Fatal("expected error from WaitFor failure")
	}
	// "wait" appears in many phrases — pin the actual format from
	// fmt.Errorf: "wait step %q wait: %w" — looking for ` wait: `
	// (with surrounding space + colon).
	if !strings.Contains(err.Error(), " wait: ") {
		t.Errorf("err = %v, want \" wait: \" stage marker (distinguishes from create-approval failure)", err)
	}
	if !strings.Contains(err.Error(), "listener torn down") {
		t.Errorf("err = %v, must wrap the original", err)
	}
}

func TestRunWaitStep_Approval_PromptRenderedFromTemplate(t *testing.T) {
	// ApprovalPrompt goes through Render so authors can template
	// the prompt with inputs/step outputs ("Approve deploy to
	// {{ inputs.env }}?"). Pin so a regression to literal-only would
	// break informative prompts.
	store := &fakeWaitpointStore{waitApprove: true}
	e := &Executor{waitpoints: store}
	step := waitStepReq("approval")
	step.Wait.ApprovalPrompt = "Ship to {{ inputs.env }}?"

	render := emptyRender()
	render.Inputs["env"] = "production"

	_, _, _, err := e.runWaitStep(context.Background(), step, render, RunInput{}, "run-1")
	if err != nil {
		t.Fatalf("approval with template: %v", err)
	}
	if store.lastCreateReq.Prompt != "Ship to production?" {
		t.Errorf("CreateApproval Prompt = %q, want \"Ship to production?\" (template should have rendered)", store.lastCreateReq.Prompt)
	}
}
