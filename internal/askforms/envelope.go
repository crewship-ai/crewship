package askforms

// The submission envelope — what a form submit leaves behind besides the
// message.
//
// ─── What this is, and what it deliberately is not ────────────────────────
//
// Submitting a form sends an ORDINARY USER MESSAGE. That decision is what
// makes forms work against every CLI adapter without teaching any of them a
// new shape, and it is not reopened here. The envelope is ADDITIONAL METADATA
// carried with that message, never a replacement payload: strip it and the
// conversation still reads exactly as it did.
//
// What it buys is the thing P0.6 found missing. Before it, the answers were
// local component state and the "via Add a receipt" badge was an in-memory map
// keyed by the rendered message CONTENT. Content is not an identity — two
// identical submissions collide, and the second silently relabels the first —
// and a reload lost the form, the field/value relationship and which upload
// answered which question, even when the message itself survived.
//
// ─── Where it rides, and why there is no migration ────────────────────────
//
// conversation.Message already has `Metadata any`, persisted into a schemaless
// JSONL line with `omitempty` and unmarshalled straight back on read. The
// envelope goes in that map under EnvelopeMetadataKey. Nothing about the
// conversation store changes, no column is added, and a message written before
// this existed reads back exactly as before — envelope_test.go asserts the
// round trip rather than asserting the sentence.
//
// The searchable mirror (conversation_messages) stores no metadata and needs
// none: it is a keyword index over the readable text, and the readable text is
// still the whole message.
//
// ─── The rule about secrets ───────────────────────────────────────────────
//
// A field whose type fails closed (fieldtypes.go) has no entry in the
// envelope. Not a redacted one, not an empty one — none. The envelope is a
// durable record that outlives the tab, so "we stored it but marked it" is the
// wrong shape of answer.

import (
	"encoding/json"
	"fmt"
)

// EnvelopeMetadataKey is the key the envelope occupies in a message's
// metadata map. One key, spelled once, because the frontend
// (components/features/chat/asks/ask-envelope.ts) has to agree with it and a
// second spelling is a badge that silently stops rendering.
const EnvelopeMetadataKey = "ask_submission"

// Envelope is one answered form.
type Envelope struct {
	// SubmissionID is the identity content could not be. Minted where the
	// submission happens, one per press of Send, so two identical answers to
	// the same form are two records rather than one overwritten one.
	SubmissionID string `json:"submission_id"`
	FormID       string `json:"form_id"`
	// FormLabel is the chip's text at the moment of submitting. Carried
	// because a transcript is read long after the definition may have been
	// renamed or deleted, and "via receipt" is a slug, not an answer to what
	// the user actually clicked.
	FormLabel string `json:"form_label,omitempty"`
	// FormVersion is the author's revision (Form.Version), 1 when unset.
	FormVersion int `json:"form_version"`
	// Values are the typed answers, keyed by field name. Upload fields are
	// NOT here — they are in FieldAttachmentIDs, so a file appears once and
	// under the question it answers.
	Values map[string]any `json:"values"`
	// FieldAttachmentIDs is which uploads answered which field.
	//
	// The values are the durable handles the platform has TODAY, which are the
	// agent-visible relative paths the upload endpoint returns
	// (`attachments/<chatId>/<file>`). When canonical attachment identity
	// lands (audit P0.2/P0.3, `att_…` ids with a content hash), the ids change
	// and this shape does not — which is why the field is named for identity
	// rather than for paths.
	FieldAttachmentIDs map[string][]string `json:"field_attachment_ids,omitempty"`
	// RenderedText is the message this envelope belongs to, as the form
	// rendered it. The tie-break for a reader holding both: an envelope whose
	// text does not match the turn it is attached to is not describing it.
	RenderedText string `json:"rendered_text"`
}

// NewEnvelope builds the record for one submission.
//
// values are the answers as the renderer received them; attachments is the
// per-field upload list. Anything the field-type rule fails closed on is
// dropped rather than carried, and upload fields are moved out of values so
// the two halves cannot disagree about which file answered what.
func NewEnvelope(f Form, submissionID string, values Values, attachments map[string][]string, renderedText string) Envelope {
	version := f.Version
	if version < 1 {
		version = 1
	}

	env := Envelope{
		SubmissionID: submissionID,
		FormID:       f.ID,
		FormLabel:    f.Label,
		FormVersion:  version,
		Values:       map[string]any{},
		RenderedText: renderedText,
	}

	safe := SanitizeValues(f, values)
	for _, field := range f.Fields {
		if IsAttachmentType(field.Type) {
			continue
		}
		v, present := safe[field.Name]
		if !present {
			continue
		}
		env.Values[field.Name] = v
	}
	// A money field's currency is a second answer under a derived name and is
	// not a field of its own, so it is copied across explicitly.
	for _, field := range f.Fields {
		if field.Type != "money" {
			continue
		}
		if v, present := safe[CurrencyPlaceholder(field.Name)]; present {
			env.Values[CurrencyPlaceholder(field.Name)] = v
		}
	}

	for _, field := range f.Fields {
		if !IsAttachmentType(field.Type) || !SafeFieldType(field.Type) {
			continue
		}
		if ids := attachments[field.Name]; len(ids) > 0 {
			if env.FieldAttachmentIDs == nil {
				env.FieldAttachmentIDs = map[string][]string{}
			}
			env.FieldAttachmentIDs[field.Name] = append([]string(nil), ids...)
		}
	}

	return env
}

// Validate refuses an envelope that could not describe a real submission. A
// reader that finds one of these has a record it cannot attribute, which is
// worse than no record at all.
func (e Envelope) Validate() error {
	if e.SubmissionID == "" {
		return fmt.Errorf("submission_id is required — it is the identity the rendered text could not be")
	}
	if e.FormID == "" {
		return fmt.Errorf("form_id is required")
	}
	if e.FormVersion < 1 {
		return fmt.Errorf("form_version must be at least 1 (got %d)", e.FormVersion)
	}
	return nil
}

// EnvelopeFromMetadata reads an envelope back out of a message's metadata.
//
// Tolerant by construction: metadata is `any`, it arrives from JSONL written
// by an older build or from a WS frame, and every shape that is not an
// envelope is simply "this message did not come from a form". Nothing here
// errors, because there is no caller for whom a malformed badge is worth an
// error path.
func EnvelopeFromMetadata(metadata any) (Envelope, bool) {
	m, ok := metadata.(map[string]any)
	if !ok {
		return Envelope{}, false
	}
	raw, ok := m[EnvelopeMetadataKey]
	if !ok {
		return Envelope{}, false
	}
	blob, err := json.Marshal(raw)
	if err != nil {
		return Envelope{}, false
	}
	var env Envelope
	if err := json.Unmarshal(blob, &env); err != nil {
		return Envelope{}, false
	}
	if env.Validate() != nil {
		return Envelope{}, false
	}
	return env, true
}
