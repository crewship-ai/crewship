package runoutput

import (
	"os"
	"os/signal"
)

// SurviveControllerStop keeps a host capture helper running through the
// termination signals of its controller's service. A service manager that
// stops the whole control group (systemd's default KillMode) signals the helper
// together with the server, so without this the helper died before it could
// record the terminal result, and recovery had nothing to settle from (#5081).
// The helper's lifetime belongs to the execution it captures: the controller
// ends that through the exact-process stop, and the absolute deadline still
// applies. A final SIGKILL is not interceptable and remains an unresolved capture.
//
// The signals are received and dropped rather than ignored, so a child process
// never inherits an ignored disposition. The returned function restores default
// handling.
func SurviveControllerStop() (release func()) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, ExecutionSignals()...)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-ch:
			case <-done:
				return
			}
		}
	}()
	return func() {
		signal.Stop(ch)
		close(done)
	}
}
