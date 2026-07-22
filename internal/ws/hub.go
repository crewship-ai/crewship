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

// ChatMessageOption carries optional per-message run settings from the
// WebSocket frame (e.g. a `--max-turns` override). Passed variadically so
// existing callers stay source-compatible; only the WS dispatch supplies one.
type ChatMessageOption struct {
	// MaxTurns overrides the adapter agent-loop cap for this run. 0 = leave the
	// adapter default in place.
	MaxTurns int
}

// ChatHandler processes incoming chat messages from WebSocket clients and
// streams response events back via the provided callback.
type ChatHandler interface {
	HandleChatMessage(ctx context.Context, userID, sessionID, content string, streamFn func(event ChatEvent), opts ...ChatMessageOption) error
}

// ErrAgentBusy is returned (possibly wrapped) by ChatHandler.HandleChatMessage
// when the chat already has a live agent run (cross-user run exclusivity —
// see chatbridge.Bridge.tryMarkRunStart). The rejection carries NO stream
// frames: the handler must not emit anything through streamFn, because in
// production streamFn fans out on the shared session channel and an
// agent_busy/done pair there would reach every subscriber — the WINNING
// user's client would render the busy notice in its own transcript and
// treat the terminal done as its run ending (finalizing the live turn and
// unlocking the composer mid-generation). handleSendMessage maps this error
// to a private, sender-only agent_busy frame instead.
var ErrAgentBusy = errors.New("agent is already running for this chat")

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

	// streams holds per-session replay buffers so an agent run's output can be
	// resumed by a client that reconnects mid-generation. See session_stream.go.
	streams *sessionStreams

	// baseCtx is the hub's server-lifetime context, captured in Run. Agent runs
	// derive their context from THIS, not from the originating client socket, so
	// a client disconnect (navigating away, network blip) no longer cancels an
	// in-flight generation — the run finishes and persists server-side. Explicit
	// Stop still cancels via cancelFns. Guarded by baseCtxMu.
	baseCtx   context.Context
	baseCtxMu sync.RWMutex

	// Counts events dropped because a client's send buffer was full. The
	// channel-level dispatch (Broadcast, BroadcastExcept) uses a non-blocking
	// send to avoid one slow consumer stalling the whole hub; we lose the
	// frame in that case but still want it visible to ops. Logged at WARN on
	// each crossing of dropLogThreshold.
	droppedFrames  atomic.Uint64
	loggedDropMark atomic.Uint64

	// connSweepInFlight guards the shared connection sweep against
	// overlapping runs: the sweep executes on its own goroutine (so it
	// can never stall Run's select loop behind a slow DB), and if a
	// sweep is still running when the next tick fires, that tick is
	// skipped rather than stacking a second concurrent sweep.
	connSweepInFlight atomic.Bool

	// authReadTimeout bounds how long HandleUpgrade waits for the
	// post-upgrade auth message before closing an unauthenticated
	// connection. Defaults to wsAuthReadTimeout; tests shrink it directly
	// (unexported field, same package) to exercise the timeout path
	// without a real 10s wait.
	authReadTimeout time.Duration
}

// wsAuthReadTimeout is how long a freshly upgraded /ws connection has to
// send its {"type":"auth","token":...} message before the server closes
// it. Matches /ws/terminal's post-upgrade auth deadline
// (internal/terminal/handler.go).
const wsAuthReadTimeout = 10 * time.Second

// dropLogThreshold is how many dropped frames must accumulate between WARN
// log lines. Picked large enough that healthy traffic never logs and small
// enough that a real stuck client is noticed within a few seconds.
const dropLogThreshold = 16

// connSweepInterval is the cadence of the hub's shared per-connection sweep
// (sweepRevokedSessions + sweepChannelAuthorization) — matches the
// documented worst-case "how long until a revoked token / removed
// membership stops granting access over WS" the old per-connection poll
// carried.
const connSweepInterval = 30 * time.Second

