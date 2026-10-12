package runoutput

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"sync"
	"time"
)

type Result struct {
	ExitCode int    `json:"exit_code"`
	Reason   string `json:"reason"`
}

// Command carries execution inputs captured at admission. Env must be the
// complete environment, rather than a delta applied to an old tmux server.
type Command struct {
	Args  []string `json:"args"`
	Env   []string `json:"env"`
	Dir   string   `json:"dir"`
	Stdin []byte   `json:"stdin,omitempty"`
}

type LaunchInput struct {
	Command  Command   `json:"command"`
	Deadline time.Time `json:"deadline"`
}

type streamWriter struct {
	store      *Store
	stream     string
	cancel     context.CancelFunc
	mu         *sync.Mutex
	writeError *error
}

func (w streamWriter) Write(data []byte) (int, error) {
	n, err := w.store.Write(w.stream, data)
	if err != nil {
		w.mu.Lock()
		if *w.writeError == nil {
			*w.writeError = err
		}
		w.mu.Unlock()
		w.cancel()
	}
	return n, err
}

// Capture owns the child independently of any Follow reader. A directory is
// claimed before command creation; retrying the same directory never relaunches
// it. ctx is the execution's own deadline/stop signal, not a reader connection.
func Capture(ctx context.Context, dir string, args []string, limit int64) (Result, error) {
	return CaptureCommand(ctx, dir, Command{Args: args}, limit)
}

func CaptureCommand(ctx context.Context, dir string, command Command, limit int64) (Result, error) {
	args := command.Args
	result := Result{ExitCode: -1, Reason: "start_error"}
	if len(args) == 0 || args[0] == "" {
		return result, errors.New("missing run command")
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(runCtx, args[0], args[1:]...)
	cmd.Env = command.Env
	cmd.Dir = command.Dir
	if err := configureProcess(cmd); err != nil {
		return result, err
	}
	store, err := Create(dir, limit)
	if err != nil {
		return result, err
	}
	defer store.Close()
	var mu sync.Mutex
	var outputErr error
	cmd.Stdout = streamWriter{store, "stdout", cancel, &mu, &outputErr}
	cmd.Stderr = streamWriter{store, "stderr", cancel, &mu, &outputErr}
	cmd.Stdin = bytes.NewReader(command.Stdin)
	cmd.WaitDelay = 2 * time.Second
	err = cmd.Run()
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	mu.Lock()
	writeErr := outputErr
	mu.Unlock()
	switch {
	case errors.Is(writeErr, ErrLimit):
		result.Reason = "output_limit"
	case writeErr != nil:
		result.Reason = "output_error"
	case ctx.Err() != nil:
		result.Reason = "execution_cancelled"
	case errors.Is(err, exec.ErrWaitDelay):
		result.Reason = "output_incomplete"
		result.ExitCode = -1
	case cmd.ProcessState != nil && result.ExitCode < 0:
		result.Reason = "signal"
	case cmd.ProcessState != nil:
		result.Reason = "exited"
	}
	finishErr := store.Finish(result.ExitCode, result.Reason)
	// A quota refusal is represented by its durable terminal event. A storage
	// error is still an error even if a caller observed some output beforehand.
	if errors.Is(writeErr, ErrLimit) {
		writeErr = nil
	}
	return result, errors.Join(writeErr, finishErr)
}

var _ io.Writer = streamWriter{}
