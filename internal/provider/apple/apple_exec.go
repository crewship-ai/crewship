package apple

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

// execRunningExitCode is the exit code ExecInspect reports for an exec that has
// not finished yet. It is deliberately not 0: several callers throw the running
// flag away and branch on the code alone (routes_files.go, keeper_execute.go,
// exec_sidecar.go's prep paths), so a 0 for "no result yet" reads as success.
// -1 is the same "no usable status" value the finished path uses when the exit
// status cannot be determined, so those callers fail closed either way.
const execRunningExitCode = -1

func (p *Provider) ContainerStats(_ context.Context, _ string) (*provider.ContainerMetrics, error) {
	return nil, fmt.Errorf("container stats not supported on Apple Containers")
}

// Exec runs a command inside a container via the Apple Container CLI exec.
// It returns a reader for stdout/stderr and tracks the exec process for ExecInspect.
func (p *Provider) Exec(ctx context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	user, err := p.resolveExecUser(ctx, "exec", cfg.ContainerID, cfg.User, cfg.AllowPrivileged)
	if err != nil {
		return nil, err
	}

	args := []string{"exec"}

	// -i is required for the CLI to attach our stdin. Without it `sh` (no
	// operands) reads its commands from a stream already at EOF: it runs
	// nothing and exits 0. That is how the merged preflight batch — which
	// delivers its script over stdin so credentials never reach argv — silently
	// executed nothing on macOS while reporting success, and the run then died
	// on a missing .mcp.json three layers later (#1779). Verified on 1.2.0:
	// without -i a piped `exit 7` script produced no output and exit 0; with
	// -i it printed and exited 7.
	if cfg.Stdin != nil {
		args = append(args, "-i")
	}

	for _, env := range cfg.Env {
		args = append(args, "--env", env)
	}
	if cfg.WorkingDir != "" {
		args = append(args, "--workdir", cfg.WorkingDir)
	}
	args = append(args, "--user", user)

	args = append(args, cfg.ContainerID)
	args = append(args, cfg.Cmd...)

	cmd := exec.CommandContext(ctx, "container", args...)

	// Attach stdin when supplied so oversized agent prompts (too large to pass
	// as an argv element) reach the CLI. nil leaves stdin unset — the historic
	// behaviour.
	if cfg.Stdin != nil {
		cmd.Stdin = cfg.Stdin
	}

	// A spool rather than an io.Pipe: the process must be able to finish (and
	// so produce a real exit code) whether or not the caller drains it. See
	// execSpool's doc comment.
	spool := newExecSpool()
	cmd.Stdout = spool
	cmd.Stderr = spool

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("exec start: %w", err)
	}

	containerID := cfg.ContainerID
	execID := p.registerExec(cmd, func() {
		spool.closeWrite()
		// Say when output was dropped rather than handing back a silently
		// shortened stream.
		if n := spool.droppedBytes(); n > 0 {
			p.logger.Warn("exec output truncated: reader closed or fell behind",
				"container", provider.ShortID(containerID), "dropped_bytes", n)
		}
	})

	return &provider.ExecResult{
		ExecID: execID,
		Reader: spool,
	}, nil
}

// resolveExecUser returns the user an exec must run as, or an error refusing to
// run at all — the #1158 fail-closed guard, mirroring the Docker provider.
//
// Two separate things are enforced. An EMPTY user is resolved to the
// container's real configured run-as user (ContainerUser, the same source
// keeper's /execute path uses) instead of being left to the CLI, which would
// otherwise fall back to the image's default — root on nearly every base image;
// `container exec --user root` demonstrably worked here. And a PRIVILEGED user
// is refused however it arrives, so an explicit "0"/"root"/"0:0" from a call
// site is caught too.
//
// allowPrivileged is the single audited exception, set only by the
// orchestrator's root-requiring preflight steps (sidecar kill, dual-writer file
// pre-create). It never covers the resolve branch: if the container has no safe
// user of its own we refuse regardless, because "root by omission" is exactly
// the accident the guard exists to prevent.
func (p *Provider) resolveExecUser(ctx context.Context, op, containerID, user string, allowPrivileged bool) (string, error) {
	if user == "" {
		resolved, err := p.ContainerUser(ctx, containerID)
		if err != nil {
			return "", fmt.Errorf("%s: resolve run-as user for container %s: %w", op, containerID, err)
		}
		if resolved == "" || provider.IsPrivilegedExecUser(resolved) {
			return "", fmt.Errorf("%s: container %s has no safe non-root user configured (resolved %q); refusing to exec without an explicit user", op, containerID, resolved)
		}
		user = resolved
	}
	if !allowPrivileged && provider.IsPrivilegedExecUser(user) {
		return "", fmt.Errorf("%s: refusing to run as privileged user %q in container %s", op, user, containerID)
	}
	return user, nil
}

