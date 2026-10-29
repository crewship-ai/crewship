//! Experimental local draft facade. No host human admission or clock witness.
use super::{encode, parse, state_error, MAX_CHECKPOINT};
use mooring_wordcraft::{
    command::Command,
    experimental_draft::{
        ClientError, DraftAccess, DraftReplica, NativeSelection, Proposal, RecoveryDraft,
    },
};
use wasm_bindgen::prelude::*;

fn error(error: ClientError) -> String {
    match error {
        ClientError::Forbidden => "forbidden",
        ClientError::InvalidOrigin => "invalid_origin",
        ClientError::InvalidState => "invalid_state",
        ClientError::InvalidCommand => "invalid_command",
        ClientError::UnsupportedUndo => "unsupported_delete_undo",
        ClientError::InvalidOperation => "invalid_operation",
        ClientError::OperationConflict => "op_id_conflict",
        ClientError::RetiredOperation => "retired_operation",
        ClientError::NoChange => "no_change",
        ClientError::RecoveryRequired => "recovery_required",
        ClientError::Limit => "quota_exceeded",
    }
    .into()
}
fn native(raw: &str) -> Result<serde_json::Value, String> {
    if raw.len() > super::MAX_JSON {
        return Err("quota_exceeded".into());
    }
    // Validate duplicate/depth/token/byte bounds, then parse ORIGINAL bytes.
    // Normalization is not the native model: opaque integers remain lossless.
    mooring_protocol::wire::normalize_bounded(raw.as_bytes()).map_err(|_| "invalid_request")?;
    serde_json::from_str(raw).map_err(|_| "invalid_native".into())
}
fn origin(raw: &str) -> Result<u64, String> {
    if raw.len() > 16 {
        return Err("invalid_origin".into());
    }
    let value = mooring_protocol::Counter::try_from(raw.to_owned())
        .map_err(|_| "invalid_origin")?
        .0;
    if value == 0 || value >= (1 << 53) {
        return Err("invalid_origin".into());
    }
    Ok(value)
}
fn access(raw: &str) -> Result<DraftAccess, String> {
    match raw {
        "reader" => Ok(DraftAccess::Reader),
        "writer" => Ok(DraftAccess::Writer),
        _ => Err("invalid_request".into()),
    }
}

