package llm

// Shared SSE line-framing for the Anthropic and OpenAI stream parsers.
// Ollama streams NDJSON, not SSE, so it does not use this helper.

import (
	"bufio"
	"io"
	"strings"
)

// forEachSSEData scans r line by line and invokes fn with the payload of each
// "data: " line, stopping at a "[DONE]" sentinel. Lines without the "data: "
// prefix (event names, comments, blank keep-alives) are skipped.
//
// initialBuf and maxBuf size the scanner's line buffer (SSE lines are usually
// small, but tool payloads can be large).
//
// The two error returns are deliberately separate so callers can preserve
// distinct behavior: fnErr is the first error returned by fn, which aborts the
// scan immediately (scanErr is nil in that case); scanErr is scanner.Err()
// after the loop ends. fn may also return stop=true to end the scan early
// without an error, in which case scanner.Err() is still reported.
func forEachSSEData(r io.Reader, initialBuf, maxBuf int, fn func(data string) (stop bool, err error)) (fnErr, scanErr error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, initialBuf), maxBuf)

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := line[6:]
		if data == "[DONE]" {
			break
		}
		stop, err := fn(data)
		if err != nil {
			return err, nil
		}
		if stop {
			break
		}
	}
	return nil, scanner.Err()
}
