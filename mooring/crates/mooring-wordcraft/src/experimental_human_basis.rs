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
    base_revision: Option<u64>,
}
impl<'a> HostSealedOriginal<'a> {
    /// ORIGINAL outer-operation label, already bound by the host fingerprint.
    pub fn with_original_base_revision(mut self, revision: u64) -> Self {
        self.base_revision = Some(revision);
        self
    }
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
            base_revision: None,
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
    revision: Option<u64>,
}
impl<'a> HostSavedDelta<'a> {
    /// Actual post-COMMIT atomic cut revision, never Prepared/client metadata.
    pub fn with_saved_revision(mut self, revision: u64) -> Result<Self, Error> {
        if revision == 0 {
            return Err(Error::InvalidHostContext);
        }
        self.revision = Some(revision);
        Ok(self)
    }
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
            revision: None,
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
/// An earlier SAME-writer candidate. NEVER an actual Saved transcript entry.
/// The private host binds original membership and callback lifetime before use.
pub struct HostPreparedDelta<'a> {
    sealed_reference: &'a [u8],
    origin: u64,
    original: &'a [u8],
    targets: &'a [String],
    prospective_revision: u64,
    prepared_checkpoint_state_digest: &'a str,
}
impl<'a> HostPreparedDelta<'a> {
    pub fn from_trusted_writer_candidate(
        reference_bytes: &'a [u8],
        origin: u64,
        original: &'a [u8],
        targets: &'a [String],
        prospective_revision: u64,
        prepared_checkpoint_state_digest: &'a str,
    ) -> Result<Self, Error> {
        reference(reference_bytes)?;
        if origin >= 1 << 53
            || original.is_empty()
            || original.len() > 262144
            || prospective_revision == 0
            || !valid_digest(prepared_checkpoint_state_digest)
        {
            return Err(Error::InvalidHostContext);
        }
        Ok(Self {
            sealed_reference: reference_bytes,
            origin,
            original,
            targets,
            prospective_revision,
            prepared_checkpoint_state_digest,
        })
    }
}
/// Immutable AUTHOR cut for an earlier own candidate, explicitly NOT Saved.
pub struct HostPreparedOwnPrefix<'a> {
    original: HostSealedOriginal<'a>,
    authored_checkpoint_state_digest: &'a str,
}
impl<'a> HostPreparedOwnPrefix<'a> {
    pub fn from_trusted_writer_original(
        original: HostSealedOriginal<'a>,
        authored_checkpoint_state_digest: &'a str,
    ) -> Result<Self, Error> {
        if !valid_digest(authored_checkpoint_state_digest) {
            return Err(Error::InvalidHostContext);
        }
        Ok(Self {
            original,
            authored_checkpoint_state_digest,
        })
    }
}
/// Borrowed candidate lineage from ONE actual writer callback; no third full
/// checkpoint is carried. Replay verifies the exact tx-start label and hash.
pub struct HostPreparedPrefix<'a> {
    tx_start_revision: u64,
    tx_start_checkpoint_state_digest: &'a str,
    entries: &'a [HostPreparedDelta<'a>],
    own: &'a [HostPreparedOwnPrefix<'a>],
}
impl<'a> HostPreparedPrefix<'a> {
    pub fn from_trusted_writer_candidates(
        tx_start_revision: u64,
        tx_start_checkpoint_state_digest: &'a str,
        entries: &'a [HostPreparedDelta<'a>],
        own: &'a [HostPreparedOwnPrefix<'a>],
    ) -> Result<Self, Error> {
        if !valid_digest(tx_start_checkpoint_state_digest)
            || entries.is_empty()
            || entries.len() > 256
            || own.len() > entries.len()
        {
            return Err(Error::InvalidHostContext);
        }
        Ok(Self {
            tx_start_revision,
            tx_start_checkpoint_state_digest,
            entries,
            own,
        })
    }
}
fn valid_digest(digest: &str) -> bool {
    digest.len() == 71
        && digest.starts_with("sha256:")
        && digest.as_bytes()[7..]
            .iter()
            .all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(b))
}
struct OwnCut<'a> {
    original: &'a HostSealedOriginal<'a>,
    authored: &'a str,
}
/// Borrowed host-only basis DATA. Full scope/tuple/asset identity and actual
/// host provenance are verified by the private host wrapper, not these bytes.
pub struct HostBasisViews<'a> {
    original: &'a [u8],
    original_revision: u64,
    replay: Option<(&'a [u8], u64)>,
    prepared: Option<&'a HostPreparedPrefix<'a>>,
}
impl<'a> HostBasisViews<'a> {
    pub fn from_trusted_live_store(original: &'a [u8], original_revision: u64) -> Self {
        Self {
            original,
            original_revision,
            replay: None,
            prepared: None,
        }
    }
    pub fn with_prepared_prefix(
        mut self,
        prefix: &'a HostPreparedPrefix<'a>,
    ) -> Result<Self, Error> {
        if self.prepared.is_some() || prefix.tx_start_revision < self.original_revision {
            return Err(Error::UnqualifiedBasis);
        }
        self.prepared = Some(prefix);
        Ok(self)
    }
    /// At most ONE common replay basis; never a checkpoint per history entry.
    pub fn with_replay_basis(mut self, checkpoint: &'a [u8], revision: u64) -> Result<Self, Error> {
        if self.replay.is_some()
            || revision > self.original_revision
            || (revision == self.original_revision && checkpoint != self.original)
        {
            return Err(Error::UnqualifiedBasis);
        }
        self.replay = Some((checkpoint, revision));
        Ok(self)
    }
    fn replay(&self) -> (&'a [u8], u64) {
        self.replay
            .unwrap_or((self.original, self.original_revision))
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
    views: Option<HostBasisViews<'a>>,
}
impl<'a> HostConfirmedBasis<'a> {
    /// Explicit revision-labelled opt-in; old constructors keep their policy.
    pub fn from_trusted_live_store_with_basis_views(
        views: HostBasisViews<'a>,
        current: &'a [u8],
        origin: u64,
        original: HostSealedOriginal<'a>,
        next_unsettled_ordinal: u64,
        transcript: &'a [HostSavedDelta<'a>],
        own_prefix: &'a [HostSavedOwnPrefix<'a>],
    ) -> Result<Self, Error> {
        if transcript.len() > 256 || own_prefix.len() > transcript.len() {
            return Err(Error::UnqualifiedBasis);
        }
        if original.base_revision != Some(views.original_revision) {
            return Err(Error::UnqualifiedBasis);
        }
        let (_, start_revision) = views.replay();
        let mut prior = start_revision;
        for entry in transcript {
            let revision = entry.revision.ok_or(Error::UnqualifiedBasis)?;
            if revision <= prior {
                return Err(Error::UnqualifiedBasis);
            }
            prior = revision;
        }
        let mut prior_base = start_revision;
        for prefix in own_prefix {
            let base = prefix
                .original
                .base_revision
                .ok_or(Error::UnqualifiedBasis)?;
            if base < prior_base || base > views.original_revision {
                return Err(Error::UnqualifiedBasis);
            }
            prior_base = base;
        }
        let mut host = Self::from_parts_with_prepared(
            views.original,
            current,
            origin,
            original,
            next_unsettled_ordinal,
            (transcript, own_prefix, views.prepared),
        )?;
        host.views = Some(views);
        let mut prior_base = start_revision;
        for cut in host.own_cuts() {
            let base = cut.original.base_revision.ok_or(Error::UnqualifiedBasis)?;
            if base < prior_base
                || base > host.original.base_revision.ok_or(Error::UnqualifiedBasis)?
            {
                return Err(Error::UnqualifiedBasis);
            }
            prior_base = base;
        }
        Ok(host)
    }
    fn own_cuts(&self) -> Vec<OwnCut<'_>> {
        let mut own: Vec<_> = self
            .own_prefix
            .iter()
            .map(|p| OwnCut {
                original: &p.original,
                authored: p.authored_checkpoint_state_digest,
            })
            .chain(
                self.views
                    .as_ref()
                    .and_then(|v| v.prepared)
                    .into_iter()
                    .flat_map(|p| p.own)
                    .map(|p| OwnCut {
                        original: &p.original,
                        authored: p.authored_checkpoint_state_digest,
                    }),
            )
            .collect();
        own.sort_by_key(|p| p.original.ordinal);
        own
    }
    pub(crate) fn original_base_revision(&self) -> Option<u64> {
        self.original.base_revision
    }
    pub(crate) fn original_proposal(&self) -> &Proposal {
        self.original.proposal
    }
    pub(crate) fn current_checkpoint(&self) -> &[u8] {
        self.current
    }
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
        Self::from_parts_with_prepared(
            basis,
            current,
            origin,
            original,
            next_unsettled_ordinal,
            (transcript, own_prefix, None),
        )
    }
    fn from_parts_with_prepared(
        basis: &'a [u8],
        current: &'a [u8],
        origin: u64,
        original: HostSealedOriginal<'a>,
        next_unsettled_ordinal: u64,
        history: (
            &'a [HostSavedDelta<'a>],
            &'a [HostSavedOwnPrefix<'a>],
            Option<&'a HostPreparedPrefix<'a>>,
        ),
    ) -> Result<Self, Error> {
        let (transcript, own_prefix, prepared) = history;
        if origin == 0 || origin >= 1 << 53 || original.proposal.origin != origin {
            return Err(Error::InvalidHostContext);
        }
        if original.ordinal != next_unsettled_ordinal {
            return Err(Error::UnqualifiedBasis);
        }
        let prepared_entries = prepared.map_or(&[][..], |p| p.entries);
        let prepared_own = prepared.map_or(&[][..], |p| p.own);
        let count = transcript.len().checked_add(prepared_entries.len());
        let total = transcript
            .iter()
            .map(|e| e.original.len())
            .chain(prepared_entries.iter().map(|e| e.original.len()))
            .try_fold(0usize, usize::checked_add);
        if count.is_none_or(|n| n > 256) || total.is_none_or(|n| n > 262144) {
            return Err(Error::UnqualifiedBasis);
        }
        if own_prefix
            .windows(2)
            .any(|p| p[0].original.ordinal.checked_add(1) != Some(p[1].original.ordinal))
            || prepared_own
                .windows(2)
                .any(|p| p[0].original.ordinal.checked_add(1) != Some(p[1].original.ordinal))
            || own_prefix
                .last()
                .zip(prepared_own.first())
                .is_some_and(|(a, b)| a.original.ordinal >= b.original.ordinal)
            || own_prefix.iter().any(|p| {
                !transcript
                    .iter()
                    .any(|e| e.sealed_reference == p.original.sealed_reference)
            })
            || prepared_own.iter().any(|p| {
                !prepared_entries
                    .iter()
                    .any(|e| e.sealed_reference == p.original.sealed_reference)
            })
        {
            return Err(Error::UnqualifiedBasis);
        }
        let mut refs = BTreeSet::from([original.sealed_reference]);
        let mut own_refs = BTreeSet::new();
        let mut own: Vec<OwnCut<'_>> = own_prefix
            .iter()
            .map(|p| OwnCut {
                original: &p.original,
                authored: p.authored_checkpoint_state_digest,
            })
            .chain(prepared_own.iter().map(|p| OwnCut {
                original: &p.original,
                authored: p.authored_checkpoint_state_digest,
            }))
            .collect();
        if own.len() > count.unwrap_or(0) {
            return Err(Error::UnqualifiedBasis);
        }
        own.sort_by_key(|p| p.original.ordinal);
        for (r, o, raw, targets) in transcript
            .iter()
            .map(|e| (e.sealed_reference, e.origin, e.original, e.targets))
            .chain(
                prepared_entries
                    .iter()
                    .map(|e| (e.sealed_reference, e.origin, e.original, e.targets)),
            )
        {
            if !refs.insert(r)
                || (o == origin && !own.iter().any(|p| p.original.sealed_reference == r))
            {
                return Err(Error::UnqualifiedBasis);
            }
            if let Some(p) = own.iter().find(|p| p.original.sealed_reference == r) {
                if o != origin
                    || raw != p.original.proposal.update
                    || targets != p.original.proposal.targets
                {
                    return Err(Error::UnqualifiedBasis);
                }
            }
        }
        let mut previous = None;
        for p in own {
            if p.original.proposal.origin != origin
                || p.original.ordinal >= original.ordinal
                || previous.is_some_and(|n: u64| n.checked_add(1) != Some(p.original.ordinal))
                || !own_refs.insert(p.original.sealed_reference)
                || !refs.contains(p.original.sealed_reference)
            {
                return Err(Error::UnqualifiedBasis);
            }
            previous = Some(p.original.ordinal);
        }
        if previous.is_some_and(|n: u64| n.checked_add(1) != Some(original.ordinal)) {
            return Err(Error::UnqualifiedBasis);
        }
        if let Some(p) = prepared {
            let mut prior = p.tx_start_revision;
            for e in p.entries {
                if e.prospective_revision <= prior {
                    return Err(Error::UnqualifiedBasis);
                }
                prior = e.prospective_revision;
            }
            if transcript
                .last()
                .is_some_and(|e| e.revision != Some(p.tx_start_revision))
            {
                return Err(Error::UnqualifiedBasis);
            }
        }
        Ok(Self {
            basis,
            current,
            origin,
            original,
            transcript,
            own_history: &[],
            own_prefix,
            views: None,
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
            .chain(
                self.views
                    .as_ref()
                    .and_then(|v| v.prepared)
                    .into_iter()
                    .flat_map(|p| p.entries)
                    .map(|e| e.original.len()),
            )
            .chain(history.iter().map(HostCommittedOwnOperation::byte_len))
            .try_fold(0usize, usize::checked_add);
        if self
            .transcript
            .len()
            .checked_add(
                self.views
                    .as_ref()
                    .and_then(|v| v.prepared)
                    .map_or(0, |p| p.entries.len()),
            )
            .and_then(|n| n.checked_add(history.len()))
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
    prepare_with_authored_cut(authority, None)
}
/// The digest is immutable client intent, never authority. Replay must prove
/// its exact author-view state before any merge into the global Saved view.
pub fn prepare_confirmed_basis_with_authored_cut(
    authority: Authority<'_>,
    authored_checkpoint_state_digest: &str,
) -> Result<Candidate, Error> {
    prepare_with_authored_cut(authority, Some(authored_checkpoint_state_digest))
}
fn prepare_with_authored_cut(
    authority: Authority<'_>,
    expected_authored_cut: Option<&str>,
) -> Result<Candidate, Error> {
    let Authority::Writer(host) = authority else {
        return Err(Error::Forbidden);
    };
    let original = host.original.proposal;
    if host.views.is_some() {
        prove_global(&host)?;
    }
    // This is a disposable AUTHOR view, never an authoritative Saved cut.
    // Already-Saved own originals can belong to a different global interleave.
    let replay_start = host
        .views
        .as_ref()
        .map(|views| views.replay().0)
        .unwrap_or(host.basis);
    let mut authored_basis = replay_start.to_vec();
    let mut applied_peers = BTreeSet::new();
    for prefix in host.own_cuts() {
        let proposal = prefix.original.proposal;
        if host.views.is_some() {
            advance_author_peers(
                &host,
                &mut authored_basis,
                prefix
                    .original
                    .base_revision
                    .ok_or(Error::UnqualifiedBasis)?,
                &mut applied_peers,
            )?;
        }
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
        if checkpoint_state_digest(&authored_basis) != prefix.authored {
            return Err(Error::UnqualifiedBasis);
        }
    }
    if host.views.is_some() {
        advance_author_peers(
            &host,
            &mut authored_basis,
            host.original.base_revision.ok_or(Error::UnqualifiedBasis)?,
            &mut applied_peers,
        )?;
    }
    // Phase 1: unchanged bytes/frontier/targets are qualified on their EXACT
    // original confirmed basis. No read/frontier rewriting or command replay.
    let prepare_original = if host.own_cuts().is_empty() && expected_authored_cut.is_none() {
        current::prepare_current
    } else {
        current::prepare_author_view
    };
    let authored_original = prepare_original(
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
    if let Some(expected) = expected_authored_cut {
        let authored = authored_original
            .checkpoint()
            .map_err(|_| Error::UnqualifiedBasis)?;
        if checkpoint_state_digest(&authored) != expected {
            return Err(Error::UnqualifiedBasis);
        }
    }
    if host.views.is_none() {
        prove_global(&host)?;
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

fn prove_global(host: &HostConfirmedBasis<'_>) -> Result<(), Error> {
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
    let mut replay = host
        .views
        .as_ref()
        .map(|views| views.replay().0)
        .unwrap_or(host.basis)
        .to_vec();
    let mut saw_original = host
        .views
        .as_ref()
        .is_none_or(|views| views.replay().1 == views.original_revision && replay == host.basis);
    for entry in host.transcript {
        replay =
            current::merge_qualified_original(&replay, entry.original, entry.targets, entry.origin)
                .map_err(|_| Error::UnqualifiedBasis)?
                .checkpoint()
                .map_err(|_| Error::UnqualifiedBasis)?;
        if let Some(views) = &host.views {
            if entry.revision == Some(views.original_revision) {
                if replay != views.original {
                    return Err(Error::UnqualifiedBasis);
                }
                saw_original = true;
            }
        }
        if host.own_prefix.iter().any(|prefix| {
            prefix.original.sealed_reference == entry.sealed_reference
                && checkpoint_state_digest(&replay) != prefix.saved_checkpoint_state_digest
        }) {
            return Err(Error::UnqualifiedBasis);
        }
    }
    if let Some(p) = host.views.as_ref().and_then(|v| v.prepared) {
        let replay_revision = host
            .transcript
            .last()
            .and_then(|e| e.revision)
            .unwrap_or_else(|| host.views.as_ref().map_or(0, |v| v.replay().1));
        if replay_revision != p.tx_start_revision
            || checkpoint_state_digest(&replay) != p.tx_start_checkpoint_state_digest
        {
            return Err(Error::UnqualifiedBasis);
        }
        for e in p.entries {
            replay = current::merge_qualified_original(&replay, e.original, e.targets, e.origin)
                .map_err(|_| Error::UnqualifiedBasis)?
                .checkpoint()
                .map_err(|_| Error::UnqualifiedBasis)?;
            if checkpoint_state_digest(&replay) != e.prepared_checkpoint_state_digest {
                return Err(Error::UnqualifiedBasis);
            }
        }
    }
    if !saw_original || replay != host.current {
        return Err(Error::UnqualifiedBasis);
    }
    Ok(())
}
fn advance_author_peers<'a>(
    host: &HostConfirmedBasis<'a>,
    checkpoint: &mut Vec<u8>,
    observed_revision: u64,
    applied: &mut BTreeSet<&'a [u8]>,
) -> Result<(), Error> {
    for entry in host.transcript {
        let revision = entry.revision.ok_or(Error::UnqualifiedBasis)?;
        if entry.origin != host.origin
            && revision <= observed_revision
            && applied.insert(entry.sealed_reference)
        {
            *checkpoint = current::merge_author_view_original(
                checkpoint,
                entry.original,
                entry.targets,
                entry.origin,
            )
            .map_err(|_| Error::UnqualifiedBasis)?
            .checkpoint()
            .map_err(|_| Error::UnqualifiedBasis)?;
        }
    }
    Ok(())
}
