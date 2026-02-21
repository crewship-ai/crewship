package ws

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"golang.org/x/net/websocket"
)

type ChatHandler interface {
	HandleChatMessage(ctx context.Context, userID, sessionID, content string, streamFn func(event ChatEvent)) error
}

type ChatEvent struct {
	Type     string `json:"type"`                // "text", "tool_call", "tool_result", "thinking", "status", "done", "error"
	Content  string `json:"content"`
	Metadata any    `json:"metadata,omitempty"`   // structured data for tool calls, etc.
}

type Hub struct {
	logger       *slog.Logger
	chatHandler  ChatHandler
	jwtValidator *auth.JWTValidator
	clients      map[*Client]bool
	channels    map[string]map[*Client]bool
	register    chan *Client
	unregister  chan *Client
	broadcast   chan ChannelMessage
	mu          sync.RWMutex
	connCount   atomic.Int64
	cancelFns   map[string]context.CancelFunc // session_id -> cancel function for active runs
	cancelMu    sync.Mutex
}

type Client struct {
	conn     *websocket.Conn
	hub      *Hub
	userID   string
	channels map[string]bool
	send     chan []byte
	ctx      context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex // protects channels map
}

type ClientMessage struct {
	Type    string          `json:"type"`
	Channel string          `json:"channel,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

type ServerMessage struct {
	Type    string      `json:"type"`
	Channel string      `json:"channel,omitempty"`
	Payload interface{} `json:"payload"`
}

type ChannelMessage struct {
	Channel string
	Data    []byte
}

func NewHub(logger *slog.Logger, chatHandler ChatHandler, deps ...interface{}) *Hub {
	h := &Hub{
		logger:      logger,
		chatHandler: chatHandler,
		clients:     make(map[*Client]bool),
		channels:    make(map[string]map[*Client]bool),
		register:    make(chan *Client),
		unregister:  make(chan *Client),
		broadcast:   make(chan ChannelMessage, 256),
		cancelFns:   make(map[string]context.CancelFunc),
	}
	for _, d := range deps {
		if v, ok := d.(*auth.JWTValidator); ok {
			h.jwtValidator = v
		}
	}
	return h
}

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
					}
				}
			}
			h.mu.RUnlock()
		}
	}
}

func (h *Hub) ConnectionCount() int {
	return int(h.connCount.Load())
}

func (h *Hub) SetChatHandler(handler ChatHandler) {
	h.chatHandler = handler
}

func (h *Hub) Broadcast(channel string, msg ServerMessage) {
	data, err := json.Marshal(msg)
	if err != nil {
		h.logger.Error("broadcast marshal error", "error", err)
		return
	}
	h.broadcast <- ChannelMessage{Channel: channel, Data: data}
}

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
			}
		}
	}
	h.mu.RUnlock()
}

func (h *Hub) HandleUpgrade(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		http.Error(w, "missing token", http.StatusUnauthorized)
		return
	}

	if h.jwtValidator == nil {
		h.logger.Error("ws auth not configured")
		http.Error(w, "ws auth not configured", http.StatusServiceUnavailable)
		return
	}

	claims, err := h.jwtValidator.Validate(token)
	if err != nil {
		h.logger.Warn("ws auth failed", "error", err)
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}
	userID := claims.ID

	wsServer := websocket.Server{
		Handler: func(conn *websocket.Conn) {
			ctx, cancel := context.WithCancel(context.Background())
			client := &Client{
				conn:     conn,
				hub:      h,
				userID:   userID,
				channels: make(map[string]bool),
				send:     make(chan []byte, 64),
				ctx:      ctx,
				cancel:   cancel,
			}

			h.register <- client

			go client.writePump()
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
			resp, _ := json.Marshal(ServerMessage{Type: "pong", Payload: nil})
			c.send <- resp
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
			ping, _ := json.Marshal(ServerMessage{Type: "ping", Payload: nil})
			if _, err := c.conn.Write(ping); err != nil {
				return
			}
		}
	}
}

func (c *Client) subscribe(channel string) {
	if channel == "" {
		return
	}
	// TODO: validate channel access (team membership check via IPC)
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
	ChatID string `json:"session_id"`
	Content   string `json:"content"`
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

	c.hub.cancelMu.Lock()
	cancel, ok := c.hub.cancelFns[payload.ChatID]
	c.hub.cancelMu.Unlock()

	if ok {
		c.hub.logger.Info("cancelling run", "session_id", payload.ChatID, "user_id", c.userID)
		cancel()
	}
}

func (c *Client) handleSendMessage(msg ClientMessage) {
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
		resp, _ := json.Marshal(ServerMessage{
			Type:    "error",
			Channel: msg.Channel,
			Payload: map[string]string{"error": "session_id and content required"},
		})
		c.safeSend(resp)
		return
	}

	go func() {
		channel := "session:" + payload.ChatID

		// Create a cancellable context for this run
		runCtx, runCancel := context.WithCancel(c.ctx)
		defer runCancel()

		// Reject if a run is already in progress for this session
		c.hub.cancelMu.Lock()
		if _, exists := c.hub.cancelFns[payload.ChatID]; exists {
			c.hub.cancelMu.Unlock()
			errResp, _ := json.Marshal(ServerMessage{
				Type:    "error",
				Channel: channel,
				Payload: map[string]string{"error": "a message is already being processed"},
			})
			c.safeSend(errResp)
			return
		}
		c.hub.cancelFns[payload.ChatID] = runCancel
		c.hub.cancelMu.Unlock()
		defer func() {
			c.hub.cancelMu.Lock()
			delete(c.hub.cancelFns, payload.ChatID)
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
