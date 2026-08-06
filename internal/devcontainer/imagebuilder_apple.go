package devcontainer

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"
)

// AppleContainerBuilder builds images with Apple's `container` CLI (macOS 26+).
//
// It exists because Apple Containers has no `commit` — `container commit`
// reports `Plugin 'container-commit' not found` — and the container-commit path
// the Docker provider uses therefore has nothing to fall back to on a Mac
// (#1779). Building is the only way that runtime can produce a provisioned
// image, which is what makes this the seam `ImageBuilder` was written for: the
// engine changes, the provisioner does not.
//
// `container build` is real BuildKit — verified against 1.2.0 that the
// `# syntax=docker/dockerfile:1` frontend resolves and `RUN --mount=type=cache`
// takes effect — so the Dockerfile GenerateDockerfile already emits builds here
// unchanged, cache mounts and all.
type AppleContainerBuilder struct {
	bin    string // resolved `container` CLI on PATH ("" = unavailable)
	logger *slog.Logger
	// idleTimeout kills a build that stops emitting progress. See Build.
	idleTimeout time.Duration
}

// defaultBuildIdleTimeout is how long a build may stay silent before it is
// treated as finished-and-wedged.
//
// Sized against the slowest legitimate gap a real build shows: pulling a base
// image layer or an `apt-get install` inside a feature can run for minutes
// without a line, while the wedge that prompted this sat silent indefinitely at
// 0% CPU. Five minutes is comfortably past the former and far short of "a
// customer stares at a spinner forever".
const defaultBuildIdleTimeout = 5 * time.Minute

// NewAppleContainerBuilder probes for Apple's `container` CLI on PATH.
//
// Unlike the Docker builder there is no daemon to pin: `container` talks to the
// per-user apiserver it starts itself, so there is no equivalent of DOCKER_HOST
// / `docker context` for the build and the daemon-split hazard of #1705 cannot
// arise.
func NewAppleContainerBuilder(logger *slog.Logger) *AppleContainerBuilder {
	if logger == nil {
		logger = slog.Default()
	}
	bin := ""
	if p, err := exec.LookPath("container"); err == nil {
		bin = p
	}
	return &AppleContainerBuilder{bin: bin, logger: logger, idleTimeout: defaultBuildIdleTimeout}
}

// Available reports whether a usable `container` CLI was found.
//
// Presence only, matching the Docker builder: whether the apiserver is actually
// up is a question with a moving answer, and asking it here would mean a
// subprocess on every availability check. A stopped daemon surfaces as a build
// error naming the tag, which is the honest place for it.
func (b *AppleContainerBuilder) Available() bool {
	return b != nil && b.bin != ""
}

// Build runs `container build` against contextDir and tags the result.
//
// `--progress plain` is not cosmetic: the default (`auto`) emits TTY control
// sequences, and these lines are streamed verbatim into provision events, the
// journal and the live WS payload.
func (b *AppleContainerBuilder) Build(ctx context.Context, contextDir, tag string, onLog func(string)) error {
	if !b.Available() {
		return fmt.Errorf("devcontainer: Apple `container` CLI not available")
	}
	// #nosec G204 — bin is a PATH-resolved `container` binary; tag/contextDir
	// are internally constructed (cache tag + temp dir), not user-controlled.
	cmd := exec.CommandContext(ctx, b.bin, "build",
		"--tag", tag,
		"--file", filepath.Join(contextDir, "Dockerfile"),
		"--progress", "plain",
		contextDir,
	)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("build stdout pipe: %w", err)
	}
	cmd.Stderr = cmd.Stdout // BuildKit writes progress to stderr; merge streams
	// Own process group so the watchdog can take the whole tree down. Killing
	// only the CLI leaves its helpers holding the write end of the pipe, and
	// the read below then blocks until they happen to exit — which is the very
	// hang the watchdog exists to end.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting container build: %w", err)
	}

	// Watchdog. `container build` has been seen finishing the image — manifest
	// exported, tag written — and then never exiting, at 0% CPU. Waiting on
	// Wait() alone therefore means waiting forever, so a build that stops
	// talking gets killed. Whether it had already succeeded is answered
	// afterwards by ImageExists, not guessed at here.
	var lastOutput atomic.Int64
	lastOutput.Store(time.Now().UnixNano())
	idle := b.idleTimeout
	if idle <= 0 {
		idle = defaultBuildIdleTimeout
	}
	watchdogDone := make(chan struct{})
	var wedged atomic.Bool
	go func() {
		ticker := time.NewTicker(idle / 4)
		defer ticker.Stop()
		for {
			select {
			case <-watchdogDone:
				return
			case <-ticker.C:
				quiet := time.Since(time.Unix(0, lastOutput.Load()))
				if quiet >= idle {
					wedged.Store(true)
					b.logger.Warn("container build went silent — killing it",
						"tag", tag, "silent_for", quiet.String())
					// Negative pid = the whole process group.
					if pgid, gerr := syscall.Getpgid(cmd.Process.Pid); gerr == nil {
						_ = syscall.Kill(-pgid, syscall.SIGKILL)
					} else {
						_ = cmd.Process.Kill()
					}
					return
				}
			}
		}
	}()

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		lastOutput.Store(time.Now().UnixNano())
		line := scanner.Text()
		if onLog != nil {
			onLog(line)
		}
		b.logger.Debug("container build", "line", line)
	}
	close(watchdogDone)

	if err := cmd.Wait(); err != nil {
		if wedged.Load() {
			return fmt.Errorf("container build for %s stopped responding after %s of silence: %w", tag, idle, err)
		}
		return fmt.Errorf("container build failed for %s: %w", tag, err)
	}
	return nil
}

// ImageExists reports whether tag is present in the local image store.
//
// It is what makes the watchdog safe: a killed build may well have produced its
// image before wedging — that is exactly what happened the first time this was
// seen — so the tag, not the exit status, is the authority on whether the work
// got done. A non-zero exit from `image inspect` is a plain absence, not an
// error worth propagating.
func (b *AppleContainerBuilder) ImageExists(ctx context.Context, tag string) (bool, error) {
	if !b.Available() {
		return false, fmt.Errorf("devcontainer: Apple `container` CLI not available")
	}
	// #nosec G204 — bin is PATH-resolved; tag is an internally built cache tag.
	cmd := exec.CommandContext(ctx, b.bin, "image", "inspect", tag)
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return false, nil
		}
		return false, fmt.Errorf("inspecting %s: %w", tag, err)
	}
	return true, nil
}
