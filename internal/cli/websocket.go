package cli

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"

	"golang.org/x/net/websocket"
)

// WSClient is a WebSocket client for streaming chat events from the server.
type WSClient struct {
	conn   *websocket.Conn
	mu     sync.Mutex
	closed bool
}

// WSMessage is a JSON-encoded WebSocket message exchanged with the server.
type WSMessage struct {
	Type    string          `json:"type"`
	Channel string          `json:"channel,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// ChatEventPayload is the payload of a chat_event WebSocket message.
type ChatEventPayload struct {
	Type     string `json:"type"`
	Content  string `json:"content"`
	Metadata any    `json:"metadata,omitempty"`
}

// NewWSClient connects to the server's WebSocket endpoint, then
// authenticates by sending {"type":"auth","token":...} as the first
// message on the connection — mirroring internal/ws/hub.go
// authenticateUpgradedConn. The token no longer rides the dial URL (a
// `?token=<jwt>` query string used to leak the WS ticket into proxy/access
// logs and any dial-error string); the URL and Origin below carry no
// secret, so a dial failure can be returned as-is.
func NewWSClient(serverURL, token string) (*WSClient, error) {
	u, err := url.Parse(serverURL)
	if err != nil {
		return nil, fmt.Errorf("parse server URL: %w", err)
	}

	scheme := "ws"
	if u.Scheme == "https" {
		scheme = "wss"
	}

	wsURL := fmt.Sprintf("%s://%s/ws", scheme, u.Host)
	origin := serverURL

	conn, err := websocket.Dial(wsURL, "", origin)
	if err != nil {
		return nil, wrapDialError(err)
	}

	c := &WSClient{conn: conn}
	authFrame, err := json.Marshal(struct {
		Type  string `json:"type"`
		Token string `json:"token"`
	}{Type: "auth", Token: token})
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("marshal ws auth frame: %w", err)
	}
	if _, err := conn.Write(authFrame); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("websocket auth: %w", err)
	}
	// Deliberately not reading back a synchronous ack/error here: on success
	// the server sends nothing (it just proceeds to register the
	// connection), so a synchronous read would have to guess a timeout for
	// the happy path. On rejection the server writes one error/
	// session_revoked frame before closing — the caller's first ReadMessage
	// picks that up naturally as the very next frame, so a rejected auth
	// surfaces on the first real read rather than being silently lost.
	return c, nil
}

// wrapDialError wraps a websocket.Dial error with context. The dial URL no
// longer carries a token (see NewWSClient), so — unlike the pre-fix
// version of this helper — there is nothing left to redact out of the
// error string.
func wrapDialError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("websocket connect: %w", err)
}

// Subscribe sends a channel subscription request to the server.
func (c *WSClient) Subscribe(channel string) error {
	return c.send(WSMessage{
		Type:    "subscribe",
		Channel: channel,
	})
}

// SendMessage sends a chat message to the given channel and session. An
// optional maxTurns (first variadic value, when > 0) caps the adapter agent
// loop for this run — the CLI surface of the `--max-turns` flag. Kept variadic
// so existing callers stay source-compatible.
func (c *WSClient) SendMessage(channel, chatID, content string, maxTurns ...int) error {
	payload := map[string]any{
		"session_id": chatID,
		"content":    content,
	}
	if len(maxTurns) > 0 && maxTurns[0] > 0 {
		payload["max_turns"] = maxTurns[0]
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return c.send(WSMessage{
		Type:    "send_message",
		Channel: channel,
		Payload: payloadBytes,
	})
}

// CancelMessage sends a cancel request for the active run in the given chat.
func (c *WSClient) CancelMessage(chatID string) error {
	payload := map[string]string{
		"session_id": chatID,
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return c.send(WSMessage{
		Type:    "cancel_message",
		Payload: payloadBytes,
	})
}

// ReadMessage reads and parses the next WebSocket message from the server.
func (c *WSClient) ReadMessage() (*WSMessage, error) {
	var raw []byte
	if err := websocket.Message.Receive(c.conn, &raw); err != nil {
		return nil, err
	}
	var msg WSMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return nil, fmt.Errorf("parse ws message: %w", err)
	}
	return &msg, nil
}

// Close closes the WebSocket connection.
func (c *WSClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	return c.conn.Close()
}

func (c *WSClient) send(msg WSMessage) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	_, err = c.conn.Write(data)
	return err
}

// ParseChatEvent extracts a ChatEventPayload from a WSMessage payload.
func ParseChatEvent(msg *WSMessage) (*ChatEventPayload, error) {
	if msg.Type != "chat_event" {
		return nil, nil
	}
	var event ChatEventPayload
	if err := json.Unmarshal(msg.Payload, &event); err != nil {
		return nil, err
	}
	return &event, nil
}

// WSTokenFromServer retrieves a WS token from the API.
// If the auth token is a CLI token (starts with crewship_cli_), the server
// generates a short-lived JWE. Otherwise it returns the session cookie.
func WSTokenFromServer(client *Client) (string, error) {
	resp, err := client.Get("/api/v1/ws-token")
	if err != nil {
		return "", err
	}
	if err := CheckError(resp); err != nil {
		return "", fmt.Errorf("get ws-token: %w", err)
	}
	var result struct {
		Token string `json:"token"`
	}
	if err := ReadJSON(resp, &result); err != nil {
		return "", err
	}
	if result.Token == "" {
		// If ws-token endpoint doesn't return a token, try using the CLI token
		// directly if it looks like a JWE
		if client.Token != "" && !strings.HasPrefix(client.Token, "crewship_cli_") {
			return client.Token, nil
		}
		return "", fmt.Errorf("server did not return a WS token")
	}
	return result.Token, nil
}
