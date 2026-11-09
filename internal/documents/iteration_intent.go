package documents

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
)

var iterationRequired = []string{"submission_id", "page_id", "schema", "rev", "generation"}

func boolCount(b bool) int {
	if b {
		return 1
	}
	return 0
}

// IterationIntent identifies one explicit existing Document, never a slug,
// principal or mode supplied by a model. The host binds its human chat receipt.
//
// Schema 3 is the direct-draft mandate. Schema 4 is the propose mandate
// (#5137): the agent may only propose, and Blocks, when present, names the
// only blocks it may propose on. Blocks is a comma-free list of block IDs; it
// is never accepted with schema 3.
type IterationIntent struct {
	SubmissionID string `json:"submission_id"`
	PageID       string `json:"page_id"`
	Schema       int    `json:"schema"`
	Rev          int64  `json:"rev"`
	Generation   int64  `json:"generation"`
	Blocks       string `json:"-"`
}

// IterationBlockLimit bounds a propose mandate's named blocks.
const IterationBlockLimit = 16

var iterationBlockID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,80}$`)

// Propose reports whether this mandate admits proposals only.
func (v IterationIntent) Propose() bool { return v.Schema == ProposalSchema }

// BlockList returns the named blocks, or nil for the whole document.
func (v IterationIntent) BlockList() []string {
	if v.Blocks == "" {
		return nil
	}
	return strings.Split(v.Blocks, ",")
}

// Admits reports whether a block is inside this mandate.
func (v IterationIntent) Admits(block string) bool {
	if v.Blocks == "" {
		return true
	}
	for _, id := range v.BlockList() {
		if id == block {
			return true
		}
	}
	return false
}

func (v IterationIntent) Validate() error {
	if !creationSubmissionID.MatchString(v.SubmissionID) || !creationSubmissionID.MatchString(v.PageID) || (v.Schema != 3 && v.Schema != ProposalSchema) || v.Rev < 0 || v.Generation < 0 || v.Rev > 9007199254740991 || v.Generation > 9007199254740991 {
		return errors.New("one explicit Document identity and original schema/revision/generation required")
	}
	if v.Blocks != "" {
		ids := v.BlockList()
		if v.Schema != ProposalSchema || len(ids) > IterationBlockLimit {
			return errors.New("named blocks require a schema 4 proposal mandate of at most 16 blocks")
		}
		seen := map[string]bool{}
		for _, id := range ids {
			if !iterationBlockID.MatchString(id) || seen[id] {
				return errors.New("unique valid block IDs required")
			}
			seen[id] = true
		}
	}
	return nil
}

// ScopeBlocksJSON is the stored form of the named blocks: NULL for the whole document.
func (v IterationIntent) ScopeBlocksJSON() any {
	if v.Blocks == "" {
		return nil
	}
	raw, _ := json.Marshal(v.BlockList())
	return string(raw)
}

// IterationBlocksFromJSON reads the stored named blocks back.
func IterationBlocksFromJSON(raw sql.NullString) (string, error) {
	if !raw.Valid {
		return "", nil
	}
	var ids []string
	if json.Unmarshal([]byte(raw.String), &ids) != nil || len(ids) == 0 {
		return "", errors.New("stored block scope unreadable")
	}
	return strings.Join(ids, ","), nil
}

func (v IterationIntent) wire() map[string]any {
	out := map[string]any{"submission_id": v.SubmissionID, "page_id": v.PageID, "schema": v.Schema, "rev": v.Rev, "generation": v.Generation}
	if v.Blocks != "" {
		out["blocks"] = v.BlockList()
	}
	return out
}

// MarshalJSON keeps the wire identical to the original five fields when no
// blocks are named, so queued schema-3 messages replay unchanged.
func (v IterationIntent) MarshalJSON() ([]byte, error) { return json.Marshal(v.wire()) }

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
		case "submission_id", "page_id", "schema", "rev", "generation", "blocks":
		default:
			return errors.New("closed Document target required")
		}
		var value json.RawMessage
		if d.Decode(&value) != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return errors.New("non-null Document target fields required")
		}
		keys[key] = true
	}
	if _, err := d.Token(); err != nil || len(keys) != len(iterationRequired)+boolCount(keys["blocks"]) || d.Decode(new(any)) != io.EOF {
		return errors.New("complete Document target required")
	}
	for _, key := range iterationRequired {
		if !keys[key] {
			return errors.New("complete Document target required")
		}
	}
	var decoded struct {
		SubmissionID string   `json:"submission_id"`
		PageID       string   `json:"page_id"`
		Schema       int      `json:"schema"`
		Rev          int64    `json:"rev"`
		Generation   int64    `json:"generation"`
		Blocks       []string `json:"blocks"`
	}
	if json.Unmarshal(raw, &decoded) != nil {
		return errors.New("valid Document target fields required")
	}
	if keys["blocks"] && (len(decoded.Blocks) == 0 || len(decoded.Blocks) > IterationBlockLimit) {
		return errors.New("one to sixteen named blocks required")
	}
	for _, id := range decoded.Blocks {
		if !iterationBlockID.MatchString(id) {
			return errors.New("valid block IDs required")
		}
	}
	*v = IterationIntent{SubmissionID: decoded.SubmissionID, PageID: decoded.PageID, Schema: decoded.Schema, Rev: decoded.Rev, Generation: decoded.Generation, Blocks: strings.Join(decoded.Blocks, ",")}
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
	_, named := object["blocks"]
	if !ok || len(object) != len(iterationRequired)+boolCount(named) {
		return nil, errors.New("closed original Document target required")
	}
	for _, key := range iterationRequired {
		if _, ok := object[key]; !ok {
			return nil, errors.New("complete original Document target required")
		}
	}
	encoded, err := json.Marshal(object)
	if err != nil || len(encoded) > 1024 {
		return nil, errors.New("bounded original Document target required")
	}
	var intent IterationIntent
	if intent.UnmarshalJSON(encoded) != nil {
		return nil, errors.New("valid original Document target required")
	}
	return &intent, nil
}
