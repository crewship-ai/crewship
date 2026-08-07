package orchestrator

// Unit tests for preflightBatch itself (#1646) — failure ATTRIBUTION in
// particular.
//
// The run-level test in preflight_exec_count_test.go asserts that a failed
// credential step still aborts a file-mounted-credentials run. That is not
// enough on its own: the flush error embeds a tail of the script's transcript,
// and the transcript necessarily contains the failing step's name, so
// "err.Error() mentions the step" passes even when the structured attribution
// is gone. Deleting the named list from the error message left that test green.
// These tests assert the discriminating property instead — which step, and
// only that step, is marked failed.

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

func quietBatchLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// batchFake is the minimum ContainerProvider a batch flush needs.
type batchFake struct {
	countingContainer
	err error
}

func (f *batchFake) Exec(ctx context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.countingContainer.Exec(ctx, cfg)
}

// newBatchFake models a script that WAS delivered: the real merged script
// prints preflightDoneMarker as its last act, and Flush now requires it — a
// transcript without it means the script never ran (#1779). Tests that want to
// model an undelivered script use newUndeliveredBatchFake instead.
func newBatchFake(stdout string, exit int) *batchFake {
	f := &batchFake{}
	f.countingContainer.stdout = func(provider.ExecConfig, string) string {
		return stdout + preflightDoneMarker + "\n"
	}
	f.countingContainer.exitFor = func(provider.ExecConfig) int { return exit }
	return f
}

func threeStepBatch(f provider.ContainerProvider) *preflightBatch {
	b := newPreflightBatch(f, "ctr-1", "1001:1001", quietBatchLogger())
	b.add("agent-dirs", "", "true")
	b.add("credentials", "", "true")
	b.add("mcp-config", "", "true")
	return b
}

func TestPreflightBatch_FlushMarksOnlyTheNamedStepFailed(t *testing.T) {
	f := newBatchFake(preflightFailMarker+"credentials\n", 1)
	b := threeStepBatch(f)

	err := b.Flush(context.Background())
	if err == nil {
		t.Fatal("a script that reported a failed step must return an error")
	}
	if !b.stepFailed("credentials") {
		t.Error("the step the script named is not marked failed — the fail-loud paths " +
			"in preparePreflightDirs key off exactly this")
	}
	for _, ok := range []string{"agent-dirs", "mcp-config"} {
		if b.stepFailed(ok) {
			t.Errorf("step %q was marked failed although the script did not name it; "+
				"attribution collapsed to all-or-nothing and a healthy MCP config would "+
				"now abort a run", ok)
		}
	}
	if !strings.Contains(err.Error(), "steps failed (credentials)") {
		t.Errorf("the error does not carry the named failure list: %v\n"+
			"an operator reading this line must learn WHICH part failed without "+
			"having to parse the raw transcript", err)
	}
}

func TestPreflightBatch_FlushOnAHealthyScriptMarksNothingFailed(t *testing.T) {
	f := newBatchFake("", 0)
	b := threeStepBatch(f)

	if err := b.Flush(context.Background()); err != nil {
		t.Fatalf("a script whose steps all succeeded must not error: %v", err)
	}
	for _, s := range []string{"agent-dirs", "credentials", "mcp-config"} {
		if b.stepFailed(s) {
			t.Errorf("step %q marked failed on a healthy run", s)
		}
	}
}

// A script that never ran must not read as "every step is fine". The fail-loud
// checks are phrased as "did THIS step fail", so a transport error has to mark
// everything queued, or a run whose merged script was never delivered starts
// with no credential files and no complaint.
func TestPreflightBatch_TransportFailureMarksEveryQueuedStep(t *testing.T) {
	f := newBatchFake("", 0)
	f.err = errors.New("exec create: connection refused")
	b := threeStepBatch(f)

	err := b.Flush(context.Background())
	if err == nil {
		t.Fatal("a flush whose exec never started must error")
	}
	for _, s := range []string{"agent-dirs", "credentials", "mcp-config"} {
		if !b.stepFailed(s) {
			t.Errorf("step %q reads as successful although the script was never delivered", s)
		}
	}
}

