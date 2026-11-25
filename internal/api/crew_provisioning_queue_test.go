package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
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

type recordingResumer struct {
	*fakeChatResumer
	mu       sync.Mutex
	recorded []string
	reasons  []string
}

func (r *recordingResumer) RecordNotRun(_ context.Context, chatID, trigger, reason string) {
	r.mu.Lock()
	r.recorded = append(r.recorded, trigger)
	r.reasons = append(r.reasons, reason)
	r.mu.Unlock()
}

func (r *recordingResumer) reasonList() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.reasons...)
}

func (r *recordingResumer) got() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.recorded...)
}

// A failed build leaves a reason in the transcript for every saved message.
func TestProvisioningBuildFailureRecordsNotRun(t *testing.T) {
	h, wsID, crewID, _ := resumeTestRig(t, skipCfg)
	r := &recordingResumer{fakeChatResumer: newFakeChatResumer()}
	h.chatResumer = r
	job := &ProvisionJob{CrewID: crewID, Status: "running"}
	h.jobs[crewID] = job
	queueTurns(t, h, crewID, "a", "b")
	h.markJobFailed(job, wsID, errors.New("boom"))
	deadline := time.Now().Add(3 * time.Second)
	for len(r.got()) < 2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if got := r.got(); len(got) != 2 {
		t.Fatalf("recorded %v, want both saved messages", got)
	}
}

// At shutdown the queued rest is recorded, not waited for or dropped silently.
func TestProvisioningShutdownRecordsTheUnrunRest(t *testing.T) {
	h, wsID, crewID, _ := resumeTestRig(t, skipCfg)
	b := &blockingResumer{started: make(chan string, 4), release: make(chan struct{})}
	r := &recordingResumerOverBlocking{blockingResumer: b}
	h.chatResumer = r
	job := covJob(crewID)
	h.jobs[crewID] = job
	queueTurns(t, h, crewID, "first", "second")
	h.runProvisioning(crewID, wsID, skipCfg, "", "", job)
	<-b.started
	h.Stop()
	b.release <- struct{}{}
	deadline := time.Now().Add(3 * time.Second)
	for len(r.got()) < 1 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if got := r.got(); len(got) != 1 || got[0] != "id-second" {
		t.Fatalf("recorded %v, want only the second message", got)
	}
}

type recordingResumerOverBlocking struct {
	*blockingResumer
	mu       sync.Mutex
	recorded []string
}

func (r *recordingResumerOverBlocking) RecordNotRun(_ context.Context, _, trigger, _ string) {
	r.mu.Lock()
	r.recorded = append(r.recorded, trigger)
	r.mu.Unlock()
}

func (r *recordingResumerOverBlocking) got() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.recorded...)
}

func waitRecorded(t *testing.T, r *recordingResumer, n int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for len(r.got()) < n && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if len(r.got()) < n {
		t.Fatalf("recorded %v, want %d reasons", r.got(), n)
	}
}

func resumeOneSaved(t *testing.T, r *recordingResumer, h *ProvisioningHandler, buildErr error) {
	t.Helper()
	h.resumeOne(chatbridge.PendingChatMessage{UserID: "user-1", ChatID: "chat-1", Content: "hi",
		Opts: ws.ChatMessageOption{OriginalTriggerMessageID: "t1", UserMessagePersisted: true}}, buildErr)
}

// The build error stored in the shared transcript carries no credentials, no
// build output beyond the first line, and a bounded length.
func TestNotRunBuildReasonIsSanitized(t *testing.T) {
	h, _, _, _ := resumeTestRig(t, skipCfg)
	r := &recordingResumer{fakeChatResumer: newFakeChatResumer()}
	h.chatResumer = r
	secretErr := errors.New("pull https://deploy:s3cretpw@registry.example/img failed token=abc123def " + strings.Repeat("x", 500) + "\nlayer output password=hunter2")
	resumeOneSaved(t, r, h, secretErr)
	waitRecorded(t, r, 1)
	reason := r.reasonList()[0]
	for _, leak := range []string{"s3cretpw", "abc123def", "hunter2", "layer output"} {
		if strings.Contains(reason, leak) {
			t.Fatalf("reason leaks %q: %s", leak, reason)
		}
	}
	if len([]rune(reason)) > 500 {
		t.Fatalf("reason is %d runes, want it bounded", len([]rune(reason)))
	}
}

func TestNotRunRecordedWhenAgentBusyElsewhere(t *testing.T) {
	h, _, _, _ := resumeTestRig(t, skipCfg)
	f := newFakeChatResumer()
	f.err = chatbridge.ErrAgentBusyElsewhere
	r := &recordingResumer{fakeChatResumer: f}
	h.chatResumer = r
	resumeOneSaved(t, r, h, nil)
	waitRecorded(t, r, 1)
	if !strings.Contains(r.reasonList()[0], "busy with another run") {
		t.Fatalf("reason = %q", r.reasonList()[0])
	}
}

type panicResumer struct{ *recordingResumer }

func (panicResumer) HandleChatMessage(context.Context, string, string, string, func(ws.ChatEvent), ...ws.ChatMessageOption) error {
	panic("boom")
}

func TestNotRunRecordedWhenResumePanics(t *testing.T) {
	h, _, _, _ := resumeTestRig(t, skipCfg)
	r := &recordingResumer{fakeChatResumer: newFakeChatResumer()}
	h.chatResumer = panicResumer{r}
	resumeOneSaved(t, r, h, nil)
	waitRecorded(t, r, 1)
	if !strings.Contains(r.reasonList()[0], "internal error") {
		t.Fatalf("reason = %q", r.reasonList()[0])
	}
}

// A shutdown during the wait for a busy chat is reported as a stop, not as
// "busy for too long".
func TestNotRunBusyWaitInterruptedByShutdownSaysStopped(t *testing.T) {
	h, _, _, _ := resumeTestRig(t, skipCfg)
	f := newFakeChatResumer()
	f.err = ws.ErrAgentBusy
	r := &recordingResumer{fakeChatResumer: f}
	h.chatResumer = r
	h.resumeSleep = func(context.Context, time.Duration) bool { h.Stop(); return false }
	resumeOneSaved(t, r, h, nil)
	waitRecorded(t, r, 1)
	if !strings.Contains(r.reasonList()[0], "server was stopped") {
		t.Fatalf("reason = %q, want the stopped reason", r.reasonList()[0])
	}
}
