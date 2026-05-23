package main

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// readPasswordFromStdin reads every byte from r, trims a single trailing
// LF (and optional CR) so `echo "pw" | crewship ...` works, and returns
// the result.
//
// Why this exists: the older `--password <value>` flag leaks into the
// caller's shell history and is visible in `ps` to any other process on
// the host during the lifetime of the invocation. Real-world CI
// pipelines that bake bootstrap automation routinely pin the password
// in plaintext in a Dockerfile RUN, image layer, or kubectl Job spec —
// all of which are recoverable long after the build completes. The
// stdin pattern matches `docker login --password-stdin`,
// `gh auth login --with-token`, `op item get | jq | crewship ...` and
// every other tool that's been burned by argv-side leakage.
//
// We intentionally do NOT strip every kind of whitespace — leading
// spaces and tabs are valid password characters and must survive
// round-trip from a secret manager. Only the trailing newline that
// shells universally append to `echo`, `printf`, and `cat` payloads
// is removed; anything else stays verbatim.
func readPasswordFromStdin(r io.Reader) (string, error) {
	// bufio.Reader avoids a per-byte syscall hot path and gives us a
	// generous default buffer. A "password" that overflows 64 KiB is
	// almost certainly the wrong input (someone piped a tarball by
	// mistake); io.ReadAll would still happily allocate megabytes
	// before we noticed.
	buf := bufio.NewReaderSize(r, 4096)
	data, err := io.ReadAll(buf)
	if err != nil {
		return "", fmt.Errorf("read password from stdin: %w", err)
	}
	// Strip exactly one trailing newline (LF or CRLF). Multiple
	// trailing newlines might be intentional in a pathological case,
	// so we don't iterate — strip the canonical "shell appended a
	// newline" suffix and stop.
	s := string(data)
	s = strings.TrimSuffix(s, "\n")
	s = strings.TrimSuffix(s, "\r")
	if s == "" {
		return "", fmt.Errorf("empty password read from stdin")
	}
	return s, nil
}

// resolvePasswordInput centralises the three-way precedence shared by
// `crewship init` and `crewship admin reset-password`:
//
//  1. --password-stdin    → read entire stdin, no TTY prompt
//  2. --password <value>  → use the flag value, warn about leakage path
//  3. neither             → caller's responsibility (e.g. interactive
//     prompt with term.ReadPassword on a TTY)
//
// Returns the resolved password and a "source" string describing where
// it came from, used by the caller to decide whether to suppress the
// "Password:" prompt. err is non-nil when --password-stdin and
// --password are both set (a configuration mistake we surface loudly
// rather than silently picking one), or when stdin produced nothing.
func resolvePasswordInput(flagValue string, stdinFlag bool, stdin io.Reader) (password string, source string, err error) {
	if stdinFlag && flagValue != "" {
		return "", "", fmt.Errorf("--password and --password-stdin are mutually exclusive — pick one")
	}
	if stdinFlag {
		pw, err := readPasswordFromStdin(stdin)
		if err != nil {
			return "", "", err
		}
		return pw, "stdin", nil
	}
	if flagValue != "" {
		return flagValue, "flag", nil
	}
	return "", "", nil
}
