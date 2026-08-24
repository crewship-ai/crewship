package orchestrator

// Table-driven tests for AdapterExecError: the typed error RunAgent now
// returns instead of a plain fmt.Errorf when the agent CLI process exits
// non-zero inside the crew container. Each case pins a symptom this type's
// introduction was meant to close — see adapter_exec_error.go's doc comment
// and chatbridge.classifyAdapterExecError, which reads these fields with
// errors.As.

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestRunAgent_AdapterExecError(t *testing.T) {
	tests := []struct {
		name string
		opts covRunOpts
		// prevents documents what reaching the operator looked like before
		// this field/type existed.
		prevents      string
		wantExitCode  int
		wantOutputHas string // substring the captured Output must contain ("" skips the check)
		wantErrMsgHas string // substring RunAgent's returned .Error() must contain
	}{
		{
			name: "exit 127 with the not-found stderr",
			opts: covRunOpts{
				agentExit: 127,
				stream:    "stdbuf: failed to run command 'claude': No such file or directory\n",
			},
			prevents: "the reported incident verbatim: the operator saw only " +
				"'agent exited with code 127 — check the journal for details' " +
				"in the chat/server log, while the one line that explained it " +
				"(this stdbuf stderr) reached nowhere outside the container's " +
				"own exec stream.",
			wantExitCode:  127,
			wantOutputHas: "No such file or directory",
			wantErrMsgHas: "agent exited with code 127",
		},
		{
			name: "a non-zero exit with other stderr",
			opts: covRunOpts{
				agentExit: 1,
				stream:    "Error: something the CLI printed on its way out\n",
			},
			prevents: "a real, informative crash reason being discarded the same way " +
				"the 127 case was — Output must carry it so a caller can do better " +
				"than 'check the journal'.",
			wantExitCode:  1,
			wantOutputHas: "something the CLI printed",
			wantErrMsgHas: "agent exited with code 1",
		},
		{
			name: "a zero exit produces no AdapterExecError at all",
			opts: covRunOpts{
				agentExit: 0,
				stream:    "{}\n",
			},
			prevents: "a successful run being wrapped in a failure type just because the " +
				"code path exists — RunAgent must return nil, not an AdapterExecError " +
				"with ExitCode 0, on a clean exit.",
			wantExitCode: -1, // sentinel: -1 means "expect no AdapterExecError / nil err"
		},
		{
			name: "a non-zero exit with empty stderr",
			opts: covRunOpts{
				agentExit: 137,
				stream:    "",
			},
			prevents: "fabricating container output that was never produced — Output must " +
				"come back empty rather than, say, a stray newline or placeholder text " +
				"that a caller might mistake for a real diagnostic.",
			wantExitCode:  137,
			wantOutputHas: "",
			wantErrMsgHas: "agent exited with code 137",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st := newMemState()
			o := New(covNewRunContainer(tc.opts), st, covQuietLogger())
			err := o.RunAgent(context.Background(), covRunReq(), nil)

			if tc.wantExitCode == -1 {
				if err != nil {
					t.Fatalf("expected nil error for a zero exit, got %v", err)
				}
				return
			}

			if err == nil {
				t.Fatal("expected a non-nil error for a non-zero agent exit")
			}
			var execErr *AdapterExecError
			if !errors.As(err, &execErr) {
				t.Fatalf("expected RunAgent's error to be an *AdapterExecError (or wrap one), got %T: %v (prevents: %s)", err, err, tc.prevents)
			}
			if execErr.ExitCode != tc.wantExitCode {
				t.Errorf("ExitCode = %d, want %d", execErr.ExitCode, tc.wantExitCode)
			}
			if execErr.Adapter == "" {
				t.Error("Adapter must be set so the classifier can name which CLI failed")
			}
			if execErr.Binary != "claude" {
				t.Errorf("Binary = %q, want %q (argv[0] of the command actually exec'd)", execErr.Binary, "claude")
			}
			if tc.wantOutputHas != "" && !strings.Contains(execErr.Output, tc.wantOutputHas) {
				t.Errorf("Output = %q, want it to contain %q (prevents: %s)", execErr.Output, tc.wantOutputHas, tc.prevents)
			}
			if tc.wantOutputHas == "" && execErr.Output != "" {
				t.Errorf("Output = %q, want empty (prevents: %s)", execErr.Output, tc.prevents)
			}
			if tc.wantErrMsgHas != "" && !strings.Contains(err.Error(), tc.wantErrMsgHas) {
				t.Errorf("err.Error() = %q, want it to contain %q", err.Error(), tc.wantErrMsgHas)
			}

			if got := covRunStatus(t, st, "chat1"); got != "error" {
				t.Errorf("run status = %q, want error", got)
			}
		})
	}
}
