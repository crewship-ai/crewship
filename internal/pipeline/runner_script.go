package pipeline

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/scrubber"
)

// scriptAuditScrubber redacts secrets from the journaled command line —
// rendered args can carry a templated credential ({{ inputs.token }}), and the
// journal is broadcast to workspace members. Same pattern as the agent-span
// detail scrubber (internal/orchestrator/agent_span.go).
var scriptAuditScrubber = scrubber.New()

// ScriptRunner execs a bundled script inside the crew's own container —
// the same hardened sandbox (non-root 1001, cap-drop ALL, no-new-privileges,
// read-only rootfs) the crew's agents already run in. It is the deterministic,
// token-zero backbone of a routine: heavy mechanical work (parse a statement,
// reconcile, verify) runs as a real program, not an LLM turn.
//
// Production wiring installs the OrchestratorRunner (which already resolves
// the author crew's container via EnsureCrewRuntime and holds the
// ContainerProvider). Nil = script steps return a clear "not configured"
// error rather than silently succeeding.
type ScriptRunner interface {
	RunScript(ctx context.Context, req ScriptRunRequest) (ScriptRunResult, error)
}

// ScriptRunRequest is the input to ScriptRunner.RunScript. Path is the
// absolute in-container path (already resolved + safety-checked under the
// crew shared root); Interpreter + Args + Path are assembled into an argv
// (no shell), so callers cannot inject via arguments.
type ScriptRunRequest struct {
	WorkspaceID  string
	AuthorCrewID string
	Interpreter  string   // e.g. "python3", "bash", "node" (may be multi-token, e.g. "go run")
	Path         string   // absolute, under /crew/shared
	Args         []string // rendered, passed as argv after the script path
	Env          map[string]string
	TimeoutSec   int
	MaxBytes     int
	// Provenance for the runner's own audit/journal.
	PipelineRunID string
	StepID        string
}

// ScriptRunResult is the outcome of a script exec. Stdout becomes the step's
// downstream output; a non-zero ExitCode fails the step; Stderr is surfaced
// in the error + audit but does not propagate downstream.
type ScriptRunResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// isProxyEnvKey reports whether an environment variable name would be read as
// proxy configuration by something a script step can run. A step may not set
// one: the crew egress allowlist is enforced by the sidecar the platform points
// these at, so a step that could redefine them could redefine its own fence.
//
// Matching an exact list of names is not enough. CPython's
// urllib.request.getproxies_environment() LOWERCASES every variable name before
// looking for a `_proxy` suffix, so `HtTp_PrOxY` reaches Python's http proxy
// setting even though nothing else on the box reads that name — and `.py` is a
// first-class script interpreter here. curl likewise honours ALL_PROXY/all_proxy
// for protocols the specific vars don't cover. So the rule is the shape, not the
// spelling: any case of `*_proxy`, plus no_proxy itself.
//
// Nothing legitimate needs one of these in script.env — egress configuration is
// the platform's, not the routine's.
func isProxyEnvKey(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, "_proxy") || lower == "no_proxy"
}

// scriptSharedRoot is the crew shared mount where bundled scripts live and
// the only tree a script step may execute from. Anchoring here (and rejecting
// traversal) keeps a routine from execing an arbitrary host path.
const scriptSharedRoot = "/crew/shared/"

// scriptInterpreterByExt infers the interpreter from a script's extension so
// authors (and AI authors) can omit it in the common case. Explicit
// script.interpreter always wins.
var scriptInterpreterByExt = map[string]string{
	".py":   "python3",
	".sh":   "bash",
	".bash": "bash",
	".js":   "node",
	".mjs":  "node",
	".cjs":  "node",
	".ts":   "node",
	".rb":   "ruby",
	".pl":   "perl",
	".php":  "php",
	".go":   "go run",
}

// ScriptInterpreterExtensions returns a copy of the extension→interpreter
// inference table for `type: script` steps. Exported for the capabilities
// dump so an AI author knows which interpreters a script step resolves to (and
// can omit `script.interpreter` for a known extension). A copy — callers must
// not mutate the internal table.
func ScriptInterpreterExtensions() map[string]string {
	out := make(map[string]string, len(scriptInterpreterByExt))
	for ext, interp := range scriptInterpreterByExt {
		out[ext] = interp
	}
	return out
}

