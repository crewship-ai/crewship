package cli

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

// ---------------------------------------------------------------------------
// websocket.go — CLI WSClient (Subscribe/SendMessage/CancelMessage/ReadMessage
// /Close/send), ParseChatEvent, and WSTokenFromServer.
//
// Strategy: stand up a small websocket.Handler-backed test server so the
// dial-and-roundtrip paths run end-to-end. ParseChatEvent + WSTokenFromServer
// are pure / HTTP-only and are tested directly.
// ---------------------------------------------------------------------------

// startTestWSServer launches an httptest server that upgrades the /ws path
// to a websocket. Every received frame is forwarded onto recv; every value
// pushed to send is written back to the client. The pair (server URL,
// recv-channel, send-channel) lets a test verify what the client transmits
// AND what it parses back.
func startTestWSServer(t *testing.T) (serverURL string, recv chan WSMessage, send chan []byte, stop func()) {
	t.Helper()
	recv = make(chan WSMessage, 16)
	send = make(chan []byte, 16)

	handler := websocket.Handler(func(ws *websocket.Conn) {
		// Goroutine-per-connection: reader pumps frames into recv; the
		// caller pumps replies from send into the conn until either
		// side closes.
		var rg sync.WaitGroup
		rg.Add(1)
		go func() {
			defer rg.Done()
			for {
				var raw []byte
				if err := websocket.Message.Receive(ws, &raw); err != nil {
					return
				}
				var m WSMessage
				if err := json.Unmarshal(raw, &m); err == nil {
					recv <- m
				}
			}
		}()
		for frame := range send {
			if _, err := ws.Write(frame); err != nil {
				break
			}
		}
		_ = ws.Close()
		rg.Wait()
	})

	mux := http.NewServeMux()
	mux.Handle("/ws", handler)
	srv := httptest.NewServer(mux)
	stop = func() {
		close(send)
		srv.Close()
	}
	return srv.URL, recv, send, stop
}

// ---- NewWSClient ----

func TestNewWSClient_BadURL_Errors(t *testing.T) {
	// url.Parse is permissive on most strings; the dial step is the
	// real failure surface. A bare-control-char URL trips Parse itself.
	_, err := NewWSClient("ht\x00tp://invalid", "tok")
	if err == nil {
		t.Fatal("expected error on unparseable URL")
	}
	if !strings.Contains(err.Error(), "parse") && !strings.Contains(err.Error(), "connect") {
		t.Errorf("err = %v, want a parse or connect failure", err)
	}
}

func TestNewWSClient_UnreachableServer_Errors(t *testing.T) {
	// A well-formed URL pointing at nothing must surface as a connect
	// error, not a silent nil-client return.
	_, err := NewWSClient("http://127.0.0.1:1", "tok")
	if err == nil {
		t.Fatal("expected dial error on unreachable server")
	}
	if !strings.Contains(err.Error(), "websocket connect") {
		t.Errorf("err = %v, want \"websocket connect\" prefix", err)
	}
}

// ---- NewWSClient + Subscribe + SendMessage + CancelMessage + send (one server run) ----

