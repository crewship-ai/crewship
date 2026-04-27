package reflection

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/quartermaster"
)

// ----- Test doubles -----

// recordingEmitter captures every journal entry in memory so tests can
// assert on the sequence without spinning up SQLite.
type recordingEmitter struct {
	mu      sync.Mutex
	entries []journal.Entry
}

func (r *recordingEmitter) Emit(_ context.Context, e journal.Entry) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e.ID == "" {
		e.ID = fmt.Sprintf("j_%d", len(r.entries))
	}
	r.entries = append(r.entries, e)
	return e.ID, nil
}

func (r *recordingEmitter) Flush(_ context.Context) error { return nil }

func (r *recordingEmitter) byType(t journal.EntryType) []journal.Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []journal.Entry
	for _, e := range r.entries {
		if e.Type == t {
			out = append(out, e)
		}
	}
	return out
}

// stubCritiquer returns a pre-scripted critique per persona and records
// the goroutine-visible start time so tests can check parallelism.
type stubCritiquer struct {
	mu        sync.Mutex
	starts    map[Persona]time.Time
	delay     time.Duration
	scripted  map[Persona]Critique
	errOnce   *Persona
	callCount int32
}

func newStubCritiquer(scripted map[Persona]Critique, delay time.Duration) *stubCritiquer {
	return &stubCritiquer{
		starts:   make(map[Persona]time.Time),
		delay:    delay,
		scripted: scripted,
	}
}

func (s *stubCritiquer) Critique(ctx context.Context, p Persona, _ string, _ string) (Critique, error) {
	s.mu.Lock()
	s.starts[p] = time.Now()
	s.mu.Unlock()
	atomic.AddInt32(&s.callCount, 1)
	if s.errOnce != nil && *s.errOnce == p {
		s.errOnce = nil
		return Critique{}, errors.New("scripted critique failure")
	}
	if s.delay > 0 {
		select {
		case <-time.After(s.delay):
		case <-ctx.Done():
			return Critique{}, ctx.Err()
		}
	}
	c, ok := s.scripted[p]
	if !ok {
		return Critique{Persona: p, Severity: CritiqueSeverityLow}, nil
	}
	c.Persona = p
	return c, nil
}

// stubJudge returns a pre-scripted JudgeVerdict whose Reasoning field is
// the synthesized JSON envelope we want parseSynthesis to see.
type stubJudge struct {
	verdict quartermaster.JudgeVerdict
	err     error
}

func (s *stubJudge) Judge(_ context.Context, _ string, _ []string) (quartermaster.JudgeVerdict, error) {
	if s.err != nil {
		return quartermaster.JudgeVerdict{}, s.err
	}
	return s.verdict, nil
}

// ----- types / personas tests -----

func TestAllPersonasStable(t *testing.T) {
	p := AllPersonas()
	if len(p) != 3 {
		t.Fatalf("expected 3 default personas, got %d", len(p))
	}
	want := []Persona{PersonaLogician, PersonaSkeptic, PersonaDomainExpert}
	for i, w := range want {
		if p[i] != w {
			t.Errorf("persona[%d]: got %s want %s", i, p[i], w)
		}
	}
}

func TestSystemPromptForKnownAndUnknown(t *testing.T) {
	for _, p := range AllPersonas() {
		if SystemPromptFor(p) == "" {
			t.Errorf("empty prompt for %s", p)
		}
	}
	// Unknown falls back to logician, not empty.
	if got := SystemPromptFor(Persona("alien")); got == "" {
		t.Error("unknown persona returned empty prompt")
	}
}

// ----- critique.go -----

type scriptedClient struct {
	resp string
	err  error
}

func (s *scriptedClient) Call(_ context.Context, _ string, _ string) (string, error) {
	return s.resp, s.err
}

