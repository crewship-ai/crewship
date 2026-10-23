package ws

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
)

type originalCredentialChatHandler struct {
	admitted chan ChatMessageOption
	finish   chan struct{}
	alive    chan bool
}

func (h *originalCredentialChatHandler) HandleChatMessage(ctx context.Context, _, _, _ string, _ func(ChatEvent), opts ...ChatMessageOption) error {
	h.admitted <- opts[0]
	<-h.finish
	h.alive <- ctx.Err() == nil
	return nil
}

func TestOriginalCredentialDetachedMessageUsesHostSnapshot(t *testing.T) {
	hub := newRunningHub(t)
	hub.SetChannelAuthorizer(allowAllAuthorizer{})
	h := &originalCredentialChatHandler{admitted: make(chan ChatMessageOption, 1), finish: make(chan struct{}), alive: make(chan bool, 1)}
	hub.SetChatHandler(h)
	c := newClient(t, hub, "human")
	source := auth.OriginalCredential{UserID: "human", Kind: "cli_token", ID: "cli", Binding: "sha256:" + strings.Repeat("a", 64), UserAuthorityRevision: "9007199254740993"}
	c.ctx = auth.WithOriginalCredential(c.ctx, source)
	body, _ := json.Marshal(sendMessagePayload{ChatID: "chat", Content: "hello", Metadata: map[string]any{"original_credential": map[string]any{"user_id": "attacker"}}})
	c.handleSendMessage(ClientMessage{Type: "send_message", Payload: body})
	select {
	case got := <-h.admitted:
		if got.OriginalCredential != source {
			t.Fatal("metadata replaced original source", got.OriginalCredential)
		}
	case <-time.After(time.Second):
		t.Fatal("handler did not receive source")
	}
	c.cancel()
	close(h.finish)
	select {
	case alive := <-h.alive:
		if !alive {
			t.Fatal("socket disconnect canceled host run lifetime")
		}
	case <-time.After(time.Second):
		t.Fatal("handler did not finish")
	}
}
