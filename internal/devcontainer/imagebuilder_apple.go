package devcontainer

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
	return parseBuilderCPUMem(string(out))
}

// parseBuilderCPUMem reads the CPU and memory columns out of
// `container builder status`. Reports (0, 0) for anything it does not
// recognise, which callers must treat as "cannot tell" — see ensureBuilderSized.
//
// Pure and fixture-tested on purpose. Three defects on this branch came from
// decoders written against an imagined payload, and this one's failure mode is
// expensive: read as (0, 0) and treated as "undersized", it deletes the builder
// and its whole layer cache (#1779).
func parseBuilderCPUMem(out string) (cpus, memMB int) {
	// ID  IMAGE  STATE  IP  CPUS  MEMORY
	// buildkit  …  running  …  5  8192 MB
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || f[0] == "ID" {
			continue
		}
		for i := 0; i+1 < len(f); i++ {
			if !strings.EqualFold(f[i+1], "MB") {
				continue
			}
			mem, memErr := strconv.Atoi(f[i])
			if memErr != nil || i == 0 {
				return 0, 0
			}
			cpu, cpuErr := strconv.Atoi(f[i-1])
			if cpuErr != nil {
				return 0, 0
			}
			return cpu, mem
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
	// (0, 0) means the status could not be read, NOT that the builder is tiny.
	// Treating it as undersized would delete the builder and its layer cache
	// every start — the expensive way to be wrong about an unrecognised CLI
	// rendering, and exactly the failure this parser must not cause.
	if haveCPUs == 0 && haveMemMB == 0 {
		b.logger.Warn("could not read the builder's size — leaving it alone",
			"want_cpus", b.cpus, "want_memory_mb", b.memoryMB)
		return
	}
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

// imageProbeTimeout bounds one `container image inspect`. A query, so short:
// its whole job is to answer or get out of the way.
const imageProbeTimeout = 20 * time.Second

// cancelKillInterval is how often the watchdog re-signals a cancelled build's
// process group while it waits for the pipe to close. Short, because the
// caller is already gone and the only thing left to do is stop holding their
// resources; cheap, because signalling an empty group is one failing syscall.
const cancelKillInterval = 250 * time.Millisecond

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
	ownProcessGroup(cmd)
	kill := func() { killProcessGroup(cmd) }
	// exec's own cancellation has to be the same kill, not a second one.
	// CommandContext's default Cancel is Process.Kill(), which signals the
	// direct `container` process alone — and it wakes on the same ctx.Done()
	// as the watchdog below. When exec's kill won that race the child could be
	// an exited, not-yet-reaped zombie before the group kill resolved it, which
	// on Darwin is unresolvable (getpgid returns ESRCH for a zombie), so the
	// group was never signalled and the descendants kept stdout open forever
	// (#2030). One killer, no race left to lose — the same shape runCLIWithin
	// in internal/provider/apple already uses.
	//
	// Returning nil rather than os.ErrProcessDone is deliberate: exec then
	// reports ctx.Err() if the process happens to exit 0 as it is cancelled,
	// so a cancelled build can never come back as a success. A non-zero exit —
	// what SIGKILL actually produces — still wins over it in Wait.
	cmd.Cancel = func() error { kill(); return nil }

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
		tick := idle / 4
		if tick <= 0 {
			tick = time.Second
		}
		ticker := time.NewTicker(tick)
		defer ticker.Stop()
		cancelled := ctx.Done()
		for {
			select {
			case <-watchdogDone:
				return
			case <-cancelled:
				// exec.CommandContext kills the `container` process and
				// nothing else. Its descendants can still hold the write end
				// of stdout, so scanner.Scan() below keeps blocking after
				// cancellation and the build is only released when the idle
				// timer fires — minutes after the caller gave up. kill()
				// takes the whole process group, which closes the pipe.
				kill()
				// Stay armed until the read side actually closes. This branch
				// used to return, so one kill that missed left nothing able to
				// try again and no idle rescue either — the build leaked its
				// goroutine, its pipe and its process tree for good, not for
				// five minutes (#2030). A closed Done channel stays ready, so
				// drop it from the select and re-signal on a short cadence
				// instead; the loop ends when the scanner drains and Build
				// closes watchdogDone.
				cancelled = nil
				ticker.Reset(cancelKillInterval)
			case <-ticker.C:
				if cancelled == nil {
					kill()
					continue
				}
				quiet := time.Since(time.Unix(0, lastOutput.Load()))
				// Only prolonged silence ends a build. The export marker is
				// NOT the end: after BuildKit reports the export DONE the CLI
				// still registers the image into the runtime's store, and
				// killing it there aborts that write. Measured live — a build
				// killed 16.8s after the marker produced no image at all, and
				// provisioning then failed on a store that stayed empty.
				//
				// Probing the store during the silence does not work either: a
				// wedged `container build` blocks `container image inspect`
				// too, because the apiserver serialises. So there is no cheap
				// signal that separates "working" from "wedged" — only time,
				// and the tag check afterwards.
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
		if onLog != nil {
			onLog(line)
		}
		b.logger.Debug("container build", "line", line)
	}
	// A scanner error — an over-long line, most plausibly — ends the read
	// while the CLI is still writing. Retiring the watchdog here would leave
	// nothing able to kill it, the pipe would fill, and cmd.Wait() would block
	// until the outer provisioning context expired half an hour later with
	// nothing in the log naming the cause. Drain the rest instead, so the
	// process can finish and the watchdog stays armed until it does.
	if scanErr := scanner.Err(); scanErr != nil {
		b.logger.Warn("build log stream ended early; draining the rest",
			"tag", tag, "error", scanErr)
		_, _ = io.Copy(io.Discard, stdout)
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
	// Bounded on its own. waitForImage loops against a deadline, but a single
	// unbounded call defeats it entirely: a wedged apiserver blocks `image
	// inspect` — observed today during a build wedge — so the deadline would
	// never be re-evaluated and provisioning would stall past its own budget.
	ctx, cancel := context.WithTimeout(ctx, imageProbeTimeout)
	defer cancel()
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
