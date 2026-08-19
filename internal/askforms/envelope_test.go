package askforms_test

// The submission envelope: what a form submit leaves behind once the readable
// message has been sent.
//
// P0.6, restated: form values were local component state and provenance was an
// in-memory map keyed by the RENDERED MESSAGE CONTENT. Content is not an
// identity — two identical submissions collide — and a reload lost which form
// was answered and with what. The message stays an ordinary message (that
// decision is what makes forms work against every CLI adapter and is not
// reopened); the structure rides beside it as metadata.

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/askforms"
	"github.com/crewship-ai/crewship/internal/conversation"
)

func receiptForm() askforms.Form {
	return askforms.Form{
		ID:       "receipt",
		Label:    "Add a receipt",
		Version:  2,
		Template: "Supplier: {{supplier}}\nDocument: {{document}}",
		Fields: []askforms.Field{
			{Name: "supplier", Label: "Supplier", Type: "text"},
			{Name: "paid", Label: "Paid", Type: "checkbox"},
			{Name: "document", Label: "Document", Type: "file"},
		},
	}
}

func TestNewEnvelopeCarriesTheAnswersAndTheAttachmentsPerField(t *testing.T) {
	form := receiptForm()
	env := askforms.NewEnvelope(form, "sub_1", askforms.Values{
		"supplier": "Vodafone",
		"paid":     true,
		"document": []string{"attachments/chat_7f3a/IMG_4821.heic"},
	}, map[string][]string{
		"document": {"attachments/chat_7f3a/IMG_4821.heic"},
	}, "Supplier: Vodafone\nDocument: attachments/chat_7f3a/IMG_4821.heic")

	if env.FormID != "receipt" || env.FormLabel != "Add a receipt" {
		t.Errorf("envelope does not name the form: %+v", env)
	}
	// The version the author declared, so a reader knows which revision of the
	// questionnaire produced these answers — the definition can move under a
	// transcript that has already been written.
	if env.FormVersion != 2 {
		t.Errorf("form_version is %d, want 2", env.FormVersion)
	}
	if env.SubmissionID != "sub_1" {
		t.Errorf("submission_id is %q", env.SubmissionID)
	}
	if env.Values["supplier"] != "Vodafone" || env.Values["paid"] != true {
		t.Errorf("values are not the answers: %+v", env.Values)
	}
	// An upload is not a typed value. It appears once, under the field that
	// asked for it — which is the whole point of field_attachment_ids and the
	// reason "one file satisfied every upload field" was a bug worth fixing.
	if _, present := env.Values["document"]; present {
		t.Error("an upload field appears in values as well as in field_attachment_ids")
	}
	if got := env.FieldAttachmentIDs["document"]; len(got) != 1 || got[0] != "attachments/chat_7f3a/IMG_4821.heic" {
		t.Errorf("field_attachment_ids does not name the upload: %+v", env.FieldAttachmentIDs)
	}
	if err := env.Validate(); err != nil {
		t.Fatalf("a well-formed envelope was refused: %v", err)
	}
}

// A form with no declared version is version 1. Every stored definition
// predates the field, and an envelope whose version is 0 says "unknown" where
// it should say "the first one".
func TestEnvelopeDefaultsToVersionOne(t *testing.T) {
	form := receiptForm()
	form.Version = 0
	env := askforms.NewEnvelope(form, "sub_1", askforms.Values{"supplier": "Vodafone"}, nil, "Supplier: Vodafone")
	if env.FormVersion != 1 {
		t.Errorf("form_version is %d, want 1", env.FormVersion)
	}
}

func TestEnvelopeNeverCarriesASecretTypedValue(t *testing.T) {
	form := askforms.Form{
		ID:       "legacy",
		Label:    "Legacy",
		Template: "Supplier: {{supplier}}",
		Fields: []askforms.Field{
			{Name: "supplier", Label: "Supplier", Type: "text"},
			{Name: "api", Label: "API key", Type: "api_key"},
		},
	}
	env := askforms.NewEnvelope(form, "sub_1", askforms.Values{
		"supplier": "Vodafone",
		"api":      "sk-live-DO-NOT-SEND",
	}, nil, "Supplier: Vodafone")

	blob, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(blob), "sk-live") {
		t.Fatalf("the envelope carries a secret-typed answer: %s", blob)
	}
	if _, present := env.Values["api"]; present {
		t.Error("the secret-typed field has an entry in values")
	}
}

// The seam, asserted rather than assumed: conversation.Message already carries
// `Metadata any`, the JSONL line is schemaless and the reader unmarshals into
// the same field. So the envelope rides an existing column with NO migration —
// this test is what makes that claim checkable rather than a sentence in a PR.
func TestEnvelopeSurvivesAConversationRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := conversation.NewStore(dir, slog.New(slog.DiscardHandler))
	defer store.Close()

	form := receiptForm()
	env := askforms.NewEnvelope(form, "sub_1", askforms.Values{
		"supplier": "Vodafone",
		"paid":     true,
	}, map[string][]string{"document": {"attachments/chat_7f3a/IMG_4821.heic"}},
		"Supplier: Vodafone")

	// Two IDENTICAL submissions, which is the case content keying could not
	// tell apart. They differ only by submission id — everything a reader
	// needs to distinguish them has to survive the write.
	second := env
	second.SubmissionID = "sub_2"

	for _, e := range []askforms.Envelope{env, second} {
		if err := store.Append(context.Background(), "chat_7f3a", conversation.Message{
			ID:       "msg_" + e.SubmissionID,
			Role:     conversation.RoleUser,
			Content:  e.RenderedText,
			Metadata: map[string]any{askforms.EnvelopeMetadataKey: e},
		}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	msgs, err := store.Read(context.Background(), "chat_7f3a", 0, 0)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("read %d messages, want 2", len(msgs))
	}

	wantIDs := []string{"sub_1", "sub_2"}
	for i, msg := range msgs {
		got, ok := askforms.EnvelopeFromMetadata(msg.Metadata)
		if !ok {
			t.Fatalf("message %d came back with no envelope: %+v", i, msg.Metadata)
		}
		if got.SubmissionID != wantIDs[i] {
			t.Errorf("message %d is submission %q, want %q", i, got.SubmissionID, wantIDs[i])
		}
		if got.FormID != "receipt" || got.FormVersion != 2 {
			t.Errorf("message %d lost the form identity: %+v", i, got)
		}
		if got.Values["supplier"] != "Vodafone" || got.Values["paid"] != true {
			t.Errorf("message %d lost its answers: %+v", i, got.Values)
		}
		if ids := got.FieldAttachmentIDs["document"]; len(ids) != 1 {
			t.Errorf("message %d lost its per-field attachments: %+v", i, got.FieldAttachmentIDs)
		}
		// The text is still an ordinary readable message — that is the half of
		// the design the envelope does not replace.
		if msg.Content != "Supplier: Vodafone" {
			t.Errorf("message %d is no longer plain text: %q", i, msg.Content)
		}
	}
}

func TestEnvelopeFromMetadataIgnoresAnythingElse(t *testing.T) {
	for _, in := range []any{
		nil,
		map[string]any{"trace_id": "abc"},
		map[string]any{askforms.EnvelopeMetadataKey: "not an object"},
		map[string]any{askforms.EnvelopeMetadataKey: map[string]any{"form_id": ""}},
		"a string",
	} {
		if _, ok := askforms.EnvelopeFromMetadata(in); ok {
			t.Errorf("%#v was read as an envelope", in)
		}
	}
}
