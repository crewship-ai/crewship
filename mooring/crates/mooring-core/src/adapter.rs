//! Pure application preparation boundary. The host alone commits outcomes.
use mooring_protocol::{wire::valid_identifier, AtomicChange, ErrorCode};

/// An owned application candidate can retain CRDT/native state, stable IDs and
/// asset references. Its opaque checkpoint bytes represent the full candidate;
/// they are not a host checkpoint wrapper, receipt, or durability claim.
pub trait PreparedCandidate {
    fn checkpoint_bytes(&self) -> &[u8];
}

/// Implementations restore/fork the supplied authoritative state and prepare
/// one immutable atomic change. They must not mutate authoritative/external
/// state, publish, issue Saved, or retain a durable outcome ledger here.
///
/// The caller authenticates scope/participants and normalizes payload numbers
/// before this boundary. It enforces negotiated resource limits BEFORE restore
/// and AFTER candidate serialization; this trait embeds no fixed byte limit.
/// Candidate installation/delivery needs a later verified host confirmation.
pub trait PrepareAdapter {
    type Candidate: PreparedCandidate;
    /// Any error aborts preparation nonterminally; it is not an operation
    /// rejection. This includes uncertain engine outcomes and restore failures.
    type Error;

    fn validate_checkpoint(&self, checkpoint: &[u8]) -> Result<(), Self::Error>;
    fn prepare_atomic(
        &self,
        authoritative_checkpoint: &[u8],
        normalized: &AtomicChange,
    ) -> Result<Prepared<Self::Candidate>, Self::Error>;
}

pub enum Prepared<C> {
    Candidate(C),
    /// Must be qualified against original immutable membership before a host
    /// may consider persisting it. This value is never itself a receipt.
    Rejected(PreparedFailure),
}

#[derive(Debug)]
pub struct PreparedFailure {
    pub operation_id: String,
    pub code: ErrorCode,
    pub message: String,
}
#[derive(Debug, PartialEq, Eq)]
pub enum InvalidPreparedFailure {
    Member,
    Nonterminal,
    Message,
}
#[derive(Debug)]
pub struct QualifiedFailure(PreparedFailure);
impl PreparedFailure {
    /// A malformed/foreign failure is a NONTERMINAL engine/protocol error,
    /// never a guessed rejection for the first member. Dependency propagation
    /// is the planner's job; an adapter identifies the actual direct cause.
    pub fn qualify(
        self,
        original: &AtomicChange,
    ) -> Result<QualifiedFailure, InvalidPreparedFailure> {
        if !valid_identifier(&self.operation_id)
            || original
                .operations
                .iter()
                .filter(|operation| operation.op_id == self.operation_id)
                .count()
                != 1
        {
            return Err(InvalidPreparedFailure::Member);
        }
        if matches!(
            self.code,
            ErrorCode::UnknownOutcome | ErrorCode::DependencyRejected
        ) {
            return Err(InvalidPreparedFailure::Nonterminal);
        }
        if self.message.chars().count() > 1000 {
            return Err(InvalidPreparedFailure::Message);
        }
        Ok(QualifiedFailure(self))
    }
}
impl QualifiedFailure {
    pub fn operation_id(&self) -> &str {
        &self.0.operation_id
    }
    pub fn code(&self) -> &ErrorCode {
        &self.0.code
    }
    pub fn message(&self) -> &str {
        &self.0.message
    }
    /// Still only an in-memory planning decision, not a saved host outcome.
    pub fn into_planning_rejection(self) -> crate::resolve::Rejection {
        crate::resolve::Rejection::direct(&self.0.operation_id, self.0.code, &self.0.message)
    }
}
