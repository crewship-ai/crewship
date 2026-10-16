package slides

import (
	"encoding/json"
	"errors"
	"strings"
)

const (
	CommentProtocolVersion = 1
	AnchorSchemaVersion    = 1
	MaxCommentBodyBytes    = 10000
	MaxCommentRequestBytes = 16 << 10
	MaxCommentThreads      = 1000
	MaxCommentEvents       = 10000
	MaxCommentHistoryBytes = 16 << 20
	MaxCommentPageSize     = 100
)

// Anchor schema evolves independently from the deck schema. Page identity is
// supplied by the immutable request envelope, never a slug or approximate quote.
type CommentAnchor struct {
	AnchorSchemaVersion int    `json:"anchor_schema_version"`
	Kind                string `json:"kind"`
	SlideID             string `json:"slide_id"`
	ElementID           string `json:"element_id,omitempty"`
	Revision            int64  `json:"revision"`
}
type CreateCommentRequest struct {
	PageID        string        `json:"page_id"`
	SchemaVersion int           `json:"schema_version"`
	BaseRev       int64         `json:"base_rev"`
	MutationID    string        `json:"mutation_id"`
	Anchor        CommentAnchor `json:"anchor"`
	Body          string        `json:"body"`
}
type ReplyCommentRequest struct {
	PageID        string `json:"page_id"`
	SchemaVersion int    `json:"schema_version"`
	MutationID    string `json:"mutation_id"`
	IfVersion     int64  `json:"if_version"`
	Body          string `json:"body"`
}
type ResolveCommentRequest struct {
	PageID        string `json:"page_id"`
	SchemaVersion int    `json:"schema_version"`
	MutationID    string `json:"mutation_id"`
	IfVersion     int64  `json:"if_version"`
	Resolved      bool   `json:"resolved"`
}
type CommentEvent struct {
	Version    int64  `json:"version"`
	Kind       string `json:"kind"`
	Body       string `json:"body"`
	Actor      string `json:"actor"`
	MutationID string `json:"mutation_id"`
	CreatedAt  string `json:"created_at"`
}
type CommentThread struct {
	ID                  string        `json:"id"`
	PageID              string        `json:"page_id"`
	Anchor              CommentAnchor `json:"anchor"`
	Version             int64         `json:"version"`
	Resolved            bool          `json:"resolved"`
	Status              string        `json:"status"`
	ProjectionRevision  int64         `json:"projection_revision"`
	LastMissingRevision *int64        `json:"last_missing_revision,omitempty"`
	RestoredAtRevision  *int64        `json:"restored_at_revision,omitempty"`
	CreatedAt           string        `json:"created_at"`
	UpdatedAt           string        `json:"updated_at"`
	CommentCount        int           `json:"comment_count"`
	FirstComment        CommentEvent  `json:"first_comment"`
}
type CommentReceipt struct {
	MutationID string `json:"mutation_id"`
	ThreadID   string `json:"thread_id"`
	Version    int64  `json:"version"`
	Duplicate  bool   `json:"duplicate"`
}
type CommentThreadPage struct {
	Items      []CommentThread `json:"items"`
	NextCursor string          `json:"next_cursor"`
	Watermark  int64           `json:"watermark"`
	HasMore    bool            `json:"has_more"`
}
type CommentHistoryPage struct {
	Items      []CommentEvent `json:"items"`
	NextCursor string         `json:"next_cursor"`
	Watermark  int64          `json:"watermark"`
	HasMore    bool           `json:"has_more"`
}

func ValidateCommentAnchor(anchor CommentAnchor) error {
	if anchor.AnchorSchemaVersion != AnchorSchemaVersion || anchor.Revision < 1 || !identifier.MatchString(anchor.SlideID) {
		return errors.New("invalid Slides comment anchor")
	}
	if anchor.Kind == "slide" && anchor.ElementID == "" {
		return nil
	}
	if anchor.Kind == "element" && identifier.MatchString(anchor.ElementID) {
		return nil
	}
	return errors.New("invalid Slides comment anchor kind or target")
}
func validateRawCommentAnchor(raw json.RawMessage) error {
	var envelope struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return err
	}
	fields := []string{"anchor_schema_version", "kind", "slide_id", "revision"}
	if envelope.Kind == "element" {
		fields = append(fields, "element_id")
	}
	if _, err := requiredObject(raw, fields...); err != nil {
		return err
	}
	var anchor CommentAnchor
	if err := json.Unmarshal(raw, &anchor); err != nil {
		return err
	}
	return ValidateCommentAnchor(anchor)
}

// Comment envelope versions are independent of supported deck versions.
func validCommentMutation(schema int, base int64, id, pageID string) error {
	if schema != CommentProtocolVersion {
		return errors.New("unsupported Slides comment protocol")
	}
	if base < 0 || !identifier.MatchString(id) || !identifier.MatchString(pageID) {
		return errors.New("invalid comment mutation identity or base revision")
	}
	return nil
}
func commentBodyValid(body string) bool {
	return strings.TrimSpace(body) != "" && validText(body, MaxCommentBodyBytes)
}
func commentRequest(raw []byte, target any, check func(map[string]json.RawMessage) error, fields ...string) error {
	if len(raw) > MaxCommentRequestBytes {
		return errors.New("Slides comment request exceeds 16KiB")
	}
	return strictRequest(raw, target, check, fields, nil)
}
func DecodeCreateComment(raw []byte) (CreateCommentRequest, error) {
	var request CreateCommentRequest
	err := commentRequest(raw, &request, func(object map[string]json.RawMessage) error { return validateRawCommentAnchor(object["anchor"]) }, "page_id", "schema_version", "base_rev", "mutation_id", "anchor", "body")
	if err != nil {
		return request, err
	}
	if err = validCommentMutation(request.SchemaVersion, request.BaseRev, request.MutationID, request.PageID); err != nil {
		return request, err
	}
	if request.BaseRev < 1 || request.Anchor.Revision != request.BaseRev || !commentBodyValid(request.Body) {
		return request, errors.New("invalid Slides comment body or original revision")
	}
	return request, nil
}
func DecodeReplyComment(raw []byte) (ReplyCommentRequest, error) {
	var request ReplyCommentRequest
	if err := commentRequest(raw, &request, nil, "page_id", "schema_version", "mutation_id", "if_version", "body"); err != nil {
		return request, err
	}
	if err := validCommentMutation(request.SchemaVersion, 1, request.MutationID, request.PageID); err != nil {
		return request, err
	}
	if request.IfVersion < 1 || !commentBodyValid(request.Body) {
		return request, errors.New("invalid Slides reply body or version")
	}
	return request, nil
}
func DecodeResolveComment(raw []byte) (ResolveCommentRequest, error) {
	var request ResolveCommentRequest
	if err := commentRequest(raw, &request, func(object map[string]json.RawMessage) error {
		value := strings.TrimSpace(string(object["resolved"]))
		if value != "true" && value != "false" {
			return errors.New("resolved must be a JSON boolean")
		}
		return nil
	}, "page_id", "schema_version", "mutation_id", "if_version", "resolved"); err != nil {
		return request, err
	}
	if err := validCommentMutation(request.SchemaVersion, 1, request.MutationID, request.PageID); err != nil {
		return request, err
	}
	if request.IfVersion < 1 {
		return request, errors.New("invalid Slides comment version")
	}
	return request, nil
}
