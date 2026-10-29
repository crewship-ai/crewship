package api

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/websocket"

	"github.com/crewship-ai/crewship/internal/askforms"
	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/ws"
)

type resumeAskChannelAuth struct{}

func (resumeAskChannelAuth) CanSubscribe(context.Context, string, string) (bool, error) {
	return true, nil
}

func TestDeferredAskRejectionIsPrivateAndCorrelated(t *testing.T) {
	for _, mode := range []string{"form changed", "page revoked", "build failed", "unsubscribed sender", "after append"} {
		t.Run(mode, func(t *testing.T) {
			h, _, _, _ := resumeTestRig(t, "")
			validator, err := auth.NewJWTValidator("synthetic-deferred-ask-test-secret")
			if err != nil {
				t.Fatal(err)
			}
			hub := ws.NewHub(newTestLogger(), nil, validator, ws.NopSessionsForTests)
			hub.SetChannelAuthorizer(resumeAskChannelAuth{})
			h.wsHub = hub
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan struct{})
			go func() { defer close(done); hub.Run(ctx) }()
			t.Cleanup(func() {
				cancel()
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					t.Error("hub did not stop")
				}
			})
			server := httptest.NewServer(http.HandlerFunc(hub.HandleUpgrade))
			t.Cleanup(server.Close)
			type frame struct {
				Type    string          `json:"type"`
				Seq     int64           `json:"seq"`
				Payload json.RawMessage `json:"payload"`
			}
			receive := func(conn *websocket.Conn) frame {
				t.Helper()
				_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
				var f frame
				if err := websocket.JSON.Receive(conn, &f); err != nil {
					t.Fatal(err)
				}
				return f
			}
			const channel = "session:deferred-ask"
			connect := func(user string, subscribe bool) *websocket.Conn {
				t.Helper()
				conn, err := websocket.Dial("ws"+strings.TrimPrefix(server.URL, "http"), "", server.URL)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = conn.Close() })
				ticket, err := validator.IssueWSTicket(user, "", "", "")
				if err != nil {
					t.Fatal(err)
				}
				messages := []map[string]any{{"type": "auth", "token": ticket}}
				if subscribe {
					messages = append(messages, map[string]any{"type": "subscribe", "channel": channel})
				}
				messages = append(messages, map[string]any{"type": "ping"})
				for _, m := range messages {
					if err := websocket.JSON.Send(conn, m); err != nil {
						t.Fatal(err)
					}
				}
				for i := 0; i < 4; i++ {
					if f := receive(conn); f.Type == "pong" {
						return conn
					}
				}
				t.Fatal("subscription barrier missing")
				return nil
			}
			sender, colleague := connect("ask-sender", mode != "unsubscribed sender"), connect("ask-colleague", true)
			observer := hub.AddObserver(channel, "http-colleague", 8)
			defer hub.RemoveObserver(channel, observer)
			fake := newFakeChatResumer()
			fake.err = &askforms.SubmissionError{SubmissionID: "deferred-submission", Message: "The form changed; review your answers."}
			h.chatResumer = fake
			var buildErr error
			if mode == "page revoked" {
				fake.err = errors.New("Page unavailable")
				fake.stream = []ws.ChatEvent{{Type: "error", Content: "Page unavailable"}}
			}
			if mode == "build failed" {
				buildErr = errors.New("owned synthetic build failure")
			}
			if mode == "after append" {
				fake.err = errors.New("runtime unavailable")
				fake.stream = []ws.ChatEvent{
					{Type: "user_message", Content: "Answer: approved", Metadata: map[string]any{"ask_submission_id": "deferred-submission"}},
					{Type: "error", Content: "runtime unavailable"}, {Type: "done"},
				}
			}
			form := askforms.Form{ID: "format", Label: "Format", Version: 1, Template: "Answer: {{answer}}", Fields: []askforms.Field{{Name: "answer", Type: "text"}}}
			envelope := askforms.NewEnvelope(form, "deferred-submission", askforms.Values{"answer": "approved"}, nil, "Answer: approved")
			h.resumeMessage(chatbridge.PendingChatMessage{UserID: "ask-sender", ChatID: "deferred-ask", Content: "Answer: approved", Opts: ws.ChatMessageOption{Metadata: map[string]any{askforms.EnvelopeMetadataKey: envelope}}}, buildErr)
			if mode == "after append" {
				seenReceipt, seenError := false, false
				for i := 0; i < 4; i++ {
					f := receive(sender)
					if f.Type != "chat_event" {
						continue
					}
					var event ws.ChatEvent
					if err := json.Unmarshal(f.Payload, &event); err != nil {
						t.Fatal(err)
					}
					if event.Type == "ask_submission_error" {
						t.Fatalf("accepted submission falsely refused: %s", f.Payload)
					}
					if event.Type == "user_message" {
						seenReceipt = true
					}
					if event.Type == "error" {
						seenError = true
					}
				}
				if !seenReceipt || !seenError {
					t.Fatal("persisted submission lost acceptance or runtime failure")
				}
				return
			}
			f := receive(sender)
			if f.Type != "chat_event" || f.Seq != 0 {
				t.Fatalf("refusal opened or entered a shared run: %+v", f)
			}
			var event struct {
				Type     string         `json:"type"`
				Metadata map[string]any `json:"metadata"`
			}
			if err := json.Unmarshal(f.Payload, &event); err != nil {
				t.Fatal(err)
			}
			if event.Type != "ask_submission_error" || event.Metadata["submission_id"] != "deferred-submission" {
				t.Fatalf("deferred refusal not correlated: %s", f.Payload)
			}
			_ = colleague.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
			var extra frame
			if err := websocket.JSON.Receive(colleague, &extra); err == nil {
				t.Fatalf("private refusal reached another user: %+v", extra)
			} else {
				var timeout net.Error
				if !errors.As(err, &timeout) || !timeout.Timeout() {
					t.Fatalf("colleague disconnected instead of remaining private: %v", err)
				}
			}
			select {
			case raw := <-observer.Frames():
				t.Fatalf("private refusal altered shared stream: %s", raw)
			default:
			}
		})
	}
}
