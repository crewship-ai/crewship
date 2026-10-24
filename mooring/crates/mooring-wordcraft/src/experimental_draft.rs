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
    /// Exact observed frontier BEFORE this original edit/undo/redo.
    /// Captured once and retained unchanged for retries and recovery evidence.
    pub authored_frontier: Vec<u8>,
    pub targets: Vec<String>,
}
/// Local outer-command observation only. Not an authentication or host read proof.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct NativeSelection {
    origin: u64,
    frontier: Vec<u8>,
    before: [u8; 32],
    target: String,
    start: usize,
    end: usize,
}
#[derive(Clone, Debug, PartialEq, Eq)]
enum Action {
    Edit(Command),
    Native([u8; 32]),
    NativeSelected([u8; 32], NativeSelection),
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
        self.perform(operation_id, Action::Edit(command), None)
    }
    /// Inactive local native connector, not server admission.
    pub fn native_transition(
        &mut self,
        operation_id: &str,
        before: &serde_json::Value,
        after: &serde_json::Value,
    ) -> Result<Proposal, ClientError> {
        self.require_native_access()?;
        let identity =
            crate::native_transition::identity(before, after).map_err(|_| ClientError::Limit)?;
        self.perform(
            operation_id,
            Action::Native(identity),
            Some((before, after)),
        )
    }
    /// Capture from the actual outer native Session before the command. Offsets
    /// are UTF-8 byte boundaries in this exact local model, not wire positions.
    pub fn observe_native_selection(
        &self,
        before: &serde_json::Value,
        target: &str,
        start: usize,
        end: usize,
    ) -> Result<NativeSelection, ClientError> {
        self.require_native_access()?;
        if self
            .document
            .native()
            .map_err(|_| ClientError::InvalidState)?
            != *before
        {
            return Err(ClientError::InvalidCommand);
        }
        let ids = self
            .document
            .block_ids()
            .map_err(|_| ClientError::InvalidState)?;
        let index = ids
            .iter()
            .position(|id| id == target)
            .ok_or(ClientError::InvalidCommand)?;
        let text = before["body"][index]["text"]
            .as_str()
            .ok_or(ClientError::InvalidCommand)?;
        if end < start
            || crate::byte_to_utf16(text, start).is_err()
            || crate::byte_to_utf16(text, end).is_err()
        {
            return Err(ClientError::InvalidCommand);
        }
        Ok(NativeSelection {
            origin: self.origin,
            frontier: self.document.state_vector(),
            before: crate::native_transition::identity(before, before)
                .map_err(|_| ClientError::Limit)?,
            target: target.into(),
            start,
            end,
        })
    }
    fn selected_plan(
        &self,
        before: &serde_json::Value,
        after: &serde_json::Value,
        selection: &NativeSelection,
    ) -> Result<crate::native_transition::Plan, ClientError> {
        if selection.origin != self.origin
            || selection.frontier != self.document.state_vector()
            || selection.before
                != crate::native_transition::identity(before, before)
                    .map_err(|_| ClientError::Limit)?
        {
            return Err(ClientError::InvalidCommand);
        }
        crate::native_transition::Plan::prepare_selected(
            &self.document,
            before,
            after,
            &selection.target,
            selection.start,
            selection.end,
        )
        .map_err(|_| ClientError::InvalidCommand)
    }
    /// Pure native post-gate, with the actual pre-command selection binding.
    pub fn validate_native_transition_selected(
        &self,
        before: &serde_json::Value,
        after: &serde_json::Value,
        selection: &NativeSelection,
    ) -> Result<(), ClientError> {
        self.require_native_access()?;
        self.selected_plan(before, after, selection).map(|_| ())
    }
    pub fn native_transition_selected(
        &mut self,
        operation_id: &str,
        before: &serde_json::Value,
        after: &serde_json::Value,
        selection: &NativeSelection,
    ) -> Result<Proposal, ClientError> {
        self.require_native_access()?;
        let identity =
            crate::native_transition::identity(before, after).map_err(|_| ClientError::Limit)?;
        self.perform(
            operation_id,
            Action::NativeSelected(identity, selection.clone()),
            Some((before, after)),
        )
    }
    /// Pure outer-native post-gate: emits nothing and changes no client state.
    pub fn validate_native_transition(
        &self,
        before: &serde_json::Value,
        after: &serde_json::Value,
    ) -> Result<(), ClientError> {
        self.require_native_access()?;
        crate::native_transition::Plan::prepare(&self.document, before, after)
            .map(|_| ())
            .map_err(|_| ClientError::InvalidCommand)
    }
    fn require_native_access(&self) -> Result<(), ClientError> {
        if self.blocked {
            return Err(ClientError::RecoveryRequired);
        }
        if self.access != DraftAccess::Writer || self.document.is_read_only() {
            return Err(ClientError::Forbidden);
        }
        Ok(())
    }
    pub fn undo(&mut self, operation_id: &str) -> Result<Proposal, ClientError> {
        self.perform(operation_id, Action::Undo, None)
    }
    pub fn redo(&mut self, operation_id: &str) -> Result<Proposal, ClientError> {
        self.perform(operation_id, Action::Redo, None)
    }
    fn perform(
        &mut self,
        operation_id: &str,
        action: Action,
        native: Option<(&serde_json::Value, &serde_json::Value)>,
    ) -> Result<Proposal, ClientError> {
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
        // All native refusal/full-model preflight happens BEFORE capture/live
        // mutation. Exact retries above never rebuild a changed native basis.
        let native_plan = if let Action::NativeSelected(_, selection) = &action {
            let (before, after) = native.ok_or(ClientError::InvalidCommand)?;
            Some(self.selected_plan(before, after, selection)?)
        } else if matches!(&action, Action::Native(_)) {
            let (before, after) = native.ok_or(ClientError::InvalidCommand)?;
            Some(
                crate::native_transition::Plan::prepare(&self.document, before, after).map_err(
                    |error| {
                        if error == crate::native_transition::TransitionError::NoChange {
                            ClientError::NoChange
                        } else {
                            ClientError::InvalidCommand
                        }
                    },
                )?,
            )
        } else {
            None
        };
        // Capture ONCE before any own operation allocates IDs or changes the
        // frontier. Retry/recovery retain this exact observation with bytes.
        let authored_frontier = self.document.state_vector();
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
            Action::Native(_) | Action::NativeSelected(_, _) => native_plan
                .as_ref()
                .unwrap()
                .apply(
                    &mut self.document,
                    Origin::from(ClientID::new(self.origin)),
                    (local_stamp, self.origin),
                )
                .map(|_| true)
                .map_err(|_| ClientError::RecoveryRequired),
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
            authored_frontier,
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
    /// Observed optimistic state only; never a confirmed receipt or authority.
    pub fn state_vector(&self) -> Vec<u8> {
        self.document.state_vector()
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
    #[test]
    fn original_frontier_is_captured_before_edit_and_retained_after_retry_recovery() {
        let base = baseline();
        let mut local = DraftReplica::restore(&base, 17, DraftAccess::Writer).unwrap();
        let before = local.state_vector();
        let first = local.edit("z-first", insert("X")).unwrap();
        assert_eq!(first.authored_frontier, before);
        assert_ne!(first.authored_frontier, local.state_vector());
        let next = local.state_vector();
        let second = local.edit("a-second", insert("Y")).unwrap();
        assert_eq!(second.authored_frontier, next);
        let retry = local.edit("z-first", insert("X")).unwrap();
        assert_eq!(retry, first);
        let undo_before = local.state_vector();
        let undo = local.undo("undo").unwrap();
        assert_eq!(undo.authored_frontier, undo_before);
        let recovery = local.recover(&base, 18, DraftAccess::Writer).unwrap();
        assert_eq!(recovery.proposals, vec![first, second, undo]);
        let fresh = local.edit("fresh", insert("C")).unwrap();
        assert_eq!(fresh.authored_frontier, before);
    }
    #[test]
    fn recorded_actual_client_basis_prepares_without_regenerating_observations() {
        use crate::experimental_human_current::{prepare_current, Authority, HostWriter};
        let base = baseline();
        let mut local = DraftReplica::restore(&base, 17, DraftAccess::Writer).unwrap();
        let proposal = local.edit("actual", insert("Č😀")).unwrap();
        let candidate = prepare_current(
            Authority::Writer(HostWriter::current(&base, 17).unwrap()),
            &proposal.authored_frontier,
            &proposal.update,
            &proposal.targets,
        )
        .unwrap();
        assert_eq!(candidate.native().unwrap(), local.native().unwrap());
        assert_eq!(proposal, local.edit("actual", insert("Č😀")).unwrap());
    }
}

#[cfg(test)]
mod native_transition_tests {
    use super::*;
    use serde_json::{json, Value};
    fn base() -> (Value, Vec<u8>) {
        let mut native: Value =
            serde_json::from_str(include_str!("../tests/fixtures/d1-native.json")).unwrap();
        native["body"] = json!([{"kind":"para","text":"Č😀e\u{301}","runs":[{"len":9}]}]);
        native["core"]["opaqueConnectorInteger"] = json!(9007199254740993_u64);
        let doc = Document::import(native.clone(), vec!["p_one".into()]).unwrap();
        (native, doc.checkpoint().unwrap())
    }
    #[test]
    fn native_connector_one_original_proposal_prepares_and_selectively_undoes() {
        let (before, checkpoint) = base();
        let mut client = DraftReplica::restore(&checkpoint, 17, DraftAccess::Writer).unwrap();
        let frontier = client.state_vector();
        let mut after = before.clone();
        after["body"][0]["text"] = json!("Č😀Xe\u{301}");
        after["body"][0]["runs"] =
            json!([{"len":6},{"len":1,"props":{"bold":false,"italicCs":true}},{"len":3}]);
        let proposal = client
            .native_transition("native_1", &before, &after)
            .unwrap();
        assert_eq!(proposal.targets, vec!["p_one"]);
        assert_eq!(proposal.authored_frontier, frontier);
        assert_eq!(client.native().unwrap(), after);
        assert_eq!(
            client
                .native_transition("native_1", &before, &after)
                .unwrap(),
            proposal
        );
        let authority = crate::experimental_human_current::Authority::Writer(
            crate::experimental_human_current::HostWriter::current(&checkpoint, 17).unwrap(),
        );
        let candidate = crate::experimental_human_current::prepare_current(
            authority,
            &proposal.authored_frontier,
            &proposal.update,
            &proposal.targets,
        )
        .unwrap();
        assert_eq!(
            Document::restore(&candidate.checkpoint().unwrap())
                .unwrap()
                .native()
                .unwrap(),
            after
        );
        client.undo("undo_1").unwrap();
        assert_eq!(client.native().unwrap(), before);
        client.redo("redo_1").unwrap();
        assert_eq!(client.native().unwrap(), after);
        assert_eq!(
            client
                .native_transition("native_1", &before, &after)
                .unwrap(),
            proposal
        );
    }
    #[test]
    fn native_connector_reader_and_structural_refusal_emit_nothing() {
        let (before, checkpoint) = base();
        let mut reader = DraftReplica::restore(&checkpoint, 17, DraftAccess::Reader).unwrap();
        assert_eq!(
            reader.native_transition("reader", &Value::Null, &Value::Null),
            Err(ClientError::Forbidden)
        );
        let mut client = DraftReplica::restore(&checkpoint, 18, DraftAccess::Writer).unwrap();
        let frontier = client.state_vector();
        let mut after = before.clone();
        after["body"][0]["props"] = json!({"keepNext":true});
        assert_eq!(
            client.native_transition("bad_structure", &before, &after),
            Err(ClientError::InvalidCommand)
        );
        assert_eq!(client.native().unwrap(), before);
        assert_eq!(client.state_vector(), frontier);
        assert!(client.proposals.is_empty());
        assert!(!client.blocked);
    }
}

#[cfg(test)]
mod native_selection_tests {
    use super::*;
    use serde_json::{json, Value};
    fn fixture() -> (Value, Vec<u8>) {
        let mut native: Value =
            serde_json::from_str(include_str!("../tests/fixtures/d1-native.json")).unwrap();
        native["body"] = json!([{"kind":"para","text":"aa","runs":[{"len":2}]}]);
        let bytes = Document::import(native.clone(), vec!["p_one".into()])
            .unwrap()
            .checkpoint()
            .unwrap();
        (native, bytes)
    }
    fn with_text(native: &Value, text: &str) -> Value {
        let mut value = native.clone();
        value["body"][0]["text"] = json!(text);
        value["body"][0]["runs"] = json!([{"len":text.len()}]);
        value
    }
    #[test]
    fn native_selection_preserves_actual_repeated_glyph_item_positions_under_peer_delivery() {
        let (before, checkpoint) = fixture();
        let mut a = DraftReplica::restore(&checkpoint, 17, DraftAccess::Writer).unwrap();
        let mut b = DraftReplica::restore(&checkpoint, 18, DraftAccess::Writer).unwrap();
        let selection_a = a.observe_native_selection(&before, "p_one", 0, 0).unwrap();
        let selection_b = b.observe_native_selection(&before, "p_one", 1, 1).unwrap();
        let after_a = with_text(&before, "aaa");
        let after_b = with_text(&before, "aXa");
        a.validate_native_transition_selected(&before, &after_a, &selection_a)
            .unwrap();
        let pa = a
            .native_transition_selected("a_native", &before, &after_a, &selection_a)
            .unwrap();
        let pb = b
            .native_transition_selected("b_native", &before, &after_b, &selection_b)
            .unwrap();
        a.apply_verified_host_delta(18, &pb.update, &pb.targets)
            .unwrap();
        b.apply_verified_host_delta(17, &pa.update, &pa.targets)
            .unwrap();
        let expected = with_text(&before, "aaXa");
        assert_eq!(a.native().unwrap(), expected);
        assert_eq!(b.native().unwrap(), expected);
        a.undo("undo_a").unwrap();
        assert_eq!(a.native().unwrap(), after_b);
        a.redo("redo_a").unwrap();
        assert_eq!(a.native().unwrap(), expected);
        assert_eq!(
            a.native_transition_selected("a_native", &before, &after_a, &selection_a)
                .unwrap(),
            pa
        );
    }
    #[test]
    fn native_selection_is_bound_to_origin_frontier_and_immutable_retry_position() {
        let (before, checkpoint) = fixture();
        let mut client = DraftReplica::restore(&checkpoint, 17, DraftAccess::Writer).unwrap();
        let at_start = client
            .observe_native_selection(&before, "p_one", 0, 0)
            .unwrap();
        let at_end = client
            .observe_native_selection(&before, "p_one", 2, 2)
            .unwrap();
        let after = with_text(&before, "aaa");
        let original = client
            .native_transition_selected("same_op", &before, &after, &at_start)
            .unwrap();
        assert_eq!(
            client.native_transition_selected("same_op", &before, &after, &at_end),
            Err(ClientError::OperationConflict)
        );
        assert_eq!(
            client.native_transition_selected("stale", &before, &after, &at_start),
            Err(ClientError::InvalidCommand)
        );
        client.undo("undo").unwrap();
        assert_eq!(client.native().unwrap(), before);
        // Visible equality after undo does not refresh an old authored frontier.
        assert_eq!(
            client.native_transition_selected("still_stale", &before, &after, &at_start),
            Err(ClientError::InvalidCommand)
        );
        assert_eq!(
            client
                .native_transition_selected("same_op", &before, &after, &at_start)
                .unwrap(),
            original
        );
        let mut peer = DraftReplica::restore(&checkpoint, 18, DraftAccess::Writer).unwrap();
        assert_eq!(
            peer.native_transition_selected("wrong_origin", &before, &after, &at_start),
            Err(ClientError::InvalidCommand)
        );
    }
    #[test]
    fn native_selection_rejects_off_span_effects_bad_utf8_and_reader_before_capture() {
        let (before, checkpoint) = fixture();
        let client = DraftReplica::restore(&checkpoint, 17, DraftAccess::Writer).unwrap();
        let selection = client
            .observe_native_selection(&before, "p_one", 0, 0)
            .unwrap();
        assert_eq!(
            client.validate_native_transition_selected(
                &before,
                &with_text(&before, "aXa"),
                &selection
            ),
            Err(ClientError::InvalidCommand)
        );
        let reader = DraftReplica::restore(&checkpoint, 18, DraftAccess::Reader).unwrap();
        assert_eq!(
            reader.observe_native_selection(&Value::Null, "bad", usize::MAX, 0),
            Err(ClientError::Forbidden)
        );
        let mut unicode = before.clone();
        unicode["body"][0]["text"] = json!("Č😀");
        unicode["body"][0]["runs"] = json!([{"len":6}]);
        let bytes = Document::import(unicode.clone(), vec!["p_one".into()])
            .unwrap()
            .checkpoint()
            .unwrap();
        let client = DraftReplica::restore(&bytes, 19, DraftAccess::Writer).unwrap();
        for (start, end) in [(1, 1), (3, 3), (2, 7), (6, 2)] {
            assert_eq!(
                client.observe_native_selection(&unicode, "p_one", start, end),
                Err(ClientError::InvalidCommand)
            );
        }
        let selected = client
            .observe_native_selection(&unicode, "p_one", 2, 6)
            .unwrap();
        let mut format = unicode.clone();
        format["body"][0]["runs"] = json!([{"len":2,"props":{"bold":true}},{"len":4}]);
        assert_eq!(
            client.validate_native_transition_selected(&unicode, &format, &selected),
            Err(ClientError::InvalidCommand)
        );
    }
    #[test]
    fn native_selection_deletes_actual_repeated_item_and_preserves_peer_origin_undo() {
        let (before, checkpoint) = fixture();
        let mut a = DraftReplica::restore(&checkpoint, 17, DraftAccess::Writer).unwrap();
        let mut b = DraftReplica::restore(&checkpoint, 18, DraftAccess::Writer).unwrap();
        let selection_a = a.observe_native_selection(&before, "p_one", 0, 1).unwrap();
        let selection_b = b.observe_native_selection(&before, "p_one", 1, 1).unwrap();
        let own_only = with_text(&before, "a");
        let peer_only = with_text(&before, "aXa");
        let pa = a
            .native_transition_selected("delete_first_a", &before, &own_only, &selection_a)
            .unwrap();
        let pb = b
            .native_transition_selected("insert_peer", &before, &peer_only, &selection_b)
            .unwrap();
        a.apply_verified_host_delta(18, &pb.update, &pb.targets)
            .unwrap();
        b.apply_verified_host_delta(17, &pa.update, &pa.targets)
            .unwrap();
        assert_eq!(a.native().unwrap(), with_text(&before, "Xa"));
        assert_eq!(a.native().unwrap(), b.native().unwrap());
        a.undo("undo_delete").unwrap();
        assert_eq!(a.native().unwrap(), peer_only);
        a.redo("redo_delete").unwrap();
        assert_eq!(a.native().unwrap(), b.native().unwrap());
        b.undo("undo_peer").unwrap();
        assert_eq!(b.native().unwrap(), own_only);
    }
}
