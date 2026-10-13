package main

import (
	"context"
	"fmt"
	"io"

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
	if _, err := runoutput.CaptureHost(context.Background(), spec, executor); err != nil {
		fmt.Fprintln(diagnostics, "host capture failed")
		return 125
	}
	return 0
}