func TestWSClient_SubscribeSendCancel_RoundTrip(t *testing.T) {
	url, recv, _, stop := startTestWSServer(t)
	defer stop()

	c, err := NewWSClient(url, "tok-1")
	if err != nil {
		t.Fatalf("NewWSClient: %v", err)
	}
	defer c.Close()

	if err := c.Subscribe("workspace:ws-1"); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if err := c.SendMessage("session:chat-1", "chat-1", "hello"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if err := c.CancelMessage("chat-1"); err != nil {
		t.Fatalf("CancelMessage: %v", err)
	}

	deadline := time.After(2 * time.Second)
	want := []struct {
		typ, channel string
		hasPayload   bool
	}{
		{"subscribe", "workspace:ws-1", false},
		{"send_message", "session:chat-1", true},
		{"cancel_message", "", true}, // CancelMessage intentionally leaves Channel empty
	}
	for i, w := range want {
		select {
		case got := <-recv:
			if got.Type != w.typ {
				t.Errorf("frame %d type = %q, want %q", i, got.Type, w.typ)
			}
			if got.Channel != w.channel {
				t.Errorf("frame %d channel = %q, want %q", i, got.Channel, w.channel)
			}
			if w.hasPayload {
				var p map[string]string
				_ = json.Unmarshal(got.Payload, &p)
				if p["session_id"] != "chat-1" {
					t.Errorf("frame %d payload session_id = %q, want chat-1", i, p["session_id"])
				}
				if w.typ == "send_message" && p["content"] != "hello" {
					t.Errorf("send_message payload content = %q, want hello", p["content"])
				}
			} else if len(got.Payload) > 0 {
				t.Errorf("frame %d (subscribe) has non-empty payload: %s", i, got.Payload)
			}
		case <-deadline:
			t.Fatalf("timed out waiting for frame %d (%s)", i, w.typ)
		}
	}
}

// ---- ReadMessage ----

func TestWSClient_ReadMessage_ParsesServerFrame(t *testing.T) {
	url, _, send, stop := startTestWSServer(t)
	defer stop()

	c, err := NewWSClient(url, "tok")
	if err != nil {
		t.Fatalf("NewWSClient: %v", err)
	}
	defer c.Close()

	// Push a chat_event frame from server → client.
	frame, _ := json.Marshal(WSMessage{
		Type:    "chat_event",
		Channel: "workspace:ws-1",
		Payload: json.RawMessage(`{"type":"message","content":"hi from server"}`),
	})
	send <- frame

	got, err := c.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	if got.Type != "chat_event" {
		t.Errorf("Type = %q, want chat_event", got.Type)
	}
	if got.Channel != "workspace:ws-1" {
		t.Errorf("Channel = %q, want workspace:ws-1", got.Channel)
	}
	if !strings.Contains(string(got.Payload), "hi from server") {
		t.Errorf("Payload = %s", got.Payload)
	}
}

func TestWSClient_ReadMessage_MalformedJSON_Errors(t *testing.T) {
	url, _, send, stop := startTestWSServer(t)
	defer stop()

	c, err := NewWSClient(url, "tok")
	if err != nil {
		t.Fatalf("NewWSClient: %v", err)
	}
	defer c.Close()

	send <- []byte("not-json-at-all")
	_, err = c.ReadMessage()
	if err == nil {
		t.Error("expected parse error on malformed frame")
	}
	if !strings.Contains(err.Error(), "parse ws message") {
		t.Errorf("err = %v, want \"parse ws message\" prefix", err)
	}
}

// ---- Close ----

func TestWSClient_Close_IsIdempotent(t *testing.T) {
	url, _, _, stop := startTestWSServer(t)
	defer stop()

	c, err := NewWSClient(url, "tok")
	if err != nil {
		t.Fatalf("NewWSClient: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Errorf("first Close: %v", err)
	}
	// Second Close must be a no-op (closed flag short-circuits).
	if err := c.Close(); err != nil {
		t.Errorf("second Close: %v, want nil (idempotent)", err)
	}
}

// ---- ParseChatEvent ----

func TestParseChatEvent_NonChatEventReturnsNil(t *testing.T) {
	// Source: only "chat_event" type is parsed; everything else returns
	// (nil, nil) so the caller's `if event != nil` check is sufficient.
	got, err := ParseChatEvent(&WSMessage{Type: "subscribed"})
	if err != nil {
		t.Errorf("err = %v, want nil", err)
	}
	if got != nil {
		t.Errorf("got = %+v, want nil", got)
	}
}

func TestParseChatEvent_HappyPath(t *testing.T) {
	msg := &WSMessage{
		Type:    "chat_event",
		Payload: json.RawMessage(`{"type":"message","content":"hi","metadata":{"foo":"bar"}}`),
	}
	got, err := ParseChatEvent(msg)
	if err != nil {
		t.Fatalf("ParseChatEvent: %v", err)
	}
	if got.Type != "message" || got.Content != "hi" {
		t.Errorf("got = %+v", got)
	}
	if got.Metadata == nil {
		t.Error("metadata = nil, want decoded object")
	}
}

func TestParseChatEvent_MalformedPayloadErrors(t *testing.T) {
	msg := &WSMessage{Type: "chat_event", Payload: json.RawMessage(`not-json`)}
	_, err := ParseChatEvent(msg)
	if err == nil {
		t.Error("expected unmarshal error")
	}
}

// ---- WSTokenFromServer ----

func TestWSTokenFromServer_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/ws-token" {
			http.Error(w, "wrong path "+r.URL.Path, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token":"ws-jwe-from-server"}`))
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "any-cli-token", "")
	got, err := WSTokenFromServer(client)
	if err != nil {
		t.Fatalf("WSTokenFromServer: %v", err)
	}
	if got != "ws-jwe-from-server" {
		t.Errorf("token = %q, want ws-jwe-from-server", got)
	}
}

func TestWSTokenFromServer_EmptyResponse_FallsBackToJWELikeCLIToken(t *testing.T) {
	// Source comment: "If ws-token endpoint doesn't return a token, try
	// using the CLI token directly if it looks like a JWE" (i.e. not
	// prefixed with crewship_cli_).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`)) // no token field
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "eyJ-fake-jwe-token", "")
	got, err := WSTokenFromServer(client)
	if err != nil {
		t.Fatalf("WSTokenFromServer: %v", err)
	}
	if got != "eyJ-fake-jwe-token" {
		t.Errorf("token = %q, want CLI token to pass through unchanged", got)
	}
}

