package pipeline

import (
	"context"
	"strings"
	"testing"

	"github.com/google/cel-go/cel"
)

// These tests guard the fix for findings M4 and M5 from the 2026-06 security
// audit (.claude/context/SECURITY-AUDIT-2026-06.md):
//
//   M4 — CelCodeRunner.RunCode formerly built a fresh cel.NewEnv + Compile +
//        Program on EVERY call. The fix shares one env and caches compiled
//        cel.Programs keyed by expression text, so a routine whose step fires
//        repeatedly with unchanging source compiles once.
//
//   M5 — celCostLimit is passed to cel.Program as a CostLimit, which bounds
//        Eval ONLY; Compile was unbounded. Because the step body is Render()'d
//        (runner_code.go) BEFORE it reaches the runner, a webhook-controlled
//        `{{ inputs.payload }}` is substituted directly into the CEL *source*
//        that gets compiled — a "compile-bomb". The fix caps the rendered
//        source length (maxCELSourceBytes) and rejects oversized expressions
//        BEFORE env.Compile.
//
// The tests below now assert that SECURE behavior: an oversized rendered
// expression is rejected before compile, and a repeated expression hits the
// program cache. They would FAIL if either guard regressed.
//
// The benchmarks motivate M4's program cache: BenchmarkCEL_RunCode walks the
// full production path (now cached), while BenchmarkCEL_EvalOnly_CachedProgram
// measures Eval against a pre-built program.

// renderCELBody mirrors runner_code.go:74 — the step body is Render()'d before
// it reaches the runner, so an attacker-controlled input is substituted into
// the CEL source string that env.Compile() then parses.
func renderCELBody(payload string) string {
	ctx := RenderContext{Inputs: map[string]any{"payload": payload}}
	return Render("{{ inputs.payload }}", ctx)
}

// bigList builds a valid CEL expression of the form "size([1, 1, ..., 1])"
// with n list elements. A list literal is a single FLAT AST node with n
// children, so it sidesteps cel-go's own parser recursion-depth cap (which a
// chained binary-operator payload like "1 + 1 + ..." trips around depth ~250)
// while still scaling the parse + type-check workload linearly with n. That is
// the lever an attacker turns via a routine input: there is no crewship cap on
// the rendered source length / compile cost in front of env.Compile, and
// celCostLimit guards only Eval.
func bigList(n int) string {
	if n <= 0 {
		return "size([])"
	}
	var sb strings.Builder
	sb.Grow(n*3 + 8)
	sb.WriteString("size([")
	for i := 0; i < n-1; i++ {
		sb.WriteString("1,")
	}
	sb.WriteString("1])")
	return sb.String()
}

// TestCEL_CompileBomb_Rejected asserts the M5 fix: a webhook-controlled payload
// rendered into an oversized CEL source is rejected BEFORE env.Compile by the
// maxCELSourceBytes cap, so an attacker can't drive unbounded compile CPU.
func TestCEL_CompileBomb_Rejected(t *testing.T) {
	r := CelCodeRunner{}
	ctx := context.Background()

	// An attacker who controls a routine input (e.g. a webhook field bound to
	// {{ inputs.payload }}) submits a large-but-valid expression. The renderer
	// inlines it verbatim into the CEL source.
	const terms = 5000
	code := renderCELBody(bigList(terms))

	if len(code) <= maxCELSourceBytes {
		t.Fatalf("test setup: expected an attacker-controlled source > %d bytes, got %d", maxCELSourceBytes, len(code))
	}

	res, err := r.RunCode(ctx, CodeRunRequest{Runtime: "cel", Code: code})
	if err == nil {
		t.Fatalf("M5 regression: oversized rendered CEL source (%d bytes) compiled without rejection", len(code))
	}
	if res.ExitCode == 0 {
		t.Fatalf("M5 regression: oversized source returned exit 0 (stderr %q)", res.Stderr)
	}
	if !strings.Contains(res.Stderr, "too large") {
		t.Errorf("expected a source-length rejection, got stderr %q", res.Stderr)
	}
}