// A script killed before it could print its failure line (OOM, sh parse error)
// must still surface. The exit code is the independent second signal.
func TestPreflightBatch_NonZeroExitWithoutAFailureLineStillFails(t *testing.T) {
	f := newBatchFake("some partial output\n", 137)
	b := threeStepBatch(f)

	err := b.Flush(context.Background())
	if err == nil {
		t.Fatal("a non-zero exit with no failure line must not read as success")
	}
	if !strings.Contains(err.Error(), "137") {
		t.Errorf("the error should carry the exit code: %v", err)
	}
	for _, s := range []string{"agent-dirs", "credentials", "mcp-config"} {
		if !b.stepFailed(s) {
			t.Errorf("step %q reads as successful although the whole script died", s)
		}
	}
}

// An empty queue must issue no exec at all — otherwise every read probe that
// flushes for ordering would add a round-trip of its own, which is the cost
// this change exists to remove.
func TestPreflightBatch_EmptyFlushIssuesNoExec(t *testing.T) {
	f := newBatchFake("", 0)
	b := newPreflightBatch(f, "ctr-1", "1001:1001", quietBatchLogger())
	if err := b.Flush(context.Background()); err != nil {
		t.Fatalf("empty flush: %v", err)
	}
	if n := len(f.countingContainer.snapshot()); n != 0 {
		t.Errorf("empty flush issued %d execs, want 0", n)
	}
	if b.flushes != 0 {
		t.Errorf("flushes = %d, want 0", b.flushes)
	}
}

// accepts is the whole safety boundary between "may ride the merged script"
// and "runs on its own". Anything that carries its own identity, environment,
// stdin or privilege must not be folded in.
func TestPreflightBatch_AcceptsRefusesAnythingUnlikeAPlainAgentScript(t *testing.T) {
	b := newPreflightBatch(newBatchFake("", 0), "ctr-1", "1001:1001", quietBatchLogger())
	base := provider.ExecConfig{
		ContainerID: "ctr-1",
		Cmd:         []string{"sh", "-c", "true"},
		User:        "1001:1001",
	}
	if !b.accepts(base) {
		t.Fatal("a plain agent-uid `sh -c` script must be batchable")
	}

	cases := []struct {
		name  string
		mutar func(c *provider.ExecConfig)
	}{
		{"another container", func(c *provider.ExecConfig) { c.ContainerID = "ctr-2" }},
		{"another user", func(c *provider.ExecConfig) { c.User = "1002:1002" }},
		{"privileged opt-in", func(c *provider.ExecConfig) { c.AllowPrivileged = true }},
		{"its own stdin", func(c *provider.ExecConfig) { c.Stdin = strings.NewReader("x") }},
		{"its own env", func(c *provider.ExecConfig) { c.Env = []string{"A=b"} }},
		{"a bare argv, not a script", func(c *provider.ExecConfig) { c.Cmd = []string{"mkdir", "-p", "/x"} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base
			tc.mutar(&cfg)
			if b.accepts(cfg) {
				t.Errorf("accepts() folded in an exec with %s; the merged script has one "+
					"identity and one stdin, so this would run as the wrong thing or "+
					"silently drop the caller's input", tc.name)
			}
		})
	}
}

// runOrBatch must keep working when no batch is active — that is every call
// site outside preparePreflightDirs, and it is why batching is opt-in.
func TestRunOrBatch_WithoutABatchExecsImmediately(t *testing.T) {
	f := newBatchFake("", 0)
	cfg := provider.ExecConfig{
		ContainerID: "ctr-1",
		Cmd:         []string{"sh", "-c", "true"},
		User:        "1001:1001",
	}
	if err := runOrBatch(context.Background(), f, "solo", cfg); err != nil {
		t.Fatalf("runOrBatch: %v", err)
	}
	if n := len(f.countingContainer.snapshot()); n != 1 {
		t.Errorf("issued %d execs, want 1 — without a batch the call must behave exactly "+
			"as it did before #1646", n)
	}
}
