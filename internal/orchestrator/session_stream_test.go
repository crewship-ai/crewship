package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/ws"
)

// ---- fake publisher ----

type fakeSessionRun struct {
	pub  *fakeSessionPublisher
	ends int
}

// Emit models the real recorder's rule rather than just recording the call: a
// frame emitted after End gets no sequence number and is never buffered, so a
// client that reconnects can never see it. Counting those separately is what
// gives the "terminal frames come BEFORE End" invariant a test that can fail.
func (r *fakeSessionRun) Emit(ev ws.ChatEvent) {
	r.pub.mu.Lock()
	defer r.pub.mu.Unlock()
	if r.ends > 0 {
		r.pub.afterEnd = append(r.pub.afterEnd, ev)
		return
	}
	r.pub.events = append(r.pub.events, ev)
}

func (r *fakeSessionRun) End() {
	r.pub.mu.Lock()
	defer r.pub.mu.Unlock()
	r.ends++
	r.pub.ends++
}

type fakeSessionPublisher struct {
	mu    sync.Mutex
	chats []string
	// events are the frames that landed inside the recording (sequenced and
	// replayable); afterEnd are the ones that arrived too late to be either.
	events   []ws.ChatEvent
	afterEnd []ws.ChatEvent
	ends     int
	run      *fakeSessionRun
}

// lost reports the frames emitted after the recording closed — invisible to
// anyone who reconnects, so a run whose terminal `done` lands here looks like a
// run that never finished.
func (p *fakeSessionPublisher) lost() []ws.ChatEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]ws.ChatEvent(nil), p.afterEnd...)
}

func (p *fakeSessionPublisher) BeginSessionRun(chatID string) ws.SessionRun {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.chats = append(p.chats, chatID)
	p.run = &fakeSessionRun{pub: p}
	return p.run
}

func (p *fakeSessionPublisher) types() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, 0, len(p.events))
	for _, e := range p.events {
		out = append(out, e.Type)
	}
	return out
}

func (p *fakeSessionPublisher) began() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.chats...)
}

func (p *fakeSessionPublisher) endCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.ends
}

func has(types []string, want string) bool {
	for _, t := range types {
		if t == want {
			return true
		}
	}
	return false
}

// The issue in one test: a run started anywhere OTHER than the WebSocket send
// path — scheduler, webhook, pipeline step, agent-start IPC — publishes its
// events on the chat's session channel. All four reach RunAgent with a chat id
// on the request, so the assertion is made against RunAgent itself rather than
// against four call sites.
func TestRunAgent_PublishesEventsOnTheSessionChannel(t *testing.T) {
	t.Parallel()
	stream := `{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"hello"}}}` + "\n"
	o := New(covNewRunContainer(covRunOpts{stream: stream}), newMemState(), covQuietLogger())
	pub := &fakeSessionPublisher{}
	o.SetSessionPublisher(pub)

	if err := o.RunAgent(context.Background(), covRunReq(), nil); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}

	if got := pub.began(); len(got) != 1 || got[0] != "chat1" {
		t.Fatalf("session runs begun for %v, want exactly [chat1]", got)
	}
	types := pub.types()
	if !has(types, "text") {
		t.Errorf("the agent's text never reached the session channel: %v", types)
	}
	if len(types) == 0 || types[len(types)-1] != "done" {
		t.Errorf("last published event = %v, want a terminal done — without it a watcher waits out its idle timeout", types)
	}
	if pub.endCount() != 1 {
		t.Errorf("End called %d times, want exactly 1 (leaked active-run state otherwise)", pub.endCount())
	}
	if lost := pub.lost(); len(lost) != 0 {
		t.Errorf("%d frames were published AFTER the recording closed (%v) — unsequenced, unbuffered, invisible to a reconnecting watcher", len(lost), lost)
	}
}

// A failed run must still terminate the stream, and must say why. Otherwise a
// watcher sits until its idle timeout on a run that is already over.
func TestRunAgent_FailedRunPublishesErrorThenDone(t *testing.T) {
	t.Parallel()
	o := New(covNewRunContainer(covRunOpts{}), newMemState(), covQuietLogger())
	o.SetApprovalGate(&covGate{err: errors.New("gate db down")})
	pub := &fakeSessionPublisher{}
	o.SetSessionPublisher(pub)

	if err := o.RunAgent(context.Background(), covRunReq(), nil); err == nil {
		t.Fatal("expected the gate error")
	}

	types := pub.types()
	if len(types) != 2 || types[0] != "error" || types[1] != "done" {
		t.Fatalf("published %v, want [error done]", types)
	}
	if pub.endCount() != 1 {
		t.Errorf("End called %d times on the failure path, want 1", pub.endCount())
	}
}

