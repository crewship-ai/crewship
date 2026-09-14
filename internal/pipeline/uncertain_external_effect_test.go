package pipeline

// Uncertain external effect and the at-least-once boundary of
// resume-from-step (PRD §9: "HTTP chyba po možném externím zápisu" and
// "Restart u waitpointu a rozpracovaného kroku").
//
// Every assertion here is made against a service that COUNTS what really
// reached it, not against the executor's own belief about what it did. That
// is the whole point: the two claims under test — "the effect may already
// have happened when the response is lost" and "the in-flight step
// re-executes after a restart" — are only checkable from the far side of
// the wire. A mock runner that returns whatever the test told it to return
// can confirm neither.
//
// The SSRF guard makes the live-server layer unable to host this scenario at
// all: an http step refuses every loopback/RFC1918 address (httpsafe's
// privateReachableCIDRs), so no controlled recorder can be reached from a
// real crewshipd. SetAllowPrivateHTTPForTesting is the only hatch, and it
// relaxes exactly that guard and nothing else.
//
// What IS production here: runHTTPStep itself, the run store and its step-
// output persistence, the boot resume scan and its drift gate. What is NOT:
// the crew network policy gate and the credential resolver, which
// NewWiredExecutor installs and the rigs below leave nil — this file measures
// what happens AFTER a request is allowed out, so the layers that decide
// whether it may leave are deliberately absent and are covered by
// runner_http_test.go and http_egress_credentials_test.go.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// effectRecorder is the controlled external service. It appends to its own
// ledger BEFORE it decides how (or whether) to answer, so the ledger records
// the effect even on the responses the caller never sees.
type effectRecorder struct {
	mu     sync.Mutex
	hits   map[string]int
	bodies map[string][]string

	// gate, when non-nil for a path, blocks that path's FIRST request until
	// the channel is closed. Later requests to the same path are not gated —
	// a resumed run must be able to get through.
	gate     map[string]chan struct{}
	gateDone map[string]bool

	// cut names the paths whose response is truncated: headers and a partial
	// body are flushed, then the connection is closed without the rest. The
	// caller sees a transport error after the effect has been applied.
	cut map[string]bool

	// sleep names paths that stall before answering, so a step timeout fires
	// while the far side is already committed.
	sleep map[string]time.Duration
}

func newEffectRecorder() *effectRecorder {
	return &effectRecorder{
		hits:     map[string]int{},
		bodies:   map[string][]string{},
		gate:     map[string]chan struct{}{},
		gateDone: map[string]bool{},
		cut:      map[string]bool{},
		sleep:    map[string]time.Duration{},
	}
}

func (r *effectRecorder) count(path string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.hits[path]
}

func (r *effectRecorder) record(path, body string) (gate chan struct{}, cut bool, sleep time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hits[path]++
	r.bodies[path] = append(r.bodies[path], body)
	if g, ok := r.gate[path]; ok && !r.gateDone[path] {
		r.gateDone[path] = true
		gate = g
	}
	return gate, r.cut[path], r.sleep[path]
}

