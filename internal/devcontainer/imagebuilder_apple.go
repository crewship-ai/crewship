package devcontainer

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
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
	// settleDelay is how long a build must be quiet before its finished image
	// is trusted. See Build.
	settleDelay time.Duration
	// cpus and memoryMB size the builder container. Zero = leave it to the CLI.
	cpus     int
	memoryMB int
	sizeOnce sync.Once
}

// builderCPUMem reads the running builder's CPU and memory allocation.
// Reports (0, 0) when there is no builder or the output cannot be read.
func (b *AppleContainerBuilder) builderCPUMem(ctx context.Context) (cpus, memMB int) {
	out, err := exec.CommandContext(ctx, b.bin, "builder", "status").Output() // #nosec G204
	if err != nil {
		return 0, 0
	}
	// ID  IMAGE  STATE  IP  CPUS  MEMORY
	// buildkit  …  running  …  2  2048 MB
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || f[0] == "ID" {
			continue
		}
		for i := 0; i+1 < len(f); i++ {
			if strings.EqualFold(f[i+1], "MB") {
				memMB, _ = strconv.Atoi(f[i])
				if i > 0 {
					cpus, _ = strconv.Atoi(f[i-1])
				}
				return cpus, memMB
			}
		}
	}
	return 0, 0
}

// ensureBuilderSized recreates the builder when it is smaller than the share
// this host can afford.
//
// The sizing has to happen here rather than on the build: `container build`
// accepts --cpus/--memory and silently ignores them once the builder container
// is running (verified on 1.2.0 — a 6-CPU request against a live 2-CPU builder
// still gave nproc=3). Recreating costs the builder's layer cache, so an
// already-sufficient builder is left alone, and the whole thing runs once per
// process.
//
// Best-effort throughout: a builder that cannot be read or recreated just means
// the build runs at Apple's defaults, which is what it did before.
func (b *AppleContainerBuilder) ensureBuilderSized(ctx context.Context) {
	if b.cpus <= 0 && b.memoryMB <= 0 {
		return
	}
	haveCPUs, haveMemMB := b.builderCPUMem(ctx)
	if haveCPUs >= b.cpus && haveMemMB >= b.memoryMB {
		return
	}
	b.logger.Info("resizing the container builder for this host",
		"from_cpus", haveCPUs, "from_memory_mb", haveMemMB,
		"to_cpus", b.cpus, "to_memory_mb", b.memoryMB)
	// #nosec G204 — bin is PATH-resolved; arguments are internally built.
	_ = exec.CommandContext(ctx, b.bin, "builder", "delete", "--force").Run()
	if err := exec.CommandContext(ctx, b.bin, "builder", "start",
		"--cpus", strconv.Itoa(b.cpus),
		"--memory", strconv.Itoa(b.memoryMB)+"m",
	).Run(); err != nil {
		b.logger.Warn("could not resize the builder — continuing at its defaults", "error", err)
	}
}

