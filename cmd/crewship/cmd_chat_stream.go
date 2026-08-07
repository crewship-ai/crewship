package main

// `crewship chat stream` — watch an agent run over plain HTTP (#1818).
//
// The API↔CLI parity half of the NDJSON run stream. `crewship run` and
// `crewship ask` already watch a run they START, over WebSocket. This watches
// a run somebody ELSE started — the web UI, a routine, a peer agent, another
// shell — with no socket and no /api/v1/ws-token dance, which is what makes it
// usable from an agent that only has `crewship` and a pipe.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

var chatStreamCmd = &cobra.Command{
	Use:   "stream <chat-id>",
	Short: "Watch a chat session's agent run as newline-delimited JSON",
	Long: `Stream an agent run over HTTP — no WebSocket client required.

Prints the same events the browser receives on the session channel: text,
thinking, tool_call, tool_result, done, error. By default the stream ends when
the run finishes, so it composes in a script; --follow keeps it open for the
next run on the same session.

Every event carries a monotonic sequence number. If the connection drops, the
command reconnects and resumes from the last sequence it saw, so a blip does
not lose or duplicate output.

Exit status is the run's: a run that ends in an error exits non-zero.

Examples:
  crewship chat stream c_abc123
  crewship chat stream c_abc123 --follow
  crewship chat stream c_abc123 --format ndjson | jq -r 'select(.type=="text").content'
  crewship chat stream c_abc123 --last-seq 42     # resume where you left off`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		follow, _ := cmd.Flags().GetBool("follow")
		lastSeq, _ := cmd.Flags().GetInt64("last-seq")
		idle, _ := cmd.Flags().GetInt("idle")
		quiet, _ := cmd.Flags().GetBool("quiet")
		return streamChatRun(client, args[0], chatStreamOptions{
			follow:      follow,
			lastSeq:     lastSeq,
			idleSeconds: idle,
			quiet:       quiet,
		})
	},
}

// chatStreamOptions carries the knobs the endpoint understands plus the local
// presentation choice. Kept as a struct so the reconnect loop, the renderer
// and the tests all agree on one shape.
type chatStreamOptions struct {
	lastSeq     int64
	follow      bool
	idleSeconds int
	// quiet suppresses the stderr chatter (thinking, tool calls, status lines)
	// but never the run's own text on stdout.
	quiet bool
}

// chatStreamBackoffBase is the first reconnect delay after a transient drop.
// Doubles up to chatStreamBackoffMax. A package var only so it stays in one
// place; nothing rewrites it.
var (
	chatStreamBackoffBase = time.Second
	chatStreamBackoffMax  = 30 * time.Second
)

// errStreamEnded stops the read loop from inside the line callback once the
// server has announced stream.end. The server closes the connection right
// after, but returning deterministically means the outer loop decides what to
// do next on the frame, not on a race with EOF.
var errStreamEnded = errors.New("stream ended")

