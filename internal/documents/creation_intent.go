package documents

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
)

const CreationMetadataKey = "document_creation"

// CreationIntent is one authenticated human's explicit request to create a
// private Docs from this chat message. It cannot nominate another identity.
type CreationIntent struct {
	SubmissionID string `json:"submission_id"`
}

var creationSubmissionID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func (v *CreationIntent) UnmarshalJSON(raw []byte) error {
	if len(raw) > 512 {
		return errors.New("document creation intent exceeds limit")
	}
	type wire CreationIntent
	var decoded wire
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&decoded) != nil || d.Decode(new(any)) != io.EOF {
		return errors.New("closed document creation intent required")
	}
	*v = CreationIntent(decoded)
	return v.Validate()
}

func (v CreationIntent) Validate() error {
	if !creationSubmissionID.MatchString(v.SubmissionID) {
		return errors.New("document creation requires one valid submission_id")
	}
	return nil
}

// CreationIntentFromMetadata accepts only the closed consent shape. Other
// metadata is handled by its existing consumer, never copied as authority.
func CreationIntentFromMetadata(metadata map[string]any) (*CreationIntent, error) {
	raw, present := metadata[CreationMetadataKey]
	if !present {
		return nil, nil
	}
	object, ok := raw.(map[string]any)
	if !ok || len(object) != 1 {
		return nil, errors.New("document creation intent must contain only submission_id")
	}
	id, ok := object["submission_id"].(string)
	if !ok {
		return nil, errors.New("document creation submission_id must be text")
	}
	value := &CreationIntent{SubmissionID: id}
	return value, value.Validate()
}
