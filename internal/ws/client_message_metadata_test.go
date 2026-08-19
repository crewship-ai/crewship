package ws

// The transport hop for message metadata.
//
// A form submission sends an ordinary user message with the submission
// envelope riding BESIDE it (internal/askforms). This file pins the one thing
// this package is responsible for in that chain: whatever the client put in
// `metadata` reaches the chat handler unchanged, and a message that carries
// none reaches it with none — the content is identical either way.
//
// Validating the metadata is deliberately NOT done here. It is untrusted, and
// the handler that has to persist it is the one that decides what shape is
// worth keeping (chatbridge.HandleChatMessage rebuilds it through
// askforms.EnvelopeFromMetadata). A transport that pre-judged the payload
// would have to be edited every time the envelope grows a field.

import (
	"context"
	"encoding/json"
	"reflect"
	"sync"
	"testing"
)

// recordingChatHandler captures the arguments of the last HandleChatMessage
// call, which the shared stub deliberately discards.
type recordingChatHandler struct {
	mu      sync.Mutex
	calls   int
	content string
	opts    []ChatMessageOption
}

func (r *recordingChatHandler) HandleChatMessage(_ context.Context, _, _, content string, _ func(event ChatEvent), opts ...ChatMessageOption) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	r.content = content
	r.opts = opts
	return nil
}

func (r *recordingChatHandler) snapshot() (int, string, []ChatMessageOption) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls, r.content, r.opts
}

func TestHandleSendMessageForwardsMetadata(t *testing.T) {
	envelope := map[string]any{
		"ask_submission": map[string]any{
			"submission_id": "sub_1",
			"form_id":       "receipt",
			"form_version":  float64(1),
			"values":        map[string]any{"vendor": "Acme"},
			"rendered_text": "Receipt from Acme",
		},
	}

	tests := []struct {
		name     string
		metadata map[string]any
		want     map[string]any
	}{
		{name: "a form submission", metadata: envelope, want: envelope},
		{name: "a plain message", metadata: nil, want: nil},
		{
			name:     "metadata the transport has no opinion about",
			metadata: map[string]any{"something_else": "value"},
			want:     map[string]any{"something_else": "value"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			hub := newRunningHub(t)
			handler := &recordingChatHandler{}
			hub.SetChatHandler(handler)
			hub.SetChannelAuthorizer(allowAllAuthorizer{})

			c := newClient(t, hub, "u1")
			body, err := json.Marshal(sendMessagePayload{
				ChatID:   "s1",
				Content:  "Receipt from Acme",
				Metadata: tt.metadata,
			})
			if err != nil {
				t.Fatalf("marshal payload: %v", err)
			}
			c.handleSendMessage(ClientMessage{Type: "send_message", Channel: "session:s1", Payload: body})

			waitFor(t, func() bool {
				calls, _, _ := handler.snapshot()
				return calls == 1
			}, "chat handler call")

			_, content, opts := handler.snapshot()
			// The message stays an ordinary message: metadata never touches it.
			if content != "Receipt from Acme" {
				t.Errorf("content = %q, want %q", content, "Receipt from Acme")
			}
			if len(opts) != 1 {
				t.Fatalf("opts = %+v, want exactly one", opts)
			}
			if !reflect.DeepEqual(opts[0].Metadata, tt.want) {
				t.Errorf("metadata = %+v, want %+v", opts[0].Metadata, tt.want)
			}
		})
	}
}

// A payload written before `metadata` existed must decode to no metadata
// rather than to an error — an older CLI is a supported client.
func TestSendMessagePayloadWithoutMetadataDecodes(t *testing.T) {
	t.Parallel()
	var payload sendMessagePayload
	if err := json.Unmarshal([]byte(`{"session_id":"s1","content":"hi"}`), &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload.Metadata != nil {
		t.Errorf("metadata = %+v, want nil", payload.Metadata)
	}
	// …and re-marshalling one omits the key entirely, so the frame a plain
	// message produces is the bytes it always was.
	out, err := json.Marshal(sendMessagePayload{ChatID: "s1", Content: "hi"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got, want := string(out), `{"session_id":"s1","content":"hi"}`; got != want {
		t.Errorf("marshalled = %s, want %s", got, want)
	}
}