// builderShare picks how much of the host to hand the builder.
//
// Apple's builder defaults to 2 CPUs and 2048 MB whatever the machine, and a
// devcontainer feature's `apt-get install` / `npm ci` then runs inside that —
// which is most of a cold provisioning run. Taking a share of the host makes
// those installs finish in proportion to the hardware.
//
// Deliberately a share and not the lot: the build runs while someone is using
// the machine, and a builder that claims every core makes the desktop
// unusable. Never below the CLI's own defaults, so a small host is not made
// worse off than it would have been.
func builderShare(hostCPUs, hostMemMB int) (cpus, memMB int) {
	cpus = hostCPUs / 2
	if cpus < 2 {
		cpus = 2
	}
	if cpus > 8 {
		cpus = 8
	}
	memMB = hostMemMB / 2
	if memMB < 2048 {
		memMB = 2048
	}
	if memMB > 8192 {
		memMB = 8192
	}
	return cpus, memMB
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

// defaultBuildSettleDelay is how long a build must be silent before a present
// image tag is taken as proof that it finished.
//
// Short because the tag is strong evidence on its own — BuildKit writes it once
// the export completes — and the delay only guards against reading a tag left
// by an earlier build while this one is still mid-export. Waiting out the full
// idle timeout instead cost 5m36s on a 1m53s build.
const defaultBuildSettleDelay = 15 * time.Second

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
	cpus, memMB := builderShare(runtime.NumCPU(), hostMemoryMB())
	return &AppleContainerBuilder{
		bin:         bin,
		logger:      logger,
		idleTimeout: defaultBuildIdleTimeout,
		settleDelay: defaultBuildSettleDelay,
		cpus:        cpus,
		memoryMB:    memMB,
	}
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
	b.sizeOnce.Do(func() { b.ensureBuilderSized(ctx) })

	args := []string{"build",
		"--tag", tag,
		"--file", filepath.Join(contextDir, "Dockerfile"),
		"--progress", "plain",
	}
	args = append(args, contextDir)
	// #nosec G204 — bin is PATH-resolved; every argument is internally built.
	cmd := exec.CommandContext(ctx, b.bin, args...)

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
	settle := b.settleDelay
	if settle <= 0 {
		settle = defaultBuildSettleDelay
	}
	watchdogDone := make(chan struct{})
	var wedged, finished, exported atomic.Bool
	// exportStep is the BuildKit step number that announced the export, so its
	// DONE line can be told apart from every other step's.
	var exportStep atomic.Value
	kill := func() {
		// Negative pid = the whole process group. Killing only the CLI leaves
		// its helpers holding the write end of the pipe.
		if pgid, gerr := syscall.Getpgid(cmd.Process.Pid); gerr == nil {
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
		} else {
			_ = cmd.Process.Kill()
		}
	}
	go func() {
		tick := settle / 2
		if tick <= 0 {
			tick = time.Second
		}
		ticker := time.NewTicker(tick)
		defer ticker.Stop()
		for {
			select {
			case <-watchdogDone:
				return
			case <-ticker.C:
				quiet := time.Since(time.Unix(0, lastOutput.Load()))
				// BuildKit announces the export and reports it DONE once the
				// image is written. Anything after that is the CLI failing to
				// exit, so a short grace is enough.
				//
				// Deliberately NOT a probe of the image store: a wedged
				// `container build` blocks `container image inspect` as well —
				// the apiserver serialises — so asking answered "no image" for
				// a build whose image was already there, and the run waited out
				// the full idle timeout anyway.
				if exported.Load() && quiet >= settle {
					finished.Store(true)
					b.logger.Info("export finished and the build went quiet — not waiting for it to exit",
						"tag", tag, "quiet_for", quiet.String())
					kill()
					return
				}
				if quiet >= idle {
					wedged.Store(true)
					b.logger.Warn("container build went silent before producing an image — killing it",
						"tag", tag, "silent_for", quiet.String())
					kill()
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
		if step, ok := exportAnnouncement(line); ok {
			exportStep.Store(step)
		}
		if step, _ := exportStep.Load().(string); step != "" && strings.HasPrefix(line, "#"+step+" DONE") {
			exported.Store(true)
		}
		if onLog != nil {
			onLog(line)
		}
		b.logger.Debug("container build", "line", line)
	}
	close(watchdogDone)

	if err := cmd.Wait(); err != nil {
		if finished.Load() {
			// Killed on purpose, with the image already built.
			return nil
		}
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

// hostMemoryMB reports physical RAM in MB, or 0 when it cannot be determined
// (builderShare then falls back to its floor).
func hostMemoryMB() int {
	out, err := exec.Command("sysctl", "-n", "hw.memsize").Output()
	if err != nil {
		return 0
	}
	bytes, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		return 0
	}
	return int(bytes / (1024 * 1024))
}

// exportAnnouncement reports the BuildKit step number of an
// "#N exporting to oci image format" line.
func exportAnnouncement(line string) (string, bool) {
	if !strings.Contains(line, "exporting to oci image format") {
		return "", false
	}
	f := strings.Fields(line)
	if len(f) == 0 || !strings.HasPrefix(f[0], "#") {
		return "", false
	}
	return strings.TrimPrefix(f[0], "#"), true
}
