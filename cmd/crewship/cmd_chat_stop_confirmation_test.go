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

func cancelFrame(kind, chat, request string, cancelled bool) *cli.WSMessage {
	b, _ := json.Marshal(map[string]any{"session_id": chat, "request_id": request, "cancelled": cancelled})
	return &cli.WSMessage{Type: kind, Payload: b}
}

// Against a server without cancel confirmation (no cancel_accepted), a done
// after the stop request degrades to StopEndedUnconfirmed after the short
// grace — neither a confirmed stop nor a wait for the caller's timeout. Once
// the server acknowledged the request, a done never degrades the outcome.
func TestCollectAgentStreamStopOutcomeWithoutConfirmation(t *testing.T) {
	prev := stopResultGrace
	stopResultGrace = 20 * time.Millisecond
	t.Cleanup(func() { stopResultGrace = prev })
	for _, tc := range []struct {
		name           string
		acknowledged   bool
		wantDegraded   bool
		wantTimedOut   bool
		trailingResult bool
	}{
		{"older-server", false, true, false, false},
		{"acknowledged-no-result-yet", true, false, true, false},
		{"acknowledged-then-result", true, false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := newFakeEventSource(nil, cli.ChatEventPayload{Type: "text", Content: "partial"}, cli.ChatEventPayload{Type: "done"})
			if tc.acknowledged {
				src.msgs = append([]*cli.WSMessage{cancelFrame("cancel_accepted", "chat", "request", false)}, src.msgs...)
			}
			if tc.trailingResult {
				src.msgs = append(src.msgs, cancelFrame("cancel_result", "chat", "request", true))
			}
			stop := &runStop{requested: true, chatID: "chat", requestID: "request"}
			res := collectAgentStream(src, 300*time.Millisecond, stop)
			if res.StopEndedUnconfirmed != tc.wantDegraded || res.TimedOut != tc.wantTimedOut || res.Cancelled != tc.trailingResult || res.GotDone {
				t.Fatalf("outcome %+v", res)
			}
			if res.Text != "partial" {
				t.Fatalf("text %q", res.Text)
			}
			if stop.endedUnconfirmed() != tc.wantDegraded {
				t.Fatalf("endedUnconfirmed=%v", stop.endedUnconfirmed())
			}
		})
	}
}

func TestCancellationAcceptedRequiresExactCorrelation(t *testing.T) {
	for _, tc := range []struct {
		name string
		msg  *cli.WSMessage
		want bool
	}{
		{"matching", cancelFrame("cancel_accepted", "chat", "request", false), true},
		{"other-request", cancelFrame("cancel_accepted", "chat", "other", false), false},
		{"other-chat", cancelFrame("cancel_accepted", "other", "request", false), false},
		{"result-is-not-ack", cancelFrame("cancel_result", "chat", "request", true), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := cancellationAccepted(tc.msg, "chat", "request"); got != tc.want {
				t.Fatalf("accepted=%v", got)
			}
		})
	}
}