// sweepQueryTimeout bounds every individual DB round-trip the sweep makes
// (sessions.Get, CanSubscribe). The sweep goroutine otherwise inherits the
// server-lifetime context, so without a per-query deadline one wedged
// query would pin the sweep (and, via the overlap guard, suppress all
// future ticks) for as long as the DB driver cares to block.
const sweepQueryTimeout = 5 * time.Second

// consecutiveDropsBeforeDisconnect is the per-client cutoff: once that
// many broadcasts in a row have failed to enqueue, dispatch treats the
// connection as stuck and tears it down asynchronously. The client's
// send buffer is 64, so this lets a full buffer drain twice before we
// give up — comfortably past any transient backpressure but short of
// "the consumer will eventually catch up." The frontend already
// auto-reconnects on close, so a healthy client just sees a brief
// blip; a genuinely stuck one stops costing us per-broadcast work.
const consecutiveDropsBeforeDisconnect = 128

// wsMaxInboundFrameBytes caps the size of a single inbound WebSocket
// frame the server is willing to read. 64 KiB is large enough for every
// legitimate ClientMessage we route (subscribe/unsubscribe/ping/
// send_message/cancel_message — all four-figure payloads at most) and
// small enough that an attacker can't use a single frame to amplify
// fan-out across all subscribers on the channel. See D5 in the
// 2026-05-14 chat/WS pentest agent report.
//
// An oversize frame isn't rejected gracefully: x/net/websocket returns
// ErrFrameTooLarge from Receive, readPump treats that as a plain read
// error, and the whole connection is torn down. The dashboard pre-flights
// outbound frames against WS_MAX_OUTBOUND_FRAME_BYTES (hooks/use-websocket.ts)
// so a large paste gets a client-side error instead of a silent
// disconnect — keep that constant comfortably under this one if it ever
// changes.
const wsMaxInboundFrameBytes = 64 * 1024

