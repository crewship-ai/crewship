package orchestrator

// The merged preflight script (#1646).
//
// preparePreflightDirs used to issue one container exec per setup step. Every
// exec is at least two daemon calls (create + start) plus a 50 ms-polled
// inspect, so a warm-container run paid ~19 execs — measured at seconds of
// pure latency before the agent CLI even started, identically on every wake.
// The per-file steps were worse than that: five canonical system-prompt files,
// five more paths per assigned skill, one to two per OAuth MCP server.
//
// The fix is to COLLAPSE, not to parallelise: concurrent execs contend on the
// same daemon path, and a provider without a local daemon socket pays more per
// round-trip, not less. One exec carries a script that does all of it.
//
// Three properties are load-bearing and each is pinned by a test:
//
//   - The script rides ExecConfig.Stdin, never argv. buildCredFileScript
//     base64s credential material into the script, and /proc/<pid>/cmdline is
//     mode 0444 — world-readable regardless of uid, printed by a bare ps, and
//     crew members share one container. Same defect #1629 fixed for the
//     agent's bearer token; the merged form is what finally closes it for the
//     preflight, because a script on stdin has no argv at all.
//
//   - Conditions are resolved in Go, not in shell `if`s. A request with memory
//     disabled emits no memory steps at all, so the shipped script — and a log
//     of it — is the truth about what that run did.
//
//   - A failing step is still named. Splitting one exec per step made failure
//     attribution free; merging must not lose it, or a broken crew becomes
//     materially harder to debug. Each step announces itself on stdout before
//     it runs and appends its name to a failure list if it exits non-zero, and
//     the Go side turns that list back into a named error. Steps are NOT
//     short-circuited on failure — the pre-merge form ran every step
//     regardless of its predecessors and callers relied on that.
//
// Nothing here is Docker-specific: provider.ContainerProvider.Exec and
// ExecConfig.Stdin are the whole surface, and both the Docker and the Apple
// provider implement them.

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

const (
	preflightStepAgentDirs      = "agent-dirs"
	preflightStepMemoryDirs     = "memory-dirs"
	preflightStepCrewMemoryDirs = "crew-memory-dirs"
	preflightStepMemoryMigrate  = "memory-migration"
	preflightStepCredentials    = "credentials"
	preflightStepClaudeConfig   = "claude-config"
	preflightStepMCPConfig      = "mcp-config"
	preflightStepSkillsPrune    = "skills-prune"

	// preflightStepMarker prefixes the line each step prints before it runs,
	// so the captured output reads as a transcript of what the run actually
	// did rather than an undifferentiated blob.
	preflightStepMarker = "crewship-preflight-step:"
	// preflightFailMarker prefixes the single trailing line that names every
	// step that exited non-zero. Absent when all steps succeeded.
	preflightFailMarker = "crewship-preflight-failed:"

	// preflightDoneMarker is printed as the script's last act. Its presence is
	// the only positive evidence that the script was delivered and reached the
	// end — the failure marker and the exit code both describe HOW a script
	// failed, and neither can distinguish that from a script that never ran.
	//
	// It had to: on Apple Containers the exec dropped stdin, so `sh` read an
	// empty stream, executed nothing and exited 0. Six preflight steps reported
	// clean, none had run, and the failure only surfaced later inside the agent
	// as a missing .mcp.json (#1779).
	preflightDoneMarker = "crewship-preflight-done"
)

// preflightBatchTimeout bounds the merged script. The pre-merge form gave each
// of ~19 steps its own 30 s ceiling — an aggregate worst case of nine minutes
// pinning the run goroutine and its runSem slot. One bounded script is both
// faster and a tighter hang ceiling; 60 s is generous for a sequence whose
// slowest member is a `cp -a` of an agent's .memory tree.
const preflightBatchTimeout = 60 * time.Second

// preflightTranscriptTail caps how much of the merged script's output is
// carried into an error. Enough to see the failing step's own diagnostics
// without pasting a whole run's transcript into a log line.
const preflightTranscriptTail = 2048

