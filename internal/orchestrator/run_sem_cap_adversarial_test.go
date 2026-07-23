package orchestrator

import (
	"bytes"
	"log/slog"
	"strconv"
	"strings"
	"testing"
)

// TestResolveRunSemCap_NeverNonPositive is the zero-value / deadlock guard
// (angle 5). A 0-capacity runSem channel would block every acquireRunSlot
// forever — no run would ever be admitted. Prove resolveRunSemCap can never
// return a value < 1 for ANY combination of env string and config default,
// including the adversarial zero/negative/whitespace/garbage inputs. The
// semaphore capacity is derived directly from this return, so this is the
// invariant that keeps make(chan struct{}, cap) from ever being a deadlock.
func TestResolveRunSemCap_NeverNonPositive(t *testing.T) {
	envs := []string{
		"", "0", "-1", "-2147483648", " 0 ", "  ", "\t", "\n",
		"not-a-number", "8x", "0x10", "1e3", "3.5", "+0", "-0",
		"9999999999999999999999999999", // Atoi range overflow
	}
	configDefaults := []int{-100, -1, 0, 1, 8, 200}

	for _, env := range envs {
		for _, cd := range configDefaults {
			t.Setenv("CREWSHIP_MAX_CONCURRENT_RUNS", env)
			got := resolveRunSemCap(cd)
			if got < 1 {
				t.Errorf("resolveRunSemCap(%d) with env=%q = %d; must never be < 1 (0-cap runSem deadlocks every run)", cd, env, got)
			}
		}
	}
}

// TestResolveRunSemCap_OverflowIgnored proves an Atoi range overflow in the
// env var is treated as invalid (ignored) rather than wrapping to a negative
// or being partially parsed — so it falls through to config/default.
func TestResolveRunSemCap_OverflowIgnored(t *testing.T) {
	t.Setenv("CREWSHIP_MAX_CONCURRENT_RUNS", "9999999999999999999999999999")
	if got := resolveRunSemCap(42); got != 42 {
		t.Errorf("overflow env should be ignored and fall through to config 42, got %d", got)
	}
}

// TestResolveRunSemCap_WhitespaceHonored documents that resolveRunSemCap trims
// before parsing — the behavior config.applyEnvOverrides was aligned to. A
// padded valid value is honored; a padded non-positive value is ignored (and
// falls through), never producing a bogus cap.
func TestResolveRunSemCap_WhitespaceHonored(t *testing.T) {
	t.Setenv("CREWSHIP_MAX_CONCURRENT_RUNS", "  20  ")
	if got := resolveRunSemCap(5); got != 20 {
		t.Errorf("padded valid env should resolve to 20, got %d", got)
	}
	t.Setenv("CREWSHIP_MAX_CONCURRENT_RUNS", "  0  ")
	if got := resolveRunSemCap(5); got != 5 {
		t.Errorf("padded zero env should be ignored and fall through to config 5, got %d", got)
	}
}

// TestNew_ZeroOptionMeansDefault is the zero-value trap (angle 5) at the
// construction boundary: WithMaxConcurrentRuns(0) must mean "no config value
// supplied — use the built-in default", NOT "cap of 0". The resulting runSem
// must have positive capacity.
func TestNew_ZeroOptionMeansDefault(t *testing.T) {
	t.Setenv("CREWSHIP_MAX_CONCURRENT_RUNS", "") // env must not interfere
	o := New(nil, nil, slog.Default(), WithMaxConcurrentRuns(0))
	if o.runSemCap != defaultRunSemCap {
		t.Errorf("WithMaxConcurrentRuns(0) should fall back to default %d, got %d", defaultRunSemCap, o.runSemCap)
	}
	if cap(o.runSem) != defaultRunSemCap {
		t.Errorf("runSem capacity should be %d, got %d", defaultRunSemCap, cap(o.runSem))
	}
	if cap(o.runSem) < 1 {
		t.Fatal("runSem has zero capacity — every run would deadlock")
	}
}