// No chat, no channel. `session:` is not a channel anyone can subscribe to, so
// recording against it is a buffer nobody reads and nobody frees until the
// sweep. The agent-start IPC can run an agent with no chat row at all.
func TestRunAgent_NoChatIDDoesNotPublish(t *testing.T) {
	t.Parallel()
	o := New(covNewRunContainer(covRunOpts{stream: "{}\n"}), newMemState(), covQuietLogger())
	pub := &fakeSessionPublisher{}
	o.SetSessionPublisher(pub)

	req := covRunReq()
	req.ChatID = ""
	if err := o.RunAgent(context.Background(), req, nil); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	if got := pub.began(); len(got) != 0 {
		t.Errorf("a chat-less run opened session recordings for %v", got)
	}
}

// The WebSocket send path records the WHOLE turn itself — container start,
// steering, the terminal done after RunAgent returns. A second recording
// underneath it would double every frame on the channel.
func TestRunAgent_SuppressedWhenTheCallerAlreadyPublishes(t *testing.T) {
	t.Parallel()
	o := New(covNewRunContainer(covRunOpts{stream: "{}\n"}), newMemState(), covQuietLogger())
	pub := &fakeSessionPublisher{}
	o.SetSessionPublisher(pub)

	req := covRunReq()
	req.SuppressSessionStream = true
	if err := o.RunAgent(context.Background(), req, nil); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	if got := pub.began(); len(got) != 0 {
		t.Errorf("suppressed run still opened session recordings for %v", got)
	}
}

// A delegated/peer sub-agent runs against the DELEGATING chat's id. Its frames
// belong to the parent turn, not to a turn of its own: publishing them would
// render a sub-agent's raw output as the primary agent's reply.
func TestRunAgentForAssignment_DoesNotPublish(t *testing.T) {
	t.Parallel()
	o := New(covNewRunContainer(covRunOpts{stream: "{}\n"}), newMemState(), covQuietLogger())
	pub := &fakeSessionPublisher{}
	o.SetSessionPublisher(pub)

	if err := o.RunAgentForAssignment(context.Background(), covRunReq(), nil); err != nil {
		t.Fatalf("RunAgentForAssignment: %v", err)
	}
	if got := pub.began(); len(got) != 0 {
		t.Errorf("a sub-agent run published on the parent chat's channel: %v", got)
	}
}

// The caller's own handler must keep seeing every event: publishing is layered
// on top of what each call site already does (log buffer, text accumulation,
// span recording), never instead of it.
func TestRunAgent_PublishingDoesNotStealEventsFromTheCallersHandler(t *testing.T) {
	t.Parallel()
	stream := `{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"hello"}}}` + "\n"
	o := New(covNewRunContainer(covRunOpts{stream: stream}), newMemState(), covQuietLogger())
	o.SetSessionPublisher(&fakeSessionPublisher{})

	var mu sync.Mutex
	var seen []string
	if err := o.RunAgent(context.Background(), covRunReq(), func(ev AgentEvent) {
		mu.Lock()
		seen = append(seen, ev.Type)
		mu.Unlock()
	}); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !has(seen, "text") {
		t.Errorf("caller handler saw %v, want the text event", seen)
	}
	// The synthesized terminal done is a TRANSPORT frame — it must not be
	// injected into the caller's event stream, or the buffering handler would
	// accumulate it and every run record would grow a phantom event.
	if has(seen, "done") {
		t.Errorf("caller handler saw the synthesized done: %v", seen)
	}
}

// With no publisher wired (CLI-only builds, most tests) RunAgent must behave
// exactly as before.
func TestRunAgent_NoPublisherIsANoOp(t *testing.T) {
	t.Parallel()
	o := New(covNewRunContainer(covRunOpts{stream: "{}\n"}), newMemState(), covQuietLogger())
	if err := o.RunAgent(context.Background(), covRunReq(), nil); err != nil {
		t.Fatalf("RunAgent without a publisher: %v", err)
	}
}