// streamChatRun opens the NDJSON stream and renders it until the run finishes,
// the user interrupts, or a permanent error occurs.
//
// The reconnect policy mirrors followJournal's, with one addition the run
// stream needs: the server names the reason it closed, and only some reasons
// are worth retrying. A completed run is done; a truncated replay buffer
// cannot be healed by reconnecting (the gap is gone — read the transcript
// instead); a dropped or backpressured connection is exactly what resume
// exists for.
func streamChatRun(client *cli.Client, chatID string, opts chatStreamOptions) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// json/ndjson mean the caller wants the wire format, and the wire format is
	// already NDJSON — pass the server's lines through byte for byte rather
	// than re-encoding them. That is the mode an agent uses.
	format := cli.ResolveFormat(flagFormat, cliCfg)
	passthrough := format == "ndjson" || format == "json"

	r := &chatStreamRenderer{quiet: opts.quiet, passthrough: passthrough, lastSeq: opts.lastSeq}
	backoff := chatStreamBackoffBase

	for {
		r.endReason = ""
		err := client.WithContext(ctx).StreamNDJSON(ctx, chatStreamPath(chatID, r.lastSeq, opts), "", r.line)
		if errors.Is(err, errStreamEnded) {
			err = nil
		}

		if ctx.Err() != nil {
			// Ctrl-C. The partial output already printed is the answer; exiting
			// 0 keeps `crewship chat stream … | head` from looking like a
			// failure.
			r.flushText()
			return nil
		}
		if err != nil {
			if isPermanentStreamError(err) {
				r.flushText()
				return err
			}
		}

		switch r.endReason {
		case "run_complete", "no_active_run", "idle_timeout":
			r.flushText()
			if r.runError != "" {
				return fmt.Errorf("agent error: %s", r.runError)
			}
			return nil
		case "replay_truncated":
			r.flushText()
			return fmt.Errorf("the server's replay buffer for this run was truncated — the gap cannot be recovered by reconnecting; read the transcript with `crewship chat %s`", chatID)
		}

		// Either the connection dropped with no terminal frame, or the server
		// ended it for a reason resume can fix (slow_consumer, stream_closed).
		// Reconnect from the last seq we actually rendered.
		if !opts.quiet {
			fmt.Fprintf(os.Stderr, "%s[reconnecting in %s — resuming at seq %d]%s\n",
				cli.Dim, backoff, r.lastSeq, cli.Reset)
		}
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			r.flushText()
			return nil
		}
		backoff *= 2
		if backoff > chatStreamBackoffMax {
			backoff = chatStreamBackoffMax
		}
	}
}

// chatStreamPath builds the request URL. lastSeq comes from the renderer's
// running watermark, not from the flag, so a reconnect resumes where the
// output actually stopped rather than where the command started.
func chatStreamPath(chatID string, lastSeq int64, opts chatStreamOptions) string {
	q := url.Values{}
	if lastSeq > 0 {
		q.Set("last_seq", strconv.FormatInt(lastSeq, 10))
	}
	if opts.follow {
		q.Set("follow", "true")
	}
	if opts.idleSeconds > 0 {
		q.Set("idle", strconv.Itoa(opts.idleSeconds))
	}
	path := "/api/v1/chats/" + url.PathEscape(chatID) + "/stream"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	return path
}

// isPermanentStreamError reports errors that reconnecting cannot fix.
//
// StreamNDJSON returns a typed *cli.APIError for a failed handshake, so the
// classification is a status check rather than the substring match the SSE
// path has to do. Every 4xx here is the caller's own input — no such chat, no
// access, a rejected last_seq — and none of them heal on retry. 5xx and
// transport errors do fall through to the reconnect loop.
//
// isPermanentSSEError remains the fallback for anything that arrives without a
// status (a malformed URL, for instance).
func isPermanentStreamError(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *cli.APIError
	if errors.As(err, &apiErr) {
		return apiErr.Status >= 400 && apiErr.Status < 500
	}
	return isPermanentSSEError(err)
}

// chatStreamRenderer turns NDJSON lines into terminal output and tracks the
// two things the reconnect loop needs: how far the stream got (lastSeq) and
// why it ended (endReason).
//
// Output split follows `crewship run`'s convention exactly, because the two
// commands render the same events and a user should not have to learn it
// twice: the run's TEXT goes to stdout and nothing else does, so
// `crewship chat stream c1 > reply.md` yields the reply and only the reply.
// Thinking, tool activity and status go to stderr.
type chatStreamRenderer struct {
	quiet       bool
	passthrough bool
	lastSeq     int64
	endReason   string
	// runError holds the content of an `error` frame so the exit status can
	// carry it after the terminal `done` arrives.
	runError string
	// wroteText tracks whether stdout ended mid-line, so the final newline is
	// added once rather than after every chunk (text arrives in fragments).
	wroteText bool
}

// streamFrame is the wire shape of one NDJSON line. Only the fields the
// renderer uses are declared; unknown fields are ignored, which is what lets
// the server add frames without breaking an older CLI.
type streamFrame struct {
	Type    string `json:"type"`
	Seq     int64  `json:"seq"`
	Content string `json:"content"`
	Reason  string `json:"reason"`
	ChatID  string `json:"chat_id"`
	Active  bool   `json:"active"`
	FromSeq int64  `json:"from_seq"`
}

