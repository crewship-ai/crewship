package main

import (
	"context"
	"fmt"
	"io"
	"os/signal"

	dockerprovider "github.com/crewship-ai/crewship/internal/provider/docker"
	"github.com/crewship-ai/crewship/internal/runoutput"
)

// This private host mode has no network listener or CLI config loading. The
// controller sends admitted inputs through stdin, never argv or a disk file.
func runHostCapture(input io.Reader, diagnostics io.Writer) int {
	spec, err := runoutput.ReadHostInput(input)
	if err != nil {
		fmt.Fprintln(diagnostics, "host capture handoff refused")
		return 125
	}
	executor, closer, err := dockerprovider.NewHostCaptureExecutor(spec)
	if err != nil {
		fmt.Fprintln(diagnostics, "host capture authority refused")
		return 125
	}
	defer closer.Close()
	ctx, stop := hostCaptureContext()
	defer stop()
	if _, err := runoutput.CaptureHost(ctx, spec, executor); err != nil {
		fmt.Fprintln(diagnostics, "host capture failed")
		return 125
	}
	return 0
}

// hostCaptureContext turns a service stop into the capture's own stop. A
// service manager that stops the whole control group (systemd's default)
// signals this helper together with the server. Dying on that signal left the
// capture without a terminal result, so a restart parked the work for good
// (#5081). Cancelling the capture instead stops exactly its own execution,
// records the whole-tree stop receipt and the terminal result, and exits.
// This is the sidecar's run-capture contract (runoutput.ExecutionSignals).
func hostCaptureContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), runoutput.ExecutionSignals()...)
}
