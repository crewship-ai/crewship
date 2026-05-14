package hooks

import (
	"strings"
	"testing"
)

func TestLintShellCommand(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		cmd    string
		expect int // expected warning count
	}{
		{"properly quoted payload", `echo "$CREWSHIP_PAYLOAD" | jq .`, 0},
		{"unquoted payload", `echo $CREWSHIP_PAYLOAD`, 1},
		{"unquoted braced payload", `echo ${CREWSHIP_PAYLOAD}`, 1},
		{"properly quoted braced payload", `echo "${CREWSHIP_PAYLOAD}"`, 0},
		{"mixed — first ok, second bad", `cmd "$CREWSHIP_PAYLOAD"; cmd $CREWSHIP_EVENT`, 1},
		{"single quotes are literal — no warning", `echo '$CREWSHIP_PAYLOAD literal'`, 0},
		{"unquoted after closing double quote", `echo "ok" $CREWSHIP_PAYLOAD`, 1},
		{"no payload reference at all", `echo hello world`, 0},
		{"non-CREWSHIP var is ignored", `echo $PATH`, 0},
		{"empty command", ``, 0},
		{"only quotes", `""`, 0},
		{"backslash-escaped quote does not toggle state", `echo "\"" $CREWSHIP_PAYLOAD`, 1},
		{"two unquoted refs in one command", `echo $CREWSHIP_EVENT and $CREWSHIP_PAYLOAD`, 2},
		// Inside single-quotes the backslash is LITERAL — it does not
		// escape the closing quote. A previous version of this lint
		// applied generic backslash-skip regardless of quote state,
		// which made `'test\' $CREWSHIP_VAR` silently swallow the
		// closing quote and stay forever "inside single quotes",
		// hiding the unquoted ref that follows.
		{"backslash inside single-quotes is literal — must still close span", `'test\' $CREWSHIP_VAR`, 1},
		{"backslash inside single-quotes — multiple unquoted refs after close", `echo 'a\b' $CREWSHIP_EVENT $CREWSHIP_PAYLOAD`, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := LintShellCommand(tc.cmd)
			if len(got) != tc.expect {
				t.Errorf("LintShellCommand(%q) returned %d warnings, want %d:\n  %v", tc.cmd, len(got), tc.expect, got)
				return
			}
			// Every warning must name the offending reference.
			for _, w := range got {
				if !strings.Contains(w, "CREWSHIP_") {
					t.Errorf("warning missing var name: %q", w)
				}
			}
		})
	}
}
