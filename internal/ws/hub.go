package ws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"golang.org/x/net/websocket"
)

// ChatHandler processes incoming chat messages from WebSocket clients and
// streams response events back via the provided callback.
type ChatHandler interface {
	HandleChatMessage(ctx context.Context, userID, sessionID, content string, streamFn func(event ChatEvent)) error
}

// ChatEvent is a streaming event sent from an agent run back to the client,
// such as text output, tool calls, thinking steps, or completion signals.
type ChatEvent struct {
	Type     string `json:"type"` // "text", "tool_call", "tool_result", "thinking", "status", "done", "error", "result", "system"
	Content  string `json:"content"`
	Metadata any    `json:"metadata,omitempty"` // structured data for tool calls, cost/usage, session init, etc.
}

// Hub manages WebSocket connections, channel subscriptions, and message
// broadcasting. It authenticates clients via JWT and routes chat messages
// to the configured ChatHandler.
type Hub struct {
	logger       *slog.Logger
	chatHandler  ChatHandler
	jwtValidator *auth.JWTValidator
	sessions     sessions.Store
	clients      map[*Client]bool
	channels     map[string]map[*Client]bool
	register     chan *Client
	unregister   chan *Client
	broadcast    chan ChannelMessage
	mu           sync.RWMutex
	connCount    atomic.Int64
	cancelFns    map[string]context.CancelFunc // session_id -> cancel function for active runs
	cancelMu     sync.Mutex
	channelAuth  ChannelAuthorizer

	// Counts events dropped because a client's send buffer was full. The
	// channel-level dispatch (Broadcast, BroadcastExcept) uses a non-blocking
	// send to avoid one slow consumer stalling the whole hub; we lose the
	// frame in that case but still want it visible to ops. Logged at WARN on
	// each crossing of dropLogThreshold.
	droppedFrames  atomic.Uint64
	loggedDropMark atomic.Uint64
}

// dropLogThreshold is how many dropped frames must accumulate between WARN
// log lines. Picked large enough that healthy traffic never logs and small
// enough that a real stuck client is noticed within a few seconds.
const dropLogThreshold = 16

// Client represents a single authenticated WebSocket connection with its
// channel subscriptions and outbound message buffer. authSessionID is
// the user_sessions row that authorized this connection (empty for
// CLI-token-derived tickets); the per-connection revoke watcher uses
// it to detect server-side logout and force-close with the
// session_revoked frame.
type Client struct {
	conn          *websocket.Conn
	hub           *Hub
	userID        string
	authSessionID string
	channels      map[string]bool
	send          chan []byte
	ctx           context.Context
	cancel        context.CancelFunc
	mu            sync.Mutex // protects channels map
}

