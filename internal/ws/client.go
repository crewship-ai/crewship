package ws

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"golang.org/x/net/websocket"
)

// Client represents a single authenticated WebSocket connection with its
// channel subscriptions and outbound message buffer. authSessionID is
// the user_sessions row that authorized this connection (empty for
// CLI-token-derived tickets); the per-connection revoke watcher uses
// it to detect server-side logout and force-close with the
// session_revoked frame.
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

// watchSessionRevocation polls user_sessions every 30s and force-closes
// the connection when the row goes revoked or expires. The frontend's
// use-websocket hook treats a "session_revoked" frame as terminal —
// stops retrying and emits the auth:session-expired event so the page
// hard-redirects to /login.
//
// Periodic-poll (rather than push) keeps the implementation independent
// of where the revocation came from (signOut on the same node, admin
// force-logout from a different process, password change, etc). The 30s
// cadence is the documented worst-case "how long until a revoked token
// stops working over WS"; tighten only if you find that's too lax.
//
// Critically — and this is what CodeRabbit caught — only an explicit
// "this session is revoked or expired" signal terminates the watcher.
// A transient sessions.Get failure (DB timeout under load, momentary
// network blip to a remote-DB deployment) MUST NOT close the WS:
// kicking users to /login on every backend hiccup defeats the whole
// "robust auth" goal. Log + skip + retry on the next tick.
func (c *Client) watchSessionRevocation() {
	if c.authSessionID == "" || c.hub == nil || c.hub.sessions == nil {
		return
	}
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(c.ctx, 5*time.Second)
			sess, err := c.hub.sessions.Get(ctx, c.authSessionID)
			cancel()

			// Definitively revoked: row is gone or revoked_at != NULL
			// or expires_at in the past. Close.
			revoked := false
			switch {
			case errors.Is(err, sessions.ErrNotFound):
				revoked = true
			case err == nil && sess != nil && !sess.Active(time.Now()):
				revoked = true
			case err != nil:
				// Transient — DB timeout, momentary unavailability.
				// Skip this tick; the next one will try again. The
				// connection stays up; the user keeps working. If
				// the row really IS revoked the very next tick will
				// see ErrNotFound and close cleanly.
				c.hub.logger.Debug("ws session-revoke poll: transient error, retrying next tick",
					"error", err, "sid", c.authSessionID)
				continue
			}

			if !revoked {
				continue
			}

			// Send the terminal frame, then close. safeSend dropping
			// is fine — readPump's defer will tear the connection
			// down on the next read failure.
			revokedFrame, _ := json.Marshal(ServerMessage{
				Type:    "session_revoked",
				Payload: map[string]string{"reason": "session_revoked"},
			})
			c.safeSend(revokedFrame)
			time.Sleep(50 * time.Millisecond) // give writePump a chance to flush
			c.conn.Close()
			return
		}
	}
}

