package cli

import (
	"strings"
	"testing"
)

func TestIsPipelineRunID(t *testing.T) {
	t.Parallel()
	cases := []struct {
		id   string
		want bool
	}{
		// Routine-run namespace (#1193).
		{"run_cmrm3xxzk0083de436e64", true},
		{"prn_cmrm3xxzk0083de436e64", true},
		{"  run_cmrm3xxzk0083de436e64  ", true},
		// Agent-run namespace — must stay untouched.
		{"msg_1783864822_0f1e2d3c4b5a6978", false},
		{"r_cmrm3xxzk0083de436e64", false},
		// Not run ids at all.
		{"", false},
		{"runner", false},
		{"prompt_x", false},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			t.Parallel()
			if got := IsPipelineRunID(tc.id); got != tc.want {
				t.Errorf("IsPipelineRunID(%q) = %v, want %v", tc.id, got, tc.want)
			}
		})
	}
}

// TestPipelineRunIDError_ExitContract pins the exit code. These commands
// already exit 3 for an unknown run; the wrong-namespace hint must not
// silently change that out from under a script.
func TestPipelineRunIDError_ExitContract(t *testing.T) {
	t.Parallel()
	err := PipelineRunIDError("run_abc", "crewship routine logs run_abc")
	if err == nil {
		t.Fatal("expected an error")
	}
	if code := ExitCodeFor(err); code != ExitNotFound {
		t.Errorf("ExitCodeFor(%v) = %d, want %d (ExitNotFound)", err, code, ExitNotFound)
	}
	msg := err.Error()
	for _, want := range []string{"run_abc", "routine", "msg_", "crewship routine logs run_abc"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message should mention %q, got: %s", want, msg)
		}
	}
}