// ClientMessage is a JSON message received from a WebSocket client.
type ClientMessage struct {
	Type    string          `json:"type"`
	Channel string          `json:"channel,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// ServerMessage is a JSON message sent from the server to WebSocket clients.
type ServerMessage struct {
	Type    string      `json:"type"`
	Channel string      `json:"channel,omitempty"`
	Payload interface{} `json:"payload"`
}

// ChannelMessage is an internal message routed to all subscribers of a channel.
type ChannelMessage struct {
	Channel string
	Data    []byte
}

// Pre-marshaled heartbeat frames. These payloads never vary, so we pay the
// JSON encoding cost once at init instead of on every ping/pong event.
var (
	pingMessageBytes = mustMarshalServerMessage("ping")
	pongMessageBytes = mustMarshalServerMessage("pong")
)

func mustMarshalServerMessage(typ string) []byte {
	b, err := json.Marshal(ServerMessage{Type: typ, Payload: nil})
	if err != nil {
		panic("ws: marshal " + typ + " message: " + err.Error())
	}
	return b
}

// NopValidatorForTests is a sentinel JWT validator suitable for tests
// that exercise hub plumbing (broadcast routing, channel auth, chat
// dispatch) without exercising the upgrade path. Production code MUST
// NOT use this — its key is a fixed test secret.
var NopValidatorForTests = mustNopValidator()

func mustNopValidator() *auth.JWTValidator {
	v, err := auth.NewJWTValidator("hub-test-nop-secret-of-sufficient-length")
	if err != nil {
		panic(fmt.Errorf("ws.NopValidatorForTests: %w", err))
	}
	return v
}

// NopSessionsForTests is a sessions.Store stub for the same purpose as
// NopValidatorForTests. Get always returns ErrNotFound, all writes are
// no-ops. Tests that need a working sessions row construct their own
// *sessions.DBStore against an in-memory DB.
var NopSessionsForTests sessions.Store = &nopHubSessions{}

type nopHubSessions struct{}

func (*nopHubSessions) Create(ctx context.Context, userID, ua, ip string, ttl time.Duration) (*sessions.Session, error) {
	return nil, errors.New("nop store: Create not supported in tests")
}
func (*nopHubSessions) Get(ctx context.Context, id string) (*sessions.Session, error) {
	return nil, sessions.ErrNotFound
}
func (*nopHubSessions) ListActiveForUser(ctx context.Context, userID string) ([]*sessions.Session, error) {
	return nil, nil
}
func (*nopHubSessions) Revoke(ctx context.Context, id, reason string) error { return nil }
func (*nopHubSessions) RevokeAllForUser(ctx context.Context, userID, reason string) (int64, error) {
	return 0, nil
}
func (*nopHubSessions) TouchLastUsed(ctx context.Context, id string) error { return nil }
func (*nopHubSessions) RotateRefreshJti(ctx context.Context, id, expected, next string) error {
	return nil
}
func (*nopHubSessions) SetClock(fn func() time.Time) {}

// NewHub creates a WebSocket hub. The JWT validator and sessions store
// are required positional parameters — the previous variadic deps bag
// allowed nil sessions which silently downgraded the upgrade path to
// "valid signature only", letting revoked tickets reconnect until the
// 15-min ticket TTL expired. Make the dependency contract explicit.
//
// chatHandler is allowed to be nil at construction time: server.go
// wires the hub before the orchestrator is ready; SetChatHandler
// fills it in later. Auth dependencies don't have that order problem
// — they're always available before the hub starts.
func NewHub(logger *slog.Logger, chatHandler ChatHandler, jwtValidator *auth.JWTValidator, sessionsStore sessions.Store) *Hub {
	if logger == nil {
		panic("ws.NewHub: logger required")
	}
	if jwtValidator == nil {
		panic("ws.NewHub: jwtValidator required")
	}
	if sessionsStore == nil {
		panic("ws.NewHub: sessionsStore required")
	}
	return &Hub{
		logger:       logger,
		chatHandler:  chatHandler,
		jwtValidator: jwtValidator,
		sessions:     sessionsStore,
		clients:      make(map[*Client]bool),
		channels:     make(map[string]map[*Client]bool),
		register:     make(chan *Client),
		unregister:   make(chan *Client),
		broadcast:    make(chan ChannelMessage, 256),
		cancelFns:    make(map[string]context.CancelFunc),
	}
}

// Run starts the hub's event loop, processing client registrations,
// unregistrations, and broadcast messages until ctx is cancelled.
func (h *Hub) Run(ctx context.Context) {
	h.logger.Info("websocket hub started")
	for {
		select {
		case <-ctx.Done():
			h.logger.Info("websocket hub stopping")
			return
		case client := <-h.register:
			h.mu.Lock()
			h.clients[client] = true
			h.mu.Unlock()
			h.connCount.Add(1)
			h.logger.Debug("client connected", "user_id", client.userID)
		case client := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				client.mu.Lock()
				for ch := range client.channels {
					if subs, ok := h.channels[ch]; ok {
						delete(subs, client)
						if len(subs) == 0 {
							delete(h.channels, ch)
						}
					}
				}
				client.mu.Unlock()
				close(client.send)
				h.connCount.Add(-1)
				h.logger.Debug("client disconnected", "user_id", client.userID)
			}
			h.mu.Unlock()
		case msg := <-h.broadcast:
			h.mu.RLock()
			if subs, ok := h.channels[msg.Channel]; ok {
				for client := range subs {
					select {
					case client.send <- msg.Data:
					default:
						h.recordDrop(msg.Channel, client.userID)
					}
				}
			}
			h.mu.RUnlock()
		}
	}
}

// ConnectionCount returns the number of currently connected WebSocket clients.
func (h *Hub) ConnectionCount() int {
	return int(h.connCount.Load())
}

// SetChatHandler replaces the current chat message handler.
func (h *Hub) SetChatHandler(handler ChatHandler) {
	h.chatHandler = handler
}

// Broadcast sends a message to all clients subscribed to the given channel.
func (h *Hub) Broadcast(channel string, msg ServerMessage) {
	data, err := json.Marshal(msg)
	if err != nil {
		h.logger.Error("broadcast marshal error", "error", err)
		return
	}
	h.broadcast <- ChannelMessage{Channel: channel, Data: data}
}

// BroadcastChannel sends a WebSocket event on the "prefix:id" channel
// (e.g. "workspace:abc", "crew:xyz", "mission:m_1"). No-op if h is nil.
func (h *Hub) BroadcastChannel(prefix, id, eventType string, payload any) {
	if h == nil {
		return
	}
	channel := prefix + ":" + id
	h.Broadcast(channel, ServerMessage{
		Type:    eventType,
		Channel: channel,
		Payload: payload,
	})
}

// BroadcastWorkspace is a shortcut for BroadcastChannel("workspace", wsID, ...).
func (h *Hub) BroadcastWorkspace(wsID, eventType string, payload any) {
	h.BroadcastChannel("workspace", wsID, eventType, payload)
}

// BroadcastExcept sends a message to all channel subscribers except the excluded client.
func (h *Hub) BroadcastExcept(channel string, exclude *Client, msg ServerMessage) {
	data, err := json.Marshal(msg)
	if err != nil {
		h.logger.Error("broadcast marshal error", "error", err)
		return
	}
	h.mu.RLock()
	if subs, ok := h.channels[channel]; ok {
		for client := range subs {
			if client == exclude {
				continue
			}
			select {
			case client.send <- data:
			default:
				h.recordDrop(channel, client.userID)
			}
		}
	}
	h.mu.RUnlock()
}

// recordDrop increments the dropped-frame counter and emits a WARN log
// roughly every dropLogThreshold drops, so a slow or stuck consumer can be
// noticed without spamming for every single non-blocking send miss.
func (h *Hub) recordDrop(channel, userID string) {
	total := h.droppedFrames.Add(1)
	lastLogged := h.loggedDropMark.Load()
	if total-lastLogged < dropLogThreshold {
		return
	}
	if !h.loggedDropMark.CompareAndSwap(lastLogged, total) {
		return
	}
	h.logger.Warn("websocket broadcast dropped — slow consumer",
		"channel", channel,
		"user_id", userID,
		"dropped_total", total,
	)
}

// DroppedFrames returns the cumulative number of WebSocket broadcast frames
// that were silently dropped because a client's send buffer was full.
// Exposed for tests and the runtime stats endpoint.
func (h *Hub) DroppedFrames() uint64 {
	return h.droppedFrames.Load()
}

// HandleUpgrade authenticates the JWT token from the query parameter and
// upgrades the HTTP connection to a WebSocket.
func (h *Hub) HandleUpgrade(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		http.Error(w, "missing token", http.StatusUnauthorized)
		return
	}

	// jwtValidator and sessions are guaranteed non-nil by NewHub. No
	// need to defend; the nil-check noise here was the original sin
	// that made revoke-on-WS optional.

	claims, err := h.jwtValidator.ValidateWS(token)
	if err != nil {
		h.logger.Warn("ws auth failed", "error", err)
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}
	userID := claims.ID
	authSessionID := claims.Sid

	// Browser tickets carry a sid that joins to user_sessions; refuse
	// the upgrade if the row is gone or already revoked. CLI-derived
	// tickets have no sid (their CLI token is the auth artifact, with
	// its own revocation table) — those skip this check.
	if authSessionID != "" {
		sess, sErr := h.sessions.Get(r.Context(), authSessionID)
		if sErr != nil {
			if errors.Is(sErr, sessions.ErrNotFound) {
				http.Error(w, "session_revoked", http.StatusUnauthorized)
				return
			}
			h.logger.Error("ws session lookup", "error", sErr)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if !sess.Active(time.Now()) {
			http.Error(w, "session_revoked", http.StatusUnauthorized)
			return
		}
	}

	wsServer := websocket.Server{
		Handshake: func(config *websocket.Config, req *http.Request) error {
			// Validate that Origin header, when present, matches the
			// Host header's hostname. This prevents cross-site WebSocket
			// hijacking while allowing dev proxies and SSH tunnels (which
			// typically don't set Origin or set it to the same host).
			//
			// Audit L1: the localhost / 127.0.0.1 bypass is harmless in
			// dev (browsers running on the same machine as the binary
			// legitimately have Origin=http://localhost:PORT) but is a
			// loosening in production where every real client is on the
			// public hostname. We gate the bypass on CREWSHIP_ENV so a
			// prod deployment refuses Origin=localhost regardless of
			// what the request looks like.
			isProduction := strings.EqualFold(os.Getenv("CREWSHIP_ENV"), "production")
			origin := req.Header.Get("Origin")
			if origin != "" {
				u, err := url.Parse(origin)
				if err != nil {
					return fmt.Errorf("invalid origin: %w", err)
				}
				host := req.Host
				// Strip port from host for comparison (dev proxies
				// often use different ports on the same hostname).
				if h, _, err := net.SplitHostPort(host); err == nil {
					host = h
				}
				originHost := u.Hostname()
				if originHost != host {
					if isProduction || (originHost != "localhost" && originHost != "127.0.0.1") {
						return fmt.Errorf("origin %q not allowed for host %q", origin, req.Host)
					}
				}
			}
			return nil
		},
		Handler: func(conn *websocket.Conn) {
			ctx, cancel := context.WithCancel(context.Background())
			client := &Client{
				conn:          conn,
				hub:           h,
				userID:        userID,
				authSessionID: authSessionID,
				channels:      make(map[string]bool),
				send:          make(chan []byte, 64),
				ctx:           ctx,
				cancel:        cancel,
			}

			h.register <- client

			go client.writePump()
			if authSessionID != "" {
				go client.watchSessionRevocation()
			}
			client.readPump()
		},
	}
	wsServer.ServeHTTP(w, r)
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
			if _, err := c.conn.Write(msg); err != nil {
				return
			}
		case <-ticker.C:
			if _, err := c.conn.Write(pingMessageBytes); err != nil {
				return
			}
		}
	}
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

// ChannelAuthorizer checks whether a user is allowed to subscribe to a given channel.
// Must be set via Hub.SetChannelAuthorizer before accepting connections.
type ChannelAuthorizer interface {
	CanSubscribe(ctx context.Context, userID, channel string) bool
}

// SetChannelAuthorizer sets the authorizer used to check channel subscription permissions.
func (h *Hub) SetChannelAuthorizer(auth ChannelAuthorizer) {
	h.channelAuth = auth
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