// resolveScriptPath cleans a declared path and anchors it under the crew
// shared root, rejecting traversal or absolute paths that escape the root.
// Returns the absolute in-container path.
func resolveScriptPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", fmt.Errorf("missing path")
	}
	var abs string
	if strings.HasPrefix(p, "/") {
		abs = path.Clean(p)
	} else {
		abs = path.Clean(scriptSharedRoot + p)
	}
	// Must live strictly under the shared root — never the root itself,
	// never a sibling of /crew, never / (traversal via "..").
	if !strings.HasPrefix(abs, scriptSharedRoot) {
		return "", fmt.Errorf("path %q must resolve under %s (no traversal, no absolute paths outside the crew shared dir)", p, scriptSharedRoot)
	}
	return abs, nil
}

// resolveInterpreter returns the explicit interpreter, or infers one from the
// script's extension. An unknown extension with no explicit interpreter is an
// author-time error (better than a cryptic exec failure at run time).
func resolveInterpreter(explicit, absPath string) (string, error) {
	if strings.TrimSpace(explicit) != "" {
		return strings.TrimSpace(explicit), nil
	}
	ext := strings.ToLower(path.Ext(absPath))
	if interp, ok := scriptInterpreterByExt[ext]; ok {
		return interp, nil
	}
	return "", fmt.Errorf("cannot infer interpreter for %q — set script.interpreter explicitly (e.g. python3, bash, node)", absPath)
}

// runScriptStep handles a StepScript by delegating to the wired ScriptRunner,
// which execs the bundled script in the crew container. Deterministic and
// token-zero — no LLM in the loop.
//
// Inputs flow two ways, mirroring code steps: every declared pipeline input
// becomes CREWSHIP_INPUT_<NAME_UPPER>, and script.args / script.env values are
// template-substituted ({{ inputs.x }} / {{ steps.y.output }}) exactly like an
// agent_run prompt. Stdout is the step output; ExitCode != 0 fails the step.
func (e *Executor) runScriptStep(ctx context.Context, step Step, parentRender RenderContext, in RunInput, runID string) (out string, cost float64, dur int64, err error) {
	stepStart := time.Now()
	if step.Script == nil {
		return "", 0, 0, fmt.Errorf("script step %q missing body", step.ID)
	}
	// Resolve {{ secrets.<type> }} into the render context and load the
	// scrubber; the deferred scrub strips the resolved value out of the
	// step output and any error before either leaves this runner.
	var secrets *secretScrub
	parentRender, secrets = e.resolveStepSecrets(ctx, step, parentRender, in)
	defer func() { out, err = secrets.scrub(out), secrets.scrubErr(err) }()
	if e.scriptRunner == nil {
		// Mirrors the code-step wiring guard: a silent no-op would let a
		// "script" step falsely succeed. Production wiring installs the
		// runner via WithScriptRunner (executor_factory.go).
		return "", 0, 0, fmt.Errorf("script step %q: no ScriptRunner wired (production wiring missing) — "+
			"scripts exec in the crew container; the executor must be built with WithScriptRunner "+
			"(see docs/manifest/routine.md `Script steps`)", step.ID)
	}

	abs, err := resolveScriptPath(step.Script.Path)
	if err != nil {
		return "", 0, 0, fmt.Errorf("script step %q: %w", step.ID, err)
	}
	interp, err := resolveInterpreter(step.Script.Interpreter, abs)
	if err != nil {
		return "", 0, 0, fmt.Errorf("script step %q: %w", step.ID, err)
	}

	// Render args ({{ inputs.x }}) — passed as argv, never through a shell.
	args := make([]string, len(step.Script.Args))
	for i, a := range step.Script.Args {
		args[i] = Render(a, parentRender)
	}

	// Env: declared inputs → CREWSHIP_INPUT_*, plus explicit (rendered) env.
	// Fresh map so the script gets only what we promised — no orchestrator leak.
	envIn := make(map[string]string, len(parentRender.Inputs)+len(step.Script.Env))
	for k, v := range parentRender.Inputs {
		envIn["CREWSHIP_INPUT_"+strings.ToUpper(k)] = stringify(v)
	}
	for k, v := range step.Script.Env {
		envIn[k] = Render(v, parentRender)
	}

	timeoutSec := step.TimeoutSec
	if timeoutSec <= 0 {
		timeoutSec = 300
	}

	res, runErr := e.scriptRunner.RunScript(ctx, ScriptRunRequest{
		WorkspaceID:   in.WorkspaceID,
		AuthorCrewID:  in.AuthorCrewID,
		Interpreter:   interp,
		Path:          abs,
		Args:          args,
		Env:           envIn,
		TimeoutSec:    timeoutSec,
		MaxBytes:      1_000_000, // 1 MB stdout cap; matches HTTP/code step default
		PipelineRunID: runID,
		StepID:        step.ID,
	})
	dur = time.Since(stepStart).Milliseconds()

	// Audit: record WHAT ran (argv), its exit code + duration, as an
	// exec.command journal entry keyed to the run — a durable, post-hoc trail
	// of every script a routine executed. Best-effort; never fails the step.
	// When the runner itself errored the process outcome is unknown — report
	// exit_code -1 rather than a false 0 ("ran clean") for an exec that may
	// never have started.
	exitForAudit := res.ExitCode
	if runErr != nil && exitForAudit == 0 {
		exitForAudit = -1
	}
	e.emitScriptAudit(ctx, in, runID, step.ID, interp, abs, args, exitForAudit, dur, runErr, secrets)

	if runErr != nil {
		return res.Stdout, 0, dur, fmt.Errorf("script step %q: %w (stderr: %s)", step.ID, runErr, truncateForGraderLog(res.Stderr))
	}
	if res.ExitCode != 0 {
		return res.Stdout, 0, dur, fmt.Errorf("script step %q exit code %d (stderr: %s)", step.ID, res.ExitCode, truncateForGraderLog(res.Stderr))
	}
	return res.Stdout, 0, dur, nil
}