// preflightStep is one named fragment of the merged script.
type preflightStep struct {
	name string
	// script is the shell body, exactly as it would have been passed to
	// `sh -c` when the step had its own exec.
	script string
	// workingDir, when set, is entered before the body runs — the merged
	// exec itself has no WorkingDir, because the directory a step wants may
	// not exist until an earlier step in the same script has created it.
	workingDir string
}

// preflightBatch collects write-only preflight scripts and runs them as one
// exec. It is itself a provider.ContainerProvider so it can be handed to the
// per-CLI adapters (WriteMCPConfig, SetupSystemPrompt) without changing their
// signatures — but note that its Exec NEVER batches. Batching is opt-in, via
// runOrBatch, at the handful of call sites that are known to be write-only.
// Anything that reaches Exec is a caller that reads output or an exit code, so
// it is flushed-then-delegated: correct by default, and new code that knows
// nothing about batching keeps the pre-#1646 behaviour.
type preflightBatch struct {
	inner       provider.ContainerProvider
	containerID string
	user        string
	logger      *slog.Logger

	steps []preflightStep
	// failed names every step that a flush reported as non-zero, accumulated
	// across flushes so a caller can ask after the last one.
	failed map[string]bool
	// flushes counts the merged execs actually issued (0 when every batch
	// was empty). Used by tests and by the debug log.
	flushes int
}

func newPreflightBatch(inner provider.ContainerProvider, containerID, user string, logger *slog.Logger) *preflightBatch {
	return &preflightBatch{
		inner:       inner,
		containerID: containerID,
		user:        user,
		logger:      logger,
		failed:      map[string]bool{},
	}
}

// accepts reports whether cfg can ride the merged script. Deliberately strict:
// anything with its own env block, its own stdin, a different container or
// user, a privileged opt-in, or a non-`sh -c` argv runs on its own.
func (b *preflightBatch) accepts(cfg provider.ExecConfig) bool {
	if b == nil {
		return false
	}
	if cfg.ContainerID != b.containerID || cfg.User != b.user {
		return false
	}
	if cfg.Stdin != nil || len(cfg.Env) > 0 || cfg.AllowPrivileged {
		return false
	}
	return len(cfg.Cmd) == 3 && cfg.Cmd[0] == "sh" && cfg.Cmd[1] == "-c"
}

// add queues one step. Names are sanitised because they are interpolated into
// the script (a printf argument and a shell string), not merely logged.
func (b *preflightBatch) add(name, workingDir, script string) {
	b.steps = append(b.steps, preflightStep{
		name:       sanitisePreflightStepName(name),
		script:     script,
		workingDir: workingDir,
	})
}

// stepFailed reports whether a named step was reported failed by any flush.
func (b *preflightBatch) stepFailed(name string) bool {
	if b == nil {
		return false
	}
	return b.failed[sanitisePreflightStepName(name)]
}

// Exec never batches — see the type comment. It flushes anything queued first
// so a read can never observe a container state that the pre-merge sequential
// form would not have produced, then delegates verbatim.
func (b *preflightBatch) Exec(ctx context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	if err := b.Flush(ctx); err != nil && b.logger != nil {
		b.logger.Warn("preflight batch flush failed before a passthrough exec", "error", err)
	}
	return b.inner.Exec(ctx, cfg)
}

func (b *preflightBatch) ExecInspect(ctx context.Context, execID string) (bool, int, error) {
	return b.inner.ExecInspect(ctx, execID)
}

func (b *preflightBatch) EnsureCrewRuntime(ctx context.Context, cfg provider.CrewConfig) (string, error) {
	return b.inner.EnsureCrewRuntime(ctx, cfg)
}
func (b *preflightBatch) StopCrewRuntime(ctx context.Context, id string) error {
	return b.inner.StopCrewRuntime(ctx, id)
}
func (b *preflightBatch) RemoveCrewRuntime(ctx context.Context, id string) error {
	return b.inner.RemoveCrewRuntime(ctx, id)
}
func (b *preflightBatch) ContainerStatus(ctx context.Context, id string) (*provider.ContainerStatus, error) {
	return b.inner.ContainerStatus(ctx, id)
}
func (b *preflightBatch) ContainerStats(ctx context.Context, id string) (*provider.ContainerMetrics, error) {
	return b.inner.ContainerStats(ctx, id)
}
func (b *preflightBatch) CrewContainerName(id, slug string) string {
	return b.inner.CrewContainerName(id, slug)
}
func (b *preflightBatch) CopyToContainer(ctx context.Context, id, dst string, content io.Reader) error {
	return b.inner.CopyToContainer(ctx, id, dst, content)
}

