package orchestrator

import (
	"log/slog"
	"sync"
	"testing"
	"time"
)

// fakeSkillObs records every SkillInvocation it receives.
type fakeSkillObs struct {
	mu   sync.Mutex
	got  []SkillInvocation
	done chan struct{}
}

func (f *fakeSkillObs) Observe(si SkillInvocation) {
	f.mu.Lock()
	f.got = append(f.got, si)
	f.mu.Unlock()
	if f.done != nil {
		f.done <- struct{}{}
	}
}

func (f *fakeSkillObs) calls() []SkillInvocation {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]SkillInvocation, len(f.got))
	copy(out, f.got)
	return out
}

func toolCallEvent(name string, input map[string]any) AgentEvent {
	return AgentEvent{
		Type:    "tool_call",
		Content: name,
		Metadata: map[string]interface{}{
			"tool_name": name,
			"input":     input,
		},
	}
}

// TestDispatchToolCallObservers_FiresSkillObserver asserts that a Skill
// tool_use reaches the skill-invocation observer with the extracted tool
// name + payload, and that Read / Bash calls also reach it (the observer
// — not the dispatcher — decides whether a call maps to an assigned
// skill). The dispatcher's job is to forward every tool_call; the match
// logic lives in the consumer.
func TestDispatchToolCallObservers_FiresSkillObserver(t *testing.T) {
	o := New(nil, nil, slog.Default())
	f := &fakeSkillObs{done: make(chan struct{}, 1)}
	o.SetSkillInvocationObserver(f)

	req := AgentRunRequest{
		WorkspaceID: "ws1", CrewID: "cr1", AgentID: "a1", MissionID: "m1",
	}
	o.dispatchToolCallObservers(req, toolCallEvent("Skill", map[string]any{"skill": "deploy"}))

	select {
	case <-f.done:
	case <-time.After(2 * time.Second):
		t.Fatal("observer not invoked within timeout")
	}

	calls := f.calls()
	if len(calls) != 1 {
		t.Fatalf("observer calls = %d, want 1", len(calls))
	}
	c := calls[0]
	if c.ToolName != "Skill" {
		t.Errorf("ToolName = %q, want Skill", c.ToolName)
	}
	if c.WorkspaceID != "ws1" || c.AgentID != "a1" || c.CrewID != "cr1" || c.MissionID != "m1" {
		t.Errorf("identity fields not threaded: %+v", c)
	}
	in, ok := c.Payload["input"].(map[string]any)
	if !ok || in["skill"] != "deploy" {
		t.Errorf("payload input not forwarded: %+v", c.Payload)
	}
}

// TestDispatchToolCallObservers_NilObserver is a no-op + no panic when no
// skill observer is wired (the dev/test default).
func TestDispatchToolCallObservers_NilObserver(t *testing.T) {
	o := New(nil, nil, slog.Default())
	// No observer set.
	o.dispatchToolCallObservers(
		AgentRunRequest{WorkspaceID: "ws1", AgentID: "a1"},
		toolCallEvent("Read", map[string]any{"file_path": "/x"}),
	)
	// Reaching here without panic is the assertion.
}

// TestDispatchToolCallObservers_EmptyToolName drops events with no
// resolvable tool name before fanning out.
func TestDispatchToolCallObservers_EmptyToolName(t *testing.T) {
	o := New(nil, nil, slog.Default())
	f := &fakeSkillObs{}
	o.SetSkillInvocationObserver(f)

	// Event with neither Content nor metadata tool_name.
	o.dispatchToolCallObservers(
		AgentRunRequest{WorkspaceID: "ws1", AgentID: "a1"},
		AgentEvent{Type: "tool_call", Metadata: map[string]interface{}{}},
	)
	// Give any (incorrectly) spawned goroutine a moment.
	time.Sleep(50 * time.Millisecond)
	if n := len(f.calls()); n != 0 {
		t.Fatalf("observer calls = %d, want 0 for empty tool name", n)
	}
}

// TestDispatchToolCallObservers_ReadAndBashForwarded confirms Read/Bash
// tool calls are forwarded (the consumer rejects non-skill tools, not the
// dispatcher) and that tool_name is taken from event.Content when set.
func TestDispatchToolCallObservers_ReadAndBashForwarded(t *testing.T) {
	o := New(nil, nil, slog.Default())
	f := &fakeSkillObs{done: make(chan struct{}, 8)}
	o.SetSkillInvocationObserver(f)

	req := AgentRunRequest{WorkspaceID: "ws1", AgentID: "a1"}
	o.dispatchToolCallObservers(req, toolCallEvent("Read", map[string]any{"file_path": "/x"}))
	o.dispatchToolCallObservers(req, toolCallEvent("Bash", map[string]any{"command": "ls"}))

	for i := 0; i < 2; i++ {
		select {
		case <-f.done:
		case <-time.After(2 * time.Second):
			t.Fatal("observer not invoked twice within timeout")
		}
	}
	calls := f.calls()
	if len(calls) != 2 {
		t.Fatalf("observer calls = %d, want 2", len(calls))
	}
	names := map[string]bool{}
	for _, c := range calls {
		names[c.ToolName] = true
	}
	if !names["Read"] || !names["Bash"] {
		t.Errorf("expected Read and Bash forwarded, got %v", names)
	}
}

