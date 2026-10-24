//! Bounded DATA-only producer mechanics, not source issuance or a receipt verifier.
//! The global confirmed actor document is separate from this speculative producer.
use crate::{adapter::WordCraftAdapter, Document};
use mooring_core::adapter::{Prepared, PreparedCandidate};
use mooring_protocol::{canonical::fingerprint_atomic, wire, PrepareContext, Scope};
use mooring_text::yrs::{updates::encoder::Encode, ReadTxn, Transact};
use std::{
    collections::{BTreeSet, VecDeque},
    io,
};

// Explicit prototype budgets, not negotiated PRD quotas or a wire profile.
pub const MAX_CUTS: usize = 4;
pub const MAX_RETAINED_BYTES: usize = 8 * 1024 * 1024;
const SMALL: usize = 262144;
const CHECKPOINT: usize = 1572864;
const METADATA: usize = 2 * 1024 * 1024;
const FRONTIER: usize = 64 * 1024;
const MAX_IDENTITIES: usize = 4096;

#[derive(Debug, PartialEq, Eq)]
pub enum Error {
    InvalidOriginal,
    Limit,
    Inactive,
    Preparation,
    Correlation,
}

/// Immutable original emission and application cuts. No field attests Saved.
pub struct ProducerCut {
    sequence: u64,
    original: Vec<u8>,
    delta: Vec<u8>,
    frontier: Vec<u8>,
    checkpoint: Vec<u8>,
    context: Vec<u8>,
    metadata: Vec<u8>,
}
impl ProducerCut {
    pub fn sequence(&self) -> u64 {
        self.sequence
    }
    pub fn original(&self) -> &[u8] {
        &self.original
    }
    pub fn delta(&self) -> &[u8] {
        &self.delta
    }
    pub fn authored_frontier(&self) -> &[u8] {
        &self.frontier
    }
    pub fn checkpoint(&self) -> &[u8] {
        &self.checkpoint
    }
    pub fn context(&self) -> &[u8] {
        &self.context
    }
    pub fn metadata(&self) -> &[u8] {
        &self.metadata
    }
    fn bytes(&self) -> usize {
        self.original.len()
            + self.delta.len()
            + self.frontier.len()
            + self.checkpoint.len()
            + self.context.len()
            + self.metadata.len()
    }
}

