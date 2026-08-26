package orchestrator

import "fmt"

// AdapterExecError is what RunAgent returns when the agent CLI process
// itself exits non-zero inside the crew container — as opposed to the
// container failing to start at all, which is EnsureCrewRuntime's error
// (classified separately by chatbridge's classifyCrewRuntimeError). It is a
// typed leaf, not a wrapper: this struct IS the cause, so it has no Unwrap.
//
// It exists so the exit code, the binary that was exec'd, and the
// container's own diagnostic text survive past this function instead of
// being collapsed into a single rendered sentence and thrown away —
// chatbridge's classifyAdapterExecError reads these fields with errors.As
// to tell "the binary is not in this image" (exit 127, "No such file or
// directory") apart from "the binary ran and crashed" (any other non-zero
// exit with output) apart from "we genuinely don't know" (non-zero exit,
// no output at all). Collapsing all three into one generic sentence — which
// is what happened before this type existed — is exactly the failure mode
// the governing rule in classifyAdapterExecError's comment names.
//
// Error() renders the same sentence RunAgent has always produced (some
// callers/tests match against that text via strings.Contains), so
// introducing this type changes nothing for a caller that only calls
// .Error(). What's new is that the structured fields are now reachable
// too, instead of being lossily re-derived from that string.
type AdapterExecError struct {
	// Adapter is the CLIAdapter name the run requested (e.g. "claude", "codex").
	Adapter string
	// Binary is argv[0] of the command actually exec'd in the container,
	// before the stdbuf/tmux wrapper (see buildExecCommand) — the name a
	// "command not found" shell error names, and the name an operator needs
	// to go check for in the crew's image.
	Binary string
	// ExitCode is the process's exit status, from ExecInspect.
	ExitCode int
	// Output is the container's captured stdout+stderr for this exec —
	// docker/apple exec return one combined stream (see
	// provider.ExecResult), so there is no separate stderr to carry —
	// already redacted by the run's own secret scrubber and capped to the
	// same 16 KB streamOutput buffers for the Crow's Nest replay snapshot.
	// Empty when the process produced no output before exiting.
	Output string
	// msg is the pre-rendered, exit-code-specific sentence (e.g. the exit
	// 123 CLI-token hint) that Error() returns verbatim. Kept private so the
	// zero value is never mistaken for a real instance — construct these
	// only via newAdapterExecError.
	msg string
}

// newAdapterExecError builds the typed error and pre-renders its Error()
// text in one place, so the wording RunAgent has always returned for each
// exit code lives next to the struct that now also carries the exit code as
// data, instead of drifting between two call sites.
func newAdapterExecError(adapter, binary string, exitCode int, output string) *AdapterExecError {
	e := &AdapterExecError{Adapter: adapter, Binary: binary, ExitCode: exitCode, Output: output}
	switch exitCode {
	case 123:
		// Claude Code specifically: exit 123 means the startup auth check
		// failed, almost always a missing or wrong-shape
		// CLAUDE_CODE_OAUTH_TOKEN. Other CLIs reuse the same code for
		// "generic exec failure"; we still surface SOMETHING actionable
		// instead of silence.
		e.msg = fmt.Sprintf( //nolint:staticcheck // ST1005: user-facing error rendered in chat / journal UI
			"agent exited with code %d — most likely a missing or invalid CLI token. "+
				"Run `claude setup-token` (or the equivalent for your adapter) and re-paste the value in Settings → Credentials.",
			exitCode,
		)
	default:
		e.msg = fmt.Sprintf("agent exited with code %d — check the journal for details", exitCode)
	}
	return e
}

func (e *AdapterExecError) Error() string {
	if e.msg != "" {
		return e.msg
	}
	return fmt.Sprintf("agent exited with code %d — check the journal for details", e.ExitCode)
}
