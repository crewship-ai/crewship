//! Proposed causal-read inputs, separate from the strict-current wire policy.
//!
//! These values attest host-owned SQL history, not authority or durability. A
//! future explicitly negotiated private dispatcher must bind them to its exact
//! request, scope, input digest and immutable change membership before use. They
//! intentionally have no deserializer and cannot enable a protocol capability.
use mooring_protocol::{Counter, PreparedOperationRevision, TargetVersion};

/// Compact original-read qualification made by the trusted SQL host after it
/// checks ALL intervening effects and the complete immutable dependency closure.
/// Missing history is a nonterminal host error, never `covered = false`.
#[derive(Clone, Debug, PartialEq)]
pub struct QualifiedRead {
    pub op_id: String,
    /// Original per-operation read index. Future wire form is a canonical
    /// decimal string 0..255; this pure representation is not a JSON schema.
    pub read_index: u32,
    pub target_id: String,
    pub observed_revision: Counter,
    /// Exact original optional observation: absence cannot become JSON null.
    pub observed_structure_revision: Option<Counter>,
    /// Required nullable entry state: None means a qualified complete inventory
    /// has no such target. This does not invent a provisional revision zero.
    pub entry: Option<EntryVersion>,
    pub covered: bool,
}

#[derive(Clone, Debug, PartialEq)]
pub struct EntryVersion {
    pub revision: Counter,
    pub structure_revision: Counter,
    pub deleted: bool,
}

/// Not accepted by the existing `agent_confirmed_current` preparation API.
/// Historical chain length does not increase this compact context's wire size.
#[derive(Clone, Debug, PartialEq)]
pub struct CausalPrepareContext {
    pub document_revision: Counter,
    pub targets: Vec<TargetVersion>,
    pub operation_revisions: Vec<PreparedOperationRevision>,
    pub qualified_reads: Vec<QualifiedRead>,
}

/// Local SQL traversal limits; distinct from frame size or generic PRD maxima.
/// Exceeding any work budget is nonterminal and preserves pending drafts.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct HistoryBudget {
    pub operations: usize,
    pub dependency_edges: usize,
    pub effects: usize,
}
impl Default for HistoryBudget {
    fn default() -> Self {
        Self {
            operations: 10_000,
            dependency_edges: 65_536,
            effects: 100_000,
        }
    }
}
