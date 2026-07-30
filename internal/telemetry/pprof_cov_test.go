package telemetry

// Coverage tests for pprof.go — disabled mode, the loopback happy path,
// the non-loopback warning, and listen failures. All binds use port 0 so
// no fixed ports are claimed.

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

// syncBuffer is a goroutine-safe log sink for these tests.
//
// Why it exists: StartPProfServer spawns a goroutine that logs immediately
// (pprof.go:72-77) — an Info line BEFORE srv.Serve, and possibly an Error
// line after Serve returns. A test that hands slog a bare *bytes.Buffer
// therefore shares that buffer with a goroutine it never joins, so reading
// buf.String() races the handler's Write. bytes.Buffer is not
// goroutine-safe. `go test ./internal/telemetry -race` reported it as four
// warnings across TestStartPProfServer_LoopbackNoWarning and
// TestStartPProfServer_LocalhostHostAccepted (two racing Buffer fields
// each, both inside bytes.Buffer.grow).
//
// Asserting after shutdown() instead is the obvious first guess and it does
// NOT work — worth recording so nobody retries it.
// http.Server.Shutdown closes listeners and waits for idle connections; it
// does not join the goroutine that called Serve, and it cannot possibly
// synchronise with a log line emitted before Serve is called at all.
// TestStartPProfServer_LocalhostHostAccepted already asserted after
// shutdown() and still raced.
//
// Used by every test in this file, including the ones that fail before the
// goroutine starts, so the safe sink is the local default and the next test
// added here cannot reintroduce the race by copying its neighbour.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestStartPProfServer_EmptyAddrDisabled(t *testing.T) {
	shutdown, err := StartPProfServer("", nil)
	if err != nil {
		t.Fatalf("empty addr: %v", err)
	}
	if shutdown == nil {
		t.Fatal("shutdown must be callable in disabled mode")
	}
	shutdown() // must not panic
}

func TestStartPProfServer_LoopbackNoWarning(t *testing.T) {
	var buf syncBuffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	shutdown, err := StartPProfServer("127.0.0.1:0", logger)
	if err != nil {
		t.Fatalf("StartPProfServer: %v", err)
	}
	defer shutdown()

	out := buf.String()
	if strings.Contains(out, "not a loopback bind") {
		t.Errorf("loopback bind should not warn: %q", out)
	}
	// Calling shutdown twice must be safe.
	shutdown()
}

func TestStartPProfServer_LocalhostHostAccepted(t *testing.T) {
	var buf syncBuffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	shutdown, err := StartPProfServer("localhost:0", logger)
	if err != nil {
		t.Fatalf("StartPProfServer(localhost:0): %v", err)
	}
	shutdown()
	if strings.Contains(buf.String(), "not a loopback bind") {
		t.Errorf("localhost should count as loopback: %q", buf.String())
	}
}

func TestStartPProfServer_BadAddrWarnsAndErrors(t *testing.T) {
	var buf syncBuffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	// "borked" has no port → SplitHostPort fails (warn branch), then
	// net.Listen fails (error return).
	shutdown, err := StartPProfServer("borked", logger)
	if err == nil {
		shutdown()
		t.Fatal("expected listen error for malformed addr")
	}
	if !strings.Contains(err.Error(), "listen pprof") {
		t.Errorf("error should be wrapped with listen pprof context: %v", err)
	}
	if !strings.Contains(buf.String(), "not a loopback bind") {
		t.Errorf("malformed addr should trigger the non-loopback warning: %q", buf.String())
	}
}
