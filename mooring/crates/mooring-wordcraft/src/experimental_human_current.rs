//! Pure host-only v2 current-frontier experiment. No transport admission,
//! credential verification, durable outcome, retirement or Saved semantics.
use crate::{AdapterError, Document};
use mooring_text::yrs::{ClientID, Map, Observable, Out, ReadTxn, StateVector, Transact, Update};
use std::{cell::RefCell, collections::BTreeSet, rc::Rc};

#[derive(Debug, PartialEq, Eq)]
pub enum Error {
    Forbidden,
    InvalidHostContext,
    InvalidCheckpoint,
    InvalidUpdate,
    UnqualifiedFrontier,
    InvalidTargets,
    UnsupportedMutation,
}
/// Only an authenticated host boundary may construct this. No Deserialize
/// implementation intentionally exists; this is never a client wire claim.
pub struct HostWriter<'a> {
    checkpoint: &'a [u8],
    origin: u64,
    history: &'a [HostCommittedOwnOperation<'a>],
}
impl<'a> HostWriter<'a> {
    pub fn current(checkpoint: &'a [u8], origin: u64) -> Result<Self, Error> {
        if origin == 0 || origin >= (1 << 53) {
            return Err(Error::InvalidHostContext);
        }
        Ok(Self {
            checkpoint,
            origin,
            history: &[],
        })
    }
    /// Host must independently qualify SAME-scope/original-epoch Saved
    /// records and immutable bytes. Never consume unverified client history.
    pub fn with_committed_own_history(
        mut self,
        history: &'a [HostCommittedOwnOperation<'a>],
    ) -> Result<Self, Error> {
        let bytes = history
            .iter()
            .try_fold(0usize, |sum, operation| {
                sum.checked_add(operation.original.len())
            })
            .ok_or(Error::InvalidHostContext)?;
        if history.len() > 256
            || bytes > 262144
            || history
                .iter()
                .any(|operation| operation.origin != self.origin)
        {
            return Err(Error::InvalidHostContext);
        }
        self.history = history;
        Ok(self)
    }
}
/// Typed host-only HISTORY INPUT, not a durable proof or wire envelope. Only
/// the authenticated host's committed store may construct it after binding
/// exact scope, participant, original epoch/source and Saved receipt.
/// Intentionally no Deserialize or client permission/provenance booleans.
pub struct HostCommittedOwnOperation<'a> {
    origin: u64,
    original: &'a [u8],
}
impl<'a> HostCommittedOwnOperation<'a> {
    pub fn from_trusted_committed_host_store(origin: u64, original: &'a [u8]) -> Self {
        Self { origin, original }
    }
}
pub enum Authority<'a> {
    Reader,
    Writer(HostWriter<'a>),
}
pub struct Candidate {
    document: Document,
    affected: Vec<String>,
}
impl Candidate {
    pub fn checkpoint(&self) -> Result<Vec<u8>, AdapterError> {
        self.document.checkpoint()
    }
    pub fn native(&self) -> Result<serde_json::Value, AdapterError> {
        self.document.native()
    }
    pub fn affected_targets(&self) -> &[String] {
        &self.affected
    }
}
/// Client records this exact confirmed frontier BEFORE authoring an operation.
/// This byte helper grants no authority and does not qualify optimistic history.
pub fn checkpoint_frontier(checkpoint: &[u8]) -> Result<Vec<u8>, Error> {
    let checked = Document::restore(checkpoint).map_err(|_| Error::InvalidCheckpoint)?;
    Ok(checked.state_vector())
}
pub fn prepare_current(
    authority: Authority<'_>,
    authored_frontier: &[u8],
    original: &[u8],
    targets: &[String],
) -> Result<Candidate, Error> {
    let Authority::Writer(writer) = authority else {
        return Err(Error::Forbidden);
    };
    if authored_frontier != checkpoint_frontier(writer.checkpoint)? {
        return Err(Error::UnqualifiedFrontier);
    }
    let document = Document::restore(writer.checkpoint).map_err(|_| Error::InvalidCheckpoint)?;
    if document.is_read_only() {
        return Err(Error::UnsupportedMutation);
    }
    declared_targets(
        &document.block_ids().map_err(|_| Error::InvalidCheckpoint)?,
        targets,
    )?;
    let txn = document.doc.transact();
    let clock = txn.state_vector().get(&ClientID::new(writer.origin));
    let before_vector = txn.state_vector();
    let stamp = before_vector
        .iter()
        .try_fold(1u64, |sum, (_, clock)| sum.checked_add(*clock as u64))
        .filter(|stamp| *stamp < 1 << 53)
        .ok_or(Error::UnqualifiedFrontier)?;
    let raw_checkpoint = txn.encode_state_as_update_v1(&StateVector::default());
    let mut history = crate::human_current_wire::history(&raw_checkpoint)?;
    for operation in writer.history {
        let old =
            crate::human_current_wire::committed_own_history(operation.original, writer.origin)?;
        for (key, value) in old {
            if let Some(prior) = history.insert(key, value.clone()) {
                if prior != value {
                    return Err(Error::InvalidHostContext);
                }
            }
        }
    }
    let update =
        crate::human_current_wire::original(original, writer.origin, clock, stamp, &history)?;
    for (client, ranges) in update.delete_set().iter() {
        for range in ranges.iter() {
            if before_vector.get(client) < range.end {
                return Err(Error::UnqualifiedFrontier);
            }
        }
    }
    drop(txn);
    apply_audited(document, update, targets)
}

