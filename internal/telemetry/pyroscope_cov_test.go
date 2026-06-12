package telemetry

// Coverage tests for pyroscope.go — RedactURL sanitisation, the
// StartPyroscopePush lifecycle against a local stub server, and the
// slog bridge.

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
)

func TestRedactURL(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"credentials_stripped", "http://user:secret@pyro.example:4040/ingest", "http://pyro.example:4040/ingest"},
		{"user_only_stripped", "https://admin@pyro.example", "https://pyro.example"},
		{"plain_passthrough", "http://localhost:4040", "http://localhost:4040"},
		{"unparseable", "http://bad host/with space", "<unparseable url>"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := RedactURL(c.in)
			if got != c.want {
				t.Errorf("RedactURL(%q) = %q, want %q", c.in, got, c.want)
			}
			if strings.Contains(got, "secret") {
				t.Errorf("redacted URL still contains the password: %q", got)
			}
		})
	}
}

func TestStartPyroscopePush_EmptyURLIsNoop(t *testing.T) {
	stop, err := StartPyroscopePush(context.Background(), "", nil)
	if err != nil {
		t.Fatalf("empty URL: %v", err)
	}
	if stop == nil {
		t.Fatal("stop must be callable even in no-op mode")
	}
	stop() // must not panic
}

func TestStartPyroscopePush_StartsAndStops(t *testing.T) {
	// Stub push server so any background upload stays on loopback.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// Snapshot + restore the runtime profiling knobs the function flips.
	prevMutex := runtime.SetMutexProfileFraction(-1)
	runtime.SetMutexProfileFraction(prevMutex)
	defer func() {
		runtime.SetMutexProfileFraction(prevMutex)
		runtime.SetBlockProfileRate(0)
	}()

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	t.Setenv("CREWSHIP_PYROSCOPE_TAG_SLOT", "test-slot")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stop, err := StartPyroscopePush(ctx, srv.URL, logger)
	if err != nil {
		t.Fatalf("StartPyroscopePush: %v", err)
	}
	if got := runtime.SetMutexProfileFraction(-1); got != 5 {
		t.Errorf("mutex profile fraction = %d, want 5 after start", got)
	}
	logLine := buf.String()
	if !strings.Contains(logLine, "pyroscope push profiler started") {
		t.Errorf("missing start log, got: %q", logLine)
	}
	if !strings.Contains(logLine, "slot=test-slot") {
		t.Errorf("slot tag missing from start log: %q", logLine)
	}

	// stop is idempotent; double-call must not panic. Cancelling the ctx
	// afterwards exercises the watchdog goroutine path against the same
	// once-guarded stop.
	stop()
	stop()
	cancel()
}

func TestPyroscopeLogger_BridgesAllLevels(t *testing.T) {
	var buf bytes.Buffer
	l := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	pl := pyroscopeLogger{l: l}

	pl.Infof("info %d", 1)
	pl.Debugf("debug %s", "two")
	pl.Errorf("error %v", 3)

	out := buf.String()
	for _, want := range []string{"info 1", "debug two", "error 3"} {
		if !strings.Contains(out, want) {
			t.Errorf("log output missing %q: %q", want, out)
		}
	}
	if !strings.Contains(out, "level=ERROR") {
		t.Errorf("Errorf should log at error level: %q", out)
	}
}
