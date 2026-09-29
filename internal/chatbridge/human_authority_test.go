package chatbridge

import (
	"context"
	"errors"
	"testing"

	"github.com/crewship-ai/crewship/internal/ws"
)

var errHumanAdmissionDenied = errors.New("human admission denied")

type humanGuardResolver struct {
	mockResolver
	actor        string
	chat         string
	legacyCalled bool
}

func (r *humanGuardResolver) ResolveChat(context.Context, string) (*ChatInfo, error) {
	r.legacyCalled = true
	return nil, errors.New("unbound legacy resolution")
}
func (r *humanGuardResolver) ResolveHumanChat(_ context.Context, actor, chat string) (*ChatInfo, error) {
	r.actor, r.chat = actor, chat
	return nil, errHumanAdmissionDenied
}

func TestHumanAdmissionPrecedesLegacyContextAndUsesAuthenticatedActor(t *testing.T) {
	r := &humanGuardResolver{}
	bridge, _ := testBridge(t, r)
	err := bridge.HandleChatMessage(t.Context(), "authenticated-human", "private-chat", "hello", func(ws.ChatEvent) {}, ws.ChatMessageOption{Metadata: map[string]any{"user_id": "forged-owner", "authority": "forged"}})
	if !errors.Is(err, errHumanAdmissionDenied) || r.legacyCalled || r.actor != "authenticated-human" || r.chat != "private-chat" {
		t.Fatalf("unbound context resolution: actor=%q chat=%q legacy=%v err=%v", r.actor, r.chat, r.legacyCalled, err)
	}
}

// An older resolver must not become a silent shared-mode fallback.
type legacyOnlyResolver struct{ ChatResolver }

func TestHumanAdmissionRejectsResolverWithoutHumanAuthority(t *testing.T) {
	bridge, _ := testBridge(t, legacyOnlyResolver{&mockResolver{}})
	if err := bridge.HandleChatMessage(t.Context(), "human", "chat", "hello", func(ws.ChatEvent) {}); err == nil {
		t.Fatal("legacy resolver admitted human work")
	}
}