// Called only after original-basis qualification, or for a host-qualified
// durable Saved transcript entry. This grants no authority to raw client bytes.
pub(crate) fn merge_qualified_original(
    checkpoint: &[u8],
    original: &[u8],
    targets: &[String],
    origin: u64,
) -> Result<Candidate, Error> {
    use mooring_text::yrs::updates::decoder::Decode;
    if original.len() > 262144 || origin >= 1 << 53 {
        return Err(Error::InvalidHostContext);
    }
    crate::checkpoint_wire::preflight(original).map_err(|_| Error::InvalidUpdate)?;
    let update = Update::decode_v1(original).map_err(|_| Error::InvalidUpdate)?;
    if update
        .insertions(true)
        .iter()
        .any(|(client, _)| client.get() != origin)
    {
        return Err(Error::InvalidHostContext);
    }
    let document = Document::restore(checkpoint).map_err(|_| Error::InvalidCheckpoint)?;
    apply_audited(document, update, targets)
}

fn declared_targets(ids: &[String], targets: &[String]) -> Result<BTreeSet<String>, Error> {
    let declared: BTreeSet<_> = targets.iter().cloned().collect();
    if targets.is_empty()
        || declared.len() != targets.len()
        || !declared.iter().all(|id| ids.contains(id))
    {
        return Err(Error::InvalidTargets);
    }
    Ok(declared)
}