// TestCEL_SourceLengthBoundary checks the cap boundary: an expression at/under
// maxCELSourceBytes still compiles+evaluates, one over is rejected.
func TestCEL_SourceLengthBoundary(t *testing.T) {
	r := CelCodeRunner{}
	ctx := context.Background()

	// A valid expression comfortably under the cap must still work.
	under := bigList(50) // small list literal, well under maxCELSourceBytes
	if len(under) > maxCELSourceBytes {
		t.Fatalf("test setup: under-cap expression is %d bytes, not under %d", len(under), maxCELSourceBytes)
	}
	res, err := r.RunCode(ctx, CodeRunRequest{Runtime: "cel", Code: under})
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("under-cap expression must evaluate cleanly, got err=%v exit=%d stderr=%q", err, res.ExitCode, res.Stderr)
	}

	// One byte over the cap is rejected before compile. Use a no-whitespace
	// body so RunCode's TrimSpace can't shrink it back under the cap; the
	// length guard runs before env.Compile, so the body need not be valid CEL.
	over := strings.Repeat("x", maxCELSourceBytes+1)
	if _, err := r.RunCode(ctx, CodeRunRequest{Runtime: "cel", Code: over}); err == nil {
		t.Fatal("over-cap expression should be rejected")
	}
}

// TestCEL_ProgramCache_HitOnRepeat asserts the M4 fix: a repeated expression is
// compiled once and reused from the program cache on subsequent calls.
func TestCEL_ProgramCache_HitOnRepeat(t *testing.T) {
	r := CelCodeRunner{}
	ctx := context.Background()

	// Reset the package cache so this test is deterministic.
	celCacheMu.Lock()
	celCache = make(map[string]cel.Program, celProgramCacheMax)
	celCacheMu.Unlock()

	req := fixedCELReq()
	code := strings.TrimSpace(req.Code)

	if _, err := r.RunCode(ctx, req); err != nil {
		t.Fatalf("first RunCode: %v", err)
	}
	celCacheMu.Lock()
	first, ok := celCache[code]
	n := len(celCache)
	celCacheMu.Unlock()
	if !ok {
		t.Fatal("M4 regression: expression was not cached after first eval")
	}
	if n != 1 {
		t.Errorf("expected exactly 1 cached program, got %d", n)
	}

	// Second call must reuse the SAME compiled program (cache hit), not rebuild.
	if _, err := r.RunCode(ctx, req); err != nil {
		t.Fatalf("second RunCode: %v", err)
	}
	celCacheMu.Lock()
	second, ok := celCache[code]
	n = len(celCache)
	celCacheMu.Unlock()
	if !ok {
		t.Fatal("cache entry vanished between calls")
	}
	if n != 1 {
		t.Errorf("repeat eval should not grow the cache, got %d entries", n)
	}
	if first != second {
		t.Error("M4 regression: repeat eval rebuilt the program instead of hitting the cache")
	}
}

// --- M4: program cache motivation -------------------------------------------

// fixedCELReq is the steady-state shape a probe/gate routine fires repeatedly:
// the same expression text, only the input values vary per run.
func fixedCELReq() CodeRunRequest {
	return CodeRunRequest{
		Runtime: "cel",
		Code:    "inputs.spend > inputs.threshold && inputs.region in [\"eu\", \"us\"]",
		Inputs:  map[string]any{"spend": 9.0, "threshold": 5.0, "region": "eu"},
	}
}

// BenchmarkCEL_RunCode measures the FULL production path (NewEnv + Compile +
// Program + Eval) that runs on every code-step invocation today. Compare its
// ns/op and allocs against BenchmarkCEL_EvalOnly_CachedProgram: the difference
// is the per-call tax a program cache (keyed by expression text) would remove.
func BenchmarkCEL_RunCode(b *testing.B) {
	r := CelCodeRunner{}
	ctx := context.Background()
	req := fixedCELReq()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		res, err := r.RunCode(ctx, req)
		if err != nil {
			b.Fatalf("RunCode: %v", err)
		}
		if res.ExitCode != 0 {
			b.Fatalf("unexpected exit %d", res.ExitCode)
		}
	}
}

// BenchmarkCEL_EvalOnly_CachedProgram measures only Eval against a program
// built ONCE — the cost floor a cache would let the production path approach.
// It deliberately reconstructs the same env/program the runner builds inline.
func BenchmarkCEL_EvalOnly_CachedProgram(b *testing.B) {
	env, err := cel.NewEnv(
		cel.Variable("inputs", cel.MapType(cel.StringType, cel.DynType)),
	)
	if err != nil {
		b.Fatalf("env: %v", err)
	}
	ast, iss := env.Compile("inputs.spend > inputs.threshold && inputs.region in [\"eu\", \"us\"]")
	if iss != nil && iss.Err() != nil {
		b.Fatalf("compile: %v", iss.Err())
	}
	prg, err := env.Program(ast, cel.CostLimit(celCostLimit))
	if err != nil {
		b.Fatalf("program: %v", err)
	}
	activation := map[string]any{"inputs": celInputs(fixedCELReq().Inputs)}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := prg.Eval(activation); err != nil {
			b.Fatalf("eval: %v", err)
		}
	}
}
