package ws

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/websocket"
)

// Client represents a single authenticated WebSocket connection with its
// channel subscriptions and outbound message buffer. authSessionID is
// the user_sessions row that authorized this connection (empty for
// CLI-token-derived tickets); the hub's shared revocation sweep
// (Hub.sweepRevokedSessions) groups clients by this field to detect
// server-side logout and force-close with the session_revoked frame.
//
// consecutiveDrops + disconnectFired implement the slow-consumer
// disconnect logic in Hub.dispatch. The hub-level recordDrop accounts
// the global counter and logs every 16 drops, but a single stuck
// client would otherwise stay subscribed indefinitely, accumulating
// wasted dispatch work on every broadcast. consecutiveDrops resets on
// the next successful send so transient hiccups don't trip the cutoff;
// disconnectFired is a one-shot guard so the burst of drops that
// follows the threshold doesn't spawn multiple teardown goroutines.
type Client struct {
	conn             *websocket.Conn
	hub              *Hub
	userID           string
	authSessionID    string
	channels         map[string]bool
	send             chan []byte
	ctx              context.Context
	cancel           context.CancelFunc
	mu               sync.Mutex // protects channels map
	consecutiveDrops atomic.Uint32
	disconnectFired  atomic.Bool
	// writeWait overrides defaultWriteWait per connection; zero means use
	// the default. Set at construction in the hub; only tests vary it.
	writeWait time.Duration
}

func (c *Client) readPump() {
	defer func() {
		c.cancel()
		c.hub.unregister <- c
		c.conn.Close()
	}()

	for {
		var raw []byte
		if err := websocket.Message.Receive(c.conn, &raw); err != nil {
			break
		}

		var msg ClientMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			continue
		}

		switch msg.Type {
		case "subscribe":
			c.subscribe(msg.Channel)
		case "unsubscribe":
			c.unsubscribe(msg.Channel)
		case "ping":
			c.send <- pongMessageBytes
		case "send_message":
			c.hub.logger.Debug("message received", "user_id", c.userID, "channel", msg.Channel)
			c.handleSendMessage(msg)
		case "cancel_message":
			c.hub.logger.Debug("cancel requested", "user_id", c.userID)
			c.handleCancelMessage(msg)
		case "resume":
			c.handleResume(msg)
		}
	}
}

// defaultWriteWait bounds how long a single outbound frame may block in
// conn.Write before the connection is torn down. Without it, a client
// that has stopped reading (full TCP receive window) blocks the write
// indefinitely — the goroutine hangs until the kernel-level TCP timeout
// (minutes), holding a slot and stalling broadcast dispatch behind it.
// The hub-level slow-consumer drop logic (consecutiveDropsBeforeDisconnect)
// only fires when the per-client send buffer overflows; it does not help
// once a write has already entered a blocking syscall.
const defaultWriteWait = 10 * time.Second

func (c *Client) writePump() {
	ticker := time.NewTicker(30 * time.Second)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case msg, ok := <-c.send:
			if !ok {
				return
			}
			if !c.writeFrame(msg) {
				return
			}
		case <-ticker.C:
			if !c.writeFrame(pingMessageBytes) {
				return
			}
		}
	}
}

// writeFrame writes one frame under a fresh write deadline and reports
// whether the caller should keep the pump running. A failed or timed-out
// write returns false so writePump exits and the deferred Close runs.
func (c *Client) writeFrame(msg []byte) bool {
	wait := c.writeWait
	if wait <= 0 {
		wait = defaultWriteWait
	}
	if err := c.conn.SetWriteDeadline(time.Now().Add(wait)); err != nil {
		return false
	}
	if _, err := c.conn.Write(msg); err != nil {
		return false
	}
	return true
}

// Session revocation is enforced by the hub's shared sweep
// (Hub.sweepRevokedSessions in hub.go), not a per-connection watcher —
// see that function's doc comment for why (#1255 item 3).

// sendError marshals the standard error frame ({"error": msg} payload on the
// given channel) and queues it on this client's send buffer.
func (c *Client) sendError(channel, msg string) {
	resp, _ := json.Marshal(ServerMessage{
		Type:    "error",
		Channel: channel,
		Payload: map[string]string{"error": msg},
	})
	c.safeSend(resp)
}

