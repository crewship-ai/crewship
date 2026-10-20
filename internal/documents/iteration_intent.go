package documents

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"
)

const IterationMetadataKey = "document_iteration_intent"

// IterationIntent identifies one explicit existing Document, never a slug,
// principal or mode supplied by a model. The host binds its human chat receipt.
type IterationIntent struct {
	SubmissionID string `json:"submission_id"`
	PageID       string `json:"page_id"`
	Schema       int    `json:"schema"`
	Rev          int64  `json:"rev"`
	Generation   int64  `json:"generation"`
}

func (v IterationIntent) Validate() error {
	if !creationSubmissionID.MatchString(v.SubmissionID) || !creationSubmissionID.MatchString(v.PageID) || v.Schema != 3 || v.Rev < 0 || v.Generation < 0 {
		return errors.New("one explicit Document identity and original schema/revision/generation required")
	}
	return nil
}

// DecodeIterationIntent accepts exactly one bounded, closed target selection.
// Detect duplicates before unmarshalling: encoding/json otherwise keeps the last.
func DecodeIterationIntent(raw []byte) (IterationIntent, error) {
	var intent IterationIntent
	invalid := errors.New("one bounded closed Document target intent required")
	if len(raw) > 1024 || !utf8.Valid(raw) {
		return intent, invalid
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	if token, err := d.Token(); err != nil || token != json.Delim('{') {
		return intent, invalid
	}
	seen := map[string]bool{}
	for d.More() {
		token, err := d.Token()
		key, ok := token.(string)
		if err != nil || !ok || seen[key] {
			return intent, invalid
		}
		switch key {
		case "submission_id", "page_id", "schema", "rev", "generation":
		default:
			return intent, invalid
		}
		seen[key] = true
		var value json.RawMessage
		if d.Decode(&value) != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return intent, invalid
		}
	}
	if token, err := d.Token(); err != nil || token != json.Delim('}') || len(seen) != 5 || d.Decode(new(any)) != io.EOF {
		return intent, invalid
	}
	if err := json.Unmarshal(raw, &intent); err != nil {
		return IterationIntent{}, invalid
	}
	if err := intent.Validate(); err != nil {
		return IterationIntent{}, err
	}
	return intent, nil
}

// IterationIntentFromMetadata selects only the human's explicit target, never a
// mode, principal or expanded authority from surrounding message metadata.
func IterationIntentFromMetadata(metadata map[string]any) (*IterationIntent, error) {
	value, present := metadata[IterationMetadataKey]
	if !present {
		return nil, nil
	}
	object, ok := value.(map[string]any)
	if !ok || len(object) != 5 {
		return nil, errors.New("closed Document target intent required")
	}
	raw, err := json.Marshal(object)
	if err != nil {
		return nil, err
	}
	intent, err := DecodeIterationIntent(raw)
	if err != nil {
		return nil, err
	}
	return &intent, nil
}