func (r *chatStreamRenderer) line(raw []byte) error {
	var f streamFrame
	if err := json.Unmarshal(raw, &f); err != nil {
		// A line we cannot parse is not worth killing the stream over, but it
		// is worth saying out loud — silently dropping it is how version skew
		// turns into "the agent said nothing".
		if !r.quiet {
			fmt.Fprintf(os.Stderr, "%s[skipped unparseable frame]%s\n", cli.Yellow, cli.Reset)
		}
		return nil
	}
	if f.Seq > r.lastSeq {
		r.lastSeq = f.Seq
	}

	if r.passthrough {
		// Byte-for-byte. The caller asked for the wire format; re-encoding
		// would reorder keys and drop anything this CLI does not know about.
		fmt.Println(string(raw))
	}

	switch f.Type {
	case "stream.open":
		if !r.quiet && !r.passthrough {
			state := "no run active"
			if f.Active {
				state = "run active"
			}
			fmt.Fprintf(os.Stderr, "%s[watching %s — %s, from seq %d]%s\n",
				cli.Dim, f.ChatID, state, f.FromSeq, cli.Reset)
		}
	case "stream.heartbeat", "run_begin":
		// Keep-alive and run boundary: structural, not output.
	case "stream.reset":
		if !r.quiet && !r.passthrough {
			fmt.Fprintf(os.Stderr, "%s[stream reset: %s]%s\n", cli.Yellow, f.Reason, cli.Reset)
		}
	case "stream.end":
		r.endReason = f.Reason
		if !r.quiet && !r.passthrough {
			fmt.Fprintf(os.Stderr, "%s[stream ended: %s]%s\n", cli.Dim, f.Reason, cli.Reset)
		}
		return errStreamEnded
	case "text":
		if !r.passthrough {
			// Strip control characters before they reach the terminal — a tool
			// result must not be able to rewrite the user's scrollback.
			fmt.Print(sanitizeTerminal(f.Content))
			r.wroteText = true
		}
	case "thinking":
		if !r.quiet && !r.passthrough {
			fmt.Fprintf(os.Stderr, "%s[thinking]%s %s\n", cli.Gray, cli.Reset, truncate(sanitizeTerminal(f.Content), 100))
		}
	case "tool_call":
		if !r.quiet && !r.passthrough {
			fmt.Fprintf(os.Stderr, "%s[tool]%s %s\n", cli.Cyan, cli.Reset, truncate(sanitizeTerminal(f.Content), 100))
		}
	case "tool_result":
		if !r.quiet && !r.passthrough && flagVerbose {
			fmt.Fprintf(os.Stderr, "%s[result]%s %s\n", cli.Gray, cli.Reset, truncate(sanitizeTerminal(f.Content), 200))
		}
	case "status":
		if !r.quiet && !r.passthrough {
			fmt.Fprintf(os.Stderr, "%s[status]%s %s\n", cli.Dim, cli.Reset, sanitizeTerminal(f.Content))
		}
	case "error":
		// Recorded, not returned: the run still emits a terminal `done` after
		// an error (ws/client.go emits the pair), and cutting the stream here
		// would drop it. The exit status carries the failure instead.
		r.runError = sanitizeTerminal(f.Content)
		if !r.passthrough {
			fmt.Fprintf(os.Stderr, "%s[error]%s %s\n", cli.Red, cli.Reset, r.runError)
		}
	case "done":
		r.flushText()
		if !r.quiet && !r.passthrough {
			fmt.Fprintf(os.Stderr, "%s[done]%s\n", cli.Green, cli.Reset)
		}
	}
	return nil
}

// flushText closes an unterminated stdout line. Agent text arrives in
// fragments with no guaranteed trailing newline; without this the shell prompt
// lands mid-sentence.
func (r *chatStreamRenderer) flushText() {
	if r.wroteText {
		fmt.Println()
		r.wroteText = false
	}
}