func TestLLMCritiquerParsesJSON(t *testing.T) {
	client := &scriptedClient{
		resp: `{"severity":"high","issues":["missing base case"],"suggestions":["add guard for n<=0"]}`,
	}
	c := NewLLMCritiquer(client)
	got, err := c.Critique(context.Background(), PersonaLogician, "recursive factorial", "")
	if err != nil {
		t.Fatalf("critique: %v", err)
	}
	if got.Severity != CritiqueSeverityHigh {
		t.Errorf("severity: got %s want high", got.Severity)
	}
	if len(got.Issues) != 1 || got.Issues[0] != "missing base case" {
		t.Errorf("issues: %v", got.Issues)
	}
	if got.Persona != PersonaLogician {
		t.Errorf("persona not preserved: %s", got.Persona)
	}
}

func TestLLMCritiquerParsesMarkdownWrappedJSON(t *testing.T) {
	client := &scriptedClient{
		resp: "Sure, here:\n```json\n{\"severity\":\"medium\",\"issues\":[\"x\"],\"suggestions\":[\"y\"]}\n```\nDone.",
	}
	c := NewLLMCritiquer(client)
	got, err := c.Critique(context.Background(), PersonaSkeptic, "s", "")
	if err != nil {
		t.Fatalf("critique: %v", err)
	}
	if got.Severity != CritiqueSeverityMedium {
		t.Errorf("severity: got %s", got.Severity)
	}
	if len(got.Issues) != 1 || got.Issues[0] != "x" {
		t.Errorf("issues: %v", got.Issues)
	}
}

func TestLLMCritiquerUnparseableLandsInRawText(t *testing.T) {
	client := &scriptedClient{resp: "this is not JSON at all"}
	c := NewLLMCritiquer(client)
	got, err := c.Critique(context.Background(), PersonaDomainExpert, "s", "")
	if err != nil {
		t.Fatalf("critique: %v", err)
	}
	if got.RawText != "this is not JSON at all" {
		t.Errorf("raw text not preserved: %q", got.RawText)
	}
	if got.Severity != CritiqueSeverityLow {
		t.Errorf("expected low severity fallback, got %s", got.Severity)
	}
}

func TestLLMCritiquerPropagatesClientError(t *testing.T) {
	client := &scriptedClient{err: errors.New("boom")}
	c := NewLLMCritiquer(client)
	_, err := c.Critique(context.Background(), PersonaLogician, "s", "")
	if err == nil {
		t.Fatal("expected error from client to propagate")
	}
}

// ----- synthesize.go -----

func TestSynthesizeParsesJudgeReasoning(t *testing.T) {
	judge := &stubJudge{verdict: quartermaster.JudgeVerdict{
		Reasoning:  `{"keep":"sound idea","revise":[{"what":"add retry","why":"network flakes"}],"reject":["frivolous"],"confidence":0.8}`,
		Score:      0.7,
		Confidence: 0.9,
	}}
	critiques := []Critique{{Persona: PersonaLogician, Severity: CritiqueSeverityMedium, Issues: []string{"x"}}}
	synth, verdict, err := Synthesize(context.Background(), judge, critiques, "subject")
	if err != nil {
		t.Fatalf("synthesize: %v", err)
	}
	if synth.Keep != "sound idea" {
		t.Errorf("keep: %q", synth.Keep)
	}
	if len(synth.Revise) != 1 || synth.Revise[0].What != "add retry" {
		t.Errorf("revise: %+v", synth.Revise)
	}
	if synth.Confidence != 0.8 {
		t.Errorf("confidence: %v want 0.8", synth.Confidence)
	}
	if verdict.Score != 0.7 {
		t.Errorf("verdict leaked: %v", verdict.Score)
	}
}

func TestSynthesizeDampensOnHighDisagreement(t *testing.T) {
	// Scores with stddev > 0.25 should knock confidence down 25%.
	judge := &stubJudge{verdict: quartermaster.JudgeVerdict{
		Reasoning:  `{"keep":"k","revise":[],"reject":[],"confidence":0.8}`,
		Score:      0.5,
		Confidence: 0.6,
		Scores:     []float64{0.1, 0.5, 0.9}, // stddev ~0.327
	}}
	critiques := []Critique{{Persona: PersonaLogician}}
	synth, _, err := Synthesize(context.Background(), judge, critiques, "s")
	if err != nil {
		t.Fatalf("synthesize: %v", err)
	}
	want := 0.8 * 0.75
	if synth.Confidence < want-0.01 || synth.Confidence > want+0.01 {
		t.Errorf("dampened confidence: got %v want ~%v", synth.Confidence, want)
	}
}