// emitScriptAudit writes an exec.command journal entry recording the exact
// argv, exit code, and duration of a script step. This is the "complete audit"
// half of first-class scripts: the run tree already carries the step + its
// stdout output; this entry captures the command itself for post-hoc review.
func (e *Executor) emitScriptAudit(ctx context.Context, in RunInput, runID, stepID, interp, absPath string, args []string, exitCode int, durMs int64, runErr error, secrets *secretScrub) {
	argv := append(append([]string{}, strings.Fields(interp)...), append([]string{absPath}, args...)...)
	// Rendered args can carry a templated secret — scrub before the command
	// line is persisted / broadcast. Two layers: the shape-based auditor
	// (catches known token formats in {{ inputs.* }} data) and the exact
	// {{ secrets.* }} value scrub (catches an opaque vault value the regex
	// set would miss).
	command := secrets.scrub(scriptAuditScrubber.Scrub(strings.Join(argv, " ")))
	payload := map[string]any{
		"run_id":      runID,
		"step_id":     stepID,
		"kind":        "script",
		"command":     command,
		"interpreter": interp,
		"path":        absPath,
		"exit_code":   exitCode,
		"duration_ms": durMs,
	}
	if runErr != nil {
		payload["error"] = secrets.scrub(scriptAuditScrubber.Scrub(runErr.Error()))
	}
	_, _ = ensureEmitter(e.emitter).Emit(ctx, journal.Entry{
		WorkspaceID: in.WorkspaceID,
		CrewID:      in.AuthorCrewID,
		Type:        journal.EntryExecCommand,
		ActorType:   journal.ActorSystem,
		ActorID:     runID,
		Summary:     fmt.Sprintf("script step %s: %s", stepID, command),
		TraceID:     runID,
		Payload:     payload,
	})
}

