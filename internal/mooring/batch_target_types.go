package mooring

import (
	"context"
	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
)

// Target versions are host bookkeeping. Application meaning and CRDT identities
// remain in the opaque checkpoint; no application text is interpreted here.
type BatchTargetIdentity struct {
	TargetID string `json:"target_id"`
	Deleted  bool   `json:"deleted"`
}
type BatchTargetVersion struct {
	TargetID          string `json:"target_id"`
	Revision          string `json:"revision"`
	StructureRevision string `json:"structure_revision"`
	Deleted           bool   `json:"deleted"`
}
type BatchOperationRevision struct {
	OperationID string `json:"op_id"`
	Revision    string `json:"revision"`
}
type BatchPrepareContext struct {
	ReadPolicy         string                   `json:"read_policy"`
	DocumentRevision   string                   `json:"document_revision"`
	Targets            []BatchTargetVersion     `json:"targets"`
	OperationRevisions []BatchOperationRevision `json:"operation_revisions"`
}
type BatchTargetEffect struct {
	TargetID         string `json:"target_id"`
	ContentChanged   bool   `json:"content_changed"`
	StructureChanged bool   `json:"structure_changed"`
	Created          bool   `json:"created"`
	Deleted          bool   `json:"deleted"`
}
type BatchOperationEffects struct {
	OperationID string              `json:"op_id"`
	Targets     []BatchTargetEffect `json:"targets"`
}
type BatchTargetCandidate struct {
	Checkpoint []byte
	Inventory  []BatchTargetIdentity
	Effects    []BatchOperationEffects
	// Session-only application DATA, never an author or Saved capability.
	NativeDelta *protocol.SessionNativeDelta
}

// TargetBatchAdapter runs the qualified private preparation contract. Context
// comes only from host SQL, outside immutable change fingerprint membership.
// Candidates never publish or issue Saved. Uncertain failure returns an error.
type TargetBatchAdapter interface {
	ValidateTargets(context.Context, []byte) ([]BatchTargetIdentity, error)
	PrepareTargets(context.Context, []byte, BatchChange, BatchPrepareContext) (BatchTargetCandidate, *PreparedFailure, error)
}
type BatchTargetLimits struct {
	Targets             int
	Effects             int
	EffectsPerOperation int
	ProvenanceBytes     int64
}

func DefaultBatchTargetLimits() BatchTargetLimits {
	return BatchTargetLimits{65536, 1000000, 256, 128 << 20}
}

// CausalTargetBatchAdapter is a separate capability. Implementations must not
// translate historical observations into current reads or fall back to strict.
type CausalTargetBatchAdapter interface {
	TargetBatchAdapter
	PrepareCausalTargets(context.Context, []byte, BatchChange, BatchCausalPrepareContext) (BatchTargetCandidate, *PreparedFailure, error)
}

const CausalTargetReadPolicy = "agent_dependency_covered"

// TargetStoreConfig is separately default-off. Enabled configuration never falls
// back to the legacy context-free adapter or ignores target read observations.
type TargetStoreConfig struct {
	// Empty selects the original strict-current policy. Causal is explicit.
	ReadPolicy    string
	HistoryBudget BatchHistoryBudget
	Enabled       bool
	Adapter       TargetBatchAdapter
	Limits        BatchTargetLimits
	// Optional immutable hook used ONLY by the full-identity session commit
	// path. Ordinary strict/causal calls never invoke or acquire this source.
	SessionHumanPrepare SessionHumanPrepareResolver
}
