package orchestrator

import (
	"io"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

// Closing the observer stream is not evidence that detached work ended. A
// synthetic success here can escape through the ordinary event handler before
// recovery ever inspects the retained terminal record.
func TestDurableStreamEOFDoesNotInventTerminalResult(t *testing.T) {
	o := New(nil, newMemState(), opencodeTestLogger())
	o.SetJournal(&chunkRecorder{})
	req := opencodeStreamReq()
	req.DurableOutputDir = "/persistent/runs"
	stream := `{"type":"text","sessionID":"ses_detached","part":{"id":"p1","text":"still working"}}` + "\n"
	result := &provider.ExecResult{ExecID: "observer", Reader: io.NopCloser(strings.NewReader(stream))}
	var text string
	o.streamOutput(t.Context(), result, req, func(event AgentEvent) {
		if event.Type == "result" {
			t.Errorf("reader EOF invented terminal result: %+v", event.Metadata)
		}
		if event.Type == "text" {
			text += event.Content
		}
	}, nil)
	if text != "still working" {
		t.Fatalf("complete observed text lost: %q", text)
	}
}
