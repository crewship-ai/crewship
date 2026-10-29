package documents

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

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
	if !creationSubmissionID.MatchString(v.SubmissionID) || !creationSubmissionID.MatchString(v.PageID) || v.Schema != 3 || v.Rev < 0 || v.Generation < 0 || v.Rev > 9007199254740991 || v.Generation > 9007199254740991 {
		return errors.New("one explicit Document identity and original schema/revision/generation required")
	}
	return nil
}

// UnmarshalJSON keeps the HTTP and CLI target contract closed, complete and
// non-null. Duplicate fields cannot select different original preconditions.
func (v *IterationIntent) UnmarshalJSON(raw []byte) error {
	if len(raw) > 1024 {
		return errors.New("Document target exceeds limit")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return errors.New("Document target object required")
	}
	keys := map[string]bool{}
	for d.More() {
		token, err := d.Token()
		if err != nil {
			return err
		}
		key, ok := token.(string)
		if !ok || keys[key] {
			return errors.New("unique Document target fields required")
		}
		switch key {
		case "submission_id", "page_id", "schema", "rev", "generation":
		default:
			return errors.New("closed Document target required")
		}
		var value json.RawMessage
		if d.Decode(&value) != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return errors.New("non-null Document target fields required")
		}
		keys[key] = true
	}
	if _, err := d.Token(); err != nil || len(keys) != 5 || d.Decode(new(any)) != io.EOF {
		return errors.New("complete Document target required")
	}
	type wire IterationIntent
	var decoded wire
	if json.Unmarshal(raw, &decoded) != nil {
		return errors.New("valid Document target fields required")
	}
	*v = IterationIntent(decoded)
	return v.Validate()
}

// IterationIntentFromMetadata accepts only an explicit original target, never
// identity, permissions or a later latest-revision lookup supplied by a model.
func IterationIntentFromMetadata(metadata map[string]any) (*IterationIntent, error) {
	raw, exists := metadata["document_iteration_intent"]
	if !exists {
		return nil, nil
	}
	object, ok := raw.(map[string]any)
	if !ok || len(object) != 5 {
		return nil, errors.New("closed original Document target required")
	}
	for _, key := range []string{"submission_id", "page_id", "schema", "rev", "generation"} {
		if _, ok := object[key]; !ok {
			return nil, errors.New("complete original Document target required")
		}
	}
	encoded, err := json.Marshal(object)
	if err != nil || len(encoded) > 1024 {
		return nil, errors.New("bounded original Document target required")
	}
	var intent IterationIntent
	d := json.NewDecoder(bytes.NewReader(encoded))
	d.DisallowUnknownFields()
	if d.Decode(&intent) != nil || d.Decode(new(any)) != io.EOF || intent.Validate() != nil {
		return nil, errors.New("valid original Document target required")
	}
	return &intent, nil
}
