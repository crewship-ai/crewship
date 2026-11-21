package chatbridge

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/ws"
)

func TestCreateRunMapsCapacityRefusal(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		status   int
		body     string
		capacity bool
	}{
		{"coded 429", 429, `{"error":"chat execution capacity unavailable","code":"chat_capacity_unavailable"}`, true},
		{"429 without code", 429, `{"error":"rate limited"}`, false},
		{"other status with code", 409, `{"error":"x","code":"chat_capacity_unavailable"}`, false},
		{"internal error", 500, `{"error":"db down"}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			t.Cleanup(ts.Close)
			r := NewIPCResolver(ts.URL, "tok", nil)
			err := r.CreateRun(context.Background(), "r1", "a1", "c1", "w1", "USER", nil)
			if err == nil {
				t.Fatal("expected error")
			}
			if got := errors.Is(err, ErrChatCapacityUnavailable); got != tc.capacity {
				t.Fatalf("errors.Is capacity=%v want %v (%v)", got, tc.capacity, err)
			}
		})
	}
}

func TestHandleChatMessageCapacityRefusalIsDistinguishable(t *testing.T) {
	resolver := &capResolver{info: baseInfo(), createRunErr: fmt.Errorf("create run: %w", ErrChatCapacityUnavailable)}
	ctr := &scriptedContainer{agentExecErr: errors.New("exec must not be reached")}
	b := testBridgeWithContainer(t, resolver, ctr)
	var events []ws.ChatEvent
	err := b.HandleChatMessage(context.Background(), "user-1", "sess-busy", "hi", func(e ws.ChatEvent) { events = append(events, e) })
	if !errors.Is(err, ErrChatCapacityUnavailable) {
		t.Fatalf("error must keep the refusal: %v", err)
	}
	var got *ws.ChatEvent
	for i := range events {
		if events[i].Type == "error" {
			got = &events[i]
		}
	}
	if got == nil {
		t.Fatal("missing error event")
	}
	if got.Content == "failed to start a durable run" || !strings.Contains(got.Content, "busy") {
		t.Fatalf("generic message leaked: %q", got.Content)
	}
	meta, _ := got.Metadata.(map[string]any)
	if meta["code"] != "chat_capacity_unavailable" {
		t.Fatalf("code lost: %#v", got.Metadata)
	}
}
