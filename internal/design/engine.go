// Package design hosts the standard Yjs engine without a new long-running
// service. Go remains the only authority for credentials, ACLs and persistence.
package design

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"
)

//go:generate node build.mjs

//go:embed engine.cjs
var engine string

var ErrUnavailable = errors.New("design Yjs engine unavailable")
var ErrInvalid = errors.New("invalid design update")

type Request struct {
	State      []byte          `json:"state,omitempty"`
	Frame      []byte          `json:"frame,omitempty"`
	Replace    []byte          `json:"replace,omitempty"`
	Operations json.RawMessage `json:"operations,omitempty"`
}
type Result struct {
	Hello    []byte            `json:"hello"`
	State    []byte            `json:"state"`
	Update   []byte            `json:"update"`
	Frame    []byte            `json:"frame"`
	Response []byte            `json:"response"`
	Nodes    []json.RawMessage `json:"nodes"`
	Order    []string          `json:"order"`
}

// Default phase budgets, measured on crewship-dev (12 cores) for #4496:
// node plus the embedded bundle is ready after 27-31 ms idle and at most
// 306 ms with 48 concurrent cold starts; a 3.5 MB document is then processed
// in at most 151 ms. Start-up is the part that stretches on a starved host (a
// CI shard once needed more than 5 s), so it gets the wider budget. The work
// budget keeps the former 5 s bound for the operation itself.
const (
	DefaultStartupTimeout = 15 * time.Second
	DefaultWorkTimeout    = 5 * time.Second
	// killGrace bounds the wait for output pipes after the engine is killed,
	// so an inherited descriptor cannot hold Run past its budget.
	killGrace = time.Second
)

// readyMarker is written by the host-side suffix once the bundle has been
// evaluated and before the engine reads its request. It separates a slow
// cold start from a hung operation.
const readyMarker = "crewship-design-engine-ready\n"

// readySuffix runs after the bundle's top-level code: the engine's main has
// already suspended on stdin, so everything before it is start-up.
var readySuffix = "\n;process.stdout.write(" + strconv.Quote(readyMarker) + ");\n"

// Phase names the part of an engine run that exceeded its budget.
type Phase string

const (
	PhaseStartup Phase = "startup"
	PhaseWork    Phase = "work"
)

// TimeoutError reports which phase of an engine run exceeded its budget. It
// is an ErrUnavailable, so callers keep mapping it to a retryable failure.
type TimeoutError struct {
	Phase  Phase
	Budget time.Duration
}

func (e *TimeoutError) Error() string {
	if e.Phase == PhaseStartup {
		return fmt.Sprintf("%s: engine did not become ready within the %s startup budget", ErrUnavailable, e.Budget)
	}
	return fmt.Sprintf("%s: operation did not finish within the %s work budget", ErrUnavailable, e.Budget)
}

func (e *TimeoutError) Unwrap() error { return ErrUnavailable }

// Engine runs the standard Yjs engine with separate start-up and work budgets.
// Zero budgets take the defaults.
type Engine struct {
	StartupTimeout time.Duration
	WorkTimeout    time.Duration
	// command replaces node and the bundle in tests; nil runs the engine.
	command func(ctx context.Context) (*exec.Cmd, error)
}

// Run executes one bounded standard Yjs operation with the default budgets.
// Node is an existing build/application runtime, not a new host service. No
// caller chooses executable code.
func Run(ctx context.Context, r Request) (Result, error) {
	return Engine{}.Run(ctx, r)
}

// Run executes one operation. Start-up ends when the engine signals ready;
// the work budget starts only then. Either timeout kills the process and
// returns a *TimeoutError naming the phase.
func (e Engine) Run(ctx context.Context, r Request) (Result, error) {
	startupBudget, workBudget := e.StartupTimeout, e.WorkTimeout
	if startupBudget <= 0 {
		startupBudget = DefaultStartupTimeout
	}
	if workBudget <= 0 {
		workBudget = DefaultWorkTimeout
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return Result{}, err
	}
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	command := e.command
	if command == nil {
		command = nodeCommand
	}
	cmd, err := command(ctx)
	if err != nil {
		return Result{}, err
	}
	cmd.WaitDelay = killGrace
	cmd.Stdin = bytes.NewReader(raw)
	output := &readyWriter{ready: make(chan struct{})}
	var stderr bytes.Buffer
	cmd.Stdout = output
	cmd.Stderr = &stderr
	if err = cmd.Start(); err != nil {
		return Result{}, ErrUnavailable
	}
	startup := time.AfterFunc(startupBudget, func() { cancel(&TimeoutError{Phase: PhaseStartup, Budget: startupBudget}) })
	defer startup.Stop()
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	select {
	case <-output.ready:
		if startup.Stop() {
			work := time.AfterFunc(workBudget, func() { cancel(&TimeoutError{Phase: PhaseWork, Budget: workBudget}) })
			defer work.Stop()
		}
		err = <-exited
	case err = <-exited:
	}
	if err != nil {
		if ctx.Err() != nil {
			var timeout *TimeoutError
			if cause := context.Cause(ctx); errors.As(cause, &timeout) {
				return Result{}, timeout
			}
			return Result{}, fmt.Errorf("%w: %v", ErrUnavailable, context.Cause(ctx))
		}
		return Result{}, runError(err, stderr.String())
	}
	body, ok := output.result()
	if !ok {
		return Result{}, ErrUnavailable
	}
	var result Result
	if err = json.Unmarshal(body, &result); err != nil {
		return Result{}, ErrUnavailable
	}
	return result, nil
}

func nodeCommand(ctx context.Context) (*exec.Cmd, error) {
	nodePath, err := exec.LookPath("node")
	if err != nil {
		return nil, ErrUnavailable
	}
	cmd := exec.CommandContext(ctx, nodePath, "--max-old-space-size=96", "-e", engine+readySuffix)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	return cmd, nil
}

// readyWriter collects engine output and closes ready once the output starts
// with readyMarker.
type readyWriter struct {
	mu       sync.Mutex
	buf      bytes.Buffer
	ready    chan struct{}
	signaled bool
}

func (w *readyWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, _ := w.buf.Write(p)
	if !w.signaled && bytes.HasPrefix(w.buf.Bytes(), []byte(readyMarker)) {
		w.signaled = true
		close(w.ready)
	}
	return n, nil
}

// result returns the output after the marker, or false without the marker.
func (w *readyWriter) result() ([]byte, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.signaled {
		return nil, false
	}
	return w.buf.Bytes()[len(readyMarker):], true
}

// runError treats only the engine's deliberate exit code 1 as a refused
// update. A process that could not start, was killed or ran out of heap is a
// transient failure the client may retry; calling it invalid would make the
// client stop reconnecting.
func runError(err error, stderr string) error {
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return fmt.Errorf("%w: %s", ErrInvalid, stderr)
	}
	return ErrUnavailable
}
