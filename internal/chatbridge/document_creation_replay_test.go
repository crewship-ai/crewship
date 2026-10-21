package chatbridge

import (
	"errors"
	"github.com/crewship-ai/crewship/internal/ws"
	"strings"
	"testing"
)

func TestDocumentCreationReplayIsRejectedBeforeProvisioningOrSecondRun(t *testing.T) {
	b, r := documentCreationBridgeFixture(t)
	content := "Write the requested document"
	opt := ws.ChatMessageOption{Metadata: creationMetadata("stable-replay")}
	err := b.HandleChatMessage(t.Context(), "creation-human", "creation-chat", content, func(ws.ChatEvent) {}, opt)
	if !errors.Is(err, errDocumentCreationComponentBoundary) || r.runCalls != 1 {
		t.Fatalf("initial admission:%d %v", r.runCalls, err)
	}
	// A replay must be resolved from retained consent without touching runtime.
	b.container = nil
	b.containerCache = map[string]string{}
	var events []ws.ChatEvent
	err = b.HandleChatMessage(t.Context(), "creation-human", "creation-chat", content, func(e ws.ChatEvent) { events = append(events, e) }, opt)
	if err == nil || !strings.Contains(err.Error(), "already accepted") {
		t.Fatalf("duplicate reached provisioning or generic failure:%v", err)
	}
	if r.runCalls != 1 {
		t.Fatalf("new run started: %d", r.runCalls)
	}
	for _, e := range events {
		if e.Type == "user_message" || e.Type == "done" {
			t.Fatalf("replay synthesized a second user/terminal event: %+v", e)
		}
	}
	turns, e := b.convStore.Read(t.Context(), "creation-chat", 0, 0)
	if e != nil || len(turns) != 1 {
		t.Fatalf("replay changed transcript: %d %v", len(turns), e)
	}
}
