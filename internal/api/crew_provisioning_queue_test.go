package api

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/ws"
)

// Two sends saved as separate turns while a crew builds are both run, in the
// order they arrived, one after the other (#5571).
const skipCfg = `{"image":"ubuntu:22.04"}`

func TestProvisioningQueueRunsEverySavedTurnInOrder(t *testing.T) {
	h, wsID, crewID, _ := resumeTestRig(t, skipCfg)
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
	h.runProvisioning(crewID, wsID, skipCfg, "", "", job)
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

type blockingResumer struct {
	mu      sync.Mutex
	active  int
	maxSeen int
	order   []string
	started chan string
	release chan struct{}
	panicOn string
}

func (b *blockingResumer) HandleChatMessage(_ context.Context, _, _, content string, _ func(ws.ChatEvent), _ ...ws.ChatMessageOption) error {
	b.mu.Lock()
	b.active++
	if b.active > b.maxSeen {
		b.maxSeen = b.active
	}
	b.order = append(b.order, content)
	b.mu.Unlock()
	b.started <- content
	defer func() { b.mu.Lock(); b.active--; b.mu.Unlock() }()
	if content == b.panicOn {
		panic("boom")
	}
	<-b.release
	return nil
}

func queueTurns(t *testing.T, h *ProvisioningHandler, crewID string, contents ...string) {
	t.Helper()
	for _, c := range contents {
		if !h.AttachPendingMessage(crewID, chatbridge.PendingChatMessage{UserID: "user-1", ChatID: "chat-1", Content: c,
			Opts: ws.ChatMessageOption{OriginalTriggerMessageID: "id-" + c, UserMessagePersisted: true}}) {
			t.Fatalf("attach %s refused", c)
		}
	}
}

// The second turn must not start while the first run is still going (the chat
// accepts one live run), however long the first takes.
func TestProvisioningQueueWaitsForTheRunningTurn(t *testing.T) {
	h, wsID, crewID, _ := resumeTestRig(t, skipCfg)
	r := &blockingResumer{started: make(chan string, 4), release: make(chan struct{})}
	h.chatResumer = r
	job := covJob(crewID)
	h.jobs[crewID] = job
	queueTurns(t, h, crewID, "first", "second")
	h.runProvisioning(crewID, wsID, skipCfg, "", "", job)
	if got := <-r.started; got != "first" {
		t.Fatalf("started %q first", got)
	}
	select {
	case got := <-r.started:
		t.Fatalf("%q started while the first run was still going", got)
	case <-time.After(300 * time.Millisecond):
	}
	r.release <- struct{}{}
	if got := <-r.started; got != "second" {
		t.Fatalf("started %q second", got)
	}
	r.release <- struct{}{}
	if r.maxSeen != 1 {
		t.Fatalf("max concurrent runs = %d, want 1", r.maxSeen)
	}
}

// A panic in one queued message must not stop the one behind it.
func TestProvisioningQueuePanicDoesNotDropTheRest(t *testing.T) {
	h, wsID, crewID, _ := resumeTestRig(t, skipCfg)
	r := &blockingResumer{started: make(chan string, 4), release: make(chan struct{}, 4), panicOn: "first"}
	h.chatResumer = r
	job := covJob(crewID)
	h.jobs[crewID] = job
	queueTurns(t, h, crewID, "first", "second")
	h.runProvisioning(crewID, wsID, skipCfg, "", "", job)
	for _, want := range []string{"first", "second"} {
		select {
		case got := <-r.started:
			if got != want {
				t.Fatalf("started %q, want %q", got, want)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("%q never started", want)
		}
	}
	r.release <- struct{}{}
}

// When the server stops, the rest of the queue is not waited for.
func TestProvisioningQueueStopsAtShutdown(t *testing.T) {
	h, wsID, crewID, _ := resumeTestRig(t, skipCfg)
	r := &blockingResumer{started: make(chan string, 4), release: make(chan struct{})}
	h.chatResumer = r
	job := covJob(crewID)
	h.jobs[crewID] = job
	queueTurns(t, h, crewID, "first", "second")
	h.runProvisioning(crewID, wsID, skipCfg, "", "", job)
	<-r.started
	h.Stop()
	r.release <- struct{}{}
	select {
	case got := <-r.started:
		t.Fatalf("%q started after shutdown", got)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestProvisioningQueueCapsTurnsPerChat(t *testing.T) {
	h, _, crewID, _ := resumeTestRig(t, skipCfg)
	h.jobs[crewID] = &ProvisionJob{CrewID: crewID, Status: "running"}
	for i := 0; i < maxPendingPerChat; i++ {
		queueTurns(t, h, crewID, fmt.Sprintf("m%d", i))
	}
	if h.AttachPendingMessage(crewID, chatbridge.PendingChatMessage{UserID: "user-1", ChatID: "chat-1", Content: "over",
		Opts: ws.ChatMessageOption{OriginalTriggerMessageID: "id-over"}}) {
		t.Fatal("the turn over the cap was accepted")
	}
}
