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
			h.dispatch(msg.Channel, msg.Data, nil)
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
	data, ok := h.marshalFrame(msg)
	if !ok {
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
	data, ok := h.marshalFrame(msg)
	if !ok {
		return
	}
	h.dispatch(channel, data, func(c *Client) bool { return c != exclude })
}

func (h *Hub) marshalFrame(msg ServerMessage) ([]byte, bool) {
	data, err := json.Marshal(msg)
	if err != nil {
		h.logger.Error("broadcast marshal error", "error", err)
		return nil, false
	}
	return data, true
}

// dispatch is the canonical fan-out loop: iterate the channel's subscribers,
// apply filter (nil = accept all), attempt a non-blocking send, account for
// backpressure drops. Acquires the hub RLock; callers must not already hold it.
func (h *Hub) dispatch(channel string, data []byte, filter func(*Client) bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	subs, ok := h.channels[channel]
	if !ok {
		return
	}
	for client := range subs {
		if filter != nil && !filter(client) {
			continue
		}
		select {
		case client.send <- data:
		default:
			h.recordDrop(channel, client.userID)
		}
	}
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

// ChannelAuthorizer checks whether a user is allowed to subscribe to a given channel.
// Must be set via Hub.SetChannelAuthorizer before accepting connections.
type ChannelAuthorizer interface {
	CanSubscribe(ctx context.Context, userID, channel string) bool
}

// SetChannelAuthorizer sets the authorizer used to check channel subscription permissions.
func (h *Hub) SetChannelAuthorizer(auth ChannelAuthorizer) {
	h.channelAuth = auth
}
