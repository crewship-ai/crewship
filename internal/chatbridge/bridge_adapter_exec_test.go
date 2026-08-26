package chatbridge

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/orchestrator"
)

// TestClassifyAdapterExecError is table-driven over the ways RunAgent's
// non-nil error can look once the agent CLI process itself is the thing that
// failed inside the crew container (as opposed to the container never
// starting — that's classifyCrewRuntimeError, tested separately). Each case
// names the regression it pins: the incident this whole change closes was
// exit 127 reaching the operator as "check the journal for details" while
// the one fact that explained it — the container's own "No such file or
// directory" — sat in the journal and nowhere else.
func TestClassifyAdapterExecError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		// prevents documents the symptom this case guards against, i.e. what
		// used to reach (or would reach) the operator before/without this
		// classifier.
		prevents string
		wantCode string
		// wantMsgContains are substrings the message MUST carry.
		wantMsgContains []string
		// wantMsgExcludes are substrings the message must NOT carry — used to
		// pin the governing rule that a cause we don't actually know must not
		// be claimed.
		wantMsgExcludes []string
		wantMeta        bool
	}{
		{
			name: "exit 127 with the not-found stderr names the missing binary",
			err: &orchestrator.AdapterExecError{
				Adapter:  "claude",
				Binary:   "claude",
				ExitCode: 127,
				Output:   "stdbuf: failed to run command 'claude': No such file or directory",
			},
			prevents: `the exact incident this task closes: the operator saw only ` +
				`"agent exited with code 127 — check the journal for details" while ` +
				`the container-side text that explained it (stdbuf's "No such file or ` +
				`directory") never left the container.`,
			wantCode: "adapter_missing",
			wantMsgContains: []string{
				`"claude"`,      // names the binary
				"not installed", // says what's wrong
				"not a crash",   // distinguishes from adapter_crashed
				"Reprovision",   // says what to do
			},
			wantMeta: true,
		},
		{
			name: "non-zero exit with other stderr is reported as a crash, not a missing binary",
			err: &orchestrator.AdapterExecError{
				Adapter:  "codex",
				Binary:   "codex",
				ExitCode: 1,
				Output:   "Error: rate limit exceeded for this API key",
			},
			prevents: "a real crash (the binary ran, and told us why) being flattened to " +
				"the same generic 'check the journal' sentence exit 127 got before this " +
				"classifier existed — the CLI's own explanation is right there and was " +
				"being thrown away.",
			wantCode: "adapter_crashed",
			wantMsgContains: []string{
				"exited with code 1",
				"rate limit exceeded",
			},
			wantMsgExcludes: []string{
				"not installed", // must not be misreported as a missing binary
			},
			wantMeta: true,
		},
		{
			name: "zero exit must never be reported as a failure cause",
			err: &orchestrator.AdapterExecError{
				Adapter:  "claude",
				Binary:   "claude",
				ExitCode: 0,
				Output:   "",
			},
			prevents: "the governing rule stated in the classifier's own comment: a check " +
				"that could not run (or, here, one that actually succeeded) must never be " +
				"reported as one that failed. A future caller that starts feeding exit-0 " +
				"runs through this path by mistake must get a loud 'this should not " +
				"happen', not a confident but false diagnosis.",
			wantCode: "internal",
			wantMsgContains: []string{
				"should not happen",
			},
			wantMsgExcludes: []string{
				"not installed",
				"Container output",
			},
			wantMeta: true,
		},
		{
			name: "non-zero exit with empty stderr is not upgraded into a specific claim",
			err: &orchestrator.AdapterExecError{
				Adapter:  "gemini",
				Binary:   "gemini",
				ExitCode: 137,
				Output:   "",
			},
			prevents: "the other half of the governing rule: a failure whose cause is " +
				"NOT known (no captured output at all, so we cannot say it's a missing " +
				"binary or read a real crash reason) must not be dressed up as either — " +
				"exit 137 alone must not be guessed at as OOM-kill or anything else this " +
				"function cannot actually see.",
			wantCode: "internal",
			wantMsgContains: []string{
				"exited with code 137",
				"produced no output",
			},
			wantMsgExcludes: []string{
				"not installed",
				"Container output:",
			},
			wantMeta: true,
		},
		{
			name: "exit 127 with unrelated output is not claimed as a missing binary",
			err: &orchestrator.AdapterExecError{
				Adapter:  "cursor-agent",
				Binary:   "cursor-agent",
				ExitCode: 127,
				Output:   "some unrelated line the CLI printed before dying",
			},
			prevents: "127 is treated as a strong hint, not proof — without the shell's own " +
				"'No such file or directory' (or 'command not found') text confirming it, " +
				"this must fall back to reporting what it actually captured instead of " +
				"asserting a specific binary is missing.",
			wantCode: "adapter_crashed",
			wantMsgExcludes: []string{
				"not installed",
			},
			wantMeta: true,
		},
		{
			name: "an unrelated error type is passed through unchanged",
			err:  fmt.Errorf("run aborted: agent repeated the same tool call 6 times (stuck loop)"),
			prevents: "this classifier claiming a code/metadata for failure classes it does " +
				"not own (loop-guard aborts, in-band failures, MCP injection errors, …) — " +
				"those already have their own handling upstream and must reach the chat " +
				"exactly as before.",
			wantCode: "internal",
			wantMsgContains: []string{
				"stuck loop",
			},
			wantMeta: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code, msg, meta := classifyAdapterExecError(tc.err)
			if code != tc.wantCode {
				t.Errorf("code = %q, want %q (prevents: %s)", code, tc.wantCode, tc.prevents)
			}
			for _, want := range tc.wantMsgContains {
				if !strings.Contains(msg, want) {
					t.Errorf("message %q must contain %q (prevents: %s)", msg, want, tc.prevents)
				}
			}
			for _, exclude := range tc.wantMsgExcludes {
				if strings.Contains(msg, exclude) {
					t.Errorf("message %q must NOT contain %q (prevents: %s)", msg, exclude, tc.prevents)
				}
			}
			if tc.wantMeta && meta == nil {
				t.Errorf("expected non-nil metadata for an AdapterExecError (prevents: %s)", tc.prevents)
			}
			if !tc.wantMeta && meta != nil {
				t.Errorf("expected nil metadata for a non-adapter-exec error, got %v (prevents: %s)", meta, tc.prevents)
			}
		})
	}
}