// ClientMessage is a JSON message received from a WebSocket client.
type ClientMessage struct {
	Type    string          `json:"type"`
	Channel string          `json:"channel,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// ServerMessage is a JSON message sent from the server to WebSocket clients.
//
// Seq is a per-session monotonic sequence number stamped on streamed chat
// events so a reconnecting client can replay the gap and reassemble events in
// order without duplicates (Last-Event-ID pattern). Zero/omitted for frames
// that aren't part of a resumable run stream (heartbeats, workspace/crew
// broadcasts, control frames).
type ServerMessage struct {
	Type    string      `json:"type"`
	Channel string      `json:"channel,omitempty"`
	Payload interface{} `json:"payload"`
	Seq     int64       `json:"seq,omitempty"`
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
		logger:          logger,
		chatHandler:     chatHandler,
		jwtValidator:    jwtValidator,
		sessions:        sessionsStore,
		clients:         make(map[*Client]bool),
		channels:        make(map[string]map[*Client]bool),
		register:        make(chan *Client),
		unregister:      make(chan *Client),
		broadcast:       make(chan ChannelMessage, 256),
		cancelFns:       make(map[string]context.CancelFunc),
		streams:         newSessionStreams(),
		authReadTimeout: wsAuthReadTimeout,
	}
}

// baseRunContext returns the context an agent run should derive from — the
// hub's server-lifetime context, so a client disconnect can't cancel the run.
// Falls back to context.Background() if Run hasn't captured a context yet
// (constructed-but-not-started hub, e.g. some unit tests).
func (h *Hub) baseRunContext() context.Context {
	h.baseCtxMu.RLock()
	ctx := h.baseCtx
	h.baseCtxMu.RUnlock()
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

// Run starts the hub's event loop, processing client registrations,
// unregistrations, and broadcast messages until ctx is cancelled.
func (h *Hub) Run(ctx context.Context) {
	h.logger.Info("websocket hub started")
	// Capture the server-lifetime context so agent runs derive from it rather
	// than from the client socket (see baseRunContext / handleSendMessage).
	h.baseCtxMu.Lock()
	h.baseCtx = ctx
	h.baseCtxMu.Unlock()

	// Sweep ended session-replay buffers past their grace TTL so a completed
	// run's buffer doesn't linger forever.
	sweepTicker := time.NewTicker(sessionStreamSweepInterval)
	defer sweepTicker.Stop()

	// Shared connection sweep: revocation (#1255 item 3) + channel
	// re-authorization (#1254 item 5). One ticker, two independent checks —
	// see sweepRevokedSessions / sweepChannelAuthorization. The tick only
	// STARTS the sweep (maybeStartConnSweep runs it on its own goroutine)
	// so DB latency in the checks can never stall register/unregister/
	// broadcast processing in this loop.
	connSweepTicker := time.NewTicker(connSweepInterval)
	defer connSweepTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			h.logger.Info("websocket hub stopping")
			return
		case <-sweepTicker.C:
			h.streams.sweep(time.Now())
		case <-connSweepTicker.C:
			h.maybeStartConnSweep(ctx)
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

// IsUserSubscribed reports whether any of userID's live connections is
// currently subscribed to the given channel (e.g. "session:<chatId>").
// The chat reply notifier uses this as its presence signal: a user
// watching the session live doesn't need an inbox "agent replied" item.
// Nil-safe so headless callers (tests, CLI-only servers) can hold a nil
// hub.
func (h *Hub) IsUserSubscribed(channel, userID string) bool {
	if h == nil {
		return false
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for client := range h.channels[channel] {
		if client.userID == userID {
			return true
		}
	}
	return false
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
//
// Slow-consumer policy: a successful enqueue resets the client's
// consecutiveDrops counter to zero, so isolated backpressure spikes
// have no lasting effect. A run of drops past consecutiveDropsBeforeDisconnect
// flips the client into "force-close on the next goroutine" mode and the
// disconnect is dispatched asynchronously so this loop never blocks on
// network teardown.
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
			client.consecutiveDrops.Store(0)
		default:
			h.recordDrop(channel, client.userID)
			if client.consecutiveDrops.Add(1) >= consecutiveDropsBeforeDisconnect {
				if client.disconnectFired.CompareAndSwap(false, true) {
					h.logger.Warn("websocket force-closing stuck consumer",
						"user_id", client.userID,
						"channel", channel,
						"consecutive_drops", consecutiveDropsBeforeDisconnect,
					)
					go client.forceDisconnect()
				}
			}
		}
	}
}

// forceDisconnect tears the WebSocket connection down so a stuck client
// stops receiving fan-out work. Closing c.conn unblocks readPump's
// websocket.Message.Receive call; readPump's defer then handles the
// hub.unregister send (which closes c.send, exiting writePump). The
// cancel() also releases anyone parked in safeSend's c.ctx.Done()
// select arm.
//
// Safe to call when c.conn is nil — that's the shape test fixtures
// build via newClient, and the disconnect should still cancel the
// context so test assertions can observe the trip.
func (c *Client) forceDisconnect() {
	c.cancel()
	if c.conn != nil {
		c.conn.Close()
	}
}

// maybeStartConnSweep launches one shared connection sweep on its own
// goroutine, unless the previous sweep is still running — then the tick
// is dropped (the next one retries). Reports whether a sweep was started,
// which tests use to exercise the overlap guard directly.
//
// The sweep MUST NOT run inline in Run's select loop: register/unregister
// are unbuffered channels, so any DB latency inside the sweep would
// freeze connection setup/teardown and broadcast dispatch for its
// duration — and a wedged DB query would freeze the hub outright.
func (h *Hub) maybeStartConnSweep(ctx context.Context) bool {
	if !h.connSweepInFlight.CompareAndSwap(false, true) {
		return false
	}
	go func() {
		defer h.connSweepInFlight.Store(false)
		h.sweepRevokedSessions(ctx)
		h.sweepChannelAuthorization(ctx)
	}()
	return true
}

// NotifySessionRevoked immediately force-disconnects every live client
// authenticated under the given user_sessions ID. This is the push half
// of revocation enforcement (#1255 item 3): the API layer calls it (via
// the notifying session store) the moment a Revoke lands, so logout /
// admin revoke / refresh-reuse detection close the socket in
// milliseconds with zero DB queries. The 30s sweep remains as the
// backstop for anything that mutates user_sessions without going
// through that chokepoint.
//
// Nil-safe (headless callers may hold a nil hub) and safe to call from
// any goroutine: the client snapshot is taken under RLock and the
// teardown path (closeRevokedClients → trySend/forceDisconnect) never
// blocks.
func (h *Hub) NotifySessionRevoked(sessionID string) {
	if h == nil || sessionID == "" {
		return
	}
	var matched []*Client
	h.mu.RLock()
	for c := range h.clients {
		if c.authSessionID == sessionID {
			matched = append(matched, c)
		}
	}
	h.mu.RUnlock()
	if len(matched) == 0 {
		return
	}
	h.logger.Info("ws closing connections for revoked session",
		"sid", sessionID, "connections", len(matched))
	h.closeRevokedClients(matched)
}

// NotifyUserRevoked immediately force-disconnects every live
// browser-session client belonging to userID — the push companion to
// sessions.Store.RevokeAllForUser (password change, forced Google
// re-auth). Clients with an empty authSessionID are left alone: those
// are CLI-token-derived connections whose auth artifact has its own
// revocation table, and RevokeAllForUser does not touch it.
func (h *Hub) NotifyUserRevoked(userID string) {
	if h == nil || userID == "" {
		return
	}
	var matched []*Client
	h.mu.RLock()
	for c := range h.clients {
		if c.userID == userID && c.authSessionID != "" {
			matched = append(matched, c)
		}
	}
	h.mu.RUnlock()
	if len(matched) == 0 {
		return
	}
	h.logger.Info("ws closing connections for revoked user sessions",
		"user_id", userID, "connections", len(matched))
	h.closeRevokedClients(matched)
}

// sweepRevokedSessions checks every DISTINCT authenticated session ID
// currently connected — once per tick, not once per connection — and
// force-disconnects every client registered under a session that comes
// back revoked or expired.
//
// This replaces the old per-connection watchSessionRevocation goroutine
// (#1255 item 3): a user with N tabs open under the same session drove N
// independent 30s polls of the identical user_sessions row (documented
// live at ~3k conns ≈ 100 q/s). Grouping by session ID means the query
// count now scales with distinct sessions, not connections — a user with
// 5 tabs open costs exactly the same one query per tick as a user with 1.
//
// Same transient-vs-definitive distinction as the watcher it replaces: a
// sessions.Get failure (DB timeout, momentary unavailability) MUST NOT
// disconnect anyone — only an explicit "not found" or "inactive" result
// does.
func (h *Hub) sweepRevokedSessions(ctx context.Context) {
	if h.sessions == nil {
		return
	}
	h.mu.RLock()
	bySession := make(map[string][]*Client)
	for c := range h.clients {
		if c.authSessionID != "" {
			bySession[c.authSessionID] = append(bySession[c.authSessionID], c)
		}
	}
	h.mu.RUnlock()

	for sid, clients := range bySession {
		qCtx, cancel := context.WithTimeout(ctx, sweepQueryTimeout)
		sess, err := h.sessions.Get(qCtx, sid)
		cancel()

		revoked := false
		switch {
		case errors.Is(err, sessions.ErrNotFound):
			revoked = true
		case err == nil && sess != nil && !sess.Active(time.Now()):
			revoked = true
		case err != nil:
			// Transient — DB timeout, momentary unavailability. Skip this
			// tick; the next one retries. If the session really IS revoked
			// the next tick sees ErrNotFound and closes cleanly.
			h.logger.Debug("ws session-revoke sweep: transient error, retrying next tick",
				"error", err, "sid", sid)
			continue
		}
		if !revoked {
			continue
		}
		h.closeRevokedClients(clients)
	}
}

// closeRevokedClients sends the terminal session_revoked frame to every
// client under a just-revoked session, then force-disconnects each after a
// brief delay so writePump has a chance to flush the frame first — mirrors
// the old per-connection watcher's behaviour, just fanned out to every
// client sharing the session instead of one.
//
// The frame goes out via trySend (non-blocking), not safeSend: this runs
// on the sweep/notify goroutine, and a client with a full send buffer
// would park a blocking send until its buffer drains — stalling the
// courtesy frame for every OTHER revoked client behind it. Losing the
// frame on a saturated connection is fine; the force-disconnect that
// follows is the enforcement.
func (h *Hub) closeRevokedClients(clients []*Client) {
	revokedFrame, _ := json.Marshal(ServerMessage{
		Type:    "session_revoked",
		Payload: map[string]string{"reason": "session_revoked"},
	})
	for _, c := range clients {
		c.trySend(revokedFrame)
	}
	go func(clients []*Client) {
		time.Sleep(50 * time.Millisecond)
		for _, c := range clients {
			c.forceDisconnect()
		}
	}(clients)
}

// sweepChannelAuthorization re-verifies CanSubscribe for every client's
// currently-subscribed channels once per tick, unsubscribing (and notifying
// the client with an error frame) for any channel that no longer passes.
//
// Without this, CanSubscribe is enforced only at subscribe/resume/send time
// (#1254 item 5): a user removed from a workspace — membership change is a
// distinct event from session revocation, so the revocation sweep above
// doesn't cover it — kept receiving that workspace's broadcasts for the
// rest of the connection's lifetime. This closes that gap on the same
// cadence as the revocation sweep rather than adding a second ticker.
//
// Deny vs. error: only a DEFINITIVE deny (false, nil) revokes the
// subscription. A CanSubscribe error (DB hiccup, per-query timeout) skips
// the channel and retries next tick — the production authorizer fails
// closed on any error, so treating "check failed" as "access removed"
// would strip EVERY subscription from EVERY client on one transient DB
// blip, and the frontend does not re-subscribe without a reconnect.
// Subscribe-time checks still fail closed on error; only this
// take-access-away path needs the distinction.
func (h *Hub) sweepChannelAuthorization(ctx context.Context) {
	if h.channelAuth == nil {
		return
	}
	h.mu.RLock()
	clients := make([]*Client, 0, len(h.clients))
	for c := range h.clients {
		clients = append(clients, c)
	}
	h.mu.RUnlock()

	for _, c := range clients {
		c.mu.Lock()
		chans := make([]string, 0, len(c.channels))
		for ch := range c.channels {
			chans = append(chans, ch)
		}
		c.mu.Unlock()

		for _, ch := range chans {
			qCtx, cancel := context.WithTimeout(ctx, sweepQueryTimeout)
			ok, err := h.channelAuth.CanSubscribe(qCtx, c.userID, ch)
			cancel()
			if err != nil {
				// Transient — keep the subscription, retry next tick. If
				// access really was removed, a later tick returns a clean
				// (false, nil) and revokes it then.
				h.logger.Debug("ws channel re-check: transient error, retrying next tick",
					"error", err, "user_id", c.userID, "channel", ch)
				continue
			}
			if ok {
				continue
			}
			c.unsubscribe(ch)
			c.trySendError(ch, "access revoked")
			h.logger.Info("ws subscription revoked by periodic re-check",
				"user_id", c.userID, "channel", ch)
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

// HandleUpgrade upgrades the HTTP connection to a WebSocket, then
// authenticates via the first post-upgrade message rather than a URL query
// parameter — a `?token=` carries a PII-bearing 15-min JWE into proxy/access
// logs, browser history, and Referer headers. Mirrors /ws/terminal
// (internal/terminal/handler.go ServeHTTP), which moved to this pattern
// first. The Origin/CSRF handshake check below is unrelated to auth and
// still runs pre-upgrade, same as before.
func (h *Hub) HandleUpgrade(w http.ResponseWriter, r *http.Request) {
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
					// CREWSHIP_ALLOWED_ORIGINS — the same operator
					// allowlist the HTTP layer's EnforceOrigin consults
					// (internal/api/origin_check.go) — extends to the
					// WS upgrade, production included. Without it a
					// desktop shell (Origin: tauri://localhost) could
					// make every authed HTTP call but never open the
					// realtime socket.
					if wsOriginAllowlisted(origin) {
						return nil
					}
					if isProduction || (originHost != "localhost" && originHost != "127.0.0.1") {
						return fmt.Errorf("origin %q not allowed for host %q", origin, req.Host)
					}
				}
			}
			return nil
		},
		Handler: func(conn *websocket.Conn) {
			// Cap inbound frame size at 64 KiB. The WS protocol allows
			// arbitrarily large messages, but our use cases — subscribe,
			// unsubscribe, ping, send_message, cancel_message — all fit
			// well under 4 KiB in practice. A 1 MB JSON sent on
			// `send_message` would otherwise be parsed and then fanned
			// out to every other subscriber on the channel, an
			// N-amplifier on memory and bandwidth (D5 from the chat/WS
			// pentest agent). The x/net/websocket Conn returns
			// ErrFrameTooLarge from Receive when a frame exceeds the
			// limit; readPump treats that as a normal read error and
			// closes the connection.
			conn.MaxPayloadBytes = wsMaxInboundFrameBytes

			userID, authSessionID, authOK := h.authenticateUpgradedConn(r, conn)
			if !authOK {
				conn.Close()
				return
			}

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
				writeWait:     defaultWriteWait,
			}

			h.register <- client

			// Session revocation is enforced by the hub's shared sweep
			// (sweepRevokedSessions), not a per-connection goroutine — see
			// its doc comment.
			go client.writePump()
			client.readPump()
		},
	}
	wsServer.ServeHTTP(w, r)
}

// authenticateUpgradedConn reads the first message off a freshly upgraded
// connection, expects {"type":"auth","token":"..."}, and validates it the
// same way the old pre-upgrade path did: JWT signature/expiry, then (for
// browser tickets carrying a sid) that the joined user_sessions row is
// still active. CLI-derived tickets have no sid — their CLI token is the
// auth artifact, with its own revocation table — so they skip that check.
//
// Returns ok=false on any failure, having already sent a client-visible
// frame (session_revoked or a generic error) and left the connection ready
// for the caller to Close. Never registers a client on failure.
func (h *Hub) authenticateUpgradedConn(r *http.Request, conn *websocket.Conn) (userID, authSessionID string, ok bool) {
	timeout := h.authReadTimeout
	if timeout <= 0 {
		timeout = wsAuthReadTimeout
	}
	if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		h.logger.Warn("ws set auth read deadline", "error", err)
		return "", "", false
	}

	var raw []byte
	if err := websocket.Message.Receive(conn, &raw); err != nil {
		h.logger.Debug("ws auth read failed (no auth message within deadline)", "error", err)
		return "", "", false
	}

	var authMsg struct {
		Type  string `json:"type"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(raw, &authMsg); err != nil || authMsg.Type != "auth" || authMsg.Token == "" {
		h.sendWSAuthFrame(conn, "error", "invalid auth message")
		return "", "", false
	}

	// jwtValidator and sessions are guaranteed non-nil by NewHub. No
	// need to defend; the nil-check noise here was the original sin
	// that made revoke-on-WS optional.
	claims, err := h.jwtValidator.ValidateWS(authMsg.Token)
	if err != nil {
		h.logger.Warn("ws auth failed", "error", err)
		h.sendWSAuthFrame(conn, "error", "invalid token")
		return "", "", false
	}
	userID = claims.ID
	authSessionID = claims.Sid

	// Browser tickets carry a sid that joins to user_sessions; refuse the
	// connection if the row is gone or already revoked. CLI-derived
	// tickets skip this check (see doc comment above).
	if authSessionID != "" {
		sess, sErr := h.sessions.Get(r.Context(), authSessionID)
		if sErr != nil {
			if errors.Is(sErr, sessions.ErrNotFound) {
				h.sendWSAuthFrame(conn, "session_revoked", "session_revoked")
				return "", "", false
			}
			h.logger.Error("ws session lookup", "error", sErr)
			h.sendWSAuthFrame(conn, "error", "internal error")
			return "", "", false
		}
		if !sess.Active(time.Now()) {
			h.sendWSAuthFrame(conn, "session_revoked", "session_revoked")
			return "", "", false
		}
	}

	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		h.logger.Warn("ws clear auth read deadline", "error", err)
		return "", "", false
	}
	return userID, authSessionID, true
}

