package main

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// readValueStdin reads a credential value piped on stdin — the WHOLE of it.
//
// It replaces a bufio.Scanner that took the first line, which was right for
// every value the command had seen (keys are one line) and silently wrong
// for the first one that was not: a Codex `auth.json` piped from disk
// arrived as `{` and the server refused it as truncated JSON (#2428). A PEM
// key on --value-stdin would have met the same fate. Only the trailing
// newline a shell or an editor appends is trimmed; interior newlines are
// the value's own.
func readValueStdin() (string, error) {
	b, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", fmt.Errorf("read value from stdin: %w", err)
	}
	return strings.TrimRight(string(b), "\r\n"), nil
}