func TestSynthesizeFallsBackToVerdictConfidence(t *testing.T) {
	// No confidence in the JSON → use the judge's verdict confidence.
	judge := &stubJudge{verdict: quartermaster.JudgeVerdict{
		Reasoning:  `{"keep":"k","revise":[],"reject":[]}`,
		Confidence: 0.55,
	}}
	synth, _, err := Synthesize(context.Background(), judge, []Critique{{Persona: PersonaLogician}}, "s")
	if err != nil {
		t.Fatalf("synthesize: %v", err)
	}
	if synth.Confidence != 0.55 {
		t.Errorf("confidence fallback: got %v want 0.55", synth.Confidence)
	}
}

func TestSynthesizeRejectsEmptyCritiques(t *testing.T) {
	_, _, err := Synthesize(context.Background(), &stubJudge{}, nil, "s")
	if err == nil {
		t.Fatal("expected error for empty critiques")
	}
}

func TestSynthesizeRejectsNilJudge(t *testing.T) {
	_, _, err := Synthesize(context.Background(), nil, []Critique{{Persona: PersonaLogician}}, "s")
	if err == nil {
		t.Fatal("expected error for nil judge")
	}
}

// ----- orchestrate.go -----

func TestReflectRunsAllPersonasInParallel(t *testing.T) {
	// 50ms delay per critique. If they ran serially total >= 150ms;
	// parallel should finish <100ms. We give a generous 120ms ceiling to
	// tolerate slow CI boxes.
	cr := newStubCritiquer(map[Persona]Critique{
		PersonaLogician:     {Severity: CritiqueSeverityLow, Issues: []string{"a"}},
		PersonaSkeptic:      {Severity: CritiqueSeverityMedium, Issues: []string{"b"}},
		PersonaDomainExpert: {Severity: CritiqueSeverityHigh, Issues: []string{"c"}},
	}, 50*time.Millisecond)

	judge := &stubJudge{verdict: quartermaster.JudgeVerdict{
		Reasoning:  `{"keep":"","revise":[],"reject":[],"confidence":0.9}`,
		Score:      0.7,
		Confidence: 0.9,
	}}
	em := &recordingEmitter{}

	start := time.Now()
	res, err := Reflect(context.Background(),
		ReflectionRequest{Subject: "sub", Context: "ctx"},
		cr, judge, em,
		Scope{WorkspaceID: "ws"},
	)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("reflect: %v", err)
	}
	if elapsed > 120*time.Millisecond {
		t.Errorf("critiques appear to run serially: elapsed=%v", elapsed)
	}
	if len(res.Critiques) != 3 {
		t.Fatalf("expected 3 critiques, got %d", len(res.Critiques))
	}
	if atomic.LoadInt32(&cr.callCount) != 3 {
		t.Errorf("expected 3 critique calls, got %d", cr.callCount)
	}

	// Confirm 3 critique journal entries + 1 synthesis entry, all tagged.
	entries := em.byType(journal.EntrySummaryGenerated)
	if len(entries) != 4 {
		t.Fatalf("expected 4 summary.generated entries, got %d", len(entries))
	}
	stages := map[string]int{}
	for _, e := range entries {
		if e.Refs["reflection"] != true {
			t.Errorf("entry missing reflection=true in refs: %+v", e.Refs)
		}
		stage, _ := e.Refs["stage"].(string)
		stages[stage]++
	}
	if stages["critique"] != 3 {
		t.Errorf("expected 3 critique-stage entries, got %d", stages["critique"])
	}
	if stages["synthesis"] != 1 {
		t.Errorf("expected 1 synthesis-stage entry, got %d", stages["synthesis"])
	}
}