// TestDispatchToolCallObservers_SemaphoreOverflowDrops verifies the
// bounded fan-out: when the skill semaphore is saturated, further events
// are dropped rather than spawning unbounded goroutines.
func TestDispatchToolCallObservers_SemaphoreOverflowDrops(t *testing.T) {
	o := New(nil, nil, slog.Default())
	// Block the observer so in-flight goroutines hold their tokens.
	release := make(chan struct{})
	var started sync.WaitGroup
	blocking := observeFunc(func(SkillInvocation) {
		started.Done()
		<-release
	})
	o.SetSkillInvocationObserver(blocking)

	req := AgentRunRequest{WorkspaceID: "ws1", AgentID: "a1"}
	// Saturate the semaphore: spawn skillInvocationSemCap blocked
	// observations.
	started.Add(skillInvocationSemCap)
	for i := 0; i < skillInvocationSemCap; i++ {
		o.dispatchToolCallObservers(req, toolCallEvent("deploy", nil))
	}
	started.Wait() // all cap tokens held

	// One more dispatch must be dropped (no token available) — it
	// returns immediately without blocking and without a new goroutine.
	done := make(chan struct{})
	go func() {
		o.dispatchToolCallObservers(req, toolCallEvent("deploy", nil))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("overflow dispatch blocked; expected immediate drop")
	}

	close(release) // let the held goroutines finish
}

// observeFunc adapts a func to the SkillInvocationObserver interface.
type observeFunc func(SkillInvocation)

func (f observeFunc) Observe(si SkillInvocation) { f(si) }

// fakePostObs records ToolCallObservations for the behavior-monitor side.
type fakePostObs struct {
	mu   sync.Mutex
	got  []ToolCallObservation
	done chan struct{}
}

func (f *fakePostObs) Observe(o ToolCallObservation) {
	f.mu.Lock()
	f.got = append(f.got, o)
	f.mu.Unlock()
	if f.done != nil {
		f.done <- struct{}{}
	}
}

func (f *fakePostObs) calls() []ToolCallObservation {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]ToolCallObservation, len(f.got))
	copy(out, f.got)
	return out
}

// TestDispatchToolCallObservers_FiresPostToolCallObserver covers the
// behavior-monitor fan-out branch (sibling to the skill observer).
func TestDispatchToolCallObservers_FiresPostToolCallObserver(t *testing.T) {
	o := New(nil, nil, slog.Default())
	f := &fakePostObs{done: make(chan struct{}, 1)}
	o.SetPostToolCallObserver(f)

	req := AgentRunRequest{WorkspaceID: "ws1", CrewID: "cr1", AgentID: "a1", MissionID: "m1"}
	o.dispatchToolCallObservers(req, toolCallEvent("Skill", map[string]any{"skill": "deploy"}))

	select {
	case <-f.done:
	case <-time.After(2 * time.Second):
		t.Fatal("post-tool-call observer not invoked")
	}
	calls := f.calls()
	if len(calls) != 1 || calls[0].ToolName != "Skill" {
		t.Fatalf("unexpected post-tool-call calls: %+v", calls)
	}
	in, ok := calls[0].Payload["input"].(map[string]any)
	if !ok || in["skill"] != "deploy" {
		t.Errorf("payload not forwarded to post observer: %+v", calls[0].Payload)
	}
}

// TestDispatchToolCallObservers_ToolNameFromMetadata covers the fallback
// path where event.Content is empty and the tool name is read from the
// metadata map.
func TestDispatchToolCallObservers_ToolNameFromMetadata(t *testing.T) {
	o := New(nil, nil, slog.Default())
	f := &fakeSkillObs{done: make(chan struct{}, 1)}
	o.SetSkillInvocationObserver(f)

	ev := AgentEvent{
		Type: "tool_call",
		// Content empty → resolver must fall back to metadata.tool_name.
		Metadata: map[string]interface{}{
			"tool_name": "deploy",
			"input":     map[string]any{"x": 1},
		},
	}
	o.dispatchToolCallObservers(AgentRunRequest{WorkspaceID: "ws1", AgentID: "a1"}, ev)

	select {
	case <-f.done:
	case <-time.After(2 * time.Second):
		t.Fatal("observer not invoked from metadata tool name")
	}
	calls := f.calls()
	if len(calls) != 1 || calls[0].ToolName != "deploy" {
		t.Fatalf("expected tool name 'deploy' from metadata, got %+v", calls)
	}
}
