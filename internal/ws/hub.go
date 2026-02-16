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
	Type    string `json:"type"` // "text", "tool_call", "thinking", "done", "error"
	Content string `json:"content"`
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

func (c *Client) safeSend(data []byte) bool {
	select {
	case c.send <- data:
		return true
	case <-c.ctx.Done():
		return false
	}
}

type sendMessagePayload struct {
	SessionID string `json:"session_id"`
	Content   string `json:"content"`
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
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		resp, _ := json.Marshal(ServerMessage{
			Type:    "error",
			Channel: msg.Channel,
			Payload: map[string]string{"error": "invalid payload"},
		})
		c.safeSend(resp)
		return
	}

	if payload.SessionID == "" || payload.Content == "" {
		resp, _ := json.Marshal(ServerMessage{
			Type:    "error",
			Channel: msg.Channel,
			Payload: map[string]string{"error": "session_id and content required"},
		})
		c.safeSend(resp)
		return
	}

	go func() {
		channel := "session:" + payload.SessionID

		streamFn := func(event ChatEvent) {
			resp, _ := json.Marshal(ServerMessage{
				Type:    "chat_event",
				Channel: channel,
				Payload: event,
			})
			c.safeSend(resp)

			c.hub.Broadcast(channel, ServerMessage{
				Type:    "chat_event",
				Channel: channel,
				Payload: event,
			})
		}

		err := c.hub.chatHandler.HandleChatMessage(
			c.ctx,
			c.userID,
			payload.SessionID,
			payload.Content,
			streamFn,
		)
		if err != nil {
			c.hub.logger.Error("chat message error", "error", err, "session_id", payload.SessionID)
			errResp, _ := json.Marshal(ServerMessage{
				Type:    "chat_event",
				Channel: channel,
				Payload: ChatEvent{Type: "error", Content: err.Error()},
			})
			c.safeSend(errResp)
		}
	}()
}
