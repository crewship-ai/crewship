package cli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// NDJSONMediaType is the content type the agent-run stream
// (GET /api/v1/chats/{chatId}/stream) answers with.
const NDJSONMediaType = "application/x-ndjson"

// StreamNDJSON opens a newline-delimited JSON stream at `path` (relative to
// BaseURL) and invokes onLine once per record, with the trailing newline
// stripped. It returns when the context is cancelled, the server closes the
// stream, or onLine returns an error — which is returned verbatim so a caller
// can use a sentinel to stop reading.
//
// This is the NDJSON sibling of StreamSSE and deliberately mirrors it:
//
//   - The request is built through NewRequest, so workspace injection and the
//     issue #571 token-host guard run here too. Setting the bearer by hand is
//     what leaked a token to a mismatched --server host once already.
//   - A fresh http.Client with NO timeout is used (streams are long-lived by
//     design) but the configured Transport is inherited, preserving proxy and
//     TLS settings.
//   - Reconnect is the caller's job. Wrap this in a backoff loop and pass the
//     last seq you saw back as `?last_seq=` (or lastEventID) to resume.
//
// Why not reuse StreamSSE: SSE frames an event across several `field: value`
// lines terminated by a blank line, so every consumer pays a parser and every
// producer pays the `data: ` prefix. NDJSON's frame is the line. For a stream
// whose entire audience is `jq` and shell pipelines, that is the format.
func (c *Client) StreamNDJSON(ctx context.Context, path string, lastEventID string, onLine func([]byte) error) error {
	req, err := c.NewRequest(ctx, "GET", path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", NDJSONMediaType)
	req.Header.Set("Cache-Control", "no-cache")
	if lastEventID != "" {
		req.Header.Set("Last-Event-ID", lastEventID)
	}

	transport := http.DefaultTransport
	if c.HTTPClient != nil && c.HTTPClient.Transport != nil {
		transport = c.HTTPClient.Transport
	}
	streamingClient := &http.Client{Transport: transport}

	resp, err := streamingClient.Do(req)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// Route the handshake failure through CheckError rather than a bare
		// fmt.Errorf, so the *APIError it produces carries the status into the
		// CLI exit-code contract (404 → 3, 401/403 → 4, 5xx → 7). StreamSSE
		// predates that contract and still returns a plain error; a caller of
		// this one gets a script-usable exit code for free.
		if err := CheckError(resp); err != nil {
			return err
		}
		return fmt.Errorf("NDJSON handshake: status %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" && !strings.HasPrefix(ct, NDJSONMediaType) {
		// A JSON body here means the server answered something other than the
		// stream — an older build with no such route behind a proxy that
		// rewrites 404s, most likely. Reading it line by line would hand the
		// caller fragments of one document.
		return fmt.Errorf("NDJSON handshake: unexpected content-type %q", ct)
	}

	return parseNDJSON(resp.Body, onLine)
}

// parseNDJSON reads r line by line and dispatches each non-empty line.
//
// bufio.Reader + ReadBytes (not bufio.Scanner) for the same reason parseSSE
// avoids Scanner: Scanner's 64 KiB token cap would turn one large tool_result
// frame into a hard error mid-stream. A record with no trailing newline (the
// server's last write before closing) is still dispatched.
func parseNDJSON(r io.Reader, onLine func([]byte) error) error {
	br := bufio.NewReader(r)
	for {
		line, err := br.ReadBytes('\n')
		trimmed := bytes.TrimRight(line, "\r\n")
		if len(bytes.TrimSpace(trimmed)) > 0 {
			if cbErr := onLine(trimmed); cbErr != nil {
				return cbErr
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("read: %w", err)
		}
	}
}
