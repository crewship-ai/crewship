//go:build linux

package restricteddispatch

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/access"
)

type boundaryExecute func(context.Context, string, string, string, string, func(string, string) error) error

func boundaryRunner(t *testing.T, native bool, start func(context.Context, string) (TextSession, error)) (Authority, boundaryExecute) {
	t.Helper()
	a := nativeFixture(t)
	if !native {
		if _, err := a.Store.DB.ExecContext(t.Context(), `UPDATE agents SET restricted_execution_profile='responses_text' WHERE id='a'`); err != nil {
			t.Fatal(err)
		}
	}
	if native {
		return a, (&NativeRunner{Authority: a, MaxOutputTokens: 128, StartSession: start}).ExecuteRun
	}
	return a, (&TextRunner{Authority: a, MaxOutputTokens: 128, StartSession: start}).ExecuteRun
}

func TestTextRunnerRejectsUntrustedOutputFrames(t *testing.T) {
	for _, tc := range []struct {
		name, output string
		outputErr    error
	}{
		{name: "invalid JSON", output: "{\n"},
		{name: "unknown frame", output: "{\"type\":\"credential\"}\n"},
		{name: "missing done", output: "{\"type\":\"text\",\"text\":\"partial\"}\n"},
		{name: "duplicate done", output: "{\"type\":\"done\"}\n{\"type\":\"done\"}\n"},
		{name: "text after done", output: "{\"type\":\"done\"}\n{\"type\":\"text\",\"text\":\"late\"}\n"},
		{name: "oversized text", output: "{\"type\":\"text\",\"text\":\"" + strings.Repeat("x", 32769) + "\"}\n"},
		{name: "output unavailable", outputErr: errors.New("fixture output unavailable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session := &nativeBoundarySession{output: tc.output, outputErr: tc.outputErr, done: make(chan struct{})}
			close(session.done)
			var handle string
			a, run := boundaryRunner(t, false, func(_ context.Context, h string) (TextSession, error) { handle = h; return session, nil })
			completed := false
			if err := run(t.Context(), "h1", "w", "c1", "input", func(kind, text string) error {
				if kind == "done" {
					completed = true
				}
				return nil
			}); err == nil || completed || session.stops != 1 {
				t.Fatalf("invalid output accepted: %v completed=%v stops=%d", err, completed, session.stops)
			}
			if _, err := a.Store.Resolve(t.Context(), handle); !errors.Is(err, access.ErrDenied) {
				t.Fatalf("failed text worker retained authority: %v", err)
			}
		})
	}
}

func TestScopedRunnersCancelAndRecordOutcomeWithoutReusingAuthority(t *testing.T) {
	for _, native := range []bool{false, true} {
		t.Run(map[bool]string{false: "text", true: "native"}[native], func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			session := &nativeBoundarySession{done: make(chan struct{})}
			var handle string
			a, run := boundaryRunner(t, native, func(_ context.Context, h string) (TextSession, error) { handle = h; cancel(); return session, nil })
			if err := run(ctx, "h1", "w", "c1", "input", func(string, string) error { t.Fatal("delivered after cancellation"); return nil }); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation hidden: %v", err)
			}
			if session.stops != 1 {
				t.Fatalf("cancelled session not stopped: %d", session.stops)
			}
			if _, err := a.Store.Resolve(t.Context(), handle); !errors.Is(err, access.ErrDenied) {
				t.Fatalf("cancelled attempt reusable: %v", err)
			}
			var state string
			if err := a.Store.DB.QueryRowContext(t.Context(), `SELECT state FROM access_attempt_outcomes`).Scan(&state); err != nil || state != "canceled" {
				t.Fatalf("cancelled outcome missing: %s %v", state, err)
			}
		})
	}
}

func TestScopedRunnerDeliveryAndStartupErrorsRemainFailures(t *testing.T) {
	for _, native := range []bool{false, true} {
		for _, stage := range []string{"start", "text", "done"} {
			t.Run(map[bool]string{false: "text", true: "native"}[native]+"/"+stage, func(t *testing.T) {
				failure := errors.New("fixture client disconnected")
				session := &nativeBoundarySession{output: "{\"type\":\"text\",\"text\":\"answer\"}\n{\"type\":\"done\"}\n", done: make(chan struct{})}
				close(session.done)
				_, run := boundaryRunner(t, native, func(context.Context, string) (TextSession, error) {
					if stage == "start" {
						return nil, failure
					}
					return session, nil
				})
				err := run(t.Context(), "h1", "w", "c1", "input", func(kind, text string) error {
					if kind == stage {
						return failure
					}
					return nil
				})
				if !errors.Is(err, failure) {
					t.Fatalf("delivery failure hidden: %v", err)
				}
				want := 1
				if stage == "start" {
					want = 0
				}
				if session.stops != want {
					t.Fatalf("session cleanup count %d want %d", session.stops, want)
				}
			})
		}
	}
}

type shrinkingBoundarySession struct {
	nativeBoundarySession
	reads int
}

func (s *shrinkingBoundarySession) Output(context.Context) (string, error) {
	s.reads++
	if s.reads == 1 {
		return "{\"type\":\"text\",\"text\":\"partial\"}\n", nil
	}
	return "", nil
}
func TestScopedRunnerRejectsOutputTruncationDuringPolling(t *testing.T) {
	for _, native := range []bool{false, true} {
		t.Run(map[bool]string{false: "text", true: "native"}[native], func(t *testing.T) {
			session := &shrinkingBoundarySession{nativeBoundarySession: nativeBoundarySession{done: make(chan struct{})}}
			_, run := boundaryRunner(t, native, func(context.Context, string) (TextSession, error) { return session, nil })
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			if err := run(ctx, "h1", "w", "c1", "input", func(string, string) error { return nil }); !errors.Is(err, access.ErrDenied) {
				t.Fatalf("truncated worker output accepted: %v", err)
			}
			if session.reads != 2 || session.stops != 1 {
				t.Fatalf("polling/cleanup: reads=%d stops=%d", session.reads, session.stops)
			}
		})
	}
}

func TestScopedRunnerRejectsMissingConfigurationAndInvalidInput(t *testing.T) {
	start := func(context.Context, string) (TextSession, error) { t.Fatal("invalid runner started"); return nil, nil }
	emit := func(string, string) error { return nil }
	var absentNative *NativeRunner
	var absentText *TextRunner
	for _, run := range []boundaryExecute{absentNative.ExecuteRun, absentText.ExecuteRun, (&NativeRunner{}).ExecuteRun, (&TextRunner{}).ExecuteRun, (&TextRunner{StartSession: start}).ExecuteRun} {
		if err := run(t.Context(), "h1", "w", "c1", "input", emit); !errors.Is(err, access.ErrDenied) {
			t.Fatalf("unavailable runner accepted: %v", err)
		}
	}
	for _, native := range []bool{false, true} {
		_, run := boundaryRunner(t, native, start)
		for _, input := range []string{"", strings.Repeat("x", 32769)} {
			if err := run(t.Context(), "h1", "w", "c1", input, emit); !errors.Is(err, access.ErrDenied) {
				t.Fatalf("invalid input accepted: %v", err)
			}
		}
		if err := run(t.Context(), "h1", "w", "c1", "input", nil); !errors.Is(err, access.ErrDenied) {
			t.Fatalf("nil consumer accepted: %v", err)
		}
		if err := run(t.Context(), "h1", "w", "missing", "input", emit); !errors.Is(err, access.ErrDenied) {
			t.Fatalf("missing chat accepted: %v", err)
		}
	}
}
