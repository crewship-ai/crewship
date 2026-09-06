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
//
// Bounded to the server's own cap (internal/api/credentials_types.go
// maxCredentialValueLen), one optional line ending and a sentinel byte:
// a value the server would refuse is
// refused here without buffering an unbounded pipe first.
func readValueStdin() (string, error) {
	const maxValueStdinBytes = 64 * 1024
	b, err := io.ReadAll(io.LimitReader(os.Stdin, maxValueStdinBytes+3))
	if err != nil {
		return "", fmt.Errorf("read value from stdin: %w", err)
	}
	if len(b) > maxValueStdinBytes+2 {
		return "", fmt.Errorf("stdin value is too long (max %d bytes)", maxValueStdinBytes)
	}
	value := string(b)
	if strings.HasSuffix(value, "\n") {
		value = strings.TrimSuffix(strings.TrimSuffix(value, "\n"), "\r")
	}
	if len(value) > maxValueStdinBytes {
		return "", fmt.Errorf("stdin value is too long (max %d bytes)", maxValueStdinBytes)
	}
	return value, nil
}
