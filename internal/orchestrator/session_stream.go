package orchestrator

import (
	"context"
	"errors"

	"github.com/crewship-ai/crewship/internal/ws"
)

// Publishing an agent run on its chat's session channel (#1823).
//
// # The problem
//
// Until this file existed, exactly one code path produced frames on
// `session:{chatId}`: ws.Client.handleSendMessage, i.e. a WebSocket
// send_message. Every other way to start a run — the scheduler
// (internal/scheduler), a webhook (internal/api/webhook.go), a routine's
// agent_run step (internal/pipeline/runner_orchestrator.go), the agent-start
// IPC (internal/server/routes_agent.go) — called RunAgent with a handler that
// wrote to a log buffer and nothing else. Attaching to one of those runs, over
// the socket or over `crewship chat stream`, produced `stream.open
// active:false` then `stream.end no_active_run`: exit 0, no output, for a run
// that was very much executing.
//
// # Why this is one wrapper and not four call-site edits
//
// The obvious fix is to paste begin/emit/end into each of those four sites.
// Rejected, because:
//
//   - All four already funnel through RunAgent, and all four already put the
//     run's chat id on the request (they have to — the run record, the
//     conversation store and the journal are all keyed by it). The information
//     needed to publish is therefore present at the chokepoint; copying the
//     lifecycle outward adds nothing but places to forget it.
//   - The lifecycle has invariants that are easy to break silently: `End` must
//     balance `Begin` on every exit path INCLUDING panic, or the channel
//     reports "generating" forever and every later watcher waits out its idle
//     timeout; the terminal `done` must be published BEFORE `End`, or it lands
//     outside the replay buffer and a client that reconnects never learns the
//     run finished. One implementation gets audited once.
//   - The fifth caller. A dispatch path added next month is watchable by
//     construction rather than by remembering.
//
// The two paths that must NOT be published here say so on the request
// (SuppressSessionStream), which is a positive statement by the caller rather
// than a heuristic here:
//
//   - the WebSocket send path, where ws.Client already records the whole turn
//     (container start, steering, the terminal done after RunAgent returns) —
//     a second recording underneath it would double every frame;
//   - delegated/peer sub-agent runs (RunAgentForAssignment), whose frames
//     belong to the parent's turn, not to a turn of their own.
//
// # Cost when nobody is watching
//
// Publication is a JSON marshal plus an append to the channel's replay buffer,
// then a non-blocking fan-out (ws.Hub.dispatch drops rather than blocks on a
// slow consumer). It happens whether or not anyone is attached, deliberately:
// the buffer is what lets a watcher that attaches AFTER the run started catch
// up, which is the normal case for a routine — you learn the chat id from the
// run, then attach. The buffer is capped (5000 frames / 8 MiB per run, see
// ws/session_stream.go) and swept two minutes after the run ends, so a long
// routine run degrades to `replay_truncated` exactly the way #1822 established
// rather than growing without bound.

// SessionPublisher opens a recording on a chat's session channel. Implemented
// by *ws.Hub; nil on CLI-only builds and in most tests, where RunAgent behaves
// exactly as it did before this file.
type SessionPublisher interface {
	BeginSessionRun(chatID string) ws.SessionRun
}

// SetSessionPublisher wires the hub in so runs started by the scheduler, a
// webhook, a routine step or the agent-start IPC are watchable. Called once
// during server startup.
func (o *Orchestrator) SetSessionPublisher(p SessionPublisher) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.sessionPublisher = p
}

func (o *Orchestrator) sessionPub() SessionPublisher {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.sessionPublisher
}

// errRunPanicked is what a watcher is told when the run crashed. The panic
// itself is re-raised for the caller and the telemetry span; the stream just
// needs a terminal frame that does not claim success.
var errRunPanicked = errors.New("agent run crashed")

// RunAgent executes an agent run inside its crew's container, streaming events
// to handler — and publishing those same events on `session:{req.ChatID}` so
// the run is watchable over the WebSocket and over `crewship chat stream`.
//
// See the file comment for why the publication lives here rather than at each
// dispatch site.
func (o *Orchestrator) RunAgent(ctx context.Context, req AgentRunRequest, handler EventHandler) (err error) {
	pub := o.sessionPub()
	// No publisher wired, no chat to publish on, or a caller that already owns
	// the channel: run exactly as before. Note the empty-chat-id guard — the
	// agent-start IPC can run an agent with no chat row at all, and recording
	// against `session:` would open a buffer on a channel no authorizer can
	// grant and no client can subscribe to.
	if pub == nil || req.ChatID == "" || req.SuppressSessionStream {
		return o.runAgent(ctx, req, handler)
	}

	run := pub.BeginSessionRun(req.ChatID)
	defer func() {
		// Wrapping OUTSIDE runAgent is what makes this correct on the panic
		// path. runAgent's own outermost defer (telemetry.RecoverPanic) has
		// already stamped the span and re-panicked by the time this runs, so
		// recovering here neither hides the crash nor rewrites the stack that
		// was captured: it only borrows the unwind long enough to close the
		// stream honestly, then re-raises the original value.
		if p := recover(); p != nil {
			finishSessionRun(run, errRunPanicked)
			panic(p)
		}
		finishSessionRun(run, err)
	}()

	return o.runAgent(ctx, req, publishingHandler(run, handler))
}

// publishingHandler forwards every agent event to the session channel and then
// to the caller's own handler. Publishing FIRST is deliberate: the watcher is
// the latency-sensitive consumer, and the caller's handler does bookkeeping
// (log-buffer append, text accumulation, span recording) whose cost should not
// sit in front of the frame. It mirrors the WebSocket path, where the bridge's
// handler also streams before it accumulates.
//
// A nil caller handler is normal (several dispatch sites want the run, not the
// events).
func publishingHandler(run ws.SessionRun, next EventHandler) EventHandler {
	return func(event AgentEvent) {
		run.Emit(ws.ChatEvent{
			Type:     event.Type,
			Content:  event.Content,
			Metadata: event.Metadata,
		})
		if next != nil {
			next(event)
		}
	}
}

// finishSessionRun writes the run's terminal frames and closes the recording.
//
// Frame order matters and is the same order the WebSocket path uses: an
// `error` (when the run failed for a reason the watcher can act on) followed
// by the terminal `done` that ends the stream — both BEFORE End, because a
// frame recorded after the run is over gets no sequence number, is not
// buffered, and is therefore invisible to anyone who reconnects.
//
// A cancelled run gets a bare `done`: cancellation is the operator stopping
// the run, not a fault, and the WebSocket path reports it the same way.
func finishSessionRun(run ws.SessionRun, err error) {
	if err != nil && !errors.Is(err, context.Canceled) {
		run.Emit(ws.ChatEvent{Type: "error", Content: err.Error()})
	}
	// The `done` is synthesized here rather than expected from an adapter: no
	// CLI adapter emits one (the chat bridge synthesizes it for the WebSocket
	// path too), and without it `crewship chat stream` has nothing to close on
	// and burns its full idle timeout on a run that already finished.
	run.Emit(ws.ChatEvent{Type: "done"})
	run.End()
}
