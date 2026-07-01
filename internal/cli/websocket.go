package cli

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"

	"golang.org/x/net/websocket"

	"github.com/crewship-ai/crewship/internal/cli/redact"
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

// NewWSClient connects to the server's WebSocket endpoint with JWT authentication.
func NewWSClient(serverURL, token string) (*WSClient, error) {
	u, err := url.Parse(serverURL)
	if err != nil {
		return nil, fmt.Errorf("parse server URL: %w", err)
	}

	scheme := "ws"
	if u.Scheme == "https" {
		scheme = "wss"
	}

	wsURL := fmt.Sprintf("%s://%s/ws?token=%s", scheme, u.Host, url.QueryEscape(token))
	origin := serverURL

	conn, err := websocket.Dial(wsURL, "", origin)
	if err != nil {
		return nil, wrapDialError(err)
	}

	return &WSClient{conn: conn}, nil
}

// wrapDialError wraps a websocket.Dial error with context, scrubbing any
// embedded credentials before the error can reach stderr or CI logs.
//
// golang.org/x/net/websocket embeds the full dial URL — including the
// `?token=<jwt>` query string we authenticate with — in its error
// messages. Returning that error verbatim leaks the (short-lived but
// still session-bearing) WS token to the terminal and any log scraper.
// We run the whole error text through redact.URL, which masks the
// `token` query param (and any other known secret-bearing param) while
// leaving the rest of the dial diagnostic intact.
func wrapDialError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("websocket connect: %s", redactErrToken(err.Error()))
}

// redactErrToken strips secret-bearing query params from any URL-shaped
// token embedded in an arbitrary error string. x/net's dial errors are
// free-form ("websocket.Dial ws://host/ws?token=…: dial tcp …"), so we
// can't url.Parse the whole thing — we redact the longest space-delimited
// field that parses as a URL with a redactable query param.
func redactErrToken(s string) string {
	fields := strings.Fields(s)
	for _, f := range fields {
		// Trim a trailing ':' that x/net appends after the URL.
		trimmed := strings.TrimRight(f, ":")
		if trimmed == "" {
			continue
		}
		masked := redact.URL(trimmed)
		if masked != trimmed {
			s = strings.Replace(s, trimmed, masked, 1)
		}
	}
	return s
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
