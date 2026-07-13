package api

import (
	"regexp"
	"strings"
)

// Free-function helpers used by the Keeper handler. Lifted out of
// keeper.go to keep that file focused on HandleRequest / HandleExecute
// / GetRequest and their tight state. Nothing in this file touches
// *KeeperHandler — pure functions + package-level regexes.

// envVarNamePattern allows only characters valid in POSIX environment variable names.
var envVarNamePattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// envVarSanitizePattern matches runs of characters that are NOT valid in the
// uppercased env-var name we derive from a credential name as a fallback.
// Used to collapse those runs into a single underscore.
var envVarSanitizePattern = regexp.MustCompile(`[^A-Z0-9]+`)

// interpreterPattern matches commands that invoke a shell or scripting
// language interpreter with inline code. An attacker can bypass the
// metachar filter by wrapping a payload inside single quotes passed
// to "bash -c '...|...'".
//
// The short-option match is intentionally permissive about clustered
// flags — `bash -lc '…'`, `sh -ec '…'`, and other combinations where a
// `c`/`e`/`E` is glommed together with login/errexit letters all
// re-parse the quoted payload, so they need to trigger the precheck
// just as surely as the bare `-c` form.
//
// Additionally, we allow any sequence of non-dangerous flags BEFORE
// the `-c`/`--eval` trigger (e.g. `bash --noprofile --norc -c '…'`,
// `python -B -c '…'`). Without this, attackers can prepend benign
// flags to push the dangerous option past the simple "first token"
// boundary and slip under a regex that demanded it be adjacent to
// the interpreter name.
//
// Each option may carry its value either attached (`--key=value`) or
// in a separate token (`--rcfile /tmp/x`, `python -W ignore -c …`,
// `bash --rcfile '/tmp/x' -c '…'`, `python -W "ignore" -c '…'`).
// The separate-token value can be a single-quoted string, a
// double-quoted string, or a bare non-flag token — a regex that
// only accepts unquoted values would let the quoted forms break the
// sequence and allow `-c` to slip past.
// The eval-flag trigger class covers -c/-E/-e (bash/python/perl/ruby),
// -p/--print and -r/--require (node evaluate/require), and long forms --eval /
// --print / --require. The interpreter alternation includes php/lua/tclsh/
// Rscript/osascript/groovy, all of which can evaluate arbitrary code and were
// previously absent. (This is still a denylist — an argv[0] allowlist would be
// stronger; tracked separately.)
var interpreterPattern = regexp.MustCompile(`(?i)\b(bash|dash|zsh|ksh|sh|python[0-9.]*|python3|perl|ruby|node|deno|bun|php|lua|tclsh|Rscript|osascript|groovy)\b(?:\s+--?[A-Za-z][A-Za-z0-9-]*(?:(?:=[^\s]+)|(?:\s+(?:'[^']*'|"[^"]*"|[^\s'"-][^\s]*)))?)*\s+(-[A-Za-z]*[cEerp][A-Za-z]*|--eval|--print|--require)\b`)

// scriptToolPattern matches tools with built-in shell execution
// capabilities.
//   - awk: system(), getline with pipe — executes arbitrary commands
//   - sed: the /e flag executes the pattern space as a shell command
//     (GNU sed)
//
// These bypass the metachar filter since payloads are inside single
// quotes.
var scriptToolPattern = regexp.MustCompile(`(?i)\b([gmnp]?awk|sed)\b`)

// containsDangerousShellChars checks if a command contains shell
// operators that could be used for credential exfiltration or command
// injection. It parses the command carefully: content inside single
// quotes is safe, everything else is checked against the dangerous
// pattern list.
func containsDangerousShellChars(cmd string) bool {
	// Reject any non-printable control characters (except space and tab which
	// are legitimate in shell commands). This catches \n, \r, vertical tab,
	// form feed, and critically Unicode line/paragraph separators (U+2028,
	// U+2029) that some shells treat as line breaks.
	for _, r := range cmd {
		if r == ' ' || r == '\t' {
			continue
		}
		// Block C0 controls (0x00–0x1F), DEL (0x7F), C1 controls (0x80–0x9F),
		// Unicode line separator (U+2028), paragraph separator (U+2029).
		if r <= 0x1F || r == 0x7F || (r >= 0x80 && r <= 0x9F) || r == 0x2028 || r == 0x2029 {
			return true
		}
	}

	// Block interpreter re-invocation: "bash -c '...|...'" bypasses the
	// single-quote-aware metachar check below because content inside quotes
	// is treated as literal, but the invoked interpreter re-parses it.
	if interpreterPattern.MatchString(cmd) {
		return true
	}

	// Block awk/gawk/nawk/mawk: awk scripts can call system() or use
	// getline with pipe, executing arbitrary shell commands within
	// single-quoted script arguments that bypass our metachar check.
	if scriptToolPattern.MatchString(cmd) {
		return true
	}

	// ANSI-C quoting ($'...') decodes backslash escapes, so $'\n' yields a
	// real newline (a command separator) that the single-quote splitter
	// below would otherwise treat as quoted-and-safe. The opening $' also
	// straddles the split boundary, so it has to be checked on the raw
	// command before splitting.
	if strings.Contains(cmd, "$'") {
		return true
	}

	// Scan the command tracking real shell quote state. A single quote is only
	// a quote-context toggle when it is NOT itself inside double quotes (or
	// backslash-escaped) — the previous strings.Split(cmd, "'") assumed every
	// ' toggles, so one literal ' inside "…" desynced the parser against the
	// shell and the entire remainder (with its ; | & etc.) was skipped as
	// "quoted". We only skip bytes genuinely inside single quotes; inside double
	// quotes the shell still expands `…`, $(…) and ${…}, so those stay flagged.
	const danger = ";|>&`"
	rs := []rune(cmd)
	var inSingle, inDouble, esc bool
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		switch {
		case esc:
			esc = false
		case inSingle:
			if c == '\'' {
				inSingle = false
			}
		case inDouble:
			switch {
			case c == '\\':
				esc = true
			case c == '"':
				inDouble = false
			case c == '`':
				return true
			case c == '$' && i+1 < len(rs) && (rs[i+1] == '(' || rs[i+1] == '{'):
				return true
			}
		default:
			switch {
			case c == '\'':
				inSingle = true
			case c == '"':
				inDouble = true
			case c == '\\':
				esc = true
			case strings.ContainsRune(danger, c):
				// ; | > & backtick — separators/redirects/background/exec.
				return true
			case c == '$' && i+1 < len(rs) && (rs[i+1] == '(' || rs[i+1] == '{'):
				return true
			case c == '<' && i+1 < len(rs) && rs[i+1] == '(':
				// <( process substitution — runs the inner command.
				return true
			case c == '<' && i+1 < len(rs) && rs[i+1] == '<':
				// << here-doc / <<< here-string — feeds data to a transform
				// tool without a pipe, defeating output scrubbing. Plain
				// `cmd < file` (single <) stays permitted, as before.
				return true
			}
		}
	}
	return false
}

// reverseString reverses a string by runes (not bytes) so multi-byte
// UTF-8 sequences are preserved.
func reverseString(s string) string {
	runes := []rune(s)
	for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
		runes[i], runes[j] = runes[j], runes[i]
	}
	return string(runes)
}

// nullIfEmpty returns nil for the empty string and a pointer to s
// otherwise. Used to serialise optional fields as JSON null.
func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
