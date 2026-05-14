package hooks

import (
	"fmt"
	"strings"
)

// LintShellCommand returns human-readable warnings about a shell hook
// command. The dominant gotcha is referencing $CREWSHIP_PAYLOAD (or any
// CREWSHIP_* env var) without surrounding double-quotes — agent-controlled
// JSON in the payload can carry shell metacharacters that `sh -c` will
// gladly interpret if the reference is unquoted. The shell handler caps
// payload size to limit blast radius but cannot rescue an unquoted
// substitution: documented at the top of shell.go (audit H6).
//
// The walk is character-by-character so quote state (single vs double) is
// tracked accurately. `$VAR` and `${VAR}` are both recognized. Single
// quotes are treated as safe even when un-double-quoted because the
// shell does not expand $VAR inside single quotes — the literal text
// reaches the script as-is.
//
// Always non-blocking — Register() logs the warnings; callers building
// UI surfaces can also call LintShellCommand directly and surface them
// to the user before persisting.
func LintShellCommand(cmd string) []string {
	var warnings []string
	inDouble := false
	inSingle := false
	for i := 0; i < len(cmd); i++ {
		c := cmd[i]
		switch {
		case c == '\\' && i+1 < len(cmd) && !inSingle:
			// Outside single-quotes a backslash escapes the next char,
			// so skip it to avoid e.g. `\"` flipping the double-quote
			// state. Inside single-quotes (POSIX shell + bash)
			// backslash is LITERAL — it does not escape the closing
			// quote — so we fall through to the default case and let
			// the next iteration see a real `'` that closes the span.
			// Without the !inSingle guard `'test\' $CREWSHIP_VAR`
			// would silently swallow the closing quote and miss every
			// unquoted CREWSHIP_* reference that follows.
			i++
		case c == '\'' && !inDouble:
			inSingle = !inSingle
		case c == '"' && !inSingle:
			inDouble = !inDouble
		case c == '$' && !inSingle:
			j := i + 1
			braced := false
			if j < len(cmd) && cmd[j] == '{' {
				braced = true
				j++
			}
			nameStart := j
			for j < len(cmd) && (cmd[j] == '_' ||
				(cmd[j] >= 'A' && cmd[j] <= 'Z') ||
				(cmd[j] >= 'a' && cmd[j] <= 'z') ||
				(cmd[j] >= '0' && cmd[j] <= '9')) {
				j++
			}
			name := cmd[nameStart:j]
			if braced && j < len(cmd) && cmd[j] == '}' {
				j++
			}
			if strings.HasPrefix(name, "CREWSHIP_") && !inDouble {
				warnings = append(warnings, fmt.Sprintf(
					"unquoted shell reference %q — agent-controlled "+
						"payload bytes can contain metacharacters; wrap "+
						"every CREWSHIP_* env reference in double quotes "+
						"(e.g. \"$%s\")",
					cmd[i:j], name,
				))
			}
			// Skip past the variable we just consumed. The for-loop's
			// post-increment will advance to j.
			i = j - 1
		}
	}
	return warnings
}
