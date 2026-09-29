package chatbridge

import (
	"context"
	"errors"
	"testing"

	"github.com/crewship-ai/crewship/internal/chataudience"
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

func TestQueuedHumanAdmissionRequiresOriginalReceipt(t *testing.T) {
	r := &humanGuardResolver{}
	bridge, _ := testBridge(t, r)
	err := bridge.HandleChatMessage(t.Context(), "human", "chat", "hello", func(ws.ChatEvent) {}, ws.ChatMessageOption{HumanResume: true, Metadata: map[string]any{"human_authority": map[string]any{"member_id": "forged"}}})
	if err == nil || r.actor != "" {
		t.Fatal("missing server receipt reached context resolution")
	}
}

type changedReceiptResolver struct{ mockResolver }

func (r *changedReceiptResolver) ResolveHumanChat(ctx context.Context, actor, chat string) (*ChatInfo, error) {
	receipt := chataudience.ReceiptFromContext(ctx)
	if receipt == nil {
		return nil, errors.New("missing receipt")
	}
	changed := *receipt
	changed.MemberRevision++
	return &ChatInfo{HumanAuthority: &changed}, nil
}
func TestQueuedHumanAdmissionCannotRefreshReceipt(t *testing.T) {
	bridge, _ := testBridge(t, &changedReceiptResolver{})
	receipt := &chataudience.Receipt{UserID: "human", ChatID: "chat", MemberID: "original", MemberRevision: 1}
	err := bridge.HandleChatMessage(t.Context(), "human", "chat", "hello", func(ws.ChatEvent) {}, ws.ChatMessageOption{HumanResume: true, HumanAuthority: receipt})
	if err == nil {
		t.Fatal("resolver refreshed a queued message's authority")
	}
}
