//go:build unix

package runoutput

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

const survivorChildEnv = "CREWSHIP_RUNOUTPUT_SIGNAL_CHILD"

// A service stop signals every process of the control group at once. The
// capture helper must outlive that signal to write its terminal result (#5081).
func TestSurviveControllerStopOutlivesServiceSignals(t *testing.T) {
	if os.Getenv(survivorChildEnv) == "1" {
		release := SurviveControllerStop()
		defer release()
		for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGHUP, syscall.SIGINT} {
			if err := syscall.Kill(os.Getpid(), sig); err != nil {
				os.Exit(3)
			}
		}
		time.Sleep(200 * time.Millisecond)
		os.Exit(0)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestSurviveControllerStopOutlivesServiceSignals$")
	child.Env = append(os.Environ(), survivorChildEnv+"=1")
	err := child.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		t.Fatalf("helper did not survive the service stop signal: %v", exit)
	}
	if err != nil {
		t.Fatal(err)
	}
}

// After release the default disposition applies again, so the guard cannot
// leak into code that expects to be stoppable.
func TestSurviveControllerStopReleaseRestoresDefault(t *testing.T) {
	if os.Getenv(survivorChildEnv) == "2" {
		SurviveControllerStop()()
		_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
		time.Sleep(2 * time.Second)
		os.Exit(0)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestSurviveControllerStopReleaseRestoresDefault$")
	child.Env = append(os.Environ(), survivorChildEnv+"=2")
	err := child.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("released guard still swallowed SIGTERM: %v", err)
	}
	if status, ok := exit.Sys().(syscall.WaitStatus); !ok || !status.Signaled() || status.Signal() != syscall.SIGTERM {
		t.Fatalf("child ended with %v, want SIGTERM", exit)
	}
}
