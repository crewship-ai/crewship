package mooring

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

var (
	ErrBatchDisabled     = errors.New("Mooring batch persistence disabled")
	ErrBatchCapacity     = errors.New("capacity_exhausted before admission")
	ErrOperationConflict = errors.New("op_id_conflict")
)

type BatchReadPrecondition struct {
	TargetID          string  `json:"target_id"`
	Revision          string  `json:"revision"`
	StructureRevision *string `json:"structure_revision,omitempty"`
}
type BatchOperation struct {
	ID           string                  `json:"op_id"`
	Fingerprint  string                  `json:"fingerprint"`
	Participant  string                  `json:"participant_ref"`
	BaseRevision string                  `json:"base_revision"`
	Dependencies []string                `json:"dependencies"`
	Reads        []BatchReadPrecondition `json:"read_preconditions"`
	Payload      json.RawMessage         `json:"payload"`
}
type BatchChange struct {
	ID             string           `json:"change_id"`
	MembershipHash string           `json:"membership_hash"`
	TaskID         string           `json:"task_id,omitempty"`
	Operations     []BatchOperation `json:"operations"`
}
type BatchRequest struct {
	Lease            OwnerLease
	ExpectedRevision string
	Changes          []BatchChange
}
type BatchRootCause struct {
	OperationID string `json:"op_id"`
	Code        string `json:"code"`
	Message     string `json:"message"`
}
type BatchRejection struct {
	Code            string          `json:"code"`
	Message         string          `json:"message"`
	Retry           string          `json:"retry"`
	RootOperationID string          `json:"root_op_id,omitempty"`
	RootCause       *BatchRootCause `json:"root_cause,omitempty"`
}
type BatchOperationResult struct {
	ID         string          `json:"op_id"`
	Status     string          `json:"status"`
	Revision   string          `json:"revision,omitempty"`
	Issuer     string          `json:"issuer,omitempty"`
	Durability string          `json:"durability,omitempty"`
	Reason     *BatchRejection `json:"reason,omitempty"`
}
type BatchChangeResult struct {
	ID            string                 `json:"change_id"`
	Status        string                 `json:"status"`
	FirstRevision string                 `json:"first_revision,omitempty"`
	LastRevision  string                 `json:"last_revision,omitempty"`
	Operations    []BatchOperationResult `json:"operations"`
}
type BatchResult struct {
	Revision string
	Changes  []BatchChangeResult
}

// BatchAccess is a host authorization intent, never a grant from a participant.
// Outcome access must authorize the caller's current scope before any ID lookup;
// it need not authorize the historical author's now-revoked write capability.
type BatchAccess struct {
	Intent      string // initialize, outcome, read_checkpoint, write, task
	Scope       SessionKey
	Operation   *BatchOperation
	TaskID      string
	Participant string
}
type BatchAuthority func(context.Context, *sql.Tx, BatchAccess) error

// BatchAdapter is a qualified application boundary. Apply receives normalized
// operation payloads and the latest accepted checkpoint. A rejection is only a
// prepared decision until the host commits it durably. An error (including an
// uncertain engine outcome) aborts the SQL transaction and is never terminal.
// Implementations must not publish, issue Saved, or mutate external state here.
type BatchAdapter interface {
	ValidateCheckpoint(context.Context, []byte) error
	Apply(context.Context, []byte, BatchChange) ([]byte, *BatchRejection, error)
}

type BatchLimits struct {
	LedgerOperations     int
	Changes              int
	Tasks                int
	AuditEvents          int
	Checkpoints          int
	CheckpointBytes      int
	TotalCheckpointBytes int64
}

func DefaultBatchLimits() BatchLimits {
	return BatchLimits{100000, 100000, 10000, 100000, 1000, 4 << 20, 64 << 20}
}

// BatchCommitEvent is internal to the trusted host. A separate delivery gate
// must authorize each recipient; this callback grants no content authority.
type BatchCommitEvent struct {
	Scope  SessionKey
	Result BatchResult
}
type BatchStoreConfig struct {
	Enabled     bool
	Owners      *OwnerStore
	Authority   BatchAuthority
	Adapter     BatchAdapter
	Limits      BatchLimits
	AfterCommit func(BatchCommitEvent)
}
type BatchStore struct {
	db     *sql.DB
	config BatchStoreConfig
}

func NewBatchStore(db *sql.DB, config BatchStoreConfig) *BatchStore {
	return &BatchStore{db: db, config: config}
}
