//! EXPERIMENTAL local draft replica, not human update admission.
//! Original client-authored deltas/IDs and selective undo are retained. Origins
//! and host bytes are caller-authenticated inputs. The local frontier stamp
//! rule is NOT a qualified host causal witness and cannot enable a wire profile.
//! No network, durable outcome or authoritative Saved state is implemented.
use crate::{command::Command, AdapterError, Document};
use mooring_text::yrs::{
    updates::decoder::Decode, ClientID, Doc, Map, Observable, OffsetKind, Options, Origin, Out,
    ReadTxn, Transact, UndoManager, Update,
};
use std::{
    cell::RefCell,
    collections::{BTreeMap, BTreeSet, HashSet},
    rc::Rc,
    sync::Arc,
};

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum DraftAccess {
    Reader,
    Writer,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Proposal {
    pub origin: u64,
    pub operation_id: String,
    pub update: Vec<u8>,
    pub targets: Vec<String>,
}
#[derive(Clone, Debug, PartialEq, Eq)]
enum Action {
    Edit(Command),
    Undo,
    Redo,
}
#[derive(Debug, PartialEq, Eq)]
pub enum ClientError {
    Forbidden,
    InvalidOrigin,
    InvalidState,
    InvalidCommand,
    InvalidOperation,
    OperationConflict,
    RetiredOperation,
    NoChange,
    RecoveryRequired,
    Limit,
}
#[derive(Default)]
struct Capture {
    active: bool,
    updates: Vec<Vec<u8>>,
    targets: BTreeSet<String>,
}
pub struct DraftReplica {
    document: Document,
    undo: UndoManager,
    capture: Rc<RefCell<Capture>>,
    blocked: bool,
    origin: u64,
    access: DraftAccess,
    used_origins: BTreeSet<u64>,
    proposals: BTreeMap<String, (Action, Proposal)>,
    capture_order: Vec<String>,
}
/// Own unsaved evidence; never interpreted as a host checkpoint/receipt.
pub struct RecoveryDraft {
    pub native: serde_json::Value,
    pub proposals: Vec<Proposal>,
}

impl DraftReplica {
    pub fn restore(
        checkpoint: &[u8],
        origin: u64,
        access: DraftAccess,
    ) -> Result<Self, ClientError> {
        if origin == 0 || origin >= (1 << 53) {
            return Err(ClientError::InvalidOrigin);
        }
        let checked = Document::restore(checkpoint).map_err(|_| ClientError::InvalidState)?;
        if checked
            .doc
            .transact()
            .state_vector()
            .get(&ClientID::new(origin))
            != 0
        {
            return Err(ClientError::InvalidOrigin);
        }
        let original = checked
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
            .apply_update(Update::decode_v1(&original).map_err(|_| ClientError::InvalidState)?)
            .map_err(|_| ClientError::InvalidState)?;
        let document = Document {
            doc,
            read_only: checked.read_only,
            project_text: checked.project_text,
            model: checked.model,
        };
        let ids = document
            .block_ids()
            .map_err(|_| ClientError::InvalidState)?;
        let capture = Rc::new(RefCell::new(Capture::default()));
        let received = capture.clone();
        document
            .doc
            .observe_update_v1("human-capture", move |_, event| {
                let mut capture = received.borrow_mut();
                if capture.active {
                    capture.updates.push(event.update.clone());
                }
            })
            .map_err(|_| ClientError::InvalidState)?;
        let texts = document.doc.get_or_insert_map("wordcraft.texts");
        if !document.is_read_only() {
            let txn = document.doc.transact();
            for id in ids {
                let Some(Out::YText(text)) = texts.get(&txn, &id) else {
                    return Err(ClientError::InvalidState);
                };
                let received = capture.clone();
                text.observe("human-capture", move |_, _| {
                    let mut capture = received.borrow_mut();
                    if capture.active {
                        capture.targets.insert(id.clone());
                    }
                });
            }
        }
        let mut undo = UndoManager::with_options(mooring_text::yrs::undo::Options {
            capture_timeout_millis: 0,
            tracked_origins: HashSet::from([Origin::from(ClientID::new(origin))]),
            capture_transaction: None,
            timestamp: Arc::new(|| 0_u64),
            init_undo_stack: vec![],
            init_redo_stack: vec![],
        });
        undo.expand_scope(&document.doc, &texts);
        Ok(Self {
            document,
            undo,
            capture,
            blocked: false,
            origin,
            access,
            used_origins: BTreeSet::from([origin]),
            proposals: BTreeMap::new(),
            capture_order: Vec::new(),
        })
    }
    pub fn native(&self) -> Result<serde_json::Value, AdapterError> {
        self.document.native()
    }
    pub fn stable_ids(&self) -> Result<Vec<String>, AdapterError> {
        self.document.block_ids()
    }
    pub fn optimistic_checkpoint(&self) -> Result<Vec<u8>, AdapterError> {
        self.document.checkpoint()
    }
    pub fn edit(&mut self, operation_id: &str, command: Command) -> Result<Proposal, ClientError> {
        self.perform(operation_id, Action::Edit(command))
    }
    pub fn undo(&mut self, operation_id: &str) -> Result<Proposal, ClientError> {
        self.perform(operation_id, Action::Undo)
    }
    pub fn redo(&mut self, operation_id: &str) -> Result<Proposal, ClientError> {
        self.perform(operation_id, Action::Redo)
    }
    fn perform(&mut self, operation_id: &str, action: Action) -> Result<Proposal, ClientError> {
        if self.blocked {
            return Err(ClientError::RecoveryRequired);
        }
        if self.access != DraftAccess::Writer || self.document.is_read_only() {
            return Err(ClientError::Forbidden);
        }
        if operation_id.is_empty()
            || operation_id.len() > 128
            || !operation_id
                .bytes()
                .all(|b| b.is_ascii_alphanumeric() || b == b'_' || b == b'-')
        {
            return Err(ClientError::InvalidOperation);
        }
        if let Some((prior_action, proposal)) = self.proposals.get(operation_id) {
            if proposal.origin != self.origin {
                return Err(ClientError::RetiredOperation);
            }
            return if prior_action == &action {
                Ok(proposal.clone())
            } else {
                Err(ClientError::OperationConflict)
            };
        }
        if self.proposals.len() >= 256 {
            return Err(ClientError::Limit);
        }
        // Experimental evidence only: it intentionally does not claim that
        // unrelated optimistic operations are required semantic dependencies.
        let local_stamp = self
            .document
            .doc
            .transact()
            .state_vector()
            .iter()
            .try_fold(1_u64, |sum, (_, clock)| sum.checked_add(*clock as u64))
            .filter(|stamp| *stamp <= crate::format::MAX_STAMP)
            .ok_or(ClientError::Limit)?;
        *self.capture.borrow_mut() = Capture {
            active: true,
            ..Capture::default()
        };
        let changed = match &action {
            Action::Edit(command) => crate::candidate::apply_draft(
                &mut self.document,
                command,
                Origin::from(ClientID::new(self.origin)),
                (local_stamp, self.origin),
            )
            .map(|_| true)
            .map_err(|_| ClientError::InvalidCommand),
            Action::Undo => Ok(self.undo.undo_blocking()),
            Action::Redo => Ok(self.undo.redo_blocking()),
        };
        let mut captured = self.capture.take();
        captured.active = false;
        if let Err(error) = changed {
            self.blocked = true;
            return Err(error);
        }
        if captured.updates.is_empty() && captured.targets.is_empty() {
            return Err(ClientError::NoChange);
        }
        if captured.updates.len() != 1 || captured.targets.is_empty() {
            self.blocked = true;
            return Err(ClientError::RecoveryRequired);
        }
        let update = captured.updates.pop().unwrap();
        if update.len() > 262_144 || crate::checkpoint_wire::preflight(&update).is_err() {
            // Preserve the own draft but freeze further allocation until fresh
            // host-origin recovery. Never silently continue after an unsealed edit.
            self.blocked = true;
            return Err(ClientError::RecoveryRequired);
        }
        let proposal = Proposal {
            operation_id: operation_id.into(),
            origin: self.origin,
            update,
            targets: captured.targets.into_iter().collect(),
        };
        self.capture_order.push(operation_id.to_owned());
        self.proposals
            .insert(operation_id.to_owned(), (action, proposal.clone()));
        Ok(proposal)
    }
    /// The caller has authenticated scope/revision/permission and original host
    /// bytes. This merges peer items into the SAME Doc, retaining selective undo.
    /// An acknowledgement of already optimistic own bytes is a separate receipt,
    /// not a reason to rerun this method or reexecute the command.
    pub fn apply_verified_host_delta(
        &mut self,
        origin: u64,
        update: &[u8],
        targets: &[String],
    ) -> Result<(), ClientError> {
        if origin == 0 || origin >= (1 << 53) || update.len() > 262_144 {
            return Err(ClientError::InvalidState);
        }
        crate::checkpoint_wire::preflight(update).map_err(|_| ClientError::InvalidState)?;
        let checkpoint = self
            .document
            .checkpoint()
            .map_err(|_| ClientError::InvalidState)?;
        let fork = Document::restore(&checkpoint).map_err(|_| ClientError::InvalidState)?;
        let ids = fork.block_ids().map_err(|_| ClientError::InvalidState)?;
        if targets.is_empty() || targets.iter().any(|target| !ids.contains(target)) {
            return Err(ClientError::InvalidState);
        }
        fork.doc
            .transact_mut()
            .apply_update(Update::decode_v1(update).map_err(|_| ClientError::InvalidState)?)
            .map_err(|_| ClientError::InvalidState)?;
        if fork.doc.transact().has_missing_updates() {
            return Err(ClientError::InvalidState);
        }
        // Validate ALL roots and native consistency on a disposable fork before
        // touching the live replica and its local selective undo history.
        let fork_checkpoint = fork.checkpoint().map_err(|_| ClientError::InvalidState)?;
        Document::restore(&fork_checkpoint).map_err(|_| ClientError::InvalidState)?;
        let mut before_native = self
            .document
            .native()
            .map_err(|_| ClientError::InvalidState)?;
        let mut after_native = fork.native().map_err(|_| ClientError::InvalidState)?;
        if fork.block_ids().map_err(|_| ClientError::InvalidState)? != ids {
            return Err(ClientError::InvalidState);
        }
        for (index, target) in ids.iter().enumerate() {
            let changed = before_native["body"][index]["text"]
                != after_native["body"][index]["text"]
                || before_native["body"][index]["runs"] != after_native["body"][index]["runs"];
            if changed && !targets.contains(target) {
                return Err(ClientError::InvalidState);
            }
            for key in ["text", "runs"] {
                before_native["body"][index]
                    .as_object_mut()
                    .ok_or(ClientError::InvalidState)?
                    .remove(key);
                after_native["body"][index]
                    .as_object_mut()
                    .ok_or(ClientError::InvalidState)?
                    .remove(key);
            }
        }
        if before_native != after_native {
            return Err(ClientError::InvalidState);
        }
        self.document
            .doc
            .transact_mut_with("verified-host")
            .apply_update(Update::decode_v1(update).map_err(|_| ClientError::InvalidState)?)
            .map_err(|_| ClientError::InvalidState)?;
        Ok(())
    }
    /// Origin uniqueness/retirement across sessions is the host's responsibility.
    /// This instance additionally refuses every origin it has ever used.
    pub fn recover(
        &mut self,
        checkpoint: &[u8],
        fresh_origin: u64,
        access: DraftAccess,
    ) -> Result<RecoveryDraft, ClientError> {
        if self.used_origins.contains(&fresh_origin) {
            return Err(ClientError::InvalidOrigin);
        }
        if self.used_origins.len() >= 1024 {
            return Err(ClientError::Limit);
        }
        let mut replacement = Self::restore(checkpoint, fresh_origin, access)?;
        let draft = RecoveryDraft {
            native: self.native().map_err(|_| ClientError::InvalidState)?,
            proposals: self
                .capture_order
                .iter()
                .map(|id| {
                    self.proposals
                        .get(id)
                        .map(|(_, proposal)| proposal.clone())
                        .ok_or(ClientError::InvalidState)
                })
                .collect::<Result<Vec<_>, _>>()?,
        };
        replacement.used_origins = self.used_origins.clone();
        replacement.used_origins.insert(fresh_origin);
        replacement.proposals = std::mem::take(&mut self.proposals);
        replacement.capture_order = std::mem::take(&mut self.capture_order);
        *self = replacement;
        Ok(draft)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::command::Anchor;
    use serde_json::{json, Value};
    fn baseline() -> Vec<u8> {
        let native =
            serde_json::from_str(include_str!("../tests/fixtures/d1-native.json")).unwrap();
        Document::import(native, (0..8).map(|index| format!("p{index}")).collect())
            .unwrap()
            .checkpoint()
            .unwrap()
    }
    fn insert(text: &str) -> Command {
        Command::Insert {
            target_id: "p0".into(),
            at: Anchor::Start {},
            text: text.into(),
            properties: Some(BTreeMap::from([
                ("bold".into(), false),
                ("italic".into(), true),
            ])),
        }
    }
    fn peer_only(base: &Value) -> Value {
        let mut expected = base.clone();
        expected["body"][0]["text"] =
            json!(format!("PEER{}", base["body"][0]["text"].as_str().unwrap()));
        expected["body"][0]["runs"]
            .as_array_mut()
            .unwrap()
            .insert(0, json!({"len":4,"props":{"bold":false,"italic":true}}));
        expected
    }
    #[test]
    fn original_delta_retry_is_byte_identical_and_never_reallocates_authored_ids() {
        let bytes = baseline();
        let mut own = DraftReplica::restore(&bytes, 17, DraftAccess::Writer).unwrap();
        let first = own.edit("first", insert("A")).unwrap();
        let snapshot = own.optimistic_checkpoint().unwrap();
        assert_eq!(own.edit("first", insert("A")).unwrap(), first);
        assert_eq!(own.optimistic_checkpoint().unwrap(), snapshot);
        assert_eq!(
            own.edit("first", insert("different")),
            Err(ClientError::OperationConflict)
        );
        let mut peer = DraftReplica::restore(&bytes, 18, DraftAccess::Writer).unwrap();
        peer.apply_verified_host_delta(first.origin, &first.update, &first.targets)
            .unwrap();
        assert_eq!(peer.native().unwrap(), own.native().unwrap());
        assert_eq!(peer.stable_ids().unwrap(), own.stable_ids().unwrap());
        assert_eq!(
            peer.document.doc.transact().state_vector(),
            own.document.doc.transact().state_vector()
        );
    }
    #[test]
    fn full_native_peer_only_undo_redo_and_retry_survive_original_delta_delivery() {
        let bytes = baseline();
        let base = Document::restore(&bytes).unwrap().native().unwrap();
        let mut own = DraftReplica::restore(&bytes, 17, DraftAccess::Writer).unwrap();
        own.edit("own", insert("OWN")).unwrap();
        let mut peer = DraftReplica::restore(
            &own.optimistic_checkpoint().unwrap(),
            18,
            DraftAccess::Writer,
        )
        .unwrap();
        let p = peer.edit("peer", insert("PEER")).unwrap();
        own.apply_verified_host_delta(p.origin, &p.update, &p.targets)
            .unwrap();
        let combined = own.native().unwrap();
        let undo = own.undo("undo-own").unwrap();
        assert_eq!(own.native().unwrap(), peer_only(&base));
        assert_eq!(own.undo("undo-own").unwrap(), undo);
        peer.apply_verified_host_delta(undo.origin, &undo.update, &undo.targets)
            .unwrap();
        assert_eq!(peer.native().unwrap(), peer_only(&base));
        let undo_peer = peer.undo("undo-peer").unwrap();
        own.apply_verified_host_delta(undo_peer.origin, &undo_peer.update, &undo_peer.targets)
            .unwrap();
        assert_eq!(own.native().unwrap(), base);
        assert_eq!(peer.native().unwrap(), base);
        let redo_peer = peer.redo("redo-peer").unwrap();
        own.apply_verified_host_delta(redo_peer.origin, &redo_peer.update, &redo_peer.targets)
            .unwrap();
        assert_eq!(own.native().unwrap(), peer_only(&base));
        let redo = own.redo("redo-own").unwrap();
        peer.apply_verified_host_delta(redo.origin, &redo.update, &redo.targets)
            .unwrap();
        assert_eq!(own.native().unwrap(), combined);
        assert_eq!(peer.native().unwrap(), combined);
        let restored = Document::restore(&own.optimistic_checkpoint().unwrap()).unwrap();
        assert_eq!(restored.native().unwrap(), combined);
    }
    #[test]
    fn fresh_recovery_preserves_draft_and_never_aliases_rejected_a_or_dependent_b() {
        let bytes = baseline();
        let mut own = DraftReplica::restore(&bytes, 17, DraftAccess::Writer).unwrap();
        let a = own.edit("a", insert("A")).unwrap();
        let b = own.edit("b", insert("B")).unwrap();
        let draft = own.native().unwrap();
        assert!(matches!(
            own.recover(&bytes, 17, DraftAccess::Writer),
            Err(ClientError::InvalidOrigin)
        ));
        let recovered = own.recover(&bytes, 18, DraftAccess::Writer).unwrap();
        assert_eq!(recovered.native, draft);
        assert_eq!(recovered.proposals, vec![a, b.clone()]);
        assert_eq!(
            own.edit("a", insert("A")),
            Err(ClientError::RetiredOperation)
        );
        let c = own.edit("c", insert("A")).unwrap();
        assert_eq!(c.origin, 18);
        let mut peer = DraftReplica::restore(&bytes, 19, DraftAccess::Writer).unwrap();
        peer.apply_verified_host_delta(c.origin, &c.update, &c.targets)
            .unwrap();
        let before = peer.optimistic_checkpoint().unwrap();
        assert_eq!(
            peer.apply_verified_host_delta(b.origin, &b.update, &b.targets),
            Err(ClientError::InvalidState)
        );
        assert_eq!(peer.optimistic_checkpoint().unwrap(), before);
        assert!(matches!(
            DraftReplica::restore(&before, 18, DraftAccess::Writer),
            Err(ClientError::InvalidOrigin)
        ));
    }
    #[test]
    fn readers_and_readonly_documents_refuse_edit_before_invalid_command_processing() {
        let bytes = baseline();
        let mut reader = DraftReplica::restore(&bytes, 17, DraftAccess::Reader).unwrap();
        assert_eq!(reader.edit("!", insert("A")), Err(ClientError::Forbidden));
        assert_eq!(reader.optimistic_checkpoint().unwrap(), bytes);
        let doc = Document::restore(&bytes).unwrap();
        let mut native = doc.native().unwrap();
        native["comments"] = json!({"unknown":{}});
        let readonly = Document::import(native, doc.block_ids().unwrap())
            .unwrap()
            .checkpoint()
            .unwrap();
        let mut own = DraftReplica::restore(&readonly, 18, DraftAccess::Writer).unwrap();
        assert_eq!(own.edit("a", insert("A")), Err(ClientError::Forbidden));
    }
    #[test]
    fn independent_original_deltas_converge_in_both_orders_without_reexecution() {
        let bytes = baseline();
        let mut a = DraftReplica::restore(&bytes, 17, DraftAccess::Writer).unwrap();
        let mut b = DraftReplica::restore(&bytes, 18, DraftAccess::Writer).unwrap();
        let pa = a.edit("a", insert("A")).unwrap();
        let pb = b.edit("b", insert("B")).unwrap();
        a.apply_verified_host_delta(pb.origin, &pb.update, &pb.targets)
            .unwrap();
        b.apply_verified_host_delta(pa.origin, &pa.update, &pa.targets)
            .unwrap();
        assert_eq!(a.native().unwrap(), b.native().unwrap());
        assert_eq!(
            a.document.doc.transact().state_vector(),
            b.document.doc.transact().state_vector()
        );
    }
    #[test]
    fn recovery_retains_original_capture_order_independent_of_operation_id_sorting() {
        let bytes = baseline();
        let mut own = DraftReplica::restore(&bytes, 17, DraftAccess::Writer).unwrap();
        let first = own.edit("z-first", insert("A")).unwrap();
        let second = own.edit("a-second", insert("B")).unwrap();
        let third = own.undo("m-third").unwrap();
        let draft = own.recover(&bytes, 18, DraftAccess::Writer).unwrap();
        assert_eq!(draft.proposals, vec![first, second, third]);
    }
}