// Flush runs everything queued as ONE exec and returns an error naming the
// steps that failed. A queue-empty flush issues no exec at all.
//
// A transport-level failure (the exec never ran) marks EVERY queued step as
// failed: the caller's fail-loud checks must not read "the credential step is
// fine" from a script that was never delivered.
func (b *preflightBatch) Flush(ctx context.Context) error {
	if b == nil || len(b.steps) == 0 {
		return nil
	}
	steps := b.steps
	b.steps = nil

	script := buildPreflightScript(steps)

	opCtx, cancel := context.WithTimeout(ctx, preflightBatchTimeout)
	defer cancel()

	b.flushes++
	res, err := b.inner.Exec(opCtx, provider.ExecConfig{
		ContainerID: b.containerID,
		// The script arrives on stdin. `sh` with no operands and a
		// non-terminal stdin reads its commands from there — so nothing,
		// including credential material, ever reaches argv.
		Cmd:   []string{"sh"},
		User:  b.user,
		Stdin: strings.NewReader(script),
	})
	if err != nil {
		for _, s := range steps {
			b.failed[s.name] = true
		}
		return fmt.Errorf("preflight batch (%d steps: %s): %w", len(steps), stepNames(steps), err)
	}
	out, _ := io.ReadAll(res.Reader)
	res.Reader.Close()

	// Delivery first: without the completion marker the script did not run to
	// the end, so nothing it "did not report" can be trusted. Marking only the
	// steps it named would be worse than useless here — it named none.
	if !strings.Contains(string(out), preflightDoneMarker) {
		for _, s := range steps {
			b.failed[s.name] = true
		}
		if b.logger != nil {
			b.logger.Error("preflight batch did not run to completion — treating every step as failed",
				"steps", len(steps), "names", stepNames(steps),
				"transcript", tailString(string(out), preflightTranscriptTail))
		}
		return fmt.Errorf("preflight batch (%d steps: %s): script was not delivered or did not complete",
			len(steps), stepNames(steps))
	}

	failed := parsePreflightFailures(string(out))
	for _, n := range failed {
		b.failed[n] = true
	}

	if b.logger != nil {
		b.logger.Debug("preflight batch flushed",
			"steps", len(steps), "failed", len(failed), "transcript", tailString(string(out), preflightTranscriptTail))
	}

	// The exit code is a second, independent signal: a script that died before
	// it could print its failure line (OOM-killed, sh parse error) still has to
	// surface as a failure rather than as a silent success.
	running, exitCode, inspectErr := b.inner.ExecInspect(ctx, res.ExecID)
	if inspectErr == nil && !running && exitCode != 0 && len(failed) == 0 {
		for _, s := range steps {
			b.failed[s.name] = true
		}
		return fmt.Errorf("preflight batch exited %d without naming a step (%d queued: %s): %s",
			exitCode, len(steps), stepNames(steps), tailString(string(out), preflightTranscriptTail))
	}
	if len(failed) > 0 {
		return fmt.Errorf("preflight batch: %d of %d steps failed (%s): %s",
			len(failed), len(steps), strings.Join(failed, ", "), tailString(string(out), preflightTranscriptTail))
	}
	return nil
}

