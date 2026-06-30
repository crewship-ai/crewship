package apple

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

func (p *Provider) ContainerStats(_ context.Context, _ string) (*provider.ContainerMetrics, error) {
	return nil, fmt.Errorf("container stats not supported on Apple Containers")
}

// Exec runs a command inside a container via the Apple Container CLI exec.
// It returns a reader for stdout/stderr and tracks the exec process for ExecInspect.
func (p *Provider) Exec(ctx context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	args := []string{"exec"}

	for _, env := range cfg.Env {
		args = append(args, "--env", env)
	}
	if cfg.WorkingDir != "" {
		args = append(args, "--workdir", cfg.WorkingDir)
	}
	if cfg.User != "" {
		args = append(args, "--user", cfg.User)
	}

	args = append(args, cfg.ContainerID)
	args = append(args, cfg.Cmd...)

	cmd := exec.CommandContext(ctx, "container", args...)

	// Attach stdin when supplied so oversized agent prompts (too large to pass
	// as an argv element) reach the CLI. nil leaves stdin unset — the historic
	// behaviour.
	if cfg.Stdin != nil {
		cmd.Stdin = cfg.Stdin
	}

	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw

	if err := cmd.Start(); err != nil {
		pw.Close()
		pr.Close()
		return nil, fmt.Errorf("exec start: %w", err)
	}

	execID := fmt.Sprintf("apple-exec-%d", p.execSeq.Add(1))

	entry := &execEntry{
		cmd:  cmd,
		done: make(chan struct{}),
	}

	p.mu.Lock()
	p.execs[execID] = entry
	p.mu.Unlock()

	go func() {
		defer pw.Close()
		err := cmd.Wait()
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				entry.exitCode = exitErr.ExitCode()
			} else {
				entry.exitCode = -1
			}
		}
		close(entry.done)
	}()

	return &provider.ExecResult{
		ExecID: execID,
		Reader: pr,
	}, nil
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
		return true, 0, nil
	}
}

// ExecInteractive creates an interactive TTY exec session with bidirectional I/O.

func (p *Provider) ExecInteractive(ctx context.Context, cfg provider.InteractiveExecConfig) (*provider.InteractiveExecResult, error) {
	args := []string{"exec", "--tty"}

	for _, env := range cfg.Env {
		args = append(args, "--env", env)
	}
	if cfg.WorkingDir != "" {
		args = append(args, "--workdir", cfg.WorkingDir)
	}
	if cfg.User != "" {
		args = append(args, "--user", cfg.User)
	}

	args = append(args, cfg.ContainerID)
	args = append(args, cfg.Cmd...)

	cmd := exec.CommandContext(ctx, "container", args...)

	// Create a bidirectional pipe for the PTY stream.
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()

	cmd.Stdin = stdinR
	cmd.Stdout = stdoutW
	cmd.Stderr = stdoutW

	if err := cmd.Start(); err != nil {
		stdinR.Close()
		stdinW.Close()
		stdoutR.Close()
		stdoutW.Close()
		return nil, fmt.Errorf("exec interactive start: %w", err)
	}

	execID := fmt.Sprintf("apple-exec-%d", p.execSeq.Add(1))

	entry := &execEntry{
		cmd:  cmd,
		done: make(chan struct{}),
	}
	p.mu.Lock()
	p.execs[execID] = entry
	p.mu.Unlock()

	go func() {
		defer stdoutW.Close()
		defer stdinR.Close()
		err := cmd.Wait()
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				entry.exitCode = exitErr.ExitCode()
			} else {
				entry.exitCode = -1
			}
		}
		close(entry.done)
	}()

	conn := &pipeReadWriteCloser{
		Reader: stdoutR,
		Writer: stdinW,
		closeFn: func() error {
			stdinW.Close()
			stdoutR.Close()
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
			p.mu.Lock()
			for id, entry := range p.execs {
				select {
				case <-entry.done:
					delete(p.execs, id)
				default:
				}
			}
			p.mu.Unlock()
		}
	}
}