func TestReflectPropagatesCritiqueError(t *testing.T) {
	p := PersonaSkeptic
	cr := newStubCritiquer(map[Persona]Critique{}, 0)
	cr.errOnce = &p

	judge := &stubJudge{verdict: quartermaster.JudgeVerdict{Reasoning: `{}`}}
	em := &recordingEmitter{}

	_, err := Reflect(context.Background(),
		ReflectionRequest{Subject: "sub"},
		cr, judge, em,
		Scope{WorkspaceID: "ws"},
	)
	if err == nil {
		t.Fatal("expected error to bubble up")
	}
	if !strings.Contains(err.Error(), "skeptic") {
		t.Errorf("error doesn't name failing persona: %v", err)
	}
}

func TestReflectValidates(t *testing.T) {
	em := &recordingEmitter{}
	judge := &stubJudge{}
	cr := newStubCritiquer(nil, 0)

	cases := []struct {
		name    string
		req     ReflectionRequest
		scope   Scope
		cr      Critiquer
		judge   quartermaster.JudgeInterface
		emitter journal.Emitter
	}{
		{"no workspace", ReflectionRequest{Subject: "s"}, Scope{}, cr, judge, em},
		{"no subject", ReflectionRequest{}, Scope{WorkspaceID: "ws"}, cr, judge, em},
		{"no critiquer", ReflectionRequest{Subject: "s"}, Scope{WorkspaceID: "ws"}, nil, judge, em},
		{"no judge", ReflectionRequest{Subject: "s"}, Scope{WorkspaceID: "ws"}, cr, nil, em},
		{"no emitter", ReflectionRequest{Subject: "s"}, Scope{WorkspaceID: "ws"}, cr, judge, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Reflect(context.Background(), tc.req, tc.cr, tc.judge, tc.emitter, tc.scope)
			if err == nil {
				t.Error("expected validation error")
			}
		})
	}
}

// ----- loop.go -----

// scriptedGenerator returns a canned sequence of outputs. Each Generate
// call advances to the next entry; if the slice is exhausted the last
// entry is repeated.
type scriptedGenerator struct {
	outputs     []string
	mu          sync.Mutex
	call        int
	lastContext []string
}

func (g *scriptedGenerator) Generate(_ context.Context, _ string, ctx []string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.lastContext = append([]string(nil), ctx...)
	i := g.call
	g.call++
	if i >= len(g.outputs) {
		i = len(g.outputs) - 1
	}
	return g.outputs[i], nil
}

// scriptedVerifier plays a sequence of VerifyResults in order.
type scriptedVerifier struct {
	results []VerifyResult
	mu      sync.Mutex
	call    int
}

func (v *scriptedVerifier) Verify(_ context.Context, _ string) (VerifyResult, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	i := v.call
	v.call++
	if i >= len(v.results) {
		i = len(v.results) - 1
	}
	return v.results[i], nil
}

func TestEvaluatorLoopPassesEarly(t *testing.T) {
	gen := &scriptedGenerator{outputs: []string{"draft"}}
	ver := &scriptedVerifier{results: []VerifyResult{{Status: "pass"}}}
	em := &recordingEmitter{}

	out, iters, err := EvaluatorLoop(context.Background(), gen, ver, em, "", "prompt", 5, Scope{WorkspaceID: "ws"})
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if iters != 1 {
		t.Errorf("iters: got %d want 1", iters)
	}
	if out != "draft" {
		t.Errorf("output: %q", out)
	}
	metrics := em.byType(journal.EntryEvalMetric)
	if len(metrics) != 1 {
		t.Errorf("expected 1 metric entry, got %d", len(metrics))
	}
}

func TestEvaluatorLoopConsumesInitialBeforeGenerating(t *testing.T) {
	gen := &scriptedGenerator{outputs: []string{"should-not-be-used"}}
	ver := &scriptedVerifier{results: []VerifyResult{{Status: "pass"}}}

	out, iters, err := EvaluatorLoop(context.Background(), gen, ver, nil, "initial-candidate", "prompt", 5, Scope{WorkspaceID: "ws"})
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if iters != 1 {
		t.Errorf("iters: %d", iters)
	}
	if out != "initial-candidate" {
		t.Errorf("initial not used: %q", out)
	}
	if gen.call != 0 {
		t.Errorf("generator should not have been called: %d", gen.call)
	}
}

