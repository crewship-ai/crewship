package artifacts

import (
	"bytes"
	"encoding/json"
	"errors"
	"regexp"
)

const ProjectIntentMetadataKey = "artifact_project_intent"

// CodeCrewNotAllowed is the typed code for a refusal because the agent's crew
// is outside the workspace artifact-creation policy. CrewNotAllowedMessage is
// the specific text a client shows instead of a generic save failure.
const (
	CodeCrewNotAllowed    = "artifact_crew_not_allowed"
	CrewNotAllowedMessage = "This agent's crew is not allowed to create artifact projects in this workspace"
)

// ErrCrewNotAllowed marks an admission refused by the crew allowlist.
var ErrCrewNotAllowed = errors.New("agent crew is not allowed by the artifact creation policy")

// ProjectIntent is explicit human purpose at the trusted send boundary. It is
// never a model argument. The host binds the mirrored message to one live run.
type ProjectIntent struct {
	Kind          string `json:"kind,omitempty"`
	SubmissionID  string `json:"submission_id"`
	PageID        string `json:"page_id,omitempty"`
	CommentID     string `json:"comment_id,omitempty"`
	BaseVersion   int64  `json:"base_version,omitempty"`
	CreatedByTest bool   `json:"created_by_test,omitempty"`
}

var projectIntentID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

func (p ProjectIntent) Validate() error {
	if p.CreatedByTest && p.PageID != "" || (p.Kind != "" && p.Kind != "design") || (p.Kind == "design" && p.PageID == "") || !projectIntentID.MatchString(p.SubmissionID) || p.PageID != "" && !projectIntentID.MatchString(p.PageID) || p.CommentID != "" && !projectIntentID.MatchString(p.CommentID) || p.PageID == "" && (p.CommentID != "" || p.BaseVersion != 0) || p.PageID != "" && p.BaseVersion < 1 {
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
		if key != "created_by_test" && key != "kind" && key != "submission_id" && key != "page_id" && key != "comment_id" && key != "base_version" {
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
