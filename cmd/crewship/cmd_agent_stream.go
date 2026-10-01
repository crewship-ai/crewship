package main

// Shared agent-stream collection (#998). `crewship ask --no-stream`
// (runNoStream) and `routine iterate` (askAgentText) used to carry two
// copies of the same WS read loop; event-handling fixes landed in one and
// drifted from the other. collectAgentStream is the single home for the
// loop; callers keep their own presentation/error mapping.

import (
	"fmt"
	"os"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
	wsproto "github.com/crewship-ai/crewship/internal/ws"
)

// chatEventSource is the subset of *cli.WSClient the collector reads from —
// an interface so tests can script events without a live socket.
type chatEventSource interface {
	ReadMessage() (*cli.WSMessage, error)
}

// collectResult is the terminal state of one agent conversation stream.
// Exactly one of the five terminal causes is set: GotDone (clean finish),
// StreamErr (agent emitted an "error" event; sanitized on capture),
// ReadErr (socket-level failure), TimedOut (no terminal event within the
// deadline), or Busy (the send bounced off the per-agent run lock and
// nothing ran). Text holds everything accumulated up to that point.
type collectResult struct {
	Text      string
	StreamErr string
	GotDone   bool
	ReadErr   error
	TimedOut  bool
	// Busy is set when the server bounced the send off the per-agent run
	// lock (#2269): the agent already had a live run, this message was
	// never persisted and nothing ran. Distinct from StreamErr because the
	// remedy is different — a busy bounce is "retry when the agent frees
	// up", not "the agent failed" — and distinct from TimedOut because the
	// answer arrived immediately. BusyNotice carries the server's wording.
	Busy       bool
	BusyNotice string
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
	var wait provisioningWait
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
			wait.observe(event)
			switch event.Type {
			case wsproto.CrewProvisioningEventType:
				if failed, notice := provisioningFailed(event); failed {
					res.StreamErr = notice
					res.Text = string(fullText)
					return res
				}
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
				if wait.deferredDone() {
					continue // the deferral's done; the replayed run follows
				}
				res.GotDone = true
				res.Text = string(fullText)
				return res
			case wsproto.AgentBusyEventType:
				// A busy bounce is terminal FOR THIS SEND and the server
				// deliberately sends no `done` after it (internal/ws/
				// client.go: a terminal frame here would travel the shared
				// session channel and finalize the winning sender's live
				// turn). Returning is therefore not an optimisation — with
				// timeout 0, which is what `crewship run --no-stream`
				// passes, not returning means blocking forever. Verified on
				// dev3 2026-09-05: two concurrent runs against one agent sat
				// silent for 200 s and exited only on the caller's timeout.
				res.Busy = true
				res.BusyNotice = sanitizeTerminal(event.Content)
				res.Text = string(fullText)
				return res
			default:
				// Unknown event type. Silence here is exactly how the
				// agent_busy hang survived — a frame arrived, nothing
				// matched it, and the loop went back to waiting for a
				// terminal event the server had already decided not to
				// send. Under -v, name what came in.
				if flagVerbose {
					fmt.Fprintf(os.Stderr, "%s[unhandled event]%s type=%q\n",
						cli.Gray, cli.Reset, event.Type)
				}
			}
		}
	}
}

// provisioningWait tracks a send the server parked behind an environment
// build. The chat bridge answers such a send with a crew_provisioning event
// and a done, attaches the message to the build job, and replays it on the
// same session channel once the image is ready — that replay is the real run
// and ends with its own done or error. Before this existed `crewship run`
// treated the first done as the end and printed an empty answer while the
// agent ran unseen (cache image evicted, first run of a new crew).
type provisioningWait struct {
	deferred bool // a crew_provisioning event arrived
	ran      bool // a run event arrived after it
}

func (w *provisioningWait) observe(event *cli.ChatEventPayload) {
	switch event.Type {
	case wsproto.CrewProvisioningEventType:
		w.deferred, w.ran = true, false
	case "done", "error", wsproto.AgentBusyEventType:
	default:
		if w.deferred {
			w.ran = true
		}
	}
}

// deferredDone reports whether a done only closes the deferral. A done that
// follows any event of the replayed run is that run's own terminal frame.
func (w *provisioningWait) deferredDone() bool {
	if w.deferred && !w.ran {
		w.deferred = false
		return true
	}
	return false
}

// provisioningFailed reports a crew_provisioning event whose build never
// started: nothing will replay the message, so waiting would hang.
func provisioningFailed(event *cli.ChatEventPayload) (bool, string) {
	meta, _ := event.Metadata.(map[string]any)
	if status, _ := meta["status"].(string); status != "failed" {
		return false, ""
	}
	notice := sanitizeTerminal(event.Content)
	if notice == "" {
		notice = "the environment build could not be started"
	}
	return true, notice
}