// RunScript implements ScriptRunner by exec'ing a bundled script inside the
// author crew's container — the same hardened sandbox the crew's agents run
// in. OrchestratorRunner already holds the ContainerProvider and (optionally)
// a crew-runtime resolver, so a cold crew launches from its PROVISIONED image
// (which carries the interpreters) rather than the bare base.
//
// stdout stays clean (it becomes the step's downstream output — e.g. strict
// JSON): the interpreter+path+args are passed as positional args to a tiny
// `sh -c` wrapper that redirects stderr to a per-step file, so (a) args can
// never inject (they are never interpolated into the shell string) and (b)
// stderr never contaminates the JSON payload. On failure the stderr file is
// read back for diagnostics.
func (r *OrchestratorRunner) RunScript(ctx context.Context, req ScriptRunRequest) (ScriptRunResult, error) {
	if r.container == nil {
		return ScriptRunResult{}, fmt.Errorf("script runner: container provider not configured")
	}

	// Resolve the crew's provisioned config so a cold container has the
	// interpreters; fall back to a minimal {ID} (reuses a warm container).
	cfg := provider.CrewConfig{ID: req.AuthorCrewID}
	if r.crewRuntime != nil {
		resolved, err := r.crewRuntime(ctx, req.AuthorCrewID, req.WorkspaceID)
		if err != nil {
			return ScriptRunResult{}, fmt.Errorf("script runner: resolve crew runtime for %s: %w", req.AuthorCrewID, err)
		}
		cfg = resolved
	}
	containerID, cfg, err := r.startCrew(ctx, cfg, req.WorkspaceID)
	if err != nil {
		return ScriptRunResult{}, fmt.Errorf("script runner: ensure container: %w", err)
	}

	// #1662: a script step woke the container and registered nothing — no
	// TTL, no stats, no port scanner — so it ran until crewshipd restarted.
	// The hold matters as much as the registration: the script execs INSIDE
	// this container, so a TTL sweep landing mid-step would stop the runtime
	// out from under it and the step would surface as an unattributable exec
	// failure.
	if r.orch != nil {
		r.orch.NoteCrewActivity(cfg.ID, containerID, cfg.TTLHours)
		releaseHold := r.orch.RetainCrewContainer(cfg.ID, containerID)
		defer releaseHold()
		r.orch.RegisterStatsContainer(containerID, cfg.ID, req.WorkspaceID)

		// The LIVENESS half of #1473. The exec below carries
		// orchestrator.SidecarProxyEnv, so the crew egress allowlist applies to
		// this step — but only crewship-sidecar enforces it, and until now the
		// ONLY thing that started one was an agent run (ensureSidecar needs an
		// *AgentRunRequest). startCrew brings the crew's declared SERVICE
		// sidecars up, never that one. So on a crew no agent run had warmed,
		// every script step that touched the network died on
		//
		//	Failed to connect to 127.0.0.1 port 9119: Couldn't connect to server
		//
		// and self-healed the moment anything ran an agent there — which is why
		// it read as flaky rather than as a missing start. Deterministic on a
		// fresh seed: ci-nightly-triage's `probe` script step runs before its
		// `triage` agent step, so it failed on every fresh install.
		//
		// EnsureCrewSidecar is the crew-level door onto the same
		// start / healthy-reuse / policy-change-restart sequence (and the same
		// #1220 per-container lifecycle lock) the agent path uses. It gets the
		// crew's full network policy and no credentials — see its doc for
		// exactly what a crew-level start can and cannot supply.
		//
		// Hard failure on purpose: the proxy env is injected unconditionally, so
		// carrying on would run the user's script against a dead proxy and
		// surface as an unattributable connection-refused from inside their own
		// code. It is a no-op when the sidecar is disabled instance-wide.
		if err := r.orch.EnsureCrewSidecar(ctx, orchestrator.CrewSidecarSpec{
			CrewID:                cfg.ID,
			WorkspaceID:           req.WorkspaceID,
			ContainerID:           containerID,
			NetworkMode:           cfg.NetworkMode,
			AllowedDomains:        cfg.AllowedDomains,
			AllowPrivateEndpoints: cfg.AllowPrivateEndpoints,
		}); err != nil {
			return ScriptRunResult{}, fmt.Errorf("script runner: %w", err)
		}
	}

	maxBytes := req.MaxBytes
	if maxBytes <= 0 {
		maxBytes = 1_000_000
	}

	// argv = [interpreter tokens...] + script path + rendered args.
	argv := append(strings.Fields(req.Interpreter), append([]string{req.Path}, req.Args...)...)

	// Each invocation owns a process group and private control directory.
	// Foreach items/retries cannot overwrite a sibling's PID or stderr.
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return ScriptRunResult{}, err
	}
	controlDir := "/tmp/crewship-script-" + hex.EncodeToString(nonce[:])
	stderrFile := controlDir + "/stderr"
	cmd := append([]string{"setsid", "--wait", "sh", "-c", scriptProcessWrapper, "crewship-script", controlDir}, argv...)

	// #1473: a script step runs inside the agent container but used to build
	// its environment from the step's own inputs alone, so it carried no
	// proxy — and the crew egress allowlist is enforced by the sidecar proxy.
	// A `restricted` crew allowlisted to one host reached the open internet
	// from a script step with a plain curl. Not a bypass of the fence; it
	// never met it. Sourced from the orchestrator so this path and the
	// agent-process path cannot drift apart again — that divergence is the bug.
	proxyEnv := orchestrator.SidecarProxyEnv()
	// A step must not be able to switch off the fence that constrains it, so
	// drop any proxy variable the caller declared before appending ours.
	// Relying on "the last duplicate wins" would work on Docker and silently
	// not on a provider that resolves duplicates the other way — the guarantee
	// has to hold at this layer, not in the runtime's env parser.
	//
	// Capacity hinted from the caller's map alone. Adding len(proxyEnv) trips
	// CodeQL's allocation-size-overflow rule, and the six extra entries are not
	// worth a suppression comment for a growth the runtime handles.
	env := make([]string, 0, len(req.Env))
	for k, v := range req.Env {
		if isProxyEnvKey(k) {
			r.logger.Warn("script step declared a proxy env var — ignoring, the crew egress fence is not step-configurable",
				"key", k, "run_id", req.PipelineRunID, "step_id", req.StepID)
			continue
		}
		env = append(env, k+"="+v)
	}
	env = append(env, proxyEnv...)

	// Enforce the step timeout. The exec attach read below does NOT observe
	// ctx on its own (a hijacked Docker stream blocks until EOF), so a
	// watcher closes the reader when the deadline fires — otherwise a hung
	// script (infinite loop, blocked read) would park this executor
	// goroutine forever.
	execCtx := ctx
	if req.TimeoutSec > 0 {
		var cancel context.CancelFunc
		execCtx, cancel = context.WithTimeout(ctx, time.Duration(req.TimeoutSec)*time.Second)
		defer cancel()
	}

	execRes, err := r.container.Exec(execCtx, provider.ExecConfig{
		ContainerID: containerID,
		Cmd:         cmd,
		Env:         env,
		User:        "1001:1001",
	})
	if err != nil {
		return ScriptRunResult{}, fmt.Errorf("script runner: exec: %w", err)
	}

	readDone := make(chan struct{})
	watcherDone := make(chan error, 1)
	go func() {
		select {
		case <-execCtx.Done():
			_ = execRes.Reader.Close()
			stopErr := r.stopScriptProcess(ctx, containerID, controlDir)
			watcherDone <- stopErr
		case <-readDone:
			watcherDone <- nil
		}
	}()
	stdout, readErr := io.ReadAll(io.LimitReader(execRes.Reader, int64(maxBytes)+1))
	close(readDone)
	_ = execRes.Reader.Close()
	stopErr := <-watcherDone
	if execCtx.Err() != nil || readErr != nil || len(stdout) > maxBytes {
		if stopErr == nil {
			stopErr = r.stopScriptProcess(ctx, containerID, controlDir)
		}
		reason := "script output read failed"
		if execCtx.Err() != nil {
			reason = "script cancelled or timed out"
		}
		if len(stdout) > maxBytes {
			stdout = stdout[:maxBytes]
			reason = "script output exceeded limit"
		}
		if stopErr != nil {
			return ScriptRunResult{Stdout: string(stdout), ExitCode: -1}, fmt.Errorf("%s; process termination could not be confirmed: %w", reason, stopErr)
		}
		return ScriptRunResult{Stdout: string(stdout), ExitCode: -1}, fmt.Errorf("%s; process group stopped", reason)
	}

	running, exitCode, inspErr := r.container.ExecInspect(execCtx, execRes.ExecID)
	if inspErr != nil {
		// Unknown outcome must NOT read as success — a failed inspect would
		// otherwise default exitCode to 0 and falsely mark the step COMPLETED.
		return ScriptRunResult{Stdout: string(stdout), ExitCode: -1},
			errors.Join(fmt.Errorf("script runner: exec inspect failed (outcome unknown, refusing to assume exit 0): %w", inspErr), r.stopScriptProcess(ctx, containerID, controlDir))
	}
	if running {
		return ScriptRunResult{Stdout: string(stdout), ExitCode: -1},
			errors.Join(fmt.Errorf("script runner: process still reported running after stream EOF — outcome unknown"), r.stopScriptProcess(ctx, containerID, controlDir))
	}

	res := ScriptRunResult{Stdout: string(stdout), ExitCode: exitCode}

	// On failure, surface stderr (and clean the temp file). Happy path leaves
	// control directory in tmpfs until the container recycles. Every invocation
	// owns a distinct directory, including concurrent items and retries.
	if exitCode != 0 {
		if errRes, e := r.container.Exec(ctx, provider.ExecConfig{
			ContainerID: containerID,
			Cmd:         []string{"sh", "-c", "cat " + stderrFile + " 2>/dev/null; rm -f " + stderrFile},
			User:        "1001:1001",
		}); e == nil {
			stderrOut, _ := io.ReadAll(io.LimitReader(errRes.Reader, 64*1024))
			_ = errRes.Reader.Close()
			res.Stderr = string(stderrOut)
		}
	}
	return res, nil
}

