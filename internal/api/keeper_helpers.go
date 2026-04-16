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
var interpreterPattern = regexp.MustCompile(`(?i)\b(bash|dash|zsh|ksh|sh|python[0-9.]*|python3|perl|ruby|node|deno|bun)\b(?:\s+--?[A-Za-z][A-Za-z0-9-]*(?:(?:=[^\s]+)|(?:\s+(?:'[^']*'|"[^"]*"|[^\s'"-][^\s]*)))?)*\s+(-[A-Za-z]*[cEe][A-Za-z]*|--eval)\b`)

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

	// Simple approach: check outside single-quoted strings.
	// Split by single quotes — odd-indexed segments are inside quotes.
	parts := strings.Split(cmd, "'")
	for i, part := range parts {
		if i%2 == 1 {
			// Inside single quotes — skip (shell does not interpret these)
			continue
		}
		// Check for dangerous patterns outside quotes
		if strings.ContainsAny(part, ";|>`") {
			return true
		}
		if strings.Contains(part, "&&") || strings.Contains(part, "||") || strings.Contains(part, "$(") || strings.Contains(part, "${") {
			return true
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