func (c *Client) subscribe(channel string) {
	if channel == "" {
		return
	}

	// Validate channel access: deny by default when no authorizer is configured.
	if c.hub.channelAuth == nil {
		c.hub.logger.Warn("channel subscription denied: no authorizer configured", "user_id", c.userID, "channel", channel)
		resp, _ := json.Marshal(ServerMessage{
			Type:    "error",
			Channel: channel,
			Payload: map[string]string{"error": "access denied"},
		})
		c.safeSend(resp)
		return
	}
	if !c.hub.channelAuth.CanSubscribe(c.ctx, c.userID, channel) {
		c.hub.logger.Warn("channel subscription denied", "user_id", c.userID, "channel", channel)
		resp, _ := json.Marshal(ServerMessage{
			Type:    "error",
			Channel: channel,
			Payload: map[string]string{"error": "access denied"},
		})
		c.safeSend(resp)
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

type sendMessagePayload struct {
	ChatID  string `json:"session_id"`
	Content string `json:"content"`
	// MaxTurns caps the adapter agent loop for this run (`--max-turns` CLI
	// flag). 0/omitted leaves the adapter default in place.
	MaxTurns int `json:"max_turns,omitempty"`
}

func (c *Client) handleCancelMessage(msg ClientMessage) {
	var payload sendMessagePayload
	raw := msg.Payload
	if len(raw) > 0 && raw[0] == '"' {
		var unwrapped string
		if err := json.Unmarshal(raw, &unwrapped); err == nil {
			raw = json.RawMessage(unwrapped)
		}
	}
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

func (c *Client) handleSendMessage(msg ClientMessage) {
	c.hub.logger.Debug("handleSendMessage", "user_id", c.userID, "payload_len", len(msg.Payload))
	if c.hub.chatHandler == nil {
		resp, _ := json.Marshal(ServerMessage{
			Type:    "error",
			Channel: msg.Channel,
			Payload: map[string]string{"error": "chat not available"},
		})
		c.safeSend(resp)
		return
	}

	var payload sendMessagePayload
	raw := msg.Payload
	// Handle double-encoded payload (frontend sends JSON.stringify'd string)
	if len(raw) > 0 && raw[0] == '"' {
		var unwrapped string
		if err := json.Unmarshal(raw, &unwrapped); err == nil {
			raw = json.RawMessage(unwrapped)
		}
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		c.hub.logger.Debug("invalid send_message payload", "error", err, "payload_len", len(msg.Payload))
		resp, _ := json.Marshal(ServerMessage{
			Type:    "error",
			Channel: msg.Channel,
			Payload: map[string]string{"error": "invalid payload"},
		})
		c.safeSend(resp)
		return
	}

	if payload.ChatID == "" || payload.Content == "" {
		c.hub.logger.Debug("send_message missing fields", "chat_id_empty", payload.ChatID == "", "content_empty", payload.Content == "")
		resp, _ := json.Marshal(ServerMessage{
			Type:    "error",
			Channel: msg.Channel,
			Payload: map[string]string{"error": "session_id and content required"},
		})
		c.safeSend(resp)
		return
	}

	// Validate session access: user must be authorized for this chat's channel
	if c.hub.channelAuth != nil {
		sessionCh := "session:" + payload.ChatID
		if !c.hub.channelAuth.CanSubscribe(c.ctx, c.userID, sessionCh) {
			c.hub.logger.Warn("send_message denied: no access to session", "user_id", c.userID, "chat_id", payload.ChatID)
			resp, _ := json.Marshal(ServerMessage{
				Type:    "error",
				Channel: msg.Channel,
				Payload: map[string]string{"error": "access denied"},
			})
			c.safeSend(resp)
			return
		}
	}

	go func() {
		channel := "session:" + payload.ChatID

		// Create a cancellable context for this run
		runCtx, runCancel := context.WithCancel(c.ctx)
		defer runCancel()

		// Reject if a run is already in progress for this user+session
		cancelKey := c.userID + ":" + payload.ChatID
		c.hub.cancelMu.Lock()
		if _, exists := c.hub.cancelFns[cancelKey]; exists {
			c.hub.cancelMu.Unlock()
			errResp, _ := json.Marshal(ServerMessage{
				Type:    "error",
				Channel: channel,
				Payload: map[string]string{"error": "a message is already being processed"},
			})
			c.safeSend(errResp)
			return
		}
		c.hub.cancelFns[cancelKey] = runCancel
		c.hub.cancelMu.Unlock()
		defer func() {
			c.hub.cancelMu.Lock()
			delete(c.hub.cancelFns, cancelKey)
			c.hub.cancelMu.Unlock()
		}()

		streamFn := func(event ChatEvent) {
			msg := ServerMessage{
				Type:    "chat_event",
				Channel: channel,
				Payload: event,
			}
			resp, _ := json.Marshal(msg)
			c.safeSend(resp)

			c.hub.BroadcastExcept(channel, c, msg)
		}

		err := c.hub.chatHandler.HandleChatMessage(
			runCtx,
			c.userID,
			payload.ChatID,
			payload.Content,
			streamFn,
			ChatMessageOption{MaxTurns: payload.MaxTurns},
		)
		if err != nil {
			// Don't emit error if context was cancelled (user requested stop)
			if runCtx.Err() == context.Canceled {
				streamFn(ChatEvent{Type: "done", Content: ""})
				return
			}
			c.hub.logger.Error("chat message error", "error", err, "session_id", payload.ChatID)
			errResp, _ := json.Marshal(ServerMessage{
				Type:    "chat_event",
				Channel: channel,
				Payload: ChatEvent{Type: "error", Content: "an error occurred processing your message"},
			})
			c.safeSend(errResp)
		}
	}()
}
