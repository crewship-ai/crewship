package orchestrator

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The probes run in Linux containers, but their host-fixture tests also run
// on macOS, whose /bin/sh is bash 3.2. That shell cannot parse a case
// statement written inside $( ), and a parse error fails the WHOLE probe with
// exit 2 — including managed and legacy paths that never reach tmux.
func TestRunProbesKeepCaseOutOfCommandSubstitution(t *testing.T) {
	loc := RunLocation{ContainerID: "c", AgentSlug: "fixture", RunID: "run_portable"}
	o := New(&mockContainer{}, newMemState(), covQuietLogger())
	probes := map[string]string{
		"alive": directRunProbe(loc.RunID, false) + o.runTmuxSessionProbe(loc),
		"stop":  directRunProbe(loc.RunID, true) + o.runTmuxSessionProbe(loc),
	}
	for name, probe := range probes {
		for i := strings.Index(probe, "$("); i >= 0; {
			if body := substitutionBody(probe[i+2:]); strings.Contains(body, "case ") {
				t.Fatalf("%s probe: a case statement sits inside $( ): %q", name, body)
			}
			next := strings.Index(probe[i+2:], "$(")
			if next < 0 {
				break
			}
			i += 2 + next
		}
	}
}

// Without /proc or a pane identity the no-server branch has no proof: it
// must answer UNKNOWN and exit cleanly on any POSIX shell, never ABSENT and
// never a shell error.
func TestTmuxNoServerWithoutProcIsUnknown(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte("#!/bin/sh\necho \"no server running on /tmp/tmux-1001/default\" >&2\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "absent")
	for name, pane := range map[string]string{"no pane identity": filepath.Join(missing, tmuxPaneIdentityName), "pane identity disabled": ""} {
		t.Run(name, func(t *testing.T) {
			cmd := exec.CommandContext(t.Context(), "/bin/sh", "-c", tmuxSessionProbeAt("crewship-fixture-run_x", pane, filepath.Join(missing, "proc")))
			cmd.Env = []string{"PATH=" + bin + ":/usr/bin:/bin"}
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("probe failed instead of answering: %v", err)
			}
			if got := strings.TrimSpace(string(out)); got != "UNKNOWN" {
				t.Fatalf("probe printed %q without /proc, want UNKNOWN", got)
			}
		})
	}
}

// substitutionBody returns the text up to the ")" closing a "$(", honouring
// nested parentheses and single/double quotes.
func substitutionBody(s string) string {
	depth, quote := 1, byte(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
		case c == '(':
			depth++
		case c == ')':
			depth--
			if depth == 0 {
				return s[:i]
			}
		}
	}
	return s
}