// TestNew_OptionPlumbing is angle 4: New() with no options yields the default
// cap 8 (matching the ~20 existing call sites), and WithMaxConcurrentRuns(n)
// yields n when the env var is not overriding.
func TestNew_OptionPlumbing(t *testing.T) {
	t.Setenv("CREWSHIP_MAX_CONCURRENT_RUNS", "")

	t.Run("no options -> default 8", func(t *testing.T) {
		o := New(nil, nil, slog.Default())
		if o.runSemCap != defaultRunSemCap {
			t.Errorf("no-option New should yield default %d, got %d", defaultRunSemCap, o.runSemCap)
		}
		if cap(o.runSem) != defaultRunSemCap {
			t.Errorf("runSem cap should be %d, got %d", defaultRunSemCap, cap(o.runSem))
		}
	})

	t.Run("WithMaxConcurrentRuns wins over default", func(t *testing.T) {
		o := New(nil, nil, slog.Default(), WithMaxConcurrentRuns(24))
		if o.runSemCap != 24 {
			t.Errorf("expected cap 24, got %d", o.runSemCap)
		}
		if cap(o.runSem) != 24 {
			t.Errorf("runSem cap should be 24, got %d", cap(o.runSem))
		}
	})
}

// TestNew_EnvOverridesOption is angle 3 at the construction boundary: the env
// var (read directly by resolveRunSemCap inside New) beats the option value
// (the config-provided default). This is the "double read" made concrete — the
// live cap reflects the env at New()-time, exactly as documented precedence
// says (env > config > default).
func TestNew_EnvOverridesOption(t *testing.T) {
	t.Setenv("CREWSHIP_MAX_CONCURRENT_RUNS", "3")
	o := New(nil, nil, slog.Default(), WithMaxConcurrentRuns(50))
	if o.runSemCap != 3 {
		t.Errorf("env 3 should beat option 50, got %d", o.runSemCap)
	}
}

// TestNew_HighCapWarns is angle 1: there is no hard upper bound, so a very high
// configured cap is admitted — but it must emit a loud startup warning so the
// footgun (an accidental extra zero saturating the daemon, defeating audit
// finding P1) is visible. Below the threshold, no warning fires.
func TestNew_HighCapWarns(t *testing.T) {
	t.Setenv("CREWSHIP_MAX_CONCURRENT_RUNS", "")

	newWithBuf := func(opts ...Option) string {
		var buf bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
		New(nil, nil, logger, opts...)
		return buf.String()
	}

	t.Run("above threshold warns and reports the value", func(t *testing.T) {
		out := newWithBuf(WithMaxConcurrentRuns(runSemCapWarnThreshold + 1))
		if !strings.Contains(out, "max concurrent runs is far above the default") {
			t.Errorf("expected a high-cap warning, got: %q", out)
		}
		if !strings.Contains(out, "max_concurrent_runs="+strconv.Itoa(runSemCapWarnThreshold+1)) {
			t.Errorf("warning should report the offending value, got: %q", out)
		}
	})

	t.Run("at threshold does not warn", func(t *testing.T) {
		out := newWithBuf(WithMaxConcurrentRuns(runSemCapWarnThreshold))
		if strings.Contains(out, "far above the default") {
			t.Errorf("cap == threshold should not warn, got: %q", out)
		}
	})

	t.Run("default cap does not warn", func(t *testing.T) {
		out := newWithBuf()
		if strings.Contains(out, "far above the default") {
			t.Errorf("default cap should not warn, got: %q", out)
		}
	})

	t.Run("env-driven high cap also warns", func(t *testing.T) {
		t.Setenv("CREWSHIP_MAX_CONCURRENT_RUNS", "1000000")
		out := newWithBuf()
		if !strings.Contains(out, "far above the default") {
			t.Errorf("env-driven high cap should warn, got: %q", out)
		}
		if !strings.Contains(out, "max_concurrent_runs=1000000") {
			t.Errorf("warning should report the env value, got: %q", out)
		}
	})
}