// Arguments stay positional: neither paths nor user arguments become shell code.
const scriptProcessWrapper = `original_umask=$(umask)
umask 077
control=$1
shift
mkdir "$control" || exit 125
printf '%s' "$$" > "$control/pid"
umask "$original_umask"
"$@" 2>"$control/stderr"
`

const scriptStopWrapper = `control=$1
n=0
while [ ! -s "$control/pid" ] && [ "$n" -lt 20 ]; do sleep 0.05; n=$((n+1)); done
[ -s "$control/pid" ] || exit 1
read -r pid < "$control/pid" || :
case "$pid" in ''|*[!0-9]*) exit 1;; esac
/bin/kill -KILL -- "-$pid" 2>/dev/null || :
n=0
while [ "$n" -lt 20 ]; do
  live=$(ps -eo pgid=,stat= | awk -v group="$pid" '$1 == group && $2 !~ /^Z/ { print $1 }')
  [ -z "$live" ] && exit 0
  sleep 0.05
  n=$((n+1))
done
exit 1
`

func (r *OrchestratorRunner) stopScriptProcess(ctx context.Context, containerID, controlDir string) error {
	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	res, err := r.container.Exec(stopCtx, provider.ExecConfig{ContainerID: containerID, Cmd: []string{"sh", "-c", scriptStopWrapper, "crewship-script-stop", controlDir}, User: "1001:1001"})
	if err != nil {
		return err
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-stopCtx.Done():
			_ = res.Reader.Close()
		case <-done:
		}
	}()
	_, readErr := io.Copy(io.Discard, io.LimitReader(res.Reader, 4096))
	close(done)
	_ = res.Reader.Close()
	if readErr != nil {
		return readErr
	}
	running, code, err := r.container.ExecInspect(stopCtx, res.ExecID)
	if err != nil {
		return err
	}
	if running || code != 0 {
		return fmt.Errorf("stop process group: exit %d, running %t", code, running)
	}
	return nil
}
