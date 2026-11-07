//go:build unix

package main

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

const captureSignalChildEnv = "HOST_CAPTURE_SIGNAL_TEST_CHILD"

// A service stop signals the capture helper with the server. The helper must
// turn that into its own capture cancellation (stop, receipt, terminal result),
// not die before recording anything (#5081).
func TestHostCaptureContextTurnsServiceStopIntoCancellation(t *testing.T) {
	if os.Getenv(captureSignalChildEnv) == "1" {
		ctx, stop := hostCaptureContext()
		defer stop()
		if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
			os.Exit(3)
		}
		select {
		case <-ctx.Done():
			os.Exit(0)
		case <-time.After(5 * time.Second):
			os.Exit(4)
		}
	}
	child := exec.Command(os.Args[0], "-test.run=^TestHostCaptureContextTurnsServiceStopIntoCancellation$")
	child.Env = append(os.Environ(), captureSignalChildEnv+"=1")
	if out, err := child.CombinedOutput(); err != nil {
		t.Fatalf("service stop did not become a capture cancellation: %v\n%s", err, out)
	}
}
