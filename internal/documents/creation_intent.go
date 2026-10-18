package documents

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
)

const CreationMetadataKey = "document_creation"

// ComposerCreationMetadataKey is the explicit human action on a chat send.
// The older key remains readable for existing clients and stored messages.
const ComposerCreationMetadataKey = "document_creation_intent"

// CreationIntent is one authenticated human's explicit request to create a
// private Docs from this chat message. It cannot nominate another identity.
type CreationIntent struct {
	SubmissionID string `json:"submission_id"`
	Drafting     bool   `json:"drafting,omitempty"`
}

var creationSubmissionID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func (v *CreationIntent) UnmarshalJSON(raw []byte) error {
	if len(raw) > 512 {
		return errors.New("document creation intent exceeds limit")
	}
	var decoded struct {
		SubmissionID string          `json:"submission_id"`
		Drafting     json.RawMessage `json:"drafting"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&decoded) != nil || d.Decode(new(any)) != io.EOF {
		return errors.New("closed document creation intent required")
	}
	drafting := false
	if decoded.Drafting != nil {
		switch string(bytes.TrimSpace(decoded.Drafting)) {
		case "true":
			drafting = true
		case "false":
		default:
			return errors.New("document drafting purpose must be boolean")
		}
	}
	*v = CreationIntent{SubmissionID: decoded.SubmissionID, Drafting: drafting}
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
	raw, present := metadata[ComposerCreationMetadataKey]
	legacy, legacyPresent := metadata[CreationMetadataKey]
	if present && legacyPresent {
		return nil, errors.New("one document creation intent required")
	}
	if !present {
		raw, present = legacy, legacyPresent
	}
	if !present {
		return nil, nil
	}
	object, ok := raw.(map[string]any)
	if !ok || len(object) < 1 || len(object) > 2 {
		return nil, errors.New("closed document creation intent required")
	}
	for key := range object {
		if key != "submission_id" && key != "drafting" {
			return nil, errors.New("closed document creation intent required")
		}
	}
	id, ok := object["submission_id"].(string)
	if !ok {
		return nil, errors.New("document creation submission_id must be text")
	}
	value := &CreationIntent{SubmissionID: id}
	if rawDrafting, present := object["drafting"]; present {
		drafting, ok := rawDrafting.(bool)
		if !ok {
			return nil, errors.New("document drafting purpose must be boolean")
		}
		value.Drafting = drafting
	}
	return value, value.Validate()
}
