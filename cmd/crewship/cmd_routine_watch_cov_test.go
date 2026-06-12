package main

// Coverage tests for cmd_routine_watch.go — formatWatchEntry/colourize
// plus the polling RunE loop driven to completion with --once against a
// stateful stub (error poll → bad-JSON poll → seed poll → new-event poll).

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestColourize(t *testing.T) {
	cases := []struct {
		kind string
		want string
	}{
		{"run.completed", "\x1b[32m"},
		{"step.failed", "\x1b[31m"},
		{"step.validation_failed", "\x1b[31m"},
		{"run.started", "\x1b[36m"},
		{"run.queued", ""},
	}
	for _, c := range cases {
		got := colourize(c.kind)
		if c.want == "" {
			if got != c.kind {
				t.Errorf("colourize(%q) = %q; want bare kind", c.kind, got)
			}
			continue
		}
		if !strings.HasPrefix(got, c.want) || !strings.Contains(got, c.kind) {
			t.Errorf("colourize(%q) = %q; want prefix %q", c.kind, got, c.want)
		}
	}
}

func TestFormatWatchEntry(t *testing.T) {
	e := watchEntry{
		Timestamp: "2026-06-10T12:00:00.500Z",
		EntryType: "pipeline.step.completed",
		Severity:  "info",
		Summary:   "step done",
	}
	out := formatWatchEntry(e, "step-9")
	if !strings.Contains(out, "step.completed") || !strings.Contains(out, "step=step-9") || !strings.Contains(out, "step done") {
		t.Errorf("formatWatchEntry: %q", out)
	}
	if strings.Contains(out, "pipeline.step.completed") {
		t.Errorf("pipeline. prefix should be trimmed: %q", out)
	}

	// error severity wraps the whole line in red.
	e.Severity = "error"
	e.EntryType = "pipeline.run.failed"
	out = formatWatchEntry(e, "")
	if !strings.HasPrefix(out, "\x1b[31m") {
		t.Errorf("error entry not red: %q", out)
	}

	// Non-nano timestamp falls back to RFC3339 parse.
	e.Timestamp = "2026-06-10T12:00:00Z"
	out = formatWatchEntry(e, "")
	if out == "" {
		t.Error("formatWatchEntry returned empty for RFC3339 ts")
	}
}

func TestRoutineWatchRunE_NoAuth(t *testing.T) {
	covSetupCli8(t, "http://127.0.0.1:0")
	cliCfg = &cli.CLIConfig{}
	routineWatchCmd.SetContext(context.Background())
	err := routineWatchCmd.RunE(routineWatchCmd, []string{"wf"})
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected not-logged-in; got %v", err)
	}
}

