//! Inactive native-only retained-confirmed-basis experiment. All constructors
//! below are TRUSTED HOST STORE inputs, not proof verification or wire admission.
//! There is deliberately no Deserialize or WASM constructor. No Saved outcome,
//! source authority, new packet fingerprint domain, or durable ledger is created.
use crate::{
    experimental_draft::Proposal,
    experimental_human_current::{
        self as current, Candidate, HostCommittedOwnOperation, HostWriter,
    },
    Document,
};
use std::collections::BTreeSet;

#[derive(Debug, PartialEq, Eq)]
pub enum Error {
    Forbidden,
    InvalidHostContext,
    /// Missing/uncertain basis, committed history or order: retain UnsavedDraft.
    UnqualifiedBasis,
    Original(current::Error),
}
/// The host must bind the ENTIRE unchanged atomic fingerprint and SEPARATE
/// original source/session/epoch/mandate/horizon proof before constructing this.
/// `sealed_reference` is an opaque trusted-store reference, never a delta hash.
/// This application module cannot verify that authority or invent its mapping.
pub struct HostSealedOriginal<'a> {
    proposal: &'a Proposal,
    sealed_reference: &'a [u8],
    ordinal: u64,
}
impl<'a> HostSealedOriginal<'a> {
    pub fn from_trusted_original_store(
        proposal: &'a Proposal,
        sealed_reference: &'a [u8],
        ordinal: u64,
    ) -> Result<Self, Error> {
        reference(sealed_reference)?;
        if ordinal == 0 || ordinal >= 1 << 53 {
            return Err(Error::InvalidHostContext);
        }
        Ok(Self {
            proposal,
            sealed_reference,
            ordinal,
        })
    }
}
/// Only ACTUAL immutable CRDT bytes of durable Saved operations qualify, not a
/// candidate, CLI status label, shared frontier or reexecuted typed command.
/// Trusted host storage binds reference, author origin, bytes AND target effects.
pub struct HostSavedDelta<'a> {
    sealed_reference: &'a [u8],
    origin: u64,
    original: &'a [u8],
    targets: &'a [String],
}
impl<'a> HostSavedDelta<'a> {
    pub fn from_trusted_committed_store(
        sealed_reference: &'a [u8],
        origin: u64,
        original: &'a [u8],
        targets: &'a [String],
    ) -> Result<Self, Error> {
        reference(sealed_reference)?;
        if origin >= 1 << 53 || original.is_empty() || original.len() > 262144 {
            return Err(Error::InvalidHostContext);
        }
        Ok(Self {
            sealed_reference,
            origin,
            original,
            targets,
        })
    }
}
/// Two distinct host-retained views of one unchanged original own operation.
/// The authored checkpoint is NOT Saved. Only the separately correlated global
/// SQL-returned checkpoint may be paired with an actual Saved slot by the host.
/// Both digests bind exact application checkpoint STATE bytes, not the full
/// protocol checkpoint identity. They never select a basis or source.
/// Borrowed input only: no Deserialize, client authority flag or issuer proof.
pub struct HostSavedOwnPrefix<'a> {
    original: HostSealedOriginal<'a>,
    authored_checkpoint_state_digest: &'a str,
    saved_checkpoint_state_digest: &'a str,
}
impl<'a> HostSavedOwnPrefix<'a> {
    pub fn from_trusted_live_slot(
        original: HostSealedOriginal<'a>,
        authored_checkpoint_state_digest: &'a str,
        saved_checkpoint_state_digest: &'a str,
    ) -> Result<Self, Error> {
        if [
            authored_checkpoint_state_digest,
            saved_checkpoint_state_digest,
        ]
        .iter()
        .any(|digest| {
            digest.len() != 71
                || !digest.starts_with("sha256:")
                || !digest.as_bytes()[7..]
                    .iter()
                    .all(|byte| byte.is_ascii_digit() || (b'a'..=b'f').contains(byte))
        }) {
            return Err(Error::InvalidHostContext);
        }
        Ok(Self {
            original,
            authored_checkpoint_state_digest,
            saved_checkpoint_state_digest,
        })
    }
}
/// Exact SAME-scope/model/lineage checkpoints and original admission from a
/// trusted host store. Original ordinal is the host-issued participant order;
/// a Yrs clock cannot detect a later delete-only undo. The original constructor
/// rejects own predecessors; the opt-in live constructor requires exact linked
/// authored/global views. App tests label host qualification as synthetic.
pub struct HostConfirmedBasis<'a> {
    basis: &'a [u8],
    current: &'a [u8],
    origin: u64,
    original: HostSealedOriginal<'a>,
    transcript: &'a [HostSavedDelta<'a>],
    own_history: &'a [HostCommittedOwnOperation<'a>],
    own_prefix: &'a [HostSavedOwnPrefix<'a>],
}
impl<'a> HostConfirmedBasis<'a> {
    pub fn from_trusted_confirmed_store(
        basis: &'a [u8],
        current: &'a [u8],
        origin: u64,
        original: HostSealedOriginal<'a>,
        next_unsettled_ordinal: u64,
        transcript: &'a [HostSavedDelta<'a>],
    ) -> Result<Self, Error> {
        Self::from_parts(
            basis,
            current,
            origin,
            original,
            next_unsettled_ordinal,
            transcript,
            &[],
        )
    }
    /// Opt-in LIVE qualification only after actual host COMMIT slots link both
    /// views. The old constructor intentionally keeps refusing own transcripts.
    pub fn from_trusted_live_store_with_own_prefix(
        basis: &'a [u8],
        current: &'a [u8],
        origin: u64,
        original: HostSealedOriginal<'a>,
        next_unsettled_ordinal: u64,
        transcript: &'a [HostSavedDelta<'a>],
        own_prefix: &'a [HostSavedOwnPrefix<'a>],
    ) -> Result<Self, Error> {
        if own_prefix.is_empty() {
            return Err(Error::UnqualifiedBasis);
        }
        Self::from_parts(
            basis,
            current,
            origin,
            original,
            next_unsettled_ordinal,
            transcript,
            own_prefix,
        )
    }
    fn from_parts(
        basis: &'a [u8],
        current: &'a [u8],
        origin: u64,
        original: HostSealedOriginal<'a>,
        next_unsettled_ordinal: u64,
        transcript: &'a [HostSavedDelta<'a>],
        own_prefix: &'a [HostSavedOwnPrefix<'a>],
    ) -> Result<Self, Error> {
        if origin == 0 || origin >= 1 << 53 || original.proposal.origin != origin {
            return Err(Error::InvalidHostContext);
        }
        if original.ordinal != next_unsettled_ordinal {
            return Err(Error::UnqualifiedBasis);
        }
        if transcript.len() > 256 || own_prefix.len() > transcript.len() {
            return Err(Error::UnqualifiedBasis);
        }
        let total = transcript
            .iter()
            .try_fold(0usize, |sum, entry| sum.checked_add(entry.original.len()));
        if transcript.len() > 256 || total.is_none_or(|bytes| bytes > 262144) {
            return Err(Error::UnqualifiedBasis);
        }
        let mut references = BTreeSet::from([original.sealed_reference]);
        if own_prefix.len() > transcript.len()
            || transcript.iter().any(|entry| {
                !references.insert(entry.sealed_reference)
                    || (entry.origin == origin
                        && !own_prefix.iter().any(|prefix| {
                            prefix.original.sealed_reference == entry.sealed_reference
                        }))
            })
        {
            return Err(Error::UnqualifiedBasis);
        }
        let mut previous_ordinal = None;
        let mut prefix_references = BTreeSet::new();
        for prefix in own_prefix {
            let proposal = prefix.original.proposal;
            if proposal.origin != origin
                || prefix.original.ordinal >= original.ordinal
                || previous_ordinal
                    .is_some_and(|prior: u64| prior.checked_add(1) != Some(prefix.original.ordinal))
                || !prefix_references.insert(prefix.original.sealed_reference)
                || !transcript.iter().any(|entry| {
                    entry.sealed_reference == prefix.original.sealed_reference
                        && entry.origin == origin
                        && entry.original == proposal.update
                        && entry.targets == proposal.targets
                })
            {
                return Err(Error::UnqualifiedBasis);
            }
            previous_ordinal = Some(prefix.original.ordinal);
        }
        if previous_ordinal.is_some_and(|prior| prior.checked_add(1) != Some(original.ordinal)) {
            return Err(Error::UnqualifiedBasis);
        }
        Ok(Self {
            basis,
            current,
            origin,
            original,
            transcript,
            own_history: &[],
            own_prefix,
        })
    }
    /// Already-Saved same-origin format history only. This is NOT optimistic
    /// predecessor replay or permission to advance the original authoring basis.
    pub fn with_committed_own_history(
        mut self,
        history: &'a [HostCommittedOwnOperation<'a>],
    ) -> Result<Self, Error> {
        HostWriter::current(self.basis, self.origin)
            .map_err(Error::Original)?
            .with_committed_own_history(history)
            .map_err(Error::Original)?;
        let bytes = self
            .transcript
            .iter()
            .map(|entry| entry.original.len())
            .chain(history.iter().map(HostCommittedOwnOperation::byte_len))
            .try_fold(0usize, usize::checked_add);
        if self
            .transcript
            .len()
            .checked_add(history.len())
            .is_none_or(|count| count > 256)
            || bytes.is_none_or(|count| count > 262144)
        {
            return Err(Error::UnqualifiedBasis);
        }
        self.own_history = history;
        Ok(self)
    }
}
pub enum Authority<'a> {
    Reader,
    Writer(HostConfirmedBasis<'a>),
}
// Identical raw-state encoding to protocol::canonical::digest (crate-private).
// This is not a full protocol checkpoint identity or an authority proof.
fn checkpoint_state_digest(bytes: &[u8]) -> String {
    use sha2::{Digest, Sha256};
    format!("sha256:{:x}", Sha256::digest(bytes))
}
fn reference(value: &[u8]) -> Result<(), Error> {
    if value.is_empty() || value.len() > 256 {
        return Err(Error::InvalidHostContext);
    }
    Ok(())
}
pub fn prepare_confirmed_basis(authority: Authority<'_>) -> Result<Candidate, Error> {
    let Authority::Writer(host) = authority else {
        return Err(Error::Forbidden);
    };
    let original = host.original.proposal;
    // This is a disposable AUTHOR view, never an authoritative Saved cut.
    // Already-Saved own originals can belong to a different global interleave.
    let mut authored_basis = host.basis.to_vec();
    for prefix in host.own_prefix {
        let proposal = prefix.original.proposal;
        let authored = current::prepare_author_view(
            current::Authority::Writer(
                HostWriter::current(&authored_basis, host.origin)
                    .map_err(Error::Original)?
                    .with_committed_own_history(host.own_history)
                    .map_err(Error::Original)?,
            ),
            &proposal.authored_frontier,
            &proposal.update,
            &proposal.targets,
        )
        .map_err(|_| Error::UnqualifiedBasis)?;
        authored_basis = authored.checkpoint().map_err(|_| Error::UnqualifiedBasis)?;
        if checkpoint_state_digest(&authored_basis) != prefix.authored_checkpoint_state_digest {
            return Err(Error::UnqualifiedBasis);
        }
    }
    // Phase 1: unchanged bytes/frontier/targets are qualified on their EXACT
    // original confirmed basis. No read/frontier rewriting or command replay.
    let prepare_original = if host.own_prefix.is_empty() {
        current::prepare_current
    } else {
        current::prepare_author_view
    };
    prepare_original(
        current::Authority::Writer(
            HostWriter::current(&authored_basis, host.origin)
                .map_err(Error::Original)?
                .with_committed_own_history(host.own_history)
                .map_err(Error::Original)?,
        ),
        &original.authored_frontier,
        &original.update,
        &original.targets,
    )
    .map_err(|error| match error {
        current::Error::InvalidCheckpoint | current::Error::UnqualifiedFrontier => {
            Error::UnqualifiedBasis
        }
        other => Error::Original(other),
    })?;
    let basis = Document::restore(host.basis).map_err(|_| Error::UnqualifiedBasis)?;
    let now = Document::restore(host.current).map_err(|_| Error::UnqualifiedBasis)?;
    if basis.block_ids().map_err(|_| Error::UnqualifiedBasis)?
        != now.block_ids().map_err(|_| Error::UnqualifiedBasis)?
    {
        return Err(Error::UnqualifiedBasis);
    }
    // Prove application lineage by replaying exact host-Saved changes, including
    // hidden CRDT history, into a disposable basis. SV containment alone is NOT
    // a lineage proof. No transcript entry is inferred to be durable here.
    let mut replay = host.basis.to_vec();
    for entry in host.transcript {
        replay =
            current::merge_qualified_original(&replay, entry.original, entry.targets, entry.origin)
                .map_err(|_| Error::UnqualifiedBasis)?
                .checkpoint()
                .map_err(|_| Error::UnqualifiedBasis)?;
        if host.own_prefix.iter().any(|prefix| {
            prefix.original.sealed_reference == entry.sealed_reference
                && checkpoint_state_digest(&replay) != prefix.saved_checkpoint_state_digest
        }) {
            return Err(Error::UnqualifiedBasis);
        }
    }
    if replay != host.current {
        return Err(Error::UnqualifiedBasis);
    }
    // Phase 2: apply the SAME original bytes to the newer confirmed document.
    // The shared audit checks exact roots, declared targets, actual allocation
    // coverage, full native consistency, hidden stamps, IDs and cold restore.
    current::merge_qualified_original(
        host.current,
        &original.update,
        &original.targets,
        host.origin,
    )
    .map_err(Error::Original)
}