// sendWSAuthFrame best-effort writes a ServerMessage frame to a connection
// that hasn't been registered as a Client yet (so writePump/c.send aren't
// available). typ "session_revoked" mirrors watchSessionRevocation's
// mid-session frame (client.go) so the frontend's existing
// type==="session_revoked" handling covers both connect-time and
// mid-session revocation identically. conn.Write (not
// websocket.Message.Send with a []byte, which forces a binary frame) keeps
// this a text frame, matching every other JSON frame this hub sends.
func (h *Hub) sendWSAuthFrame(conn *websocket.Conn, typ, message string) {
	data, err := json.Marshal(ServerMessage{Type: typ, Payload: map[string]string{"message": message}})
	if err != nil {
		return
	}
	_, _ = conn.Write(data)
}

// ChannelAuthorizer checks whether a user is allowed to subscribe to a given channel.
// Must be set via Hub.SetChannelAuthorizer before accepting connections.
//
// CanSubscribe returns (allowed, err). A definitive verdict has err ==
// nil; a non-nil err means the check itself failed (infrastructure
// trouble) and allowed is meaningless. Grant paths (subscribe/resume/
// send) treat error as deny — fail closed. The periodic re-authorization
// sweep treats error as "keep the existing subscription and retry" —
// see sweepChannelAuthorization for why the asymmetry matters.
type ChannelAuthorizer interface {
	CanSubscribe(ctx context.Context, userID, channel string) (bool, error)
}

