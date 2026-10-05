//go:build linux

package restricteddispatch

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
)

func TestNativeRunnerWithoutStoreFailsClosed(t *testing.T) {
	runner := &NativeRunner{StartSession: func(context.Context, string) (TextSession, error) {
		t.Fatal("started without authority storage")
		return nil, nil
	}}
	if err := runner.ExecuteRun(t.Context(), "user", "workspace", "chat", "input", func(string, string) error { return nil }); !errors.Is(err, access.ErrDenied) {
		t.Fatalf("missing store not denied: %v", err)
	}
}

type nativeBoundarySession struct {
	output    string
	outputErr error
	done      chan struct{}
	stops     int
}

func (s *nativeBoundarySession) Output(context.Context) (string, error) { return s.output, s.outputErr }
func (s *nativeBoundarySession) Done() <-chan struct{}                  { return s.done }
func (s *nativeBoundarySession) Stop(string)                            { s.stops++ }

func TestNativeRunnerRejectsUntrustedOutputFrames(t *testing.T) {
	for _, tc := range []struct {
		name, output string
		outputErr    error
	}{
		{name: "invalid JSON", output: "{\n"},
		{name: "unknown frame", output: "{\"type\":\"credential\",\"text\":\"PRIVATE\"}\n"},
		{name: "missing done", output: "{\"type\":\"text\",\"text\":\"partial\"}\n"},
		{name: "duplicate done", output: "{\"type\":\"done\"}\n{\"type\":\"done\"}\n"},
		{name: "text after done", output: "{\"type\":\"done\"}\n{\"type\":\"text\",\"text\":\"late\"}\n"},
		{name: "oversized text", output: "{\"type\":\"text\",\"text\":\"" + strings.Repeat("x", 32769) + "\"}\n"},
		{name: "duplicate artifact", output: "{\"type\":\"artifact\",\"name\":\"result.txt\",\"content\":\"eA==\"}\n{\"type\":\"artifact\",\"name\":\"result.txt\",\"content\":\"eQ==\"}\n"},
		{name: "artifact after done", output: "{\"type\":\"done\"}\n{\"type\":\"artifact\",\"name\":\"result.txt\",\"content\":\"eA==\"}\n"},
		{name: "output unavailable", outputErr: errors.New("fixture output unavailable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			authority := nativeFixture(t)
			session := &nativeBoundarySession{output: tc.output, outputErr: tc.outputErr, done: make(chan struct{})}
			close(session.done)
			var handle string
			runner := NativeRunner{Authority: authority, MaxOutputTokens: 128, StartSession: func(_ context.Context, h string) (TextSession, error) { handle = h; return session, nil }}
			completed := false
			err := runner.ExecuteRun(t.Context(), "h1", "w", "c1", "private request", func(kind, text string) error {
				if kind == "done" {
					completed = true
				}
				return nil
			})
			if err == nil || completed || session.stops != 1 {
				t.Fatalf("invalid output accepted: err=%v completed=%v stops=%d", err, completed, session.stops)
			}
			if _, err = authority.Store.Resolve(t.Context(), handle); !errors.Is(err, access.ErrDenied) {
				t.Fatalf("failed worker retained executable authority: %v", err)
			}
			var state string
			if err = authority.Store.DB.QueryRowContext(t.Context(), `SELECT state FROM access_attempt_outcomes`).Scan(&state); err != nil || state != "failed" {
				t.Fatalf("failed outcome missing: %s %v", state, err)
			}
		})
	}
}

func TestNativeRunWrappersPreserveCompletedOutputAndAdditionalRights(t *testing.T) {
	for _, method := range []string{"run", "run with rights", "run with files", "chat with files"} {
		t.Run(method, func(t *testing.T) {
			authority := nativeFixture(t)
			setRights(t, authority, "h1", []access.Right{{Kind: "agent", ID: "a", Operation: "run"}, {Kind: "agent", ID: "a", Operation: "chat"}})
			session := &nativeBoundarySession{output: "{\"type\":\"text\",\"text\":\"answer\"}\n{\"type\":\"done\"}\n", done: make(chan struct{})}
			close(session.done)
			runner := NativeRunner{Authority: authority, MaxOutputTokens: 128, StartSession: func(context.Context, string) (TextSession, error) { return session, nil }}
			var events []string
			emit := func(kind, text string) error { events = append(events, kind+":"+text); return nil }
			var err error
			switch method {
			case "run":
				err = runner.ExecuteRun(t.Context(), "h1", "w", "c1", "input", emit)
			case "run with rights":
				err = runner.ExecuteRunWithRights(t.Context(), "h1", "w", "c1", "input", []access.Right{{Kind: "agent", ID: "a", Operation: "run"}}, emit)
			case "run with files":
				err = runner.ExecuteRunWithProjectFiles(t.Context(), "h1", "w", "c1", "input", nil, emit)
			case "chat with files":
				err = runner.ExecuteWithProjectFiles(t.Context(), "h1", "w", "c1", "input", nil, emit)
			}
			if err != nil || strings.Join(events, ",") != "text:answer,done:" || session.stops != 1 {
				t.Fatalf("valid worker failed: %v %v stops=%d", err, events, session.stops)
			}
		})
	}
}