/// This local role is a UX gate, not host authority. The host independently
/// authenticates and binds origins before admitting ANY update-bearing frame.
#[wasm_bindgen]
pub struct WordCraftLocalDraft {
    client: DraftReplica,
}
#[wasm_bindgen]
impl WordCraftLocalDraft {
    pub fn native_json(&self) -> Result<String, String> {
        encode(&self.client.native().map_err(state_error)?)
    }
    /// Actual optimistic observed state, not a confirmed host receipt.
    pub fn state_vector(&self) -> Vec<u8> {
        self.client.state_vector()
    }
    pub fn stable_ids_json(&self) -> Result<String, String> {
        encode(&self.client.stable_ids().map_err(state_error)?)
    }
    pub fn optimistic_checkpoint(&self) -> Result<Vec<u8>, String> {
        let bytes = self.client.optimistic_checkpoint().map_err(state_error)?;
        if bytes.len() > MAX_CHECKPOINT {
            return Err("quota_exceeded".into());
        }
        Ok(bytes)
    }
    /// Local command execution generates original CRDT bytes ONCE; the server
    /// must validate/apply those bytes, never reexecute this command grammar.
    pub fn edit(
        &mut self,
        operation_id: &str,
        command_json: &str,
    ) -> Result<WordCraftLocalProposal, String> {
        self.client.require_native_access().map_err(error)?;
        let command: Command = parse(command_json)?;
        Ok(WordCraftLocalProposal {
            proposal: self.client.edit(operation_id, command).map_err(error)?,
        })
    }
    /// Capture only from actual outer native Session state before mutation.
    /// This opaque local object is neither host authority nor a JSON proof.
    pub fn observe_native_selection(
        &self,
        before_json: &str,
        target: &str,
        start: &str,
        end: &str,
    ) -> Result<WordCraftNativeSelection, String> {
        self.client.require_native_access().map_err(error)?;
        let before = native(before_json)?;
        Ok(WordCraftNativeSelection {
            selection: self
                .client
                .observe_native_selection(&before, target, super::bound(start)?, super::bound(end)?)
                .map_err(error)?,
        })
    }
    /// Pure post gate. Refusal may roll back native state only before emission.
    pub fn validate_native_transition_selected(
        &self,
        before_json: &str,
        after_json: &str,
        selection: &WordCraftNativeSelection,
    ) -> Result<(), String> {
        self.client.require_native_access().map_err(error)?;
        self.client
            .validate_native_transition_selected(
                &native(before_json)?,
                &native(after_json)?,
                &selection.selection,
            )
            .map_err(error)
    }
    /// One local original delta, never a server command or Saved acknowledgement.
    pub fn native_transition_selected(
        &mut self,
        operation_id: &str,
        before_json: &str,
        after_json: &str,
        selection: &WordCraftNativeSelection,
    ) -> Result<WordCraftLocalProposal, String> {
        self.client.require_native_access().map_err(error)?;
        Ok(WordCraftLocalProposal {
            proposal: self
                .client
                .native_transition_selected(
                    operation_id,
                    &native(before_json)?,
                    &native(after_json)?,
                    &selection.selection,
                )
                .map_err(error)?,
        })
    }
    /// Data from a verified host DocumentUpdate frame; this method itself
    /// authenticates no transport and changes no Saved/receipt state.
    pub fn apply_verified_document_update(&mut self, data: &str) -> Result<(), String> {
        self.client
            .apply_verified_document_update(data)
            .map_err(error)
    }
    /// Local self-echo classification only AFTER retained original membership
    /// was checked against the actual authenticated host's selected atomic cut.
    /// This is not an acknowledgement, receipt issuer or permission grant.
    pub fn apply_verified_own_document_update(
        &mut self,
        data: &str,
        operation_id: &str,
    ) -> Result<(), String> {
        self.client
            .apply_verified_own_document_update(data, operation_id)
            .map_err(error)
    }
    pub fn undo(&mut self, operation_id: &str) -> Result<WordCraftLocalProposal, String> {
        Ok(WordCraftLocalProposal {
            proposal: self.client.undo_e3(operation_id).map_err(error)?,
        })
    }
    pub fn redo(&mut self, operation_id: &str) -> Result<WordCraftLocalProposal, String> {
        Ok(WordCraftLocalProposal {
            proposal: self.client.redo_e3(operation_id).map_err(error)?,
        })
    }
}
/// Opaque local observation. No JS/JSON constructor can mint a host proof.
#[wasm_bindgen]
pub struct WordCraftNativeSelection {
    selection: NativeSelection,
}
#[wasm_bindgen]
pub struct WordCraftLocalProposal {
    proposal: Proposal,
}
#[wasm_bindgen]
impl WordCraftLocalProposal {
    /// Synchronous immutable intent capture. The supplied checkpoint is NOT a
    /// Saved cut or authority; host author-view replay must verify this digest.
    pub fn human_payload_json(&self, authored_checkpoint: &[u8]) -> Result<String, String> {
        use mooring_protocol::{boundary::encode_base64, control::encode_checkpoint};
        if authored_checkpoint.len() > MAX_CHECKPOINT {
            return Err("quota_exceeded".into());
        }
        let digest = encode_checkpoint(authored_checkpoint)
            .map_err(|_| "quota_exceeded")?
            .digest;
        let payload = serde_json::json!({"kind":"humancrdt.update","operation_id":self.proposal.operation_id,
            "origin":self.proposal.origin.to_string(),"update":encode_base64(&self.proposal.update,262144).map_err(|_|"quota_exceeded")?,
            "authored_frontier":encode_base64(&self.proposal.authored_frontier,262144).map_err(|_|"quota_exceeded")?,
            "targets":self.proposal.targets,"authored_checkpoint_state_digest":digest});
        let raw = encode(&payload)?;
        if raw.len() > 262144 {
            return Err("quota_exceeded".into());
        }
        Ok(raw)
    }
    pub fn operation_id(&self) -> String {
        self.proposal.operation_id.clone()
    }
    pub fn state(&self) -> String {
        "unconfirmed".into()
    }
    pub fn origin(&self) -> String {
        self.proposal.origin.to_string()
    }
    /// Immutable observation from BEFORE this original edit/undo/redo.
    pub fn authored_frontier(&self) -> Vec<u8> {
        self.proposal.authored_frontier.clone()
    }
    pub fn update(&self) -> Vec<u8> {
        self.proposal.update.clone()
    }
    pub fn targets_json(&self) -> Result<String, String> {
        encode(&self.proposal.targets)
    }
}
#[wasm_bindgen]
pub struct WordCraftLocalRecoveryDraft {
    draft: RecoveryDraft,
}
#[wasm_bindgen]
impl WordCraftLocalRecoveryDraft {
    pub fn native_json(&self) -> Result<String, String> {
        encode(&self.draft.native)
    }
    pub fn proposal_count(&self) -> usize {
        self.draft.proposals.len()
    }
    /// Original capture order and bytes; never reexecute these as new intents.
    pub fn proposal(&self, index: &str) -> Result<WordCraftLocalProposal, String> {
        let index = super::bound(index)?;
        let proposal = self
            .draft
            .proposals
            .get(index)
            .cloned()
            .ok_or("invalid_request")?;
        Ok(WordCraftLocalProposal { proposal })
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::{json, Value};
    fn fixture() -> (Value, Vec<u8>) {
        let mut v: Value = serde_json::from_str(include_str!(
            "../../mooring-wordcraft/tests/fixtures/d1-engine-canonical.json"
        ))
        .unwrap();
        v["body"] = json!([{"kind":"para","text":"aa","runs":[{"len":2}]}]);
        let cp = mooring_wordcraft::Document::import(v.clone(), vec!["p_one".into()])
            .unwrap()
            .checkpoint()
            .unwrap();
        (v, cp)
    }
    #[test]
    fn own_host_echo_preserves_pending_b_originals_and_peer_format_through_undo() {
        let (_, cp) = fixture();
        let mut own = WordCraftLocalDraft::new(&cp, "17", "writer").unwrap();
        let mut peer = WordCraftLocalDraft::new(&cp, "18", "writer").unwrap();
        let command = |text: &str, bold: bool| {
            json!({"kind":"text.insert","target_id":"p_one","at":{"kind":"start"},"text":text,"properties":{"bold":bold,"italic":true}}).to_string()
        };
        let a = own.edit("A", &command("A", false)).unwrap();
        let cut_a = own.optimistic_checkpoint().unwrap();
        let immutable_payload = a.human_payload_json(&cut_a).unwrap();
        let b = own.edit("B", &command("B😀", false)).unwrap();
        let before_echo = own.optimistic_checkpoint().unwrap();
        let data =
            mooring_wordcraft::document_update::encode(&a.update(), &["p_one".into()]).unwrap();
        own.apply_verified_document_update(&data).unwrap();
        assert_eq!(own.optimistic_checkpoint().unwrap(), before_echo);
        assert_eq!(a.human_payload_json(&cut_a).unwrap(), immutable_payload);
        let payload: Value = serde_json::from_str(&immutable_payload).unwrap();
        assert_eq!(
            payload["authored_checkpoint_state_digest"],
            mooring_protocol::control::encode_checkpoint(&cut_a)
                .unwrap()
                .digest
        );
        assert_eq!(payload["origin"], "17");
        assert_eq!(
            payload["update"],
            mooring_protocol::boundary::encode_base64(&a.update(), 262144).unwrap()
        );
        peer.apply_verified_document_update(&data).unwrap();
        let c = peer.edit("C", &command("PEER", true)).unwrap();
        let data_c =
            mooring_wordcraft::document_update::encode(&c.update(), &["p_one".into()]).unwrap();
        own.apply_verified_document_update(&data_c).unwrap();
        let after_peer = own.native_json().unwrap();
        own.undo("undo-B").unwrap();
        assert_eq!(own.native_json().unwrap(), peer.native_json().unwrap());
        own.redo("redo-B").unwrap();
        assert_eq!(own.native_json().unwrap(), after_peer);
        assert!(!b.update().is_empty());
    }
    #[test]
    fn native_selection_facade_preserves_original_native_bytes_and_retry() {
        let (before, cp) = fixture();
        let raw = before.to_string();
        let mut after = before.clone();
        after["body"][0]["text"] = json!("aaa");
        after["body"][0]["runs"] = json!([{"len":3}]);
        let out = after.to_string();
        let mut c = WordCraftLocalDraft::new(&cp, "17", "writer").unwrap();
        let frontier = c.state_vector();
        let selection = c.observe_native_selection(&raw, "p_one", "0", "0").unwrap();
        c.validate_native_transition_selected(&raw, &out, &selection)
            .unwrap();
        let p = c
            .native_transition_selected("native_1", &raw, &out, &selection)
            .unwrap();
        assert_eq!(p.authored_frontier(), frontier);
        assert_eq!(
            serde_json::from_str::<Value>(&c.native_json().unwrap()).unwrap(),
            after
        );
        assert_eq!(
            c.native_transition_selected("native_1", &raw, &out, &selection)
                .unwrap()
                .update(),
            p.update()
        );
        c.undo("undo").unwrap();
        assert_eq!(
            serde_json::from_str::<Value>(&c.native_json().unwrap()).unwrap(),
            before
        );
        c.redo("redo").unwrap();
        assert_eq!(
            serde_json::from_str::<Value>(&c.native_json().unwrap()).unwrap(),
            after
        );
    }
    #[test]
    fn native_selection_facade_reader_is_denied_before_invalid_json_or_position() {
        let (_, cp) = fixture();
        let reader = WordCraftLocalDraft::new(&cp, "18", "reader").unwrap();
        assert_eq!(
            reader
                .observe_native_selection("{not-json", "bad", "not-a-counter", "-1")
                .err(),
            Some("forbidden".into())
        );
    }
    #[test]
    fn unsupported_core_facade_is_lossless_readonly_and_denied_before_parse() {
        let (mut native, _) = fixture();
        native["core"]["opaqueConnectorInteger"] = json!(9007199254740993_u64);
        let model = crate::WordCraftModel::new(&native.to_string(), "[\"p_one\"]").unwrap();
        assert!(model.is_read_only());
        assert!(model
            .native_json()
            .unwrap()
            .contains("\"opaqueConnectorInteger\":9007199254740993"));
        assert_eq!(
            model.preview_agent("{bad", "{bad", "{bad").err(),
            Some("forbidden".into())
        );
        let cp = model.checkpoint().unwrap();
        let restored = crate::WordCraftModel::restore(&cp).unwrap();
        assert!(restored.is_read_only());
        assert_eq!(
            restored.native_json().unwrap(),
            model.native_json().unwrap()
        );
        let mut client = WordCraftLocalDraft::new(&cp, "20", "writer").unwrap();
        assert_eq!(
            client
                .observe_native_selection("{bad", "bad", "invalid", "-1")
                .err(),
            Some("forbidden".into())
        );
        // Obtain any separate valid opaque observation: read-only checks must
        // precede parsing even when the caller supplies a well-typed handle.
        let (_, valid) = fixture();
        let writer = WordCraftLocalDraft::new(&valid, "21", "writer").unwrap();
        let selection = writer
            .observe_native_selection(&writer.native_json().unwrap(), "p_one", "0", "0")
            .unwrap();
        assert_eq!(
            client.validate_native_transition_selected("{bad", "{bad", &selection),
            Err("forbidden".into())
        );
        assert_eq!(
            client
                .native_transition_selected("invalid", "{bad", "{bad", &selection)
                .err(),
            Some("forbidden".into())
        );
    }
}

#[cfg(test)]
mod active_mode_tests {
    use super::*;
    #[test]
    fn protected_old_checkpoint_denies_facade_before_any_command_parse() {
        let cp = include_bytes!(
            "../../mooring-wordcraft/tests/fixtures/protected-old-v2-checkpoint.json"
        );
        let model = crate::WordCraftModel::restore(cp).unwrap();
        assert!(model.is_read_only());
        assert_eq!(
            model.preview_agent("{bad", "{bad", "{bad").err(),
            Some("forbidden".into())
        );
        let mut client = WordCraftLocalDraft::new(cp, "18", "writer").unwrap();
        assert_eq!(client.edit("!bad", "{bad").err(), Some("forbidden".into()));
        assert_eq!(
            client
                .observe_native_selection("{bad", "bad", "invalid", "-1")
                .err(),
            Some("forbidden".into())
        );
        assert_eq!(client.optimistic_checkpoint().unwrap(), cp);
    }
}

// Rust construction remains typed separately from the JavaScript boundary.
impl WordCraftLocalDraft {
    pub fn new(
        checkpoint: &[u8],
        assigned_origin: &str,
        role: &str,
    ) -> Result<WordCraftLocalDraft, String> {
        if checkpoint.len() > MAX_CHECKPOINT {
            return Err("quota_exceeded".into());
        }
        Ok(Self {
            client: DraftReplica::restore(checkpoint, origin(assigned_origin)?, access(role)?)
                .map_err(error)?,
        })
    }
    /// Only already authenticated host deltas; this method itself does NOT
    /// authenticate a peer, scope, permission, epoch or revision.
    pub fn apply_verified_host_delta(
        &mut self,
        assigned_origin: &str,
        update: &[u8],
        targets_json: &str,
    ) -> Result<(), String> {
        self.client
            .apply_verified_host_delta(
                origin(assigned_origin)?,
                update,
                &parse::<Vec<String>>(targets_json)?,
            )
            .map_err(error)
    }
    pub fn recover(
        &mut self,
        checkpoint: &[u8],
        fresh_origin: &str,
        role: &str,
    ) -> Result<WordCraftLocalRecoveryDraft, String> {
        if checkpoint.len() > MAX_CHECKPOINT {
            return Err("quota_exceeded".into());
        }
        Ok(WordCraftLocalRecoveryDraft {
            draft: self
                .client
                .recover(checkpoint, origin(fresh_origin)?, access(role)?)
                .map_err(error)?,
        })
    }
}

#[cfg(target_arch = "wasm32")]
#[wasm_bindgen]
extern "C" {
    #[wasm_bindgen(typescript_type = "string")]
    type OriginString;
    #[wasm_bindgen(method, getter)]
    fn length(this: &OriginString) -> u32;
}
#[cfg(target_arch = "wasm32")]
fn origin_string(value: JsValue) -> Result<String, String> {
    use wasm_bindgen::JsCast;
    if !value.is_string() || value.unchecked_ref::<OriginString>().length() > 16 {
        return Err("invalid_origin".into());
    }
    value.as_string().ok_or_else(|| "invalid_origin".into())
}
#[cfg(target_arch = "wasm32")]
#[wasm_bindgen]
impl WordCraftLocalDraft {
    #[wasm_bindgen(constructor)]
    pub fn new_js(
        checkpoint: &[u8],
        assigned_origin: JsValue,
        role: &str,
    ) -> Result<WordCraftLocalDraft, String> {
        Self::new(checkpoint, &origin_string(assigned_origin)?, role)
    }
    #[wasm_bindgen(js_name = apply_verified_host_delta)]
    pub fn apply_verified_host_delta_js(
        &mut self,
        assigned_origin: JsValue,
        update: &[u8],
        targets_json: &str,
    ) -> Result<(), String> {
        self.apply_verified_host_delta(&origin_string(assigned_origin)?, update, targets_json)
    }
    #[wasm_bindgen(js_name = recover)]
    pub fn recover_js(
        &mut self,
        checkpoint: &[u8],
        fresh_origin: JsValue,
        role: &str,
    ) -> Result<WordCraftLocalRecoveryDraft, String> {
        self.recover(checkpoint, &origin_string(fresh_origin)?, role)
    }
}