func TestWSTokenFromServer_EmptyResponse_CLITokenErrors(t *testing.T) {
	// crewship_cli_ tokens are NOT usable as WS bearers; expect a
	// clear error rather than passing them through to silently fail
	// at the websocket upgrade.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "crewship_cli_abc123", "")
	_, err := WSTokenFromServer(client)
	if err == nil {
		t.Fatal("expected error when server returns empty + caller has cli token")
	}
	if !strings.Contains(err.Error(), "did not return a WS token") {
		t.Errorf("err = %v, want \"did not return a WS token\"", err)
	}
}

func TestWSTokenFromServer_EmptyResponseEmptyCLIToken_Errors(t *testing.T) {
	// No CLI token at all + server returns no token → must error.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "", "")
	_, err := WSTokenFromServer(client)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestWSTokenFromServer_5xx_Errors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "tok", "")
	_, err := WSTokenFromServer(client)
	if err == nil {
		t.Fatal("expected error on upstream 500")
	}
	if !strings.Contains(err.Error(), "get ws-token") {
		t.Errorf("err = %v, want \"get ws-token\" prefix from CheckError", err)
	}
}

// Sentinel: nil-error guard on the Receive path so callers can distinguish
// EOF (connection closed) from a parse error.
func TestWSClient_ReadMessage_ConnClosedErrors(t *testing.T) {
	url, _, _, stop := startTestWSServer(t)

	c, err := NewWSClient(url, "tok")
	if err != nil {
		t.Fatalf("NewWSClient: %v", err)
	}
	// Close server before reading → Receive surfaces a transport error.
	stop()
	_, err = c.ReadMessage()
	if err == nil {
		t.Error("expected error when server closes connection")
	}
	// Don't pin the exact error string (it's transport-dependent) but
	// it must satisfy a recognized closed-connection shape — EOF or
	// "closed" substring, or http.ErrAbortHandler. A failure here would
	// indicate a real regression in how the websocket layer surfaces
	// peer-closed errors; a t.Logf would let that drift through silently.
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "eof") && !strings.Contains(msg, "closed") && !errors.Is(err, http.ErrAbortHandler) {
		t.Errorf("ReadMessage on closed conn err = %v; want one of: EOF / contains \"closed\" / http.ErrAbortHandler", err)
	}
}
