package api

import (
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/ws"
)

// Two sends saved as separate turns while a crew builds are both run, in the
// order they arrived, one after the other (#5571).
func TestProvisioningQueueRunsEverySavedTurnInOrder(t *testing.T) {
	h, wsID, crewID, _ := resumeTestRig(t, "")
	fake := newFakeChatResumer()
	h.chatResumer = fake
	job := covJob(crewID)
	h.jobs[crewID] = job
	for _, m := range []struct{ id, content string }{{"turn-1", "first"}, {"turn-2", "second"}} {
		if !h.AttachPendingMessage(crewID, chatbridge.PendingChatMessage{UserID: "user-1", ChatID: "chat-1", Content: m.content,
			Opts: ws.ChatMessageOption{OriginalTriggerMessageID: m.id, UserMessagePersisted: true}}) {
			t.Fatal("attach failed")
		}
	}
	h.runProvisioning(crewID, wsID, "", "", "", job)
	var got []string
	for i := 0; i < 2; i++ {
		select {
		case c := <-fake.called:
			got = append(got, c.content)
		case <-time.After(3 * time.Second):
			t.Fatalf("only %v ran; every saved turn must run", got)
		}
	}
	if got[0] != "first" || got[1] != "second" {
		t.Fatalf("order = %v, want [first second]", got)
	}
}
