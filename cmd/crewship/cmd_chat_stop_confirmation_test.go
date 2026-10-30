package main

import (
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
)

func TestCancellationResultRequiresExactPrivateCorrelation(t *testing.T) {
	for _, tc := range []struct {
		name, kind, chat, request string
		cancelled, matched        bool
	}{
		{"matching", "cancel_result", "chat", "request", true, true},
		{"negative", "cancel_result", "chat", "request", false, true},
		{"other-request", "cancel_result", "chat", "other", true, false},
		{"other-chat", "cancel_result", "other", "request", true, false},
		{"unrelated-done", "chat_event", "chat", "request", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, _ := json.Marshal(map[string]any{"session_id": tc.chat, "request_id": tc.request, "cancelled": tc.cancelled})
			matched, cancelled := cancellationResult(&cli.WSMessage{Type: tc.kind, Payload: b}, "chat", "request")
			if matched != tc.matched || matched && cancelled != tc.cancelled {
				t.Fatalf("result %v/%v", matched, cancelled)
			}
		})
	}
}

func TestCollectAgentStreamRequiresCancellationResult(t *testing.T) {
	for _, cancelled := range []bool{true, false} {
		src := newFakeEventSource(io.EOF, cli.ChatEventPayload{Type: "text", Content: "partial"}, cli.ChatEventPayload{Type: "done"})
		wrong, _ := json.Marshal(map[string]any{"session_id": "chat", "request_id": "other", "cancelled": true})
		right, _ := json.Marshal(map[string]any{"session_id": "chat", "request_id": "request", "cancelled": cancelled})
		src.msgs = append(src.msgs, &cli.WSMessage{Type: "cancel_result", Payload: wrong}, &cli.WSMessage{Type: "cancel_result", Payload: right})
		res := collectAgentStream(src, time.Second, &runStop{requested: true, chatID: "chat", requestID: "request"})
		if res.Cancelled != cancelled || res.GotDone || res.Text != "partial" || !cancelled && res.StreamErr == "" {
			t.Fatalf("invalid terminal cancellation: %+v", res)
		}
	}
}