// A panic inside the run must still balance begin/end. A leaked active run
// keeps the channel reporting "generating" forever, so every later watcher
// waits out its idle timeout on a chat that is doing nothing.
func TestRunAgent_PanicStillEndsTheSessionRun(t *testing.T) {
	t.Parallel()
	o := New(covNewRunContainer(covRunOpts{stream: "{}\n"}), newMemState(), covQuietLogger())
	pub := &fakeSessionPublisher{}
	o.SetSessionPublisher(pub)

	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Error("the panic was swallowed — it must propagate to the caller")
			}
		}()
		_ = o.RunAgent(context.Background(), covRunReq(), func(AgentEvent) {
			panic("handler exploded")
		})
	}()

	if pub.endCount() != 1 {
		t.Fatalf("End called %d times after a panic, want 1", pub.endCount())
	}
	types := pub.types()
	if len(types) == 0 || types[len(types)-1] != "done" {
		t.Errorf("published %v, want a terminal done even on panic", types)
	}
	if !has(types, "error") {
		t.Errorf("published %v, want an error frame naming the crash", types)
	}
}

// The end-to-end shape, against a REAL hub rather than a fake publisher: a run
// dispatched the way a routine dispatches one lands on an HTTP observer — the
// exact subscriber `crewship chat stream` attaches with — as sequenced frames
// ending in `done`. The fakes above pin the wrapper's decisions; this pins that
// the wrapper and the hub actually fit together.
func TestRunAgent_DeliversTheRunToAnHTTPObserverOnARealHub(t *testing.T) {
	t.Parallel()
	hub := ws.NewHub(covQuietLogger(), nil, ws.NopValidatorForTests, ws.NopSessionsForTests)
	obs := hub.AddObserver("session:chat1", "u1", 64)
	defer hub.RemoveObserver("session:chat1", obs)

	stream := `{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"ACKNOWLEDGED"}}}` + "\n"
	o := New(covNewRunContainer(covRunOpts{stream: stream}), newMemState(), covQuietLogger())
	o.SetSessionPublisher(hub)

	if err := o.RunAgent(context.Background(), covRunReq(), nil); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}

	var types []string
	var lastSeq int64
	var sawText bool
drain:
	for {
		select {
		case data := <-obs.Frames():
			var msg ws.ServerMessage
			if err := json.Unmarshal(data, &msg); err != nil {
				t.Fatalf("observed frame is not a ServerMessage: %v", err)
			}
			if msg.Seq <= lastSeq {
				t.Fatalf("seq went backwards: %d after %d — resume watermarks are meaningless", msg.Seq, lastSeq)
			}
			lastSeq = msg.Seq
			types = append(types, msg.Type)
			if msg.Type == "chat_event" {
				raw, _ := json.Marshal(msg.Payload)
				var ev ws.ChatEvent
				if err := json.Unmarshal(raw, &ev); err != nil {
					t.Fatalf("chat_event payload: %v", err)
				}
				types[len(types)-1] = ev.Type
				if ev.Type == "text" && strings.Contains(ev.Content, "ACKNOWLEDGED") {
					sawText = true
				}
			}
		default:
			break drain
		}
	}

	if len(types) == 0 || types[0] != "run_begin" {
		t.Fatalf("observer saw %v, want run_begin first", types)
	}
	if !sawText {
		t.Errorf("observer saw %v — the agent's text never arrived", types)
	}
	if types[len(types)-1] != "done" {
		t.Errorf("observer saw %v, want a terminal done last", types)
	}
	if hub.ReplaySession("session:chat1", 0).Active {
		t.Error("the channel still reports a run generating after RunAgent returned")
	}
}

// The published frames must carry the event's metadata verbatim: a tool_call
// with its arguments stripped is not the same frame the WebSocket delivers.
func TestRunAgent_PublishedFramesCarryContentAndMetadata(t *testing.T) {
	t.Parallel()
	stream := `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls"}}]}}` + "\n"
	o := New(covNewRunContainer(covRunOpts{stream: stream}), newMemState(), covQuietLogger())
	pub := &fakeSessionPublisher{}
	o.SetSessionPublisher(pub)

	if err := o.RunAgent(context.Background(), covRunReq(), nil); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}

	pub.mu.Lock()
	defer pub.mu.Unlock()
	var found bool
	for _, ev := range pub.events {
		if ev.Type != "tool_call" {
			continue
		}
		found = true
		if !strings.Contains(ev.Content, "Bash") {
			t.Errorf("tool_call content = %q, want the tool name", ev.Content)
		}
		if ev.Metadata == nil {
			t.Error("tool_call published with no metadata — the arguments are gone")
		}
	}
	if !found {
		t.Errorf("no tool_call reached the session channel: %v", pub.events)
	}
}
