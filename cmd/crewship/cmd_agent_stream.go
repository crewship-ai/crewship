package main

// Shared agent-stream collection (#998). `crewship ask --no-stream`
// (runNoStream) and `routine iterate` (askAgentText) used to carry two
// copies of the same WS read loop; event-handling fixes landed in one and
// drifted from the other. collectAgentStream is the single home for the
// loop; callers keep their own presentation/error mapping.

import (
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
)

// chatEventSource is the subset of *cli.WSClient the collector reads from —
// an interface so tests can script events without a live socket.
type chatEventSource interface {
	ReadMessage() (*cli.WSMessage, error)
}

// collectResult is the terminal state of one agent conversation stream.
// Exactly one of the four terminal causes is set: GotDone (clean finish),
// StreamErr (agent emitted an "error" event; sanitized on capture),
// ReadErr (socket-level failure), or TimedOut (no terminal event within
// the deadline). Text holds everything accumulated up to that point.
type collectResult struct {
	Text      string
	StreamErr string
	GotDone   bool
	ReadErr   error
	TimedOut  bool
}

// collectAgentStream drains chat events from an already-subscribed source
// until a terminal condition. timeout bounds the WHOLE conversation —
// a stalled agent container stops sending events without closing the
// socket, and unattended runs must not hang forever on ReadMessage;
// timeout 0 means no deadline (interactive `ask` blocks until the server
// closes the stream). Reads happen on a goroutine; the deadline fires on
// the select. Every return path closes `stop`, which unblocks a send the
// reader goroutine may be parked on after the collector returns — without
// it, a source that keeps emitting past done/timeout would strand the
// goroutine on a blocked send forever (the caller's socket Close unblocks
// ReadMessage, not a channel send). The goroutine then exits after at
// most one more ReadMessage.
func collectAgentStream(src chatEventSource, timeout time.Duration) collectResult {
	type wsRead struct {
		msg *cli.WSMessage
		err error
	}
	reads := make(chan wsRead)
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			msg, err := src.ReadMessage()
			select {
			case reads <- wsRead{msg, err}:
			case <-stop:
				return
			}
			if err != nil {
				return
			}
		}
	}()

	var deadline <-chan time.Time
	if timeout > 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		deadline = t.C
	}

	var res collectResult
	var fullText []byte
	for {
		select {
		case <-deadline:
			res.TimedOut = true
			res.Text = string(fullText)
			return res
		case r := <-reads:
			if r.err != nil {
				res.ReadErr = r.err
				res.Text = string(fullText)
				return res
			}
			event, err := cli.ParseChatEvent(r.msg)
			if err != nil || event == nil {
				continue
			}
			switch event.Type {
			case "text":
				fullText = append(fullText, event.Content...)
			case "error":
				// Sanitize on capture rather than on emit so every later
				// use (stderr print, returned error string) is uniformly
				// safe and callers don't have to remember to.
				res.StreamErr = sanitizeTerminal(event.Content)
				res.Text = string(fullText)
				return res
			case "done":
				res.GotDone = true
				res.Text = string(fullText)
				return res
			}
		}
	}
}
