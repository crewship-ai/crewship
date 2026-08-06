package provider

import (
	"context"
	"fmt"
	"time"
)

// execInspector is the slice of ContainerProvider WaitExecExit needs. Narrow on
// purpose: it lets callers pass a provider, a fake, or anything else that can
// answer the one question.
type execInspector interface {
	ExecInspect(ctx context.Context, execID string) (bool, int, error)
}

// waitExecPollInterval is how often WaitExecExit re-asks. Short because the
// common case is that the exec has already finished and the first poll answers.
const waitExecPollInterval = 25 * time.Millisecond

// WaitExecExit returns an exec's REAL exit code, waiting for it to finish.
//
// ExecInspect answers two things at once — whether the process is still running
// and, if not, what it exited with — and callers have historically read the
// second while discarding the first. That was survivable while a running exec
// reported 0: wrong, but wrong in the direction of "success". It stops being
// survivable the moment the in-flight value fails closed instead, because then
// a race turns into a hard failure: the tmux probe caches "tmux is missing" for
// the container's whole life, and a successful crew-file write reports a
// non-zero status.
//
// Draining the output stream to EOF is not sufficient synchronisation on either
// provider. Docker sets Running=false on the exec-exit event, which can land
// after the stdio copiers close — docker_container.go already carries a poll
// loop for exactly that race. So the exit code is only meaningful once the
// process has actually exited, and this waits for that rather than picking a
// friendlier lie for the in-flight case (#1779).
//
// Bounded, because an exec that never finishes must not take the caller with
// it. On timeout it returns an error rather than a code: "I do not know" is the
// honest answer and every caller treats a non-nil error as failure.
func WaitExecExit(ctx context.Context, insp execInspector, execID string, timeout time.Duration) (int, error) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	deadline := time.Now().Add(timeout)

	for {
		running, code, err := insp.ExecInspect(ctx, execID)
		if err != nil {
			return -1, fmt.Errorf("inspecting exec %s: %w", execID, err)
		}
		if !running {
			return code, nil
		}
		if time.Now().After(deadline) {
			return -1, fmt.Errorf("exec %s did not finish within %s", execID, timeout)
		}
		select {
		case <-ctx.Done():
			return -1, ctx.Err()
		case <-time.After(waitExecPollInterval):
		}
	}
}