func TestEvaluatorLoopRetriesWithFeedback(t *testing.T) {
	gen := &scriptedGenerator{outputs: []string{"v1", "v2", "v3"}}
	ver := &scriptedVerifier{results: []VerifyResult{
		{Status: "fail", Issues: []string{"typo"}, SuggestedFix: "fix it"},
		{Status: "fail", Issues: []string{"still off"}},
		{Status: "pass"},
	}}
	em := &recordingEmitter{}

	out, iters, err := EvaluatorLoop(context.Background(), gen, ver, em, "", "prompt", 5, Scope{WorkspaceID: "ws"})
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if iters != 3 {
		t.Errorf("iters: got %d want 3", iters)
	}
	if out != "v3" {
		t.Errorf("final output: %q", out)
	}

	// Final Generate should have seen 2 feedback entries.
	if len(gen.lastContext) != 2 {
		t.Errorf("expected 2 feedback entries in context, got %d: %v", len(gen.lastContext), gen.lastContext)
	}
	if !strings.Contains(gen.lastContext[0], "typo") {
		t.Errorf("first feedback missing issue: %q", gen.lastContext[0])
	}
	if !strings.Contains(gen.lastContext[0], "fix it") {
		t.Errorf("first feedback missing suggested fix: %q", gen.lastContext[0])
	}
	if !strings.Contains(gen.lastContext[1], "still off") {
		t.Errorf("second feedback missing issue: %q", gen.lastContext[1])
	}

	if n := len(em.byType(journal.EntryEvalMetric)); n != 3 {
		t.Errorf("expected 3 metric entries, got %d", n)
	}
}

func TestEvaluatorLoopExhaustsAndReturnsLast(t *testing.T) {
	gen := &scriptedGenerator{outputs: []string{"a", "b", "c"}}
	ver := &scriptedVerifier{results: []VerifyResult{
		{Status: "fail", Issues: []string{"nope"}},
	}}

	out, iters, err := EvaluatorLoop(context.Background(), gen, ver, nil, "", "p", 3, Scope{WorkspaceID: "ws"})
	if err == nil {
		t.Fatal("expected exhaustion error")
	}
	if !errors.Is(err, ErrEvaluatorLoopExhausted) {
		t.Errorf("wrong error: %v", err)
	}
	if iters != 3 {
		t.Errorf("iters: got %d want 3", iters)
	}
	if out != "c" {
		t.Errorf("expected last candidate 'c', got %q", out)
	}
}

func TestEvaluatorLoopValidation(t *testing.T) {
	ver := &scriptedVerifier{results: []VerifyResult{{Status: "pass"}}}
	gen := &scriptedGenerator{outputs: []string{"x"}}

	_, _, err := EvaluatorLoop(context.Background(), nil, ver, nil, "", "p", 5, Scope{WorkspaceID: "ws"})
	if err == nil {
		t.Error("nil generator should error")
	}
	_, _, err = EvaluatorLoop(context.Background(), gen, nil, nil, "", "p", 5, Scope{WorkspaceID: "ws"})
	if err == nil {
		t.Error("nil verifier should error")
	}
}

func TestEvaluatorLoopDefaultsMaxIters(t *testing.T) {
	gen := &scriptedGenerator{outputs: []string{"v"}}
	ver := &scriptedVerifier{results: []VerifyResult{{Status: "fail", Issues: []string{"x"}}}}

	_, iters, err := EvaluatorLoop(context.Background(), gen, ver, nil, "", "p", 0, Scope{WorkspaceID: "ws"})
	if err == nil {
		t.Fatal("expected exhaustion")
	}
	if iters != DefaultMaxIterations {
		t.Errorf("expected default %d iterations, got %d", DefaultMaxIterations, iters)
	}
}
