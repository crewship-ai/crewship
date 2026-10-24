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
/// Exact SAME-scope/model/lineage checkpoints and original admission from a
/// trusted host store. Original ordinal is the host-issued participant order;
/// a Yrs clock cannot detect a later delete-only undo. No optimistic predecessor
/// is accepted in this first capability. The host ledger does not implement this
/// admission yet, so callers/tests must label host qualification as synthetic.
pub struct HostConfirmedBasis<'a> {
    basis: &'a [u8],
    current: &'a [u8],
    origin: u64,
    original: HostSealedOriginal<'a>,
    transcript: &'a [HostSavedDelta<'a>],
    own_history: &'a [HostCommittedOwnOperation<'a>],
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
        if origin == 0 || origin >= 1 << 53 || original.proposal.origin != origin {
            return Err(Error::InvalidHostContext);
        }
        if original.ordinal != next_unsettled_ordinal {
            return Err(Error::UnqualifiedBasis);
        }
        let total = transcript
            .iter()
            .try_fold(0usize, |sum, entry| sum.checked_add(entry.original.len()));
        if transcript.len() > 256 || total.is_none_or(|bytes| bytes > 262144) {
            return Err(Error::UnqualifiedBasis);
        }
        let mut references = BTreeSet::from([original.sealed_reference]);
        if transcript
            .iter()
            .any(|entry| entry.origin == origin || !references.insert(entry.sealed_reference))
        {
            // Own pending/predecessor chains require a separate qualification.
            return Err(Error::UnqualifiedBasis);
        }
        Ok(Self {
            basis,
            current,
            origin,
            original,
            transcript,
            own_history: &[],
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
        self.own_history = history;
        Ok(self)
    }
}
pub enum Authority<'a> {
    Reader,
    Writer(HostConfirmedBasis<'a>),
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
    // Phase 1: unchanged bytes/frontier/targets are qualified on their EXACT
    // original confirmed basis. No read/frontier rewriting or command replay.
    current::prepare_current(
        current::Authority::Writer(
            HostWriter::current(host.basis, host.origin)
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