// trySendError is sendError with a non-blocking enqueue (trySend). Hub
// sweep/notify goroutines use it so one client's full send buffer can't
// stall the shared loop; the frame is best-effort there.
func (c *Client) trySendError(channel, msg string) {
	resp, _ := json.Marshal(ServerMessage{
		Type:    "error",
		Channel: channel,
		Payload: map[string]string{"error": msg},
	})
	c.trySend(resp)
}

// unwrapDoubleEncoded handles a double-encoded payload (the frontend sends a
// JSON.stringify'd string): when raw is a JSON string, the inner value is
// returned as the payload; anything else passes through unchanged.
func unwrapDoubleEncoded(raw json.RawMessage) json.RawMessage {
	if len(raw) > 0 && raw[0] == '"' {
		var unwrapped string
		if err := json.Unmarshal(raw, &unwrapped); err == nil {
			return json.RawMessage(unwrapped)
		}
	}
	return raw
}

func (c *Client) subscribe(channel string) {
	if channel == "" {
		return
	}

	// Validate channel access: deny by default when no authorizer is configured.
	if c.hub.channelAuth == nil {
		c.hub.logger.Warn("channel subscription denied: no authorizer configured", "user_id", c.userID, "channel", channel)
		c.sendError(channel, "access denied")
		return
	}
	// Grant path: an authorizer ERROR is treated exactly like a deny —
	// fail closed. Only the periodic sweep (which takes access away)
	// distinguishes the two.
	if ok, err := c.hub.channelAuth.CanSubscribe(c.ctx, c.userID, channel); err != nil || !ok {
		c.hub.logger.Warn("channel subscription denied", "user_id", c.userID, "channel", channel, "error", err)
		c.sendError(channel, "access denied")
		return
	}

	c.mu.Lock()
	c.channels[channel] = true
	c.mu.Unlock()

	c.hub.mu.Lock()
	if _, ok := c.hub.channels[channel]; !ok {
		c.hub.channels[channel] = make(map[*Client]bool)
	}
	c.hub.channels[channel][c] = true
	c.hub.mu.Unlock()

	c.hub.logger.Debug("client subscribed", "user_id", c.userID, "channel", channel)
}

func (c *Client) unsubscribe(channel string) {
	if channel == "" {
		return
	}
	c.mu.Lock()
	delete(c.channels, channel)
	c.mu.Unlock()

	c.hub.mu.Lock()
	if subs, ok := c.hub.channels[channel]; ok {
		delete(subs, c)
		if len(subs) == 0 {
			delete(c.hub.channels, channel)
		}
	}
	c.hub.mu.Unlock()
}

func (c *Client) safeSend(data []byte) (sent bool) {
	defer func() {
		if r := recover(); r != nil {
			sent = false
		}
	}()
	select {
	case c.send <- data:
		return true
	case <-c.ctx.Done():
		return false
	}
}

// trySend is the non-blocking variant of safeSend: enqueue if the buffer
// has room, otherwise drop the frame immediately. Used by hub-side
// goroutines (connection sweep, revocation notify) that must never park
// behind one client's saturated buffer. Same panic guard — the hub
// closes c.send on unregister, and these callers race with that by
// design.
func (c *Client) trySend(data []byte) (sent bool) {
	defer func() {
		if r := recover(); r != nil {
			sent = false
		}
	}()
	select {
	case c.send <- data:
		return true
	default:
		return false
	}
}

// agentBusyEventType is the chat-event name for a busy rejection (the chat
// already has a live agent run — ErrAgentBusy). Rendered by the frontend's
// agent_busy case, which reuses the error-bubble path and settles the
// sender's composer state; delivered sender-only, never broadcast.
const agentBusyEventType = "agent_busy"

// agentBusyNotice is the user-facing text of the sender-only busy rejection.
const agentBusyNotice = "The agent is currently replying to another message in this chat. Please wait for it to finish."

type sendMessagePayload struct {
	ChatID  string `json:"session_id"`
	Content string `json:"content"`
	// MaxTurns caps the adapter agent loop for this run (`--max-turns` CLI
	// flag). 0/omitted leaves the adapter default in place. Clamped via
	// clampMaxTurns before use — this arrives from an untrusted client.
	MaxTurns int `json:"max_turns,omitempty"`
}

// maxAllowedTurns is the hard ceiling on a client-supplied turn cap. The whole
// point of --max-turns is to STOP a runaway from burning budget, so a WS client
// (CLI, browser devtools, anything) must not be able to disable that guard by
// sending a huge value — nor pass a negative one with undefined adapter meaning.
const maxAllowedTurns = 200