// registerExec allocates an exec ID, registers the started command for
// ExecInspect tracking, and spawns the goroutine that waits for it to finish,
// records the exit code and closes the entry's done channel. cleanup runs as
// the goroutine returns (after done is closed), closing the caller's pipe
// writers so readers observe EOF.
func (p *Provider) registerExec(cmd *exec.Cmd, cleanup func()) string {
	execID := fmt.Sprintf("apple-exec-%d", p.execSeq.Add(1))

	entry := &execEntry{
		cmd:  cmd,
		done: make(chan struct{}),
	}

	p.mu.Lock()
	p.execs[execID] = entry
	p.mu.Unlock()

	go func() {
		defer cleanup()
		err := cmd.Wait()
		switch {
		case err == nil:
			entry.exitCode = 0
		case cmd.ProcessState != nil:
			// Read the status off the process, not off Wait's error. Wait can
			// fail for reasons that have nothing to do with how the command
			// ended (an I/O error while copying its streams); stamping -1 in
			// that case threw away an exit code the kernel had already given
			// us, so a command that exited 7 was reported as "unknown".
			entry.exitCode = cmd.ProcessState.ExitCode()
		default:
			entry.exitCode = -1
		}
		// Written before the close so any reader that observes done is
		// guaranteed to see both fields (channel-close happens-before).
		entry.finishedAt = time.Now()
		close(entry.done)
	}()

	return execID
}

// ExecInspect checks if an exec process is still running and returns its exit code.

func (p *Provider) ExecInspect(_ context.Context, execID string) (bool, int, error) {
	p.mu.RLock()
	entry, ok := p.execs[execID]
	p.mu.RUnlock()

	if !ok {
		return false, -1, fmt.Errorf("exec %s not found", execID)
	}

	select {
	case <-entry.done:
		return false, entry.exitCode, nil
	default:
		return true, execRunningExitCode, nil
	}
}

// ExecInteractive creates an interactive TTY exec session with bidirectional I/O.

func (p *Provider) ExecInteractive(ctx context.Context, cfg provider.InteractiveExecConfig) (*provider.InteractiveExecResult, error) {
	// InteractiveExecConfig has no AllowPrivileged field: the web terminal is
	// reachable from a request, so there is no audited root exception here.
	user, err := p.resolveExecUser(ctx, "exec interactive", cfg.ContainerID, cfg.User, false)
	if err != nil {
		return nil, err
	}

	args := []string{"exec", "--tty"}

	for _, env := range cfg.Env {
		args = append(args, "--env", env)
	}
	if cfg.WorkingDir != "" {
		args = append(args, "--workdir", cfg.WorkingDir)
	}
	args = append(args, "--user", user)

	args = append(args, cfg.ContainerID)
	args = append(args, cfg.Cmd...)

	cmd := exec.CommandContext(ctx, "container", args...)

	// Stdin stays a pipe (the terminal writes into it); stdout is spooled for
	// the same reason as in Exec — a session whose reader goes away must still
	// let the process finish and report a real exit code.
	stdinR, stdinW := io.Pipe()
	stdout := newExecSpool()

	cmd.Stdin = stdinR
	cmd.Stdout = stdout
	cmd.Stderr = stdout

	if err := cmd.Start(); err != nil {
		stdinR.Close()
		stdinW.Close()
		return nil, fmt.Errorf("exec interactive start: %w", err)
	}

	execID := p.registerExec(cmd, func() {
		stdinR.Close()
		stdout.closeWrite()
	})

	conn := &pipeReadWriteCloser{
		Reader: stdout,
		Writer: stdinW,
		closeFn: func() error {
			stdinW.Close()
			stdout.Close()
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			return nil
		},
	}

	return &provider.InteractiveExecResult{
		ExecID: execID,
		Conn:   conn,
	}, nil
}

// ExecResize is a no-op for Apple Containers (CLI does not support resize).

func (p *Provider) ExecResize(_ context.Context, _ string, _, _ uint16) error {
	return nil
}

// RemoveCrewVolumes removes persistent home/tools directories for a crew.
// Apple Containers uses host-side directories instead of Docker named volumes.

type pipeReadWriteCloser struct {
	io.Reader
	io.Writer
	closeFn func() error
}

// Close closes both the reader and writer pipes.
func (p *pipeReadWriteCloser) Close() error {
	return p.closeFn()
}

// CopyToContainer is not supported on Apple Containers.

func (p *Provider) gcExecs() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-p.done:
			return
		case <-ticker.C:
			p.collectFinishedExecs(time.Now())
		}
	}
}

// execRetention is how long a finished exec entry stays inspectable.
//
// Every caller runs a command and inspects it afterwards, so collecting an
// entry the instant its process exited raced them: the sweep could land in
// that window and turn a completed exec into "exec ... not found" with exit
// code -1 — a fabricated failure for a command that had succeeded. The grace
// period is generous because the entries are tiny (a *exec.Cmd and a closed
// channel) and the failure it prevents is silent.
const execRetention = 10 * time.Minute

// collectFinishedExecs drops entries whose process finished more than
// execRetention ago. now is a parameter so the window is testable without
// sleeping.
func (p *Provider) collectFinishedExecs(now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, entry := range p.execs {
		select {
		case <-entry.done:
			// finishedAt is safe to read here: it is written before done is
			// closed, and we only reach this branch once it is.
			if now.Sub(entry.finishedAt) >= execRetention {
				delete(p.execs, id)
			}
		default:
		}
	}
}