// TestClassifyAdapterExecError_MetadataCarriesContainerOutput pins the actual
// deliverable: the container's own diagnostic text reaches the ChatEvent as
// structured metadata, not only the journal. Before this change the text
// existed exactly once — in the exec.output_chunk journal entry — and a
// chat-only operator (the common case) never saw it.
func TestClassifyAdapterExecError_MetadataCarriesContainerOutput(t *testing.T) {
	err := &orchestrator.AdapterExecError{
		Adapter:  "claude",
		Binary:   "claude",
		ExitCode: 127,
		Output:   "stdbuf: failed to run command 'claude': No such file or directory",
	}
	_, _, meta := classifyAdapterExecError(err)
	if meta == nil {
		t.Fatal("expected metadata, got nil")
	}
	if got, _ := meta["container_output"].(string); !strings.Contains(got, "No such file or directory") {
		t.Errorf("meta[container_output] = %q, want it to carry the container's stderr text", got)
	}
	if got, _ := meta["exit_code"].(int); got != 127 {
		t.Errorf("meta[exit_code] = %v, want 127", got)
	}
	if got, _ := meta["adapter"].(string); got != "claude" {
		t.Errorf("meta[adapter] = %q, want %q", got, "claude")
	}
	if got, _ := meta["binary"].(string); got != "claude" {
		t.Errorf("meta[binary] = %q, want %q", got, "claude")
	}
}

// TestClassifyAdapterExecError_NilError guards the defensive nil branch: a
// caller passing a nil error (which should never happen given how the one
// production call site is gated on runErr != nil) must not panic or fabricate
// a message.
func TestClassifyAdapterExecError_NilError(t *testing.T) {
	code, msg, meta := classifyAdapterExecError(nil)
	if code != "internal" || msg != "" || meta != nil {
		t.Errorf("classifyAdapterExecError(nil) = (%q, %q, %v), want (\"internal\", \"\", nil)", code, msg, meta)
	}
}

// TestClassifyAdapterExecError_StructuralBeforeSubstring pins the ordering
// rule stated in the function's own comment: the typed check (errors.As)
// runs before any substring match, mirroring classifyCrewRuntimeError. An
// error that merely CONTAINS exit-127-shaped text in its .Error() string but
// is not an *orchestrator.AdapterExecError must not be classified as
// adapter_missing by accident.
func TestClassifyAdapterExecError_StructuralBeforeSubstring(t *testing.T) {
	// This string is deliberately crafted to look like the payload a
	// substring-only classifier would misfire on.
	err := fmt.Errorf("some wrapper: exit 127: No such file or directory: %w", errors.New("claude"))
	code, msg, meta := classifyAdapterExecError(err)
	if code != "internal" {
		t.Fatalf("code = %q, want internal (a non-typed error must never earn adapter_missing by substring luck)", code)
	}
	if meta != nil {
		t.Errorf("expected nil metadata for a non-typed error, got %v", meta)
	}
	if !strings.Contains(msg, "No such file or directory") {
		t.Errorf("fallback message should be the raw error text unchanged, got %q", msg)
	}
}