// clampMaxTurns sanitizes an untrusted turn cap: negative → 0 (unset, adapter
// default), and anything above the ceiling is capped.
func clampMaxTurns(n int) int {
	if n < 0 {
		return 0
	}
	if n > maxAllowedTurns {
		return maxAllowedTurns
	}
	return n
}

func (c *Client) handleCancelMessage(msg ClientMessage) {
	var payload sendMessagePayload
	raw := unwrapDoubleEncoded(msg.Payload)
	if err := json.Unmarshal(raw, &payload); err != nil || payload.ChatID == "" {
		return
	}

	cancelKey := c.userID + ":" + payload.ChatID
	c.hub.cancelMu.Lock()
	cancel, ok := c.hub.cancelFns[cancelKey]
	c.hub.cancelMu.Unlock()

	if ok {
		c.hub.logger.Info("cancelling run", "session_id", payload.ChatID, "user_id", c.userID)
		cancel()
	}
}

type resumePayload struct {
	ChatID  string `json:"session_id"`
	LastSeq int64  `json:"last_seq"`
}

// handleResume replays the in-flight run's buffered frames to a client that
// (re)connected mid-generation, so it catches up on everything it missed. Replay
// is offered ONLY while a run is still active: a completed run is already
// persisted and served from chat history, so replaying it too would double it.
// A truncated buffer answers with "resume_reset" telling the client to reload
// history instead. Requires the same channel authorization as subscribe.
func (c *Client) handleResume(msg ClientMessage) {
	var payload resumePayload
	raw := unwrapDoubleEncoded(msg.Payload)
	if err := json.Unmarshal(raw, &payload); err != nil || payload.ChatID == "" {
		return
	}

	channel := "session:" + payload.ChatID
	// Deny by default: same authorization gate as subscribe so a client can't
	// replay another workspace's/session's stream by guessing a chat id.
	// Authorizer errors fail closed, like every grant path.
	if c.hub.channelAuth == nil {
		c.hub.logger.Warn("resume denied", "user_id", c.userID, "channel", channel)
		return
	}
	if ok, err := c.hub.channelAuth.CanSubscribe(c.ctx, c.userID, channel); err != nil || !ok {
		c.hub.logger.Warn("resume denied", "user_id", c.userID, "channel", channel, "error", err)
		return
	}

	res := c.hub.streams.replay(channel, payload.LastSeq)
	if !res.found || !res.active {
		// No in-flight run (or it already finished + persisted) — history covers
		// it; nothing to replay.
		return
	}
	if res.reset {
		// Buffer overflowed and can't serve a coherent replay — tell the client
		// to reload history.
		if data, err := json.Marshal(ServerMessage{Type: "resume_reset", Channel: channel}); err == nil {
			c.safeSend(data)
		}
		return
	}
	for _, frame := range res.frames {
		c.safeSend(frame)
	}
}