/// Neither Clone nor Deserialize. Ownership is DATA, not a host-issued source cap.
pub struct ProducerOverlay {
    scope: Scope,
    latest: Option<Document>,
    confirmed: Vec<u8>,
    pending: VecDeque<ProducerCut>,
    attempt: Option<Vec<u8>>,
    retained: usize,
    next: u64,
    blocked: bool,
    seen: BTreeSet<String>,
}
/// Recovery retains originals/cuts and any actual producer Doc still available.
/// A host must retire the source; these values cannot authorize continuation.
pub struct Recovery {
    pub latest: Option<Document>,
    pub confirmed_checkpoint: Vec<u8>,
    pub pending: Vec<ProducerCut>,
    pub attempted_original: Option<Vec<u8>>,
}
impl ProducerOverlay {
    /// Caller bounds/authenticates construction and issues the origin separately.
    /// Failure returns the actual supplied Doc rather than losing its ownership.
    pub fn from_owned_document(
        scope: Scope,
        document: Document,
    ) -> Result<Self, (Error, Document)> {
        if scope.application_id != crate::profile::APPLICATION_ID
            || ![&scope.workspace_id, &scope.artifact_id]
                .iter()
                .all(|id| wire::valid_identifier(id))
        {
            return Err((Error::InvalidOriginal, document));
        }
        let confirmed = match document.checkpoint() {
            Ok(cp) if cp.len() <= CHECKPOINT => cp,
            _ => return Err((Error::Limit, document)),
        };
        Ok(Self {
            scope,
            latest: Some(document),
            retained: confirmed.len(),
            confirmed,
            pending: VecDeque::new(),
            attempt: None,
            next: 1,
            blocked: false,
            seen: BTreeSet::new(),
        })
    }
    pub fn latest(&self) -> Option<&Document> {
        self.latest.as_ref()
    }
    pub fn pending(&self) -> &VecDeque<ProducerCut> {
        &self.pending
    }
    pub fn confirmed_checkpoint(&self) -> &[u8] {
        &self.confirmed
    }
    pub fn is_blocked(&self) -> bool {
        self.blocked
    }
    /// Original bytes are copied verbatim, not recreated from normalized JSON.
    /// Context and fingerprints are semantic data, never caller-supplied authority.
    pub fn append_original(
        &mut self,
        original: &[u8],
        context: &PrepareContext,
    ) -> Result<u64, Error> {
        if self.blocked || self.latest.is_none() {
            return Err(Error::Inactive);
        }
        if original.len() > SMALL || self.pending.len() >= MAX_CUTS || self.next == u64::MAX {
            return Err(Error::Limit);
        }
        wire::normalize_bounded(original).map_err(|_| Error::InvalidOriginal)?;
        let checked =
            fingerprint_atomic(&self.scope, original).map_err(|_| Error::InvalidOriginal)?;
        if !checked.matches_wire() {
            return Err(Error::InvalidOriginal);
        }
        let identities = std::iter::once(format!("change:{}", checked.change().change_id))
            .chain(
                checked
                    .change()
                    .operations
                    .iter()
                    .map(|op| format!("operation:{}", op.op_id)),
            )
            .collect::<Vec<_>>();
        if identities.iter().any(|id| self.seen.contains(id)) {
            return Err(Error::InvalidOriginal);
        }
        if identities.len() > MAX_IDENTITIES - self.seen.len() {
            return Err(Error::Limit);
        }
        let identity_bytes = identities.iter().map(String::len).sum::<usize>();
        let context_bytes = bounded_json(context, METADATA)?;
        if self
            .retained
            .checked_add(original.len() + context_bytes.len() + identity_bytes)
            .is_none_or(|n| n > MAX_RETAINED_BYTES)
        {
            return Err(Error::Limit);
        }
        self.attempt = Some(original.to_vec());
        let document = self.latest.take().ok_or(Error::Inactive)?;
        let frontier = document.doc.transact().state_vector();
        let frontier_bytes = frontier.encode_v1();
        if frontier_bytes.len() > FRONTIER {
            self.latest = Some(document);
            self.blocked = true;
            return Err(Error::Limit);
        }
        let candidate =
            match WordCraftAdapter.prepare_owned_atomic(document, checked.change(), context) {
                Ok(Prepared::Candidate(candidate)) => candidate,
                _ => {
                    self.blocked = true;
                    return Err(Error::Preparation);
                }
            };
        let checkpoint = candidate.checkpoint_bytes().to_vec();
        let metadata = bounded_json(&(candidate.effects(), candidate.inventory()), METADATA);
        let document = candidate.into_document();
        let delta = document.doc.transact().encode_state_as_update_v1(&frontier);
        // Preserve actual speculative state for Draft recovery on post-prepare
        // uncertainty/overflow. Never restore/rewind it under the same origin.
        self.latest = Some(document);
        let metadata = match metadata {
            Ok(bytes) => bytes,
            Err(error) => {
                self.blocked = true;
                return Err(error);
            }
        };
        if checkpoint.len() > CHECKPOINT || delta.len() > SMALL {
            self.blocked = true;
            return Err(Error::Limit);
        }
        let cut = ProducerCut {
            sequence: self.next,
            original: self.attempt.take().unwrap(),
            delta,
            frontier: frontier_bytes,
            checkpoint,
            context: context_bytes,
            metadata,
        };
        if self
            .retained
            .checked_add(cut.bytes() + identity_bytes)
            .is_none_or(|n| n > MAX_RETAINED_BYTES)
        {
            self.attempt = Some(cut.original);
            self.blocked = true;
            return Err(Error::Limit);
        }
        let sequence = self.next;
        self.next += 1;
        self.retained += cut.bytes() + identity_bytes;
        self.seen.extend(identities);
        self.pending.push_back(cut);
        Ok(sequence)
    }
    /// FIFO DATA correlation only. Caller MUST validate the actual durable host
    /// receipt before invoking; this method cannot issue Saved or install global
    /// confirmed state. Unknown does not call it and retains the whole overlay.
    pub fn acknowledge_front(
        &mut self,
        sequence: u64,
        checkpoint: &[u8],
    ) -> Result<ProducerCut, Error> {
        if self.blocked {
            return Err(Error::Inactive);
        }
        if !self
            .pending
            .front()
            .is_some_and(|cut| cut.sequence == sequence && cut.checkpoint == checkpoint)
        {
            self.blocked = true;
            return Err(Error::Correlation);
        }
        let cut = self.pending.pop_front().unwrap();
        self.retained = self.retained - cut.bytes() - self.confirmed.len() + cut.checkpoint.len();
        self.confirmed = cut.checkpoint.clone();
        // Settling A never changes the actual speculative producer carrying B.
        Ok(cut)
    }
    /// A caller-qualified failure/recovery retires this source as a whole.
    /// This is not a durable operation rejection or new-source permission.
    pub fn retire(&mut self) {
        self.blocked = true;
    }
    pub fn into_recovery(self) -> Recovery {
        Recovery {
            latest: self.latest,
            confirmed_checkpoint: self.confirmed,
            pending: self.pending.into_iter().collect(),
            attempted_original: self.attempt,
        }
    }
}
struct Bounded {
    bytes: Vec<u8>,
    maximum: usize,
}
impl io::Write for Bounded {
    fn write(&mut self, input: &[u8]) -> io::Result<usize> {
        if input.len() > self.maximum - self.bytes.len() {
            return Err(io::ErrorKind::OutOfMemory.into());
        }
        self.bytes
            .try_reserve_exact(input.len())
            .map_err(|_| io::Error::from(io::ErrorKind::OutOfMemory))?;
        self.bytes.extend_from_slice(input);
        Ok(input.len())
    }
    fn flush(&mut self) -> io::Result<()> {
        Ok(())
    }
}
fn bounded_json(value: &impl serde::Serialize, maximum: usize) -> Result<Vec<u8>, Error> {
    let mut output = Bounded {
        bytes: Vec::new(),
        maximum,
    };
    serde_json::to_writer(&mut output, value).map_err(|_| Error::Limit)?;
    Ok(output.bytes)
}
#[cfg(test)]
#[path = "producer_overlay_tests.rs"]
mod tests;