// buildPreflightScript renders the merged script.
//
// Each step runs in its own subshell with stdin redirected from /dev/null.
// The subshell keeps a step's `cd` (or any variable it sets) from leaking into
// the next one; the redirect is what makes stdin delivery safe at all, since
// without it a step that reads stdin would swallow the remainder of the script
// `sh` has not parsed yet.
func buildPreflightScript(steps []preflightStep) string {
	var sb strings.Builder
	sb.WriteString("__cs_failed=''\n")
	for _, s := range steps {
		fmt.Fprintf(&sb, "printf '%%s\\n' '%s%s'\n", preflightStepMarker, s.name)
		sb.WriteString("(\n")
		if s.workingDir != "" {
			fmt.Fprintf(&sb, "cd '%s' || exit 1\n", shellEscape(s.workingDir))
		}
		sb.WriteString(s.script)
		sb.WriteString("\n) </dev/null 2>&1 || __cs_failed=\"${__cs_failed}")
		sb.WriteString(s.name)
		sb.WriteString(" \"\n")
	}
	// The completion marker prints BEFORE the failure branch exits, so a script
	// that ran and had failures still proves it was delivered.
	fmt.Fprintf(&sb, "printf '%%s\\n' '%s'\n", preflightDoneMarker)
	fmt.Fprintf(&sb, "if [ -n \"$__cs_failed\" ]; then printf '%%s%%s\\n' '%s' \"$__cs_failed\"; exit 1; fi\nexit 0\n",
		preflightFailMarker)
	return sb.String()
}

// parsePreflightFailures extracts the step names from the script's trailing
// failure line. Returns nil when the marker is absent (every step succeeded).
func parsePreflightFailures(out string) []string {
	i := strings.LastIndex(out, preflightFailMarker)
	if i < 0 {
		return nil
	}
	rest := out[i+len(preflightFailMarker):]
	if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
		rest = rest[:nl]
	}
	names := strings.Fields(rest)
	if len(names) == 0 {
		return nil
	}
	return names
}

// sanitisePreflightStepName reduces a name to characters that are inert both
// inside the single-quoted printf argument and inside the double-quoted
// failure accumulator. Step names carry file paths and skill slugs, so this is
// a guard on interpolation, not cosmetics.
func sanitisePreflightStepName(name string) string {
	var sb strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			sb.WriteRune(r)
		case r == '-', r == '_', r == '.', r == '/', r == ':':
			sb.WriteRune(r)
		default:
			sb.WriteByte('-')
		}
	}
	if sb.Len() == 0 {
		return "unnamed"
	}
	return sb.String()
}

func stepNames(steps []preflightStep) string {
	names := make([]string, 0, len(steps))
	for _, s := range steps {
		names = append(names, s.name)
	}
	return strings.Join(names, ", ")
}

func tailString(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return "..." + s[len(s)-n:]
}

// runOrBatch dispatches one WRITE-ONLY preflight script: appended to the
// merged script when the container handle is a batch that accepts it, executed
// on its own otherwise — byte-for-byte the pre-#1646 behaviour.
//
// Only for scripts whose stdout and exit code the caller does not read. A
// probe that reads output must keep calling container.Exec directly; the
// batch's Exec flushes first, so ordering holds either way. Batching is opt-in
// precisely so that forgetting about it is safe.
func runOrBatch(ctx context.Context, container provider.ContainerProvider, name string, cfg provider.ExecConfig) error {
	if b, ok := container.(*preflightBatch); ok && b.accepts(cfg) {
		b.add(name, cfg.WorkingDir, cfg.Cmd[2])
		return nil
	}
	result, err := container.Exec(ctx, cfg)
	if err != nil {
		return err
	}
	// Read the output BEFORE inspecting: the exec is asynchronous and the exit
	// code is only final once the stream ends. The output is also what makes a
	// failure diagnosable, so it is carried into the error rather than dropped.
	out, readErr := io.ReadAll(result.Reader)
	_ = result.Reader.Close()
	if readErr != nil {
		return fmt.Errorf("%s: reading output: %w", name, readErr)
	}

	// Exit codes used to be ignored entirely — every preflight step reported
	// success whatever happened, so a write that never landed was logged as
	// done and only surfaced later as a puzzling error from the agent itself
	// ("MCP config file not found"). A step that failed has to fail here (#1779).
	_, code, inspectErr := container.ExecInspect(ctx, result.ExecID)
	if inspectErr != nil {
		return fmt.Errorf("%s: %w", name, inspectErr)
	}
	if code != 0 {
		return fmt.Errorf("%s: exited %d: %s", name, code, strings.TrimSpace(truncateOutput(string(out))))
	}
	return nil
}

// truncateOutput caps step output carried into an error. Enough to explain the
// failure, not enough to bury the log in a stack of base64.
func truncateOutput(s string) string {
	const max = 2000
	if len(s) <= max {
		return s
	}
	return s[:max] + "… (truncated)"
}
