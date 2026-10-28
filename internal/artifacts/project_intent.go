package artifacts

import (
	"bytes"
	"encoding/json"
	"errors"
	"regexp"
)

const ProjectIntentMetadataKey = "artifact_project_intent"

// ProjectIntent is explicit human purpose at the trusted send boundary. It is
// never a model argument. The host binds the mirrored message to one live run.
type ProjectIntent struct {
	CreatedByTest bool   `json:"created_by_test,omitempty"`
	SubmissionID  string `json:"submission_id"`
	PageID        string `json:"page_id,omitempty"`
	CommentID     string `json:"comment_id,omitempty"`
	BaseVersion   int64  `json:"base_version,omitempty"`
}

var projectIntentID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

func (p ProjectIntent) Validate() error {
	if p.CreatedByTest && p.PageID != "" || !projectIntentID.MatchString(p.SubmissionID) || p.PageID != "" && !projectIntentID.MatchString(p.PageID) || p.CommentID != "" && !projectIntentID.MatchString(p.CommentID) || p.PageID == "" && (p.CommentID != "" || p.BaseVersion != 0) || p.PageID != "" && p.BaseVersion < 1 {
		return errors.New("one explicit artifact project target and base version required")
	}
	return nil
}

func ProjectIntentFromMetadata(metadata map[string]any) (*ProjectIntent, error) {
	value, ok := metadata[ProjectIntentMetadataKey]
	if !ok {
		return nil, nil
	}
	raw, err := json.Marshal(value)
	if err != nil || len(raw) > 2048 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, errors.New("invalid artifact project intent")
	}
	var keys map[string]json.RawMessage
	if json.Unmarshal(raw, &keys) != nil || keys == nil {
		return nil, errors.New("artifact project intent must be a closed object")
	}
	for key := range keys {
		if key != "submission_id" && key != "page_id" && key != "comment_id" && key != "base_version" && key != "created_by_test" {
			return nil, errors.New("unknown artifact project intent field")
		}
	}
	if value, ok := keys["created_by_test"]; ok && !bytes.Equal(bytes.TrimSpace(value), []byte("true")) && !bytes.Equal(bytes.TrimSpace(value), []byte("false")) {
		return nil, errors.New("created_by_test must be a boolean")
	}
	var intent ProjectIntent
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&intent) != nil || intent.Validate() != nil {
		return nil, errors.New("invalid artifact project intent")
	}
	return &intent, nil
}
