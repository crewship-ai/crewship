// Package chatentities stores durable agent operations and their references in
// chat history. It does not authorize callers: handlers must check live rights
// before both a new operation and replaying a stored result.
package chatentities

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

var (
	ErrKeyRequired         = errors.New("operation_key_required")
	ErrKeyConflict         = errors.New("operation_key_conflict")
	ErrOperationIncomplete = errors.New("operation_incomplete")
	ErrInvalid             = errors.New("invalid_chat_entity_operation")
)

// Queryer permits a caller-owned snapshot transaction for consistent reads.
type Queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type OperationSpec struct {
	ID, WorkspaceID, Key, RequestHash string
	ActorType, ActorID, RunID         string
	HumanAuthority                    json.RawMessage
	Provenance, Kind, Verb            string
	SourceProposalID                  string
	SourceProposalVersion             int64
}
type Operation struct {
	OperationSpec
	EntityID                    string
	Revision, AggregateRevision int64
	AccessSnapshot              json.RawMessage
	Status                      string
	Result                      json.RawMessage
	CreatedAt                   string
}
type ResultSpec struct {
	EntityID                    string
	Revision, AggregateRevision int64
	AccessSnapshot              json.RawMessage
	Result                      json.RawMessage
}
type RefSpec struct {
	EventID, ChatID, OperationID, RunID, TriggerMessageID string
}
type Ref struct {
	RefSpec
	Seq       int64 `json:"-"` // Internal scan position; never serialize to a viewer.
	CreatedAt string
	Operation Operation
}
