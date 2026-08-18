package chatbridge

// The submission envelope, at the point it stops being a browser's business.
//
// A form submit sends an ORDINARY user message — that is what makes forms work
// against every CLI adapter — and the envelope rides beside it on
// conversation.Message.Metadata, which already exists and already round-trips
// through the JSONL store. These tests pin both halves of that: the envelope
// reaches the persisted turn intact, and the CONTENT of that turn is
// byte-identical to what it would be without one.
//
// They also pin what does NOT get through. The metadata arrives from an
// untrusted client, so the bridge stores an envelope it can read and validate
// and nothing else — never the raw map.

import (
	"context"
	"testing"

	"github.com/crewship-ai/crewship/internal/askforms"
	"github.com/crewship-ai/crewship/internal/conversation"
	"github.com/crewship-ai/crewship/internal/ws"
)

// groupBridge returns a bridge whose chat is a group chat, so an un-@mentioned
// line persists the human turn and returns without touching a container.
func groupBridge(t *testing.T) *Bridge {
	t.Helper()
	info := baseInfo()
	info.Visibility = "group"
	b, _ := testBridge(t, &capResolver{info: info})
	return b
}

func envelopeMetadata(submissionID string) map[string]any {
	return map[string]any{
		askforms.EnvelopeMetadataKey: map[string]any{
			"submission_id": submissionID,
			"form_id":       "receipt",
			"form_label":    "Add a receipt",
			"form_version":  float64(2),
			"values":        map[string]any{"vendor": "Acme", "amount": "12.50"},
			"field_attachment_ids": map[string]any{
				"photo": []any{"attachments/chat-1/receipt.png"},
			},
			"rendered_text": "Receipt from Acme for 12.50",
		},
	}
}

func TestHandleChatMessagePersistsAskEnvelope(t *testing.T) {
	tests := []struct {
		name         string
		metadata     map[string]any
		wantEnvelope bool
		wantSubID    string
	}{
		{
			name:         "a form submission",
			metadata:     envelopeMetadata("sub_1"),
			wantEnvelope: true,
			wantSubID:    "sub_1",
		},
		{
			name:         "a plain message",
			metadata:     nil,
			wantEnvelope: false,
		},
		{
			name:         "metadata with no envelope in it",
			metadata:     map[string]any{"something_else": "value"},
			wantEnvelope: false,
		},
		{
			name: "an envelope missing the identity content could not be",
			metadata: map[string]any{
				askforms.EnvelopeMetadataKey: map[string]any{"form_id": "receipt"},
			},
			wantEnvelope: false,
		},
		{
			name: "an envelope that is not even an object",
			metadata: map[string]any{
				askforms.EnvelopeMetadataKey: "not an object",
			},
			wantEnvelope: false,
		},
	}

	const content = "Receipt from Acme for 12.50"

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := groupBridge(t)
			chatID := "sess-" + tt.name

			opt := ws.ChatMessageOption{Metadata: tt.metadata}
			if err := b.HandleChatMessage(context.Background(), "user-1", chatID, content, func(ws.ChatEvent) {}, opt); err != nil {
				t.Fatalf("HandleChatMessage: %v", err)
			}

			msgs, err := b.convStore.Read(context.Background(), chatID, 0, 0)
			if err != nil {
				t.Fatalf("read conversation: %v", err)
			}
			if len(msgs) != 1 {
				t.Fatalf("persisted messages = %d (%+v), want just the human turn", len(msgs), msgs)
			}
			msg := msgs[0]

			if msg.Role != conversation.RoleUser {
				t.Errorf("role = %q, want user", msg.Role)
			}
			// The message stays an ordinary message: the envelope is metadata
			// riding alongside, never part of what the agent reads.
			if msg.Content != content {
				t.Errorf("content = %q, want %q — the envelope must not touch the text", msg.Content, content)
			}

			env, ok := askforms.EnvelopeFromMetadata(msg.Metadata)
			if ok != tt.wantEnvelope {
				t.Fatalf("envelope recovered = %v, want %v (metadata = %+v)", ok, tt.wantEnvelope, msg.Metadata)
			}
			if !tt.wantEnvelope {
				return
			}
			if env.SubmissionID != tt.wantSubID {
				t.Errorf("submission_id = %q, want %q", env.SubmissionID, tt.wantSubID)
			}
			if env.FormID != "receipt" || env.FormLabel != "Add a receipt" || env.FormVersion != 2 {
				t.Errorf("form identity = %+v, want receipt/Add a receipt/v2", env)
			}
			if env.Values["vendor"] != "Acme" || env.Values["amount"] != "12.50" {
				t.Errorf("values = %+v, want the answers the user gave", env.Values)
			}
			if got := env.FieldAttachmentIDs["photo"]; len(got) != 1 || got[0] != "attachments/chat-1/receipt.png" {
				t.Errorf("field_attachment_ids = %+v, want the upload that answered `photo`", env.FieldAttachmentIDs)
			}
		})
	}
}

// The collision the old content-keyed map could not survive: same form, same
// answers, same rendered text, two presses of Send. Each turn must carry its
// own submission id.
func TestTwoIdenticalSubmissionsKeepTheirOwnIDs(t *testing.T) {
	b := groupBridge(t)
	const chatID = "sess-twice"
	const content = "Receipt from Acme for 12.50"

	for _, id := range []string{"sub_a", "sub_b"} {
		opt := ws.ChatMessageOption{Metadata: envelopeMetadata(id)}
		if err := b.HandleChatMessage(context.Background(), "user-1", chatID, content, func(ws.ChatEvent) {}, opt); err != nil {
			t.Fatalf("HandleChatMessage(%s): %v", id, err)
		}
	}

	msgs, err := b.convStore.Read(context.Background(), chatID, 0, 0)
	if err != nil {
		t.Fatalf("read conversation: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("persisted messages = %d, want two human turns", len(msgs))
	}

	var got []string
	for _, m := range msgs {
		env, ok := askforms.EnvelopeFromMetadata(m.Metadata)
		if !ok {
			t.Fatalf("message %q carries no envelope (metadata = %+v)", m.ID, m.Metadata)
		}
		got = append(got, env.SubmissionID)
	}
	if len(got) != 2 || got[0] != "sub_a" || got[1] != "sub_b" {
		t.Errorf("submission ids = %v, want [sub_a sub_b]", got)
	}
}

// A message that came from no form must persist exactly as it did before any
// of this existed: no metadata key, nothing to omit, nothing to read back.
func TestPlainMessagePersistsWithNoMetadata(t *testing.T) {
	b := groupBridge(t)
	const chatID = "sess-plain"

	if err := b.HandleChatMessage(context.Background(), "user-1", chatID, "just typing", func(ws.ChatEvent) {}); err != nil {
		t.Fatalf("HandleChatMessage: %v", err)
	}

	msgs, err := b.convStore.Read(context.Background(), chatID, 0, 0)
	if err != nil {
		t.Fatalf("read conversation: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("persisted messages = %d, want one", len(msgs))
	}
	if msgs[0].Metadata != nil {
		t.Errorf("metadata = %+v, want nil for a message that came from no form", msgs[0].Metadata)
	}
	if msgs[0].Content != "just typing" {
		t.Errorf("content = %q, want %q", msgs[0].Content, "just typing")
	}
}