// SetChannelAuthorizer sets the authorizer used to check channel subscription permissions.
func (h *Hub) SetChannelAuthorizer(auth ChannelAuthorizer) {
	h.channelAuth = auth
}

// wsOriginAllowlisted reports whether origin exactly matches an entry in
// CREWSHIP_ALLOWED_ORIGINS. Matching mirrors internal/api/origin_check.go:
// exact string equality after trimming whitespace and a trailing slash —
// no suffix/host tricks (those enabled bypasses on the HTTP side and were
// deliberately removed there). The env var is read per handshake, not at
// package init, so tests can t.Setenv and operators can restart-free tools
// that re-exec the server pick up changes; one env read per connection is
// noise next to the JWT validation above.
func wsOriginAllowlisted(origin string) bool {
	raw := os.Getenv("CREWSHIP_ALLOWED_ORIGINS")
	if strings.TrimSpace(raw) == "" {
		return false
	}
	origin = strings.TrimRight(strings.TrimSpace(origin), "/")
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimRight(strings.TrimSpace(item), "/")
		// EqualFold, not ==, to stay in lockstep with the HTTP guard
		// (origin_check.go originAllowed) — scheme/host are case-
		// insensitive per RFC 3986, and a case-sensitive matcher here
		// would silently reject an allowlist entry the HTTP side accepts.
		if item != "" && strings.EqualFold(item, origin) {
			return true
		}
	}
	return false
}
