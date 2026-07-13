package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
)

// fakeEventSource feeds scripted WSMessages to collectAgentStream. After the
// script is exhausted it blocks (like a silent socket) unless closeErr is
// set, in which case it returns that error once.
type fakeEventSource struct {
	msgs     []*cli.WSMessage
	closeErr error
	i        int
	block    chan struct{} // never closed — simulates a stalled socket
}

func newFakeEventSource(closeErr error, events ...cli.ChatEventPayload) *fakeEventSource {
	f := &fakeEventSource{closeErr: closeErr, block: make(chan struct{})}
	for _, e := range events {
		payload, _ := json.Marshal(e)
		f.msgs = append(f.msgs, &cli.WSMessage{Type: "chat_event", Payload: payload})
	}
	return f
}

func (f *fakeEventSource) ReadMessage() (*cli.WSMessage, error) {
	if f.i < len(f.msgs) {
		m := f.msgs[f.i]
		f.i++
		return m, nil
	}
	if f.closeErr != nil {
		return nil, f.closeErr
	}
	<-f.block // stall forever — only the caller's timeout can end this
	return nil, errors.New("unreachable")
}

func TestCollectAgentStream_AccumulatesUntilDone(t *testing.T) {
	src := newFakeEventSource(nil,
		cli.ChatEventPayload{Type: "text", Content: "hello "},
		cli.ChatEventPayload{Type: "text", Content: "world"},
		cli.ChatEventPayload{Type: "done"},
	)
	res := collectAgentStream(src, time.Second)
	if res.Text != "hello world" {
		t.Errorf("text = %q, want %q", res.Text, "hello world")
	}
	if !res.GotDone || res.StreamErr != "" || res.ReadErr != nil || res.TimedOut {
		t.Errorf("terminal state = %+v, want clean done", res)
	}
}

func TestCollectAgentStream_ErrorEventStops(t *testing.T) {
	src := newFakeEventSource(nil,
		cli.ChatEventPayload{Type: "text", Content: "partial"},
		cli.ChatEventPayload{Type: "error", Content: "boom \x1b[31mred\x1b[0m"},
	)
	res := collectAgentStream(src, time.Second)
	if res.StreamErr == "" {
		t.Fatal("StreamErr must be set on an error event")
	}
	// Error content is sanitized on capture — control characters never
	// survive into stderr prints or returned error strings.
	if strings.ContainsRune(res.StreamErr, '\x1b') {
		t.Errorf("StreamErr = %q, control characters must be stripped", res.StreamErr)
	}
	if !strings.Contains(res.StreamErr, "boom") {
		t.Errorf("StreamErr = %q, want the error text preserved", res.StreamErr)
	}
	if res.GotDone {
		t.Error("error event must not report done")
	}
	if res.Text != "partial" {
		t.Errorf("text before the error must be preserved: %q", res.Text)
	}
}

func TestCollectAgentStream_SocketCloseSurfacesReadErr(t *testing.T) {
	src := newFakeEventSource(errors.New("connection reset"),
		cli.ChatEventPayload{Type: "text", Content: "x"},
	)
	res := collectAgentStream(src, time.Second)
	if res.ReadErr == nil {
		t.Fatal("ReadErr must surface a socket close")
	}
	if res.GotDone || res.TimedOut {
		t.Errorf("terminal state = %+v, want read error only", res)
	}
}

func TestCollectAgentStream_TimeoutOnStalledSocket(t *testing.T) {
	src := newFakeEventSource(nil,
		cli.ChatEventPayload{Type: "text", Content: "then silence"},
	)
	start := time.Now()
	res := collectAgentStream(src, 50*time.Millisecond)
	if !res.TimedOut {
		t.Fatalf("stalled socket must time out; got %+v", res)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("timeout took %s, want ~50ms", elapsed)
	}
}

func TestCollectAgentStream_ZeroTimeoutBlocksUntilTerminal(t *testing.T) {
	src := newFakeEventSource(nil,
		cli.ChatEventPayload{Type: "text", Content: "a"},
		cli.ChatEventPayload{Type: "done"},
	)
	res := collectAgentStream(src, 0) // no deadline — runNoStream's mode
	if !res.GotDone || res.Text != "a" {
		t.Errorf("zero-timeout collect = %+v, want done with text", res)
	}
}
