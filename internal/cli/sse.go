package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// SSEEvent is one parsed Server-Sent Events message.
//
// Per the SSE spec, fields are separated by single newlines and messages by
// blank lines. A message can carry an `id`, an `event` type, and `data`
// (which may itself span multiple `data:` lines — they are joined with
// newlines into a single Data string).
type SSEEvent struct {
	ID    string
	Event string // event type, "" if not set (server treats as "message")
	Data  string
	// Comment carries server-sent SSE comments (lines starting with ":").
	// Most callers ignore this; we surface it for debugging / heartbeat detection.
	Comment string
}

// StreamSSE opens an SSE connection at `path` (relative to BaseURL) and
// invokes onEvent for each parsed message. It returns when the context is
// cancelled, the server closes the stream, or onEvent returns an error.
//
// The HTTP client used here intentionally does NOT inherit the 30 s default
// timeout from c.HTTPClient — SSE connections are long-lived by design.
// We construct a fresh http.Client with no timeout but inherit the same
// Transport so any user-configured proxy / TLS settings are preserved.
//
// Reconnect on transient failure is the caller's responsibility — wrap this
// in a retry loop with exponential backoff if you want resumption.
func (c *Client) StreamSSE(ctx context.Context, path string, lastEventID string, onEvent func(SSEEvent) error) error {
	u, err := url.Parse(c.BaseURL + path)
	if err != nil {
		return fmt.Errorf("parse URL: %w", err)
	}
	if wsID := c.GetWorkspaceID(); wsID != "" {
		q := u.Query()
		if q.Get("workspace_id") == "" {
			q.Set("workspace_id", wsID)
			u.RawQuery = q.Encode()
		}
	}

	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
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
		return fmt.Errorf("SSE handshake: status %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" && !strings.HasPrefix(ct, "text/event-stream") {
		return fmt.Errorf("SSE handshake: unexpected content-type %q", ct)
	}

	return parseSSE(resp.Body, onEvent)
}

// parseSSE reads from r and dispatches each complete SSE message to onEvent.
//
// The message-framing rules implemented here:
//   - Lines ending in "\r\n" or "\n" are field lines.
//   - A blank line dispatches the accumulated event.
//   - Lines starting with ":" are SSE comments; surfaced via Comment field.
//   - Multiple `data:` lines in one event are joined with "\n".
//   - The reader is bounded by bufio.Scanner's default 64 KiB token size;
//     we use bufio.Reader + ReadString('\n') instead so individual fields
//     larger than that are tolerated (journal payloads can be 10s of KB).
func parseSSE(r io.Reader, onEvent func(SSEEvent) error) error {
	br := bufio.NewReader(r)
	var ev SSEEvent
	hasContent := false

	dispatch := func() error {
		if !hasContent {
			return nil
		}
		err := onEvent(ev)
		ev = SSEEvent{}
		hasContent = false
		return err
	}

	for {
		line, err := br.ReadString('\n')
		if line != "" {
			// Strip trailing \r\n or \n
			line = strings.TrimRight(line, "\r\n")
			if line == "" {
				if err := dispatch(); err != nil {
					return err
				}
				continue
			}
			if strings.HasPrefix(line, ":") {
				if ev.Comment == "" {
					ev.Comment = strings.TrimSpace(line[1:])
				} else {
					ev.Comment += "\n" + strings.TrimSpace(line[1:])
				}
				hasContent = true
				continue
			}
			field, value, _ := strings.Cut(line, ":")
			value = strings.TrimPrefix(value, " ")
			switch field {
			case "id":
				ev.ID = value
				hasContent = true
			case "event":
				ev.Event = value
				hasContent = true
			case "data":
				if ev.Data == "" {
					ev.Data = value
				} else {
					ev.Data += "\n" + value
				}
				hasContent = true
			case "retry":
				// Ignored — caller decides reconnect cadence.
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				// Drain any pending event (server closed without trailing blank line).
				return dispatch()
			}
			return fmt.Errorf("read: %w", err)
		}
	}
}
