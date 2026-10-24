//! Experimental application-only validation of original client-authored deltas.
//! No network handler admits these bytes. The host must independently bind the
//! origin to authenticated writer/epoch, qualify causal reads, and commit the
//! returned candidate in its final authority transaction. This API grants none
//! of those permissions and never represents a Saved receipt.
use crate::{AdapterError, Document};
use mooring_text::yrs::{Map, Observable, Out, ReadTxn, Transact};
use std::{cell::RefCell, collections::BTreeSet, rc::Rc};

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Access {
    Reader,
    Writer,
}

#[derive(Debug, PartialEq, Eq)]
pub enum DeltaError {
    Forbidden,
    InvalidCheckpoint,
    InvalidUpdate,
    InvalidTargets,
    UnsupportedMutation,
    MissingDependencies,
}

/// An unconfirmed, app-validated candidate; no authority/revision is assigned.
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

/// Pure fork validation. `origin` is trusted host context, never a value taken
/// from the untrusted payload. Callers must gate SyncStep2 at the same authority
/// boundary BEFORE decoding its update, not merely gate ordinary Update frames.
pub fn validate_delta(
    checkpoint: &[u8],
    access: Access,
    origin: u64,
    bytes: &[u8],
    targets: &[String],
) -> Result<Candidate, DeltaError> {
    // Authorization first, including malformed frames: no decoder or live
    // replica is touched for a reader.
    if access != Access::Writer {
        return Err(DeltaError::Forbidden);
    }
    let document = Document::restore(checkpoint).map_err(|_| DeltaError::InvalidCheckpoint)?;
    if document.is_read_only() {
        return Err(DeltaError::UnsupportedMutation);
    }
    let before_native = document
        .native()
        .map_err(|_| DeltaError::InvalidCheckpoint)?;
    let ids = document
        .block_ids()
        .map_err(|_| DeltaError::InvalidCheckpoint)?;
    let declared: BTreeSet<_> = targets.iter().cloned().collect();
    if targets.is_empty()
        || declared.len() != targets.len()
        || !declared.iter().all(|id| ids.contains(id))
    {
        return Err(DeltaError::InvalidTargets);
    }
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
        return Err(DeltaError::InvalidCheckpoint);
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
    let clock = txn.state_vector().get(&mooring_text::yrs::ClientID::new(
        if origin > 0 && origin < (1 << 53) {
            origin
        } else {
            return Err(DeltaError::InvalidUpdate);
        },
    ));
    let update =
        crate::human_decode::decode(bytes, origin, clock).map_err(|_| DeltaError::InvalidUpdate)?;
    let root_changed = Rc::new(RefCell::new(false));
    for name in ["wordcraft.metadata", "wordcraft.blocks", "wordcraft.texts"] {
        let map = txn.get_map(name).ok_or(DeltaError::InvalidCheckpoint)?;
        let expected: BTreeSet<_> = if name == "wordcraft.metadata" {
            ["document".to_owned()].into_iter().collect()
        } else {
            ids.iter().cloned().collect()
        };
        if map.keys(&txn).map(String::from).collect::<BTreeSet<_>>() != expected {
            return Err(DeltaError::InvalidCheckpoint);
        }
        let changed = root_changed.clone();
        map.observe("human-audit", move |_, _| *changed.borrow_mut() = true);
    }
    let order = txn
        .get_array("wordcraft.order")
        .ok_or(DeltaError::InvalidCheckpoint)?;
    let changed = root_changed.clone();
    order.observe("human-audit", move |_, _| *changed.borrow_mut() = true);
    let dirty = Rc::new(RefCell::new(BTreeSet::new()));
    let texts = txn
        .get_map("wordcraft.texts")
        .ok_or(DeltaError::InvalidCheckpoint)?;
    for id in &ids {
        let Some(Out::YText(text)) = texts.get(&txn, id) else {
            return Err(DeltaError::InvalidCheckpoint);
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
        txn.apply_update(update)
            .map_err(|_| DeltaError::InvalidUpdate)?;
        if txn.has_missing_updates() {
            return Err(DeltaError::MissingDependencies);
        }
        txn.commit();
    }
    let txn = document.doc.transact();
    let after_roots: BTreeSet<_> = txn.root_refs().map(|(name, _)| name.to_string()).collect();
    drop(txn);
    if *root_changed.borrow() || after_roots != roots {
        return Err(DeltaError::UnsupportedMutation);
    }
    if *dirty.borrow() != declared {
        return Err(DeltaError::InvalidTargets);
    }
    let after_native = document
        .native()
        .map_err(|_| DeltaError::UnsupportedMutation)?;
    let mut before_metadata = before_native;
    let mut after_metadata = after_native.clone();
    for native in [&mut before_metadata, &mut after_metadata] {
        for block in native["body"]
            .as_array_mut()
            .ok_or(DeltaError::InvalidCheckpoint)?
        {
            let block = block.as_object_mut().ok_or(DeltaError::InvalidCheckpoint)?;
            block.remove("text");
            block.remove("runs");
        }
    }
    if before_metadata != after_metadata
        || document
            .block_ids()
            .map_err(|_| DeltaError::InvalidCheckpoint)?
            != ids
        || crate::classify(&after_native).map_err(|_| DeltaError::UnsupportedMutation)?
    {
        return Err(DeltaError::UnsupportedMutation);
    }
    Ok(Candidate {
        document,
        affected: declared.into_iter().collect(),
    })
}

#[cfg(test)]
mod tests {
    use super::*;
    use mooring_text::yrs::{
        types::Attrs, updates::decoder::Decode, Any, Array, ClientID, Doc, OffsetKind, Options,
        Text, Update,
    };
    fn baseline() -> (Vec<u8>, Vec<String>) {
        let native =
            serde_json::from_str(include_str!("../tests/fixtures/d1-engine-canonical.json"))
                .unwrap();
        let document = Document::import(native, (0..8).map(|i| format!("p{i}")).collect()).unwrap();
        (
            document.checkpoint().unwrap(),
            document.block_ids().unwrap(),
        )
    }
    fn client(checkpoint: &[u8], origin: u64) -> Doc {
        let checkpoint = Document::restore(checkpoint).unwrap();
        let update = checkpoint
            .doc
            .transact()
            .encode_state_as_update_v1(&mooring_text::yrs::StateVector::default());
        let doc = Doc::with_options(Options {
            client_id: ClientID::new(origin),
            offset_kind: OffsetKind::Utf16,
            skip_gc: true,
            ..Options::default()
        });
        doc.transact_mut()
            .apply_update(Update::decode_v1(&update).unwrap())
            .unwrap();
        doc
    }
    fn text(doc: &Doc, id: &str) -> mooring_text::yrs::TextRef {
        let txn = doc.transact();
        let Some(Out::YText(text)) = txn.get_map("wordcraft.texts").unwrap().get(&txn, id) else {
            panic!()
        };
        text
    }
    fn capture(doc: &Doc, mutate: impl FnOnce()) -> Vec<u8> {
        let result = Rc::new(RefCell::new(Vec::new()));
        let received = result.clone();
        doc.observe_update_v1("capture", move |_, event| {
            *received.borrow_mut() = event.update.clone();
        })
        .unwrap();
        mutate();
        doc.unobserve_update_v1("capture").unwrap();
        result.take()
    }
    fn insert(doc: &Doc, id: &str, value: &str) -> Vec<u8> {
        capture(doc, || {
            text(doc, id).insert(&mut doc.transact_mut(), 0, value)
        })
    }
    #[test]
    fn original_a_then_b_retains_authored_ids_and_storage_restore() {
        let (checkpoint, ids) = baseline();
        let writer = client(&checkpoint, 17);
        let a = insert(&writer, &ids[0], "A");
        let b = insert(&writer, &ids[0], "B");
        let accepted_a = validate_delta(&checkpoint, Access::Writer, 17, &a, &ids[..1]).unwrap();
        let accepted_b = validate_delta(
            &accepted_a.checkpoint().unwrap(),
            Access::Writer,
            17,
            &b,
            &ids[..1],
        )
        .unwrap();
        let restored = Document::restore(&accepted_b.checkpoint().unwrap()).unwrap();
        let writer_checkpoint = crate::Checkpoint {
            format: crate::profile::LEGACY_CHECKPOINT_FORMAT.into(),
            native: accepted_b.native().unwrap(),
            block_ids: ids.clone(),
            update: writer
                .transact()
                .encode_state_as_update_v1(&Default::default()),
        };
        let exact_client =
            Document::restore(&serde_json::to_vec(&writer_checkpoint).unwrap()).unwrap();
        assert_eq!(restored.native().unwrap(), exact_client.native().unwrap());
        assert_eq!(
            restored.doc.transact().state_vector(),
            writer.transact().state_vector()
        );
        assert_eq!(restored.block_ids().unwrap(), ids);
        assert_eq!(accepted_b.affected_targets(), &ids[..1]);
    }
    #[test]
    fn rejected_a_b_recovery_fresh_c_never_aliases_old_ids() {
        let (checkpoint, ids) = baseline();
        let optimistic = client(&checkpoint, 17);
        let a = insert(&optimistic, &ids[0], "same");
        let b = insert(&optimistic, &ids[0], "B");
        assert!(matches!(
            validate_delta(&checkpoint, Access::Writer, 17, &b, &ids[..1]),
            Err(DeltaError::InvalidUpdate)
        ));
        let recovered = client(&checkpoint, 18); // MUST be a new host grant.
        let c = insert(&recovered, &ids[0], "same");
        let after_c = validate_delta(&checkpoint, Access::Writer, 18, &c, &ids[..1]).unwrap();
        assert!(matches!(
            validate_delta(
                &after_c.checkpoint().unwrap(),
                Access::Writer,
                17,
                &b,
                &ids[..1]
            ),
            Err(DeltaError::InvalidUpdate)
        ));
        let actual = Document::restore(&after_c.checkpoint().unwrap()).unwrap();
        assert_eq!(
            actual.doc.transact().state_vector().get(&ClientID::new(17)),
            0
        );
        assert_eq!(
            actual.doc.transact().state_vector().get(&ClientID::new(18)),
            4
        );
        assert!(validate_delta(&checkpoint, Access::Writer, 18, &a, &ids[..1]).is_err());
    }
    #[test]
    fn readers_are_refused_before_even_checkpoint_or_update_decode() {
        assert!(matches!(
            validate_delta(b"invalid", Access::Reader, u64::MAX, b"invalid", &[]),
            Err(DeltaError::Forbidden)
        ));
    }
    #[test]
    fn undeclared_foreign_and_unknown_root_changes_are_rejected() {
        let (checkpoint, ids) = baseline();
        let writer = client(&checkpoint, 17);
        let bytes = insert(&writer, &ids[0], "A");
        assert!(matches!(
            validate_delta(&checkpoint, Access::Writer, 18, &bytes, &ids[..1]),
            Err(DeltaError::InvalidUpdate)
        ));
        assert!(matches!(
            validate_delta(&checkpoint, Access::Writer, 17, &bytes, &ids[1..2]),
            Err(DeltaError::InvalidTargets)
        ));
        let hidden = client(&checkpoint, 17);
        let bytes = capture(&hidden, || {
            let text = hidden.get_or_insert_text("invisible.root");
            let mut txn = hidden.transact_mut();
            text.insert(&mut txn, 0, "hidden");
            text.remove_range(&mut txn, 0, 6);
        });
        assert!(matches!(
            validate_delta(&checkpoint, Access::Writer, 17, &bytes, &ids[..1]),
            Err(DeltaError::UnsupportedMutation)
        ));
    }
    #[test]
    fn metadata_order_and_transient_unknown_format_are_rejected() {
        let (checkpoint, ids) = baseline();
        let writer = client(&checkpoint, 17);
        let bytes = capture(&writer, || {
            let map = writer.transact().get_map("wordcraft.metadata").unwrap();
            let mut txn = writer.transact_mut();
            map.insert(&mut txn, "hidden", "x");
            map.remove(&mut txn, "hidden");
        });
        assert!(validate_delta(&checkpoint, Access::Writer, 17, &bytes, &ids[..1]).is_err());
        let writer = client(&checkpoint, 17);
        let bytes = capture(&writer, || {
            let order = writer.transact().get_array("wordcraft.order").unwrap();
            let mut txn = writer.transact_mut();
            order.remove_range(&mut txn, 0, 1);
        });
        assert!(
            matches!(
                validate_delta(&checkpoint, Access::Writer, 17, &bytes, &ids[..1]),
                Err(DeltaError::UnsupportedMutation)
            ),
            "order result: {:?}",
            validate_delta(&checkpoint, Access::Writer, 17, &bytes, &ids[..1]).err()
        );
        let writer = client(&checkpoint, 17);
        let bytes = capture(&writer, || {
            let text = text(&writer, &ids[0]);
            let mut txn = writer.transact_mut();
            text.format(
                &mut txn,
                0,
                1,
                Attrs::from([("unknown".into(), Any::Bool(true))]),
            );
            text.format(&mut txn, 0, 1, Attrs::from([("unknown".into(), Any::Null)]));
        });
        assert!(matches!(
            validate_delta(&checkpoint, Access::Writer, 17, &bytes, &ids[..1]),
            Err(DeltaError::InvalidUpdate)
        ));
    }
    #[test]
    fn actual_format_delete_and_selective_origin_undo_preserve_peer() {
        use mooring_text::yrs::{Origin, UndoManager};
        use std::{collections::HashSet, sync::Arc};
        let (checkpoint, ids) = baseline();
        let writer = client(&checkpoint, 17);
        let origin = Origin::from(ClientID::new(17));
        let mut undo: UndoManager = UndoManager::with_options(mooring_text::yrs::undo::Options {
            capture_timeout_millis: 0,
            tracked_origins: HashSet::from([origin.clone()]),
            capture_transaction: None,
            timestamp: Arc::new(|| 0_u64),
            init_undo_stack: vec![],
            init_redo_stack: vec![],
        });
        let texts = writer.transact().get_map("wordcraft.texts").unwrap();
        undo.expand_scope(&writer, &texts);
        let a = capture(&writer, || {
            text(&writer, &ids[0]).insert(&mut writer.transact_mut_with(origin.clone()), 0, "OWN");
        });
        let after_a = validate_delta(&checkpoint, Access::Writer, 17, &a, &ids[..1]).unwrap();
        let peer = client(&after_a.checkpoint().unwrap(), 18);
        let b = insert(&peer, &ids[0], "PEER");
        let after_b = validate_delta(
            &after_a.checkpoint().unwrap(),
            Access::Writer,
            18,
            &b,
            &ids[..1],
        )
        .unwrap();
        writer
            .transact_mut_with("verified-host")
            .apply_update(Update::decode_v1(&b).unwrap())
            .unwrap();
        let undo_delta = capture(&writer, || assert!(undo.undo_blocking()));
        let undone = validate_delta(
            &after_b.checkpoint().unwrap(),
            Access::Writer,
            17,
            &undo_delta,
            &ids[..1],
        )
        .unwrap();
        let actual = undone.native().unwrap();
        assert!(actual["body"][0]["text"]
            .as_str()
            .unwrap()
            .starts_with("PEER"));
        assert!(!actual["body"][0]["text"].as_str().unwrap().contains("OWN"));
        let redo_delta = capture(&writer, || assert!(undo.redo_blocking()));
        let redone = validate_delta(
            &undone.checkpoint().unwrap(),
            Access::Writer,
            17,
            &redo_delta,
            &ids[..1],
        )
        .unwrap();
        assert_eq!(redone.native().unwrap(), after_b.native().unwrap());
        let format = capture(&writer, || {
            text(&writer, &ids[0]).format(
                &mut writer.transact_mut_with(origin.clone()),
                0,
                4,
                Attrs::from([
                    ("wordcraft.props".into(), Any::Bool(true)),
                    ("wordcraft.chr.bold".into(), Any::Bool(true)),
                ]),
            );
        });
        let formatted = validate_delta(
            &redone.checkpoint().unwrap(),
            Access::Writer,
            17,
            &format,
            &ids[..1],
        )
        .unwrap();
        assert_eq!(
            formatted.native().unwrap()["body"][0]["runs"][0]["props"]["bold"],
            true
        );
        let delete = capture(&writer, || {
            text(&writer, &ids[0]).remove_range(&mut writer.transact_mut_with(origin), 0, 4)
        });
        let deleted = validate_delta(
            &formatted.checkpoint().unwrap(),
            Access::Writer,
            17,
            &delete,
            &ids[..1],
        )
        .unwrap();
        assert!(!deleted.native().unwrap()["body"][0]["text"]
            .as_str()
            .unwrap()
            .contains("PEER"));
    }

    #[test]
    fn missing_foreign_anchor_never_installs_partial_state() {
        let (checkpoint, ids) = baseline();
        let other = client(&checkpoint, 18);
        let a = insert(&other, &ids[0], "A");
        let writer = client(&checkpoint, 17);
        writer
            .transact_mut()
            .apply_update(Update::decode_v1(&a).unwrap())
            .unwrap();
        let b = insert(&writer, &ids[0], "B");
        let original = Document::restore(&checkpoint).unwrap().native().unwrap();
        assert!(matches!(
            validate_delta(&checkpoint, Access::Writer, 17, &b, &ids[..1]),
            Err(DeltaError::MissingDependencies)
        ));
        assert_eq!(
            Document::restore(&checkpoint).unwrap().native().unwrap(),
            original
        );
        let after_a = validate_delta(&checkpoint, Access::Writer, 18, &a, &ids[..1]).unwrap();
        assert!(validate_delta(
            &after_a.checkpoint().unwrap(),
            Access::Writer,
            17,
            &b,
            &ids[..1]
        )
        .is_ok());
    }

    #[test]
    fn independent_original_deltas_converge_in_both_orders_and_embeds_fail() {
        let (checkpoint, ids) = baseline();
        let a = insert(&client(&checkpoint, 17), &ids[0], "A");
        let b = insert(&client(&checkpoint, 18), &ids[0], "B");
        let ab_a = validate_delta(&checkpoint, Access::Writer, 17, &a, &ids[..1]).unwrap();
        let ab = validate_delta(
            &ab_a.checkpoint().unwrap(),
            Access::Writer,
            18,
            &b,
            &ids[..1],
        )
        .unwrap();
        let ba_b = validate_delta(&checkpoint, Access::Writer, 18, &b, &ids[..1]).unwrap();
        let ba = validate_delta(
            &ba_b.checkpoint().unwrap(),
            Access::Writer,
            17,
            &a,
            &ids[..1],
        )
        .unwrap();
        assert_eq!(ab.native().unwrap(), ba.native().unwrap());
        let ab = Document::restore(&ab.checkpoint().unwrap()).unwrap();
        let ba = Document::restore(&ba.checkpoint().unwrap()).unwrap();
        assert_eq!(
            ab.doc.transact().state_vector(),
            ba.doc.transact().state_vector()
        );
        assert_eq!(ab.block_ids().unwrap(), ba.block_ids().unwrap());
        let malicious = client(&checkpoint, 17);
        let embed = capture(&malicious, || {
            text(&malicious, &ids[0]).insert_embed(
                &mut malicious.transact_mut(),
                0,
                Any::Bool(true),
            );
        });
        assert!(matches!(
            validate_delta(&checkpoint, Access::Writer, 17, &embed, &ids[..1]),
            Err(DeltaError::InvalidUpdate)
        ));
    }
}