// Shared full-state audit. Callers must first qualify the ORIGINAL immutable
// update against its actual authoring basis, origin, clock and format history.
fn apply_audited(
    document: Document,
    update: Update,
    targets: &[String],
) -> Result<Candidate, Error> {
    if document.is_read_only() {
        return Err(Error::UnsupportedMutation);
    }
    let before_native = document.native().map_err(|_| Error::InvalidCheckpoint)?;
    let ids = document.block_ids().map_err(|_| Error::InvalidCheckpoint)?;
    let declared = declared_targets(&ids, targets)?;
    let txn = document.doc.transact();
    let roots: BTreeSet<_> = txn.root_refs().map(|(name, _)| name.to_string()).collect();
    let allowed: BTreeSet<_> = [
        "wordcraft.metadata",
        "wordcraft.blocks",
        "wordcraft.order",
        "wordcraft.texts",
    ]
    .into_iter()
    .map(String::from)
    .collect();
    if roots != allowed {
        return Err(Error::InvalidCheckpoint);
    }
    drop(txn);
    // Named root types are not encoded in a Yrs state update. Materialize the
    // already verified application root types before observing them; an
    // undefined restored root otherwise emits no typed shallow event.
    for name in ["wordcraft.metadata", "wordcraft.blocks", "wordcraft.texts"] {
        document.doc.get_or_insert_map(name);
    }
    document.doc.get_or_insert_array("wordcraft.order");
    let txn = document.doc.transact();
    let allocated = update.insertions(true);
    let root_changed = Rc::new(RefCell::new(false));
    for name in ["wordcraft.metadata", "wordcraft.blocks", "wordcraft.texts"] {
        let map = txn.get_map(name).ok_or(Error::InvalidCheckpoint)?;
        let expected: BTreeSet<_> = if name == "wordcraft.metadata" {
            ["document".to_owned()].into_iter().collect()
        } else {
            ids.iter().cloned().collect()
        };
        if map.keys(&txn).map(String::from).collect::<BTreeSet<_>>() != expected {
            return Err(Error::InvalidCheckpoint);
        }
        let changed = root_changed.clone();
        map.observe("human-audit", move |_, _| *changed.borrow_mut() = true);
    }
    let order = txn
        .get_array("wordcraft.order")
        .ok_or(Error::InvalidCheckpoint)?;
    let changed = root_changed.clone();
    order.observe("human-audit", move |_, _| *changed.borrow_mut() = true);
    let dirty = Rc::new(RefCell::new(BTreeSet::new()));
    let texts = txn
        .get_map("wordcraft.texts")
        .ok_or(Error::InvalidCheckpoint)?;
    for id in &ids {
        let Some(Out::YText(text)) = texts.get(&txn, id) else {
            return Err(Error::InvalidCheckpoint);
        };
        let affected = dirty.clone();
        let id = id.clone();
        text.observe("human-audit", move |_, _| {
            affected.borrow_mut().insert(id.clone());
        });
    }
    drop(txn);
    {
        let mut txn = document.doc.transact_mut();
        txn.apply_update(update).map_err(|_| Error::InvalidUpdate)?;
        if txn.has_missing_updates() {
            return Err(Error::UnqualifiedFrontier);
        }
        txn.commit();
    }
    let txn = document.doc.transact();
    for (client, ranges) in allocated.iter() {
        for range in ranges.iter() {
            if txn.state_vector().get(client) < range.end {
                return Err(Error::UnqualifiedFrontier);
            }
        }
    }
    let after_roots: BTreeSet<_> = txn.root_refs().map(|(name, _)| name.to_string()).collect();
    drop(txn);
    if *root_changed.borrow() || after_roots != roots {
        return Err(Error::UnsupportedMutation);
    }
    if *dirty.borrow() != declared {
        return Err(Error::InvalidTargets);
    }
    let after_native = document.native().map_err(|_| Error::UnsupportedMutation)?;
    let mut before_metadata = before_native;
    let mut after_metadata = after_native.clone();
    for native in [&mut before_metadata, &mut after_metadata] {
        for block in native["body"]
            .as_array_mut()
            .ok_or(Error::InvalidCheckpoint)?
        {
            let block = block.as_object_mut().ok_or(Error::InvalidCheckpoint)?;
            block.remove("text");
            block.remove("runs");
        }
    }
    if before_metadata != after_metadata
        || document.block_ids().map_err(|_| Error::InvalidCheckpoint)? != ids
        || crate::classify(&after_native).map_err(|_| Error::UnsupportedMutation)?
    {
        return Err(Error::UnsupportedMutation);
    }
    // Cold restore applies the same full model/root/retained-key integrity
    // checks as every other model0.2 checkpoint, on a disposable fork only.
    let document = Document::restore(&document.checkpoint().map_err(|_| Error::InvalidUpdate)?)
        .map_err(|_| Error::UnsupportedMutation)?;
    Ok(Candidate {
        document,
        affected: declared.into_iter().collect(),
    })
}
#[cfg(test)]
mod tests {
    use super::*;
    use crate::{
        command::{Anchor, Command},
        experimental_draft::{DraftAccess, DraftReplica},
    };
    use std::collections::BTreeMap;
    fn baseline() -> Vec<u8> {
        Document::import(
            serde_json::from_str(include_str!("../tests/fixtures/d1-engine-canonical.json"))
                .unwrap(),
            (0..8).map(|i| format!("p{i}")).collect(),
        )
        .unwrap()
        .checkpoint()
        .unwrap()
    }
    fn insert(target: &str) -> Command {
        Command::Insert {
            target_id: target.into(),
            at: Anchor::Start {},
            text: "Č😀".into(),
            properties: Some(BTreeMap::from([
                ("bold".into(), true),
                ("italic".into(), false),
            ])),
        }
    }
    #[test]
    fn actual_current_client_bytes_prepare_full_native_and_original_ids() {
        let base = baseline();
        let mut local = DraftReplica::restore(&base, 17, DraftAccess::Writer).unwrap();
        let proposal = local.edit("human-current", insert("p0")).unwrap();
        let original = proposal.update.clone();
        let candidate = prepare_current(
            Authority::Writer(HostWriter::current(&base, 17).unwrap()),
            &checkpoint_frontier(&base).unwrap(),
            &proposal.update,
            &proposal.targets,
        )
        .map_err(|e| format!("{e:?}"))
        .unwrap();
        assert_eq!(candidate.native().unwrap(), local.native().unwrap());
        assert_eq!(candidate.affected_targets(), proposal.targets);
        let checkpoint = candidate.checkpoint().unwrap();
        let cold = Document::restore(&checkpoint).unwrap();
        assert_eq!(
            cold.block_ids().unwrap(),
            Document::restore(&base).unwrap().block_ids().unwrap()
        );
        assert_eq!(
            cold.doc
                .transact()
                .state_vector()
                .get(&mooring_text::yrs::ClientID::new(17)),
            11 + 2
        );
        assert_eq!(original, proposal.update);
    }
    #[test]
    fn reader_is_denied_before_any_decoder() {
        assert!(matches!(
            prepare_current(Authority::Reader, b"bad", b"bad", &[]),
            Err(Error::Forbidden)
        ));
    }
    #[test]
    fn stale_same_origin_b_is_unknown_not_implicitly_retired() {
        let base = baseline();
        let mut local = DraftReplica::restore(&base, 17, DraftAccess::Writer).unwrap();
        local.edit("a", insert("p1")).unwrap();
        let optimistic = local.optimistic_checkpoint().unwrap();
        let b = local.edit("b", insert("p0")).unwrap();
        assert!(matches!(
            prepare_current(
                Authority::Writer(HostWriter::current(&base, 17).unwrap()),
                &checkpoint_frontier(&optimistic).unwrap(),
                &b.update,
                &b.targets
            ),
            Err(Error::UnqualifiedFrontier)
        ));
    }
    fn accept(base: &[u8], proposal: &crate::experimental_draft::Proposal) -> Candidate {
        prepare_current(
            Authority::Writer(HostWriter::current(base, proposal.origin).unwrap()),
            &checkpoint_frontier(base).unwrap(),
            &proposal.update,
            &proposal.targets,
        )
        .map_err(|e| format!("operation {}: {e:?}", proposal.operation_id))
        .unwrap()
    }
    #[test]
    fn sequential_format_undo_redo_matches_full_actual_client_model() {
        let mut base = baseline();
        let mut local = DraftReplica::restore(&base, 17, DraftAccess::Writer).unwrap();
        let first = local.edit("first", insert("p0")).unwrap();
        base = accept(&base, &first).checkpoint().unwrap();
        let second = local
            .edit(
                "second",
                Command::Format {
                    target_id: "p0".into(),
                    start: Anchor::Start {},
                    end: Anchor::End {},
                    properties: BTreeMap::from([
                        ("bold".into(), Some(false)),
                        ("italic".into(), None),
                    ]),
                },
            )
            .unwrap();
        let candidate = accept(&base, &second);
        assert_eq!(candidate.native().unwrap(), local.native().unwrap());
        base = candidate.checkpoint().unwrap();
        let undo = local.undo("undo").unwrap();
        let candidate = accept(&base, &undo);
        assert_eq!(candidate.native().unwrap(), local.native().unwrap());
        base = candidate.checkpoint().unwrap();
        let redo = local.redo("redo").unwrap();
        let trusted =
            [HostCommittedOwnOperation::from_trusted_committed_host_store(17, &second.update)];
        let authority = HostWriter::current(&base, 17)
            .unwrap()
            .with_committed_own_history(&trusted)
            .unwrap();
        let candidate = prepare_current(
            Authority::Writer(authority),
            &checkpoint_frontier(&base).unwrap(),
            &redo.update,
            &redo.targets,
        )
        .map_err(|e| format!("{e:?}"))
        .unwrap();
        assert_eq!(candidate.native().unwrap(), local.native().unwrap());
    }
    #[test]
    fn wrong_target_origin_and_stamp_refuse_without_mutating_checkpoint() {
        let base = baseline();
        let mut local = DraftReplica::restore(&base, 17, DraftAccess::Writer).unwrap();
        let proposal = local.edit("first", insert("p0")).unwrap();
        let call = |origin, bytes: &[u8], targets: &[String]| {
            prepare_current(
                Authority::Writer(HostWriter::current(&base, origin).unwrap()),
                &checkpoint_frontier(&base).unwrap(),
                bytes,
                targets,
            )
        };
        assert!(matches!(
            call(18, &proposal.update, &proposal.targets),
            Err(Error::InvalidUpdate)
        ));
        assert!(matches!(
            call(17, &proposal.update, &["p1".into()]),
            Err(Error::InvalidTargets)
        ));
        let mut changed = proposal.update.clone();
        for at in changed
            .windows(5)
            .enumerate()
            .filter_map(|(i, b)| (b == b":242:").then_some(i))
            .collect::<Vec<_>>()
        {
            changed[at + 3] = b'3';
        }
        assert!(matches!(
            call(17, &changed, &proposal.targets),
            Err(Error::UnqualifiedFrontier)
        ));
        assert_eq!(
            Document::restore(&base).unwrap().checkpoint().unwrap(),
            base
        );
    }
    #[test]
    fn unknown_delete_history_is_not_silently_buffered() {
        let base = baseline();
        let raw = [0, 1, 17, 1, 0, 1];
        assert!(matches!(
            prepare_current(
                Authority::Writer(HostWriter::current(&base, 17).unwrap()),
                &checkpoint_frontier(&base).unwrap(),
                &raw,
                &["p0".into()]
            ),
            Err(Error::UnqualifiedFrontier)
        ));
    }
    #[test]
    fn stale_same_origin_clock_cannot_claim_current_frontier() {
        let base = baseline();
        let mut local = DraftReplica::restore(&base, 17, DraftAccess::Writer).unwrap();
        local.edit("a", insert("p1")).unwrap();
        let b = local.edit("b", insert("p0")).unwrap();
        assert!(matches!(
            prepare_current(
                Authority::Writer(HostWriter::current(&base, 17).unwrap()),
                &checkpoint_frontier(&base).unwrap(),
                &b.update,
                &b.targets
            ),
            Err(Error::UnqualifiedFrontier)
        ));
    }
    fn raw_client(base: &[u8]) -> mooring_text::yrs::Doc {
        use mooring_text::yrs::{updates::decoder::Decode, Doc, OffsetKind, Options, Update};
        let raw = Document::restore(base)
            .unwrap()
            .doc
            .transact()
            .encode_state_as_update_v1(&StateVector::default());
        let client = Doc::with_options(Options {
            client_id: ClientID::new(17),
            offset_kind: OffsetKind::Utf16,
            skip_gc: true,
            ..Options::default()
        });
        client
            .transact_mut()
            .apply_update(Update::decode_v1(&raw).unwrap())
            .unwrap();
        client
    }
    fn capture(client: &mooring_text::yrs::Doc, mutate: impl FnOnce()) -> Vec<u8> {
        let result = Rc::new(RefCell::new(Vec::new()));
        let received = result.clone();
        client
            .observe_update_v1("capture-current", move |_, e| {
                *received.borrow_mut() = e.update.clone()
            })
            .unwrap();
        mutate();
        client.unobserve_update_v1("capture-current").unwrap();
        result.take()
    }
    #[test]
    fn unknown_roots_metadata_and_order_never_hide_in_projection() {
        use mooring_text::yrs::{Array, Text};
        let base = baseline();
        for kind in 0..3 {
            let client = raw_client(&base);
            let raw = capture(&client, || match kind {
                0 => {
                    let text = client.get_or_insert_text("hidden.root");
                    let mut txn = client.transact_mut();
                    text.insert(&mut txn, 0, "hidden");
                    text.remove_range(&mut txn, 0, 6);
                }
                1 => {
                    let map = client.get_or_insert_map("wordcraft.metadata");
                    let mut txn = client.transact_mut();
                    map.insert(&mut txn, "hidden", "x");
                    map.remove(&mut txn, "hidden");
                }
                _ => {
                    let order = client.get_or_insert_array("wordcraft.order");
                    order.remove_range(&mut client.transact_mut(), 0, 1);
                }
            });
            assert!(
                prepare_current(
                    Authority::Writer(HostWriter::current(&base, 17).unwrap()),
                    &checkpoint_frontier(&base).unwrap(),
                    &raw,
                    &["p0".into()]
                )
                .is_err(),
                "case{kind}"
            );
            assert_eq!(
                Document::restore(&base).unwrap().checkpoint().unwrap(),
                base
            );
        }
    }
    #[test]
    fn host_history_is_bounded_and_own_origin_only() {
        let base = baseline();
        let mut peer = DraftReplica::restore(&base, 18, DraftAccess::Writer).unwrap();
        let update = peer.edit("peer", insert("p0")).unwrap();
        let wrong_binding =
            [HostCommittedOwnOperation::from_trusted_committed_host_store(18, &update.update)];
        assert!(matches!(
            HostWriter::current(&base, 17)
                .unwrap()
                .with_committed_own_history(&wrong_binding),
            Err(Error::InvalidHostContext)
        ));
        let forged =
            [HostCommittedOwnOperation::from_trusted_committed_host_store(17, &update.update)];
        let host = HostWriter::current(&base, 17)
            .unwrap()
            .with_committed_own_history(&forged)
            .unwrap();
        let mut own = DraftReplica::restore(&base, 17, DraftAccess::Writer).unwrap();
        let proposal = own.edit("own", insert("p0")).unwrap();
        assert!(matches!(
            prepare_current(
                Authority::Writer(host),
                &checkpoint_frontier(&base).unwrap(),
                &proposal.update,
                &proposal.targets
            ),
            Err(Error::InvalidHostContext)
        ));
        let too_many = (0..257)
            .map(|_| HostCommittedOwnOperation::from_trusted_committed_host_store(17, &[]))
            .collect::<Vec<_>>();
        assert!(matches!(
            HostWriter::current(&base, 17)
                .unwrap()
                .with_committed_own_history(&too_many),
            Err(Error::InvalidHostContext)
        ));
    }
}
