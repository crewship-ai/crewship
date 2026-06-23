package telemetry

// Coverage tests for pprof.go — disabled mode, the loopback happy path,
// the non-loopback warning, and listen failures. All binds use port 0 so
// no fixed ports are claimed.

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

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
	var buf bytes.Buffer
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
	var buf bytes.Buffer
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
	var buf bytes.Buffer
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