func (c *Client) handleSendMessage(msg ClientMessage) {
	c.hub.logger.Debug("handleSendMessage", "user_id", c.userID, "payload_len", len(msg.Payload))
	if c.hub.chatHandler == nil {
		c.sendError(msg.Channel, "chat not available")
		return
	}

	var payload sendMessagePayload
	// Handle double-encoded payload (frontend sends JSON.stringify'd string)
	raw := unwrapDoubleEncoded(msg.Payload)
	if err := json.Unmarshal(raw, &payload); err != nil {
		c.hub.logger.Debug("invalid send_message payload", "error", err, "payload_len", len(msg.Payload))
		c.sendError(msg.Channel, "invalid payload")
		return
	}

	if payload.ChatID == "" || payload.Content == "" {
		c.hub.logger.Debug("send_message missing fields", "chat_id_empty", payload.ChatID == "", "content_empty", payload.Content == "")
		c.sendError(msg.Channel, "session_id and content required")
		return
	}

	// Validate session access: user must be authorized for this chat's channel
	if c.hub.channelAuth != nil {
		sessionCh := "session:" + payload.ChatID
		// Grant path — authorizer errors fail closed.
		if ok, err := c.hub.channelAuth.CanSubscribe(c.ctx, c.userID, sessionCh); err != nil || !ok {
			c.hub.logger.Warn("send_message denied: no access to session", "user_id", c.userID, "chat_id", payload.ChatID, "error", err)
			c.sendError(msg.Channel, "access denied")
			return
		}
	}

	go func() {
		channel := "session:" + payload.ChatID

		// Derive the run context from the HUB's server-lifetime context, NOT the
		// client socket (c.ctx). A client disconnect (navigating away, refresh,
		// network blip) must not cancel an in-flight generation — the run has to
		// finish and persist server-side so the reply is never lost. Explicit
		// Stop still cancels this run via cancelFns (handleCancelMessage).
		runCtx, runCancel := context.WithCancel(c.hub.baseRunContext())
		defer runCancel()

		// Reject if a run is already in progress for this user+session
		cancelKey := c.userID + ":" + payload.ChatID
		c.hub.cancelMu.Lock()
		if _, exists := c.hub.cancelFns[cancelKey]; exists {
			c.hub.cancelMu.Unlock()
			c.sendError(channel, "a message is already being processed")
			return
		}
		c.hub.cancelFns[cancelKey] = runCancel
		c.hub.cancelMu.Unlock()
		defer func() {
			c.hub.cancelMu.Lock()
			delete(c.hub.cancelFns, cancelKey)
			c.hub.cancelMu.Unlock()
		}()

		// Start (reset) this session's replay buffer for the run, and mark it
		// ended once the run returns — a client that reconnects mid-run replays
		// the buffered gap via the "resume" message.
		startSeq := c.hub.streams.begin(channel)
		defer c.hub.streams.end(channel)

		// emit stamps a per-session seq on msg, buffers the frame for replay, and
		// fans out the EXACT bytes to the sender (direct) and every other/returning
		// subscriber (dispatch), so all recipients see identical seq numbers.
		emit := func(msg *ServerMessage) {
			data, ok := c.hub.streams.record(channel, msg)
			if !ok {
				return
			}
			c.safeSend(data)
			c.hub.dispatch(channel, data, func(cl *Client) bool { return cl != c })
		}

		// run_begin is the first frame of the run. It carries the baseline seq
		// (from_seq = counter before this run) so any client — the sender, a
		// second tab, or a client that reconnects mid-run and replays the buffer
		// — can anchor its in-order reassembly without waiting for sequence
		// numbers that belong to a previous run on the same channel.
		emit(&ServerMessage{
			Type:    "run_begin",
			Channel: channel,
			Payload: map[string]any{"from_seq": startSeq},
		})

		streamFn := func(event ChatEvent) {
			emit(&ServerMessage{Type: "chat_event", Channel: channel, Payload: event})
		}

		err := c.hub.chatHandler.HandleChatMessage(
			runCtx,
			c.userID,
			payload.ChatID,
			payload.Content,
			streamFn,
			ChatMessageOption{MaxTurns: clampMaxTurns(payload.MaxTurns)},
		)
		if err != nil {
			// Don't emit error if context was cancelled (user requested stop)
			if runCtx.Err() == context.Canceled {
				streamFn(ChatEvent{Type: "done", Content: ""})
				return
			}
			// Busy rejection (cross-user run exclusivity): the chat already has
			// a live run, the bounced message was NOT persisted, and nothing may
			// touch the shared session channel. Reply to the rejected sender
			// alone via safeSend — the same sender-only mechanism the cancelKey
			// guard above uses — with no seq (not part of the resumable stream)
			// and NO terminal done: emitting agent_busy/done through streamFn
			// would broadcast to every subscriber, and the winning user's client
			// would finalize its live streaming turn and unlock its composer
			// mid-generation. The frontend's agent_busy handler settles the
			// sender's own composer state without needing a done.
			if errors.Is(err, ErrAgentBusy) {
				c.hub.logger.Info("send rejected: agent busy",
					"session_id", payload.ChatID, "user_id", c.userID)
				busy, merr := json.Marshal(ServerMessage{
					Type:    "chat_event",
					Channel: channel,
					Payload: ChatEvent{
						Type:     agentBusyEventType,
						Content:  agentBusyNotice,
						Metadata: map[string]any{"chat_id": payload.ChatID},
					},
				})
				if merr == nil {
					c.safeSend(busy)
				}
				return
			}
			c.hub.logger.Error("chat message error", "error", err, "session_id", payload.ChatID)
			// Route through streamFn (emit) so the error is seq'd, buffered for
			// replay, and dispatched to EVERY subscriber — not just the still-
			// connected originator. Follow with a terminal `done` so a second tab
			// or a client that reconnects after the failure leaves the streaming
			// state instead of spinning forever.
			streamFn(ChatEvent{Type: "error", Content: "an error occurred processing your message"})
			streamFn(ChatEvent{Type: "done", Content: ""})
		}
	}()
}