func (r *effectRecorder) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		raw, _ := io.ReadAll(req.Body)
		gate, cut, sleep := r.record(req.URL.Path, string(raw))
		if gate != nil {
			<-gate
		}
		if sleep > 0 {
			time.Sleep(sleep)
		}
		if cut {
			// Promise more body than we send, then hang up. The client's
			// read fails with an unexpected EOF — after the ledger entry
			// above is already durable.
			w.Header().Set("Content-Length", "512")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"effect":"applied","truncated`))
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			if hj, ok := w.(http.Hijacker); ok {
				conn, _, err := hj.Hijack()
				if err == nil {
					_ = conn.Close()
				}
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"effect":"applied","path":%q}`, req.URL.Path)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// effectDSL is one prepare → apply → finish routine over the recorder, with
// a distinct path per step so the ledger says which step ran how often.
func effectDSL(base string, applyTimeoutSec int) string {
	return fmt.Sprintf(`{
  "dsl_version": "1.0",
  "name": "uncertain-effect",
  "steps": [
    {"id": "prepare", "type": "http", "http": {"method": "POST", "url": %q, "body": "{\"step\":\"prepare\"}"}},
    {"id": "apply",   "type": "http", "timeout_seconds": %d, "http": {"method": "POST", "url": %q, "body": "{\"step\":\"apply\"}"}},
    {"id": "finish",  "type": "http", "http": {"method": "POST", "url": %q, "body": "{\"step\":\"finish\"}"}}
  ]
}`, base+"/prepare", applyTimeoutSec, base+"/apply", base+"/finish")
}

// effectRig wires the production executor pieces this scenario needs against
// the resume test schema: real Store, real RunStore (so step outputs land in
// pipeline_run_step_outputs the way migration v156 requires), real HTTP
// runner.
type effectRig struct {
	store    *Store
	runStore *RunStore
	exec     *Executor
	pipeline *Pipeline
}

func newEffectRig(t *testing.T, slug, definitionJSON string) *effectRig {
	t.Helper()
	db := openResumeTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	store := NewStore(db)
	runStore := NewRunStore(db)
	p := saveResumePipeline(t, store, slug, definitionJSON)
	exec := NewExecutor(store, NewResolver(db), newMockRunner(), &captureEmitter{}).
		WithRunStore(runStore)
	exec.SetAllowPrivateHTTPForTesting(true)
	return &effectRig{store: store, runStore: runStore, exec: exec, pipeline: p}
}

// TestUncertainEffect_ResponseLostAfterWrite_FailsHonestly is the §9 row
// "HTTP chyba po možném externím zápisu": the far side applied the effect and
// the answer never arrived. The run must fail naming the step and the
// transport reason, must keep the earlier step's output, and must not run the
// step after the uncertain one — "continue safely" is a claim the executor is
// in no position to make.
func TestUncertainEffect_ResponseLostAfterWrite_FailsHonestly(t *testing.T) {
	rec := newEffectRecorder()
	rec.cut["/apply"] = true
	srv := rec.server(t)

	rig := newEffectRig(t, "uncertain-cut", effectDSL(srv.URL, 0))

	res, err := rig.exec.Run(context.Background(), RunInput{
		PipelineID: rig.pipeline.ID, WorkspaceID: "ws_test", Mode: ModeRun,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	// The far side is committed exactly once, and the executor cannot know it.
	if got := rec.count("/apply"); got != 1 {
		t.Fatalf("effect ledger: /apply hit %d times, want 1 — the premise of this test is that the write DID happen", got)
	}
	if res.Status != "FAILED" {
		t.Fatalf("status = %q, want FAILED — a lost response must not read as success", res.Status)
	}
	if res.FailedAtStep != "apply" {
		t.Errorf("failed_at_step = %q, want \"apply\"", res.FailedAtStep)
	}
	if !strings.Contains(res.ErrorMessage, "apply") {
		t.Errorf("error %q does not name the step that may already have written", res.ErrorMessage)
	}
	if res.StepOutputs["prepare"] == "" {
		t.Error("the completed prepare step's output was dropped; a failure must not discard the intermediate results")
	}
	if _, ran := res.StepOutputs["finish"]; ran {
		t.Error("the step after the uncertain one ran — that is the unverified \"continue safely\" the PRD forbids")
	}
	if got := rec.count("/finish"); got != 0 {
		t.Errorf("/finish hit %d times, want 0", got)
	}

	// Durable state must say the same thing the result does.
	stored, err := rig.runStore.Get(context.Background(), res.RunID)
	if err != nil {
		t.Fatalf("reload run: %v", err)
	}
	if stored.Status != RunStatusFailed || stored.FailedAtStep != "apply" {
		t.Errorf("persisted run = (%q, %q), want (failed, apply)", stored.Status, stored.FailedAtStep)
	}
	outputs, err := rig.runStore.GetStepOutputs(context.Background(), res.RunID)
	if err != nil {
		t.Fatalf("step outputs: %v", err)
	}
	if outputs["prepare"] == "" {
		t.Error("prepare's output is not durable, so a restart could not tell the effect apart from never having started")
	}
}

// TestUncertainEffect_StepTimeout_LeavesTheEffectApplied is the same row from
// the other direction: the caller gives up first. The step timeout is the
// executor's own clock, so the failure is certain while the effect is not.
func TestUncertainEffect_StepTimeout_LeavesTheEffectApplied(t *testing.T) {
	rec := newEffectRecorder()
	rec.sleep["/apply"] = 3 * time.Second
	srv := rec.server(t)

	rig := newEffectRig(t, "uncertain-timeout", effectDSL(srv.URL, 1))

	res, err := rig.exec.Run(context.Background(), RunInput{
		PipelineID: rig.pipeline.ID, WorkspaceID: "ws_test", Mode: ModeRun,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := rec.count("/apply"); got != 1 {
		t.Fatalf("/apply hit %d times, want 1", got)
	}
	if res.Status != "FAILED" {
		t.Fatalf("status = %q, want FAILED", res.Status)
	}
	if res.FailedAtStep != "apply" {
		t.Errorf("failed_at_step = %q, want \"apply\"", res.FailedAtStep)
	}
	if got := rec.count("/finish"); got != 0 {
		t.Errorf("/finish hit %d times after an uncertain step, want 0", got)
	}
}

// TestUncertainEffect_ResumeReappliesTheInFlightStep measures the
// at-least-once boundary the PRD asks to be documented rather than promised
// away: after a hard kill, completed steps are restored and the step that was
// mid-flight runs AGAIN — and when that step has an external effect, the
// effect happens twice.
//
// The pre-kill state is produced by the real executor, not fabricated: run A
// is left blocked inside /apply, which is exactly the row a SIGKILL leaves
// (status=running, current_step_id=apply, prepare's output already durable).
func TestUncertainEffect_ResumeReappliesTheInFlightStep(t *testing.T) {
	rec := newEffectRecorder()
	release := make(chan struct{})
	rec.gate["/apply"] = release // only the FIRST /apply blocks
	srv := rec.server(t)

	rig := newEffectRig(t, "uncertain-resume", effectDSL(srv.URL, 0))

	// Run A: the process that is about to die.
	runA := make(chan struct{})
	go func() {
		defer close(runA)
		_, _ = rig.exec.Run(context.Background(), RunInput{
			PipelineID: rig.pipeline.ID, WorkspaceID: "ws_test", Mode: ModeRun,
		})
	}()
	// Let it get into /apply and persist prepare's output.
	var inFlight *RunRecord
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if rec.count("/apply") == 1 {
			recs, err := rig.runStore.ListInFlight(context.Background())
			if err == nil && len(recs) == 1 && recs[0].CurrentStepID == "apply" {
				inFlight = recs[0]
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if inFlight == nil {
		t.Fatal("run A never reached the in-flight /apply state")
	}
	outputs, err := rig.runStore.GetStepOutputs(context.Background(), inFlight.ID)
	if err != nil || outputs["prepare"] == "" {
		t.Fatalf("prepare's output is not durable before the kill (outputs=%v err=%v)", outputs, err)
	}
	if got := rec.count("/prepare"); got != 1 {
		t.Fatalf("/prepare hit %d times before the kill, want 1", got)
	}

	// The kill. Run A's goroutine is abandoned the way a SIGKILL abandons a
	// process: it never writes a terminal row. Released only at teardown,
	// after every assertion, so it cannot race the resumed run.
	t.Cleanup(func() {
		close(release)
		<-runA
	})

	// Run B: the new process. A fresh executor with a resume cutoff in the
	// future and its own registry, so the boot scan sees the row as left over
	// from a previous lifetime — the production fence.
	execB := NewExecutor(rig.store, NewResolver(rig.runStore.db), newMockRunner(), &captureEmitter{}).
		WithRunStore(rig.runStore).
		WithRunRegistry(NewRunRegistry()).
		WithResumeCutoff(time.Now().Add(time.Hour))
	execB.SetAllowPrivateHTTPForTesting(true)

	resumed, interrupted, err := execB.ResumeInterruptedRuns(context.Background(),
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("boot resume: %v", err)
	}
	if resumed != 1 || interrupted != 0 {
		t.Fatalf("boot resume = (resumed %d, interrupted %d), want (1, 0)", resumed, interrupted)
	}

	final := waitForRunStatus(t, rig.runStore, inFlight.ID, RunStatusCompleted, 15*time.Second)
	if final.ID != inFlight.ID {
		t.Errorf("resume created run %q instead of continuing %q — a resume is not a new run", final.ID, inFlight.ID)
	}

	// The measurement the whole test exists for.
	if got := rec.count("/prepare"); got != 1 {
		t.Errorf("/prepare hit %d times across the kill, want 1 — a completed step must be restored, not repeated", got)
	}
	if got := rec.count("/apply"); got != 2 {
		t.Errorf("/apply hit %d times across the kill, want 2 — the in-flight step re-executes, which is at-least-once, not exactly-once", got)
	}
	if got := rec.count("/finish"); got != 1 {
		t.Errorf("/finish hit %d times, want 1", got)
	}
}

// TestUncertainEffect_ReplayIsANewRunNotAResume separates the two things a
// reader of a failed run can do. A replay is a NEW run id that repeats every
// step from the start; only a resume continues the original id with the
// original outputs. Conflating them is how "we retried it" turns into "we
// applied it twice and called it once".
func TestUncertainEffect_ReplayIsANewRunNotAResume(t *testing.T) {
	rec := newEffectRecorder()
	srv := rec.server(t)
	rig := newEffectRig(t, "uncertain-replay", effectDSL(srv.URL, 0))

	first, err := rig.exec.Run(context.Background(), RunInput{
		PipelineID: rig.pipeline.ID, WorkspaceID: "ws_test", Mode: ModeRun,
	})
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	second, err := rig.exec.Run(context.Background(), RunInput{
		PipelineID: rig.pipeline.ID, WorkspaceID: "ws_test", Mode: ModeRun,
	})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if first.RunID == second.RunID {
		t.Fatalf("two deliberate starts share run id %q", first.RunID)
	}
	for _, p := range []string{"/prepare", "/apply", "/finish"} {
		if got := rec.count(p); got != 2 {
			t.Errorf("%s hit %d times over two deliberate runs, want 2 — a new run repeats every step", p, got)
		}
	}
}

// TestUncertainEffect_RecorderLedgerIsTheOnlyWitness pins the premise the
// three tests above rely on: the recorder counts a request whose response the
// client never reads. If this ever stops holding, the "effect applied" claims
// elsewhere in this file become unfalsifiable rather than false.
func TestUncertainEffect_RecorderLedgerIsTheOnlyWitness(t *testing.T) {
	rec := newEffectRecorder()
	rec.cut["/apply"] = true
	srv := rec.server(t)

	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	exec := NewExecutor(store, resolver, nil, nil)
	exec.SetAllowPrivateHTTPForTesting(true)

	step := Step{ID: "apply", Type: StepHTTP, HTTP: &HTTPStep{
		Method: "POST", URL: srv.URL + "/apply", Body: `{"step":"apply"}`,
	}}
	_, _, _, err := exec.runHTTPStep(context.Background(), step, RenderContext{}, RunInput{})
	if err == nil {
		t.Fatal("truncated response returned no error; the scenario under test cannot occur")
	}
	if got := rec.count("/apply"); got != 1 {
		t.Fatalf("ledger recorded %d hits for a request the caller saw fail, want 1", got)
	}
	rec.mu.Lock()
	bodies := append([]string(nil), rec.bodies["/apply"]...)
	rec.mu.Unlock()
	var decoded map[string]string
	if jerr := json.Unmarshal([]byte(bodies[0]), &decoded); jerr != nil || decoded["step"] != "apply" {
		t.Errorf("recorded body %q is not the rendered step body", bodies[0])
	}
}