// TestRoutineWatchRunE_OnceTerminates drives the full poll loop:
//
//	poll 1 → HTTP 500            (status branch, keep looping)
//	poll 2 → invalid JSON        (decode branch, keep looping)
//	poll 3 → historical entries  (first successful poll seeds dedupe, no emit)
//	poll 4 → + new run events    (emit, terminal hit, --once exits)
func TestRoutineWatchRunE_OnceTerminates(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())

	historical := `[
	  {"id":"h1","ts":"2026-06-10T11:00:00Z","entry_type":"pipeline.run.completed","severity":"info","summary":"old run done","run_id":"run-old"}
	]`
	withNew := `[
	  {"id":"n3","ts":"2026-06-10T12:00:03Z","entry_type":"pipeline.run.completed","severity":"info","summary":"new run done","run_id":"run-new"},
	  {"id":"n2","ts":"2026-06-10T12:00:02Z","entry_type":"pipeline.step.completed","severity":"info","summary":"new step done","run_id":"run-new","payload":{"step_id":"s1"}},
	  {"id":"x1","ts":"2026-06-10T12:00:01Z","entry_type":"pipeline.run.started","severity":"info","summary":"unrelated run","run_id":"run-other"},
	  {"id":"h1","ts":"2026-06-10T11:00:00Z","entry_type":"pipeline.run.completed","severity":"info","summary":"old run done","run_id":"run-old"}
	]`

	var poll atomic.Int64
	stub.OnGet("/api/v1/workspaces/"+covWSCli8+"/pipelines/wf/runs",
		func(_ *http.Request, _ []byte) (int, []byte, string) {
			switch poll.Add(1) {
			case 1:
				panic(http.ErrAbortHandler) // transport-error branch
			case 2:
				return 500, []byte(`{"error":"boom"}`), "application/json"
			case 3:
				return 200, []byte(`not json`), "application/json"
			case 4:
				return 200, []byte(historical), "application/json"
			default:
				return 200, []byte(withNew), "application/json"
			}
		})

	covSetFlagCli8(t, routineWatchCmd, "once", "true")
	covSetFlagCli8(t, routineWatchCmd, "run-id", "run-new")
	covSetFlagCli8(t, routineWatchCmd, "interval", "25ms")
	routineWatchCmd.SetContext(context.Background())

	out := covCaptureStdoutCli8(t, func() {
		if err := routineWatchCmd.RunE(routineWatchCmd, []string{"wf"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})

	if got := poll.Load(); got < 5 {
		t.Errorf("expected >=5 polls, got %d", got)
	}
	// New-run events printed (human format), in chronological order.
	if !strings.Contains(out, "new step done") || !strings.Contains(out, "new run done") {
		t.Errorf("new events not printed:\n%s", out)
	}
	if strings.Index(out, "new step done") > strings.Index(out, "new run done") {
		t.Errorf("events not oldest-first:\n%s", out)
	}
	if !strings.Contains(out, "step=s1") {
		t.Errorf("step_id suffix missing:\n%s", out)
	}
	// Historical + filtered-out runs must NOT be printed.
	if strings.Contains(out, "old run done") || strings.Contains(out, "unrelated run") {
		t.Errorf("historical/filtered entries leaked into output:\n%s", out)
	}
}

// JSON-lines mode: same flow without the error polls; asserts the emitted
// lines are machine-parseable JSON.
func TestRoutineWatchRunE_JSONMode(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())

	// Poll 1 carries a historical entry — covers the first-poll seeding
	// continue. Poll 2 repeats it (cross-poll dedupe), adds a duplicated
	// new entry (intra-poll dedupe) and a numeric step_id (type-assert
	// false branch).
	first := `[
	  {"id":"h0","ts":"2026-06-10T11:00:00Z","entry_type":"pipeline.run.completed","severity":"info","summary":"historical","run_id":"run-0"}
	]`
	second := `[
	  {"id":"n1","ts":"2026-06-10T12:00:01Z","entry_type":"pipeline.run.completed","severity":"info","summary":"done","run_id":"run-1","payload":{"step_id":42}},
	  {"id":"n1","ts":"2026-06-10T12:00:01Z","entry_type":"pipeline.run.completed","severity":"info","summary":"done","run_id":"run-1","payload":{"step_id":42}},
	  {"id":"h0","ts":"2026-06-10T11:00:00Z","entry_type":"pipeline.run.completed","severity":"info","summary":"historical","run_id":"run-0"}
	]`
	var poll atomic.Int64
	stub.OnGet("/api/v1/workspaces/"+covWSCli8+"/pipelines/wf/runs",
		func(_ *http.Request, _ []byte) (int, []byte, string) {
			if poll.Add(1) == 1 {
				return 200, []byte(first), "application/json"
			}
			return 200, []byte(second), "application/json"
		})

	covSetFlagCli8(t, routineWatchCmd, "once", "true")
	covSetFlagCli8(t, routineWatchCmd, "json", "true")
	covSetFlagCli8(t, routineWatchCmd, "interval", "25ms")
	routineWatchCmd.SetContext(context.Background())

	out := covCaptureStdoutCli8(t, func() {
		if err := routineWatchCmd.RunE(routineWatchCmd, []string{"wf"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, `"entry_type":"pipeline.run.completed"`) {
		t.Errorf("json line missing entry_type:\n%s", out)
	}
	// The duplicated entry must print exactly once; the historical one never.
	if got := strings.Count(out, `"id":"n1"`); got != 1 {
		t.Errorf("duplicate entry printed %d times, want 1:\n%s", got, out)
	}
	if strings.Contains(out, `"id":"h0"`) {
		t.Errorf("seeded historical entry leaked:\n%s", out)
	}
}

// A pre-cancelled command context must exit the loop before the first
// poll — also exercises the interval<=0 clamp.
func TestRoutineWatchRunE_CancelledContext(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	routineWatchCmd.SetContext(ctx)
	covSetFlagCli8(t, routineWatchCmd, "interval", "0s")

	if err := routineWatchCmd.RunE(routineWatchCmd, []string{"wf"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if calls := stub.Calls(); len(calls) != 0 {
		t.Errorf("no polls expected with cancelled ctx; got %d", len(calls))
	}
}

func TestFormatWatchEntry_GarbageTimestamp(t *testing.T) {
	out := formatWatchEntry(watchEntry{
		Timestamp: "garbage", EntryType: "pipeline.run.started", Summary: "s",
	}, "")
	if !strings.Contains(out, "run.started") {
		t.Errorf("formatWatchEntry with bad ts: %q", out)
	}
}
