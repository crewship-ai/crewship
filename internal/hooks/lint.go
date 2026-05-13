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
		case c == '\\' && i+1 < len(cmd):
			// Skip the escaped char so e.g. \" doesn't toggle the
			// double-quote state. We don't try to model bash escaping
			// inside single-quotes (where backslash is literal) because
			// the variable check below is short-circuited by inSingle.
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
