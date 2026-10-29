//! Thin existing protocol-boundary binding for an immutable local pending packet.
//! The caller supplies data from its actual authenticated host session. No JSON
//! issuer, constructor call or checked object authenticates a network transport.
use super::{parse, MAX_CHECKPOINT};
use mooring_protocol::{
    boundary::{validate_commit_result, CommitBinding, Durability, PendingAtomic, Transport},
    canonical::fingerprint_atomic,
    checkpoint::{self, Limits},
    wire::validate_bounded_envelope,
    Checkpoint, CommitResult, Counter, DocumentUpdate, SessionJoined, SubmitChanges,
};
use std::collections::BTreeSet;
use wasm_bindgen::prelude::*;

fn envelope<T: serde::de::DeserializeOwned>(raw: &str) -> Result<T, String> {
    let value = validate_bounded_envelope(raw.as_bytes()).map_err(|_| "invalid_request")?;
    serde_json::from_value(value.value().clone()).map_err(|_| "invalid_request".into())
}
/// Initial data from the parent's actual authenticated session. No call-site
/// or JSON flag grants authority; return bytes only after full tuple/graph checks.
#[wasm_bindgen]
pub fn validate_host_bootstrap(
    joined_json: &str,
    checkpoint_json: &str,
) -> Result<Vec<u8>, String> {
    use mooring_core::checkpoint_description::DescribeCheckpoint;
    let joined: SessionJoined = envelope(joined_json)?;
    let basis: Checkpoint = parse(checkpoint_json)?;
    if joined.versions != mooring_wordcraft::profile::qualified_versions()
        || basis.scope != joined.scope
        || basis.versions != joined.versions
        || basis.generation > joined.generation
        || joined.scope.application_id != mooring_wordcraft::profile::APPLICATION_ID
    {
        return Err("invalid_binding".into());
    }
    let identity = checkpoint::verify(
        &basis,
        Limits {
            decoded_bytes: MAX_CHECKPOINT,
            preimage_bytes: checkpoint::MAX_PREIMAGE_BYTES,
        },
    )
    .map_err(|_| "invalid_checkpoint")?;
    let description = mooring_wordcraft::adapter::WordCraftAdapter
        .describe_checkpoint(identity.state())
        .map_err(|_| "invalid_checkpoint")?;
    if description.format_version != basis.format_version
        || description.versions != basis.versions
        || description.assets != basis.assets
    {
        return Err("invalid_checkpoint".into());
    }
    Ok(identity.state().to_vec())
}
/// Peer content from the ACTUAL authenticated parent channel, never own ACK.
/// We do NOT derive PendingAtomic from this receipt: its original membership is
/// unknown here. The host publisher remains responsible for durable cut/data
/// correlation and current read rights. This checks only the delivered binding.
#[wasm_bindgen]
pub fn validate_host_peer_document_update(
    joined_json: &str,
    expected_from: &str,
    raw: &str,
) -> Result<String, String> {
    let joined: SessionJoined = envelope(joined_json)?;
    Ok(checked_peer_frame(&joined, expected_from, raw)?.data)
}
fn checked_peer_frame(
    joined: &SessionJoined,
    expected_from: &str,
    raw: &str,
) -> Result<DocumentUpdate, String> {
    let frame: DocumentUpdate = envelope(raw)?;
    let from = Counter::try_from(expected_from.to_owned()).map_err(|_| "invalid_cut")?;
    if joined.versions != mooring_wordcraft::profile::qualified_versions()
        || joined.scope.application_id != mooring_wordcraft::profile::APPLICATION_ID
        || frame.scope != joined.scope
        || frame.session_id != joined.session_id
        || frame.generation != joined.generation
        || frame.from_revision != from
        || frame.to_revision <= from
        || frame.receipt.scope != joined.scope
        || frame.receipt.generation != joined.generation
        || frame.receipt.issuer != "crewship"
        || frame.receipt.revision < frame.to_revision
        || frame.encoding != mooring_protocol::DocumentUpdateEncoding::Json
    {
        return Err("invalid_binding".into());
    }
    // Unchanged full batch receipt may name a LATER final head. Content is the
    // NEXT actual Saved atomic cut, never that guessed outer head or an own ACK.
    let mut next: Option<Counter> = None;
    for change in &frame.receipt.changes {
        if change.status == mooring_protocol::ChangeResultStatus::Saved {
            let first = change.first_revision.ok_or("invalid_cut")?;
            let last = change.last_revision.ok_or("invalid_cut")?;
            if first > last || last > frame.receipt.revision {
                return Err("invalid_cut".into());
            }
            if last > from {
                next = Some(next.map_or(last, |prior| prior.min(last)));
            }
        }
    }
    if next != Some(frame.to_revision)
        || frame
            .receipt
            .changes
            .iter()
            .filter(|change| {
                change.status == mooring_protocol::ChangeResultStatus::Saved
                    && change.last_revision == Some(frame.to_revision)
            })
            .count()
            != 1
    {
        return Err("invalid_cut".into());
    }
    mooring_wordcraft::document_update::decode(&frame.data).map_err(|_| "invalid_data")?;
    Ok(frame)
}
#[wasm_bindgen]
pub struct WordCraftPendingCommit {
    joined: SessionJoined,
    basis: Checkpoint,
    request: SubmitChanges,
    original_json: String,
    changes: Vec<PendingAtomic>,
}
#[wasm_bindgen]
impl WordCraftPendingCommit {
    /// Retain ORIGINAL packet bytes before first send. Joined and full basis
    /// must come from the actual host admission/snapshot, not client labels.
    /// This validates data bindings, grants no write permission and emits nothing.
    #[wasm_bindgen(constructor)]
    pub fn new(
        joined_json: &str,
        basis_json: &str,
        original_submit_json: &str,
    ) -> Result<Self, String> {
        let joined: SessionJoined = envelope(joined_json)?;
        let basis: Checkpoint = parse(basis_json)?;
        let request: SubmitChanges = envelope(original_submit_json)?;
        let versions = mooring_wordcraft::profile::qualified_versions();
        if joined.versions != versions
            || basis.versions != versions
            || request.versions != versions
            || basis.scope != joined.scope
            || request.scope != joined.scope
            || request.session_id != joined.session_id
            || basis.scope.application_id != mooring_wordcraft::profile::APPLICATION_ID
            || basis.format_version != mooring_wordcraft::profile::CHECKPOINT_FORMAT
            || basis.generation > joined.generation
            || !basis.assets.is_empty()
        {
            return Err("invalid_binding".into());
        }
        let identity = checkpoint::verify(
            &basis,
            Limits {
                decoded_bytes: MAX_CHECKPOINT,
                preimage_bytes: checkpoint::MAX_PREIMAGE_BYTES,
            },
        )
        .map_err(|_| "invalid_checkpoint")?;
        let document = mooring_wordcraft::Document::restore(identity.state())
            .map_err(|_| "invalid_checkpoint")?;
        if document.is_read_only() {
            return Err("forbidden".into());
        }
        let mut ids = BTreeSet::new();
        let mut change_ids = BTreeSet::new();
        let mut changes = Vec::new();
        for change in &request.changes {
            let raw = serde_json::to_vec(change).map_err(|_| "invalid_request")?;
            if !change_ids.insert(change.change_id.clone())
                || !fingerprint_atomic(&request.scope, &raw)
                    .map_err(|_| "invalid_request")?
                    .matches_wire()
                || change.operations.iter().any(|op| {
                    op.participant_ref != joined.participant_ref
                        || op.base_revision < basis.revision
                        || !ids.insert(op.op_id.clone())
                })
            {
                return Err("invalid_binding".into());
            }
            changes.push(PendingAtomic {
                change_id: change.change_id.clone(),
                op_ids: change
                    .operations
                    .iter()
                    .map(|op| op.op_id.clone())
                    .collect(),
            });
        }
        if changes.is_empty() || ids.is_empty() || ids.len() > 256 {
            return Err("invalid_binding".into());
        }
        Ok(Self {
            joined,
            basis,
            request,
            original_json: original_submit_json.into(),
            changes,
        })
    }
    /// Exact ORIGINAL transport bytes; retries must not reconstruct a new packet.
    pub fn original_request_json(&self) -> String {
        self.original_json.clone()
    }
    /// LOCAL undo-refusal classification, NEVER own ACK or a Saved transition.
    /// Caller must use the actual authenticated host channel. Match only the
    /// selected atomic cut against this retained ORIGINAL membership; the full
    /// unchanged request receipt is still checked by check_host_receipt.
    pub fn own_operation_for_host_peer_cut(
        &self,
        expected_from: &str,
        raw: &str,
    ) -> Result<Option<String>, String> {
        let frame = checked_peer_frame(&self.joined, expected_from, raw)?;
        let selected = frame
            .receipt
            .changes
            .iter()
            .filter(|change| {
                change.status == mooring_protocol::ChangeResultStatus::Saved
                    && change.last_revision == Some(frame.to_revision)
            })
            .collect::<Vec<_>>();
        if selected.len() != 1 {
            return Err("invalid_cut".into());
        }
        let selected = selected[0];
        let Some(original) = self
            .request
            .changes
            .iter()
            .find(|change| change.change_id == selected.change_id)
        else {
            return Ok(None);
        };
        if original.operations.len() != 1 || selected.operations.len() != original.operations.len()
        {
            return Err("invalid_membership".into());
        }
        for (actual, expected) in selected.operations.iter().zip(&original.operations) {
            let mooring_protocol::OperationResult::Saved(actual) = actual else {
                return Err("invalid_membership".into());
            };
            if actual.op_id != expected.op_id
                || actual.issuer != "crewship"
                || actual.revision <= expected.base_revision
                || actual.revision != frame.to_revision
                || selected.first_revision != Some(actual.revision)
                || !matches!(
                    actual.durability,
                    mooring_protocol::OperationResultVariant1Durability::Node
                        | mooring_protocol::OperationResultVariant1Durability::Replicated
                )
            {
                return Err("invalid_membership".into());
            }
        }
        Ok(Some(original.operations[0].op_id.clone()))
    }
    pub fn basis_digest(&self) -> String {
        self.basis.digest.clone()
    }
    /// Only call for bytes received on the actual retained authenticated host
    /// channel. Fixed first-pass issuer is endpoint config, NEVER a receipt claim.
    /// Returns checked terminal DATA; does not install state, consume a Draft,
    /// refresh source authority or manufacture a Saved checkpoint.
    pub fn check_host_receipt(&self, receipt_json: &str) -> Result<String, String> {
        let modes = [Durability::Node, Durability::Replicated];
        let transport = Transport::AuthenticatedHost {
            issuer: "crewship",
            durabilities: &modes,
            required: Durability::Node,
        };
        let checked = validate_commit_result(
            receipt_json.as_bytes(),
            &transport,
            &CommitBinding {
                request_id: &self.request.request_id,
                scope: &self.request.scope,
                generation: self.joined.generation,
                changes: &self.changes,
            },
        )
        .map_err(|_| "invalid_receipt")?;
        if checked.revision() < self.basis.revision {
            return Err("invalid_receipt".into());
        }
        let receipt: CommitResult =
            serde_json::from_value(checked.value().clone()).map_err(|_| "invalid_receipt")?;
        // The shared validator checks atomic order/ranges. Also bind each Saved
        // label to the ORIGINAL immutable base; a replay cannot rewrite that base.
        let value = checked.value();
        for change in value["changes"].as_array().ok_or("invalid_receipt")? {
            for operation in change["operations"].as_array().ok_or("invalid_receipt")? {
                if operation["status"] == "saved" {
                    let op_id = operation["op_id"].as_str().ok_or("invalid_receipt")?;
                    let original = self
                        .request
                        .changes
                        .iter()
                        .flat_map(|change| &change.operations)
                        .find(|op| op.op_id == op_id)
                        .ok_or("invalid_receipt")?;
                    let revision: Counter = serde_json::from_value(operation["revision"].clone())
                        .map_err(|_| "invalid_receipt")?;
                    if revision <= original.base_revision {
                        return Err("invalid_receipt".into());
                    }
                }
            }
        }
        // Typed decode above is an additional shape guard, not a new receipt.
        if receipt.revision != checked.revision() {
            return Err("invalid_receipt".into());
        }
        String::from_utf8(checked.bytes().to_vec()).map_err(|_| "invalid_receipt".into())
    }
    /// OWN-request DocumentUpdate only. Peer frames are not acknowledgements of
    /// our pending membership. Current read permission/host provenance remain
    /// the actual transport controller's responsibility before content delivery.
    pub fn check_host_document_update(&self, frame_json: &str) -> Result<String, String> {
        let frame: DocumentUpdate = envelope(frame_json)?;
        if frame.scope != self.joined.scope
            || frame.session_id != self.joined.session_id
            || frame.generation != self.joined.generation
            || frame.from_revision > frame.to_revision
            || frame.from_revision < self.basis.revision
            || frame.to_revision != frame.receipt.revision
        {
            return Err("invalid_binding".into());
        }
        let raw = serde_json::to_string(&frame.receipt).map_err(|_| "invalid_receipt")?;
        self.check_host_receipt(&raw)?;
        Ok(frame.data)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use mooring_protocol::{
        boundary::encode_base64, canonical::fingerprint_atomic, AtomicChange, CheckpointEncoding,
        Scope,
    };
    use serde_json::{json, Value};
    fn fixture() -> (String, String, String, Value) {
        let scope = Scope {
            workspace_id: "workspace".into(),
            artifact_id: "document".into(),
            application_id: "wordcraft".into(),
        };
        let versions = mooring_wordcraft::profile::qualified_versions();
        let native: Value = serde_json::from_str(include_str!(
            "../../mooring-wordcraft/tests/fixtures/d1-engine-canonical.json"
        ))
        .unwrap();
        let ids = (0..native["body"].as_array().unwrap().len())
            .map(|i| format!("p{i}"))
            .collect();
        let state = mooring_wordcraft::Document::import(native, ids)
            .unwrap()
            .checkpoint()
            .unwrap();
        let mut basis = Checkpoint {
            scope: scope.clone(),
            revision: Counter(10),
            generation: Counter(2),
            format_version: mooring_wordcraft::profile::CHECKPOINT_FORMAT.into(),
            versions: versions.clone(),
            encoding: CheckpointEncoding::Base64,
            state: encode_base64(&state, MAX_CHECKPOINT).unwrap(),
            digest: format!("sha256:{}", "0".repeat(64)),
            assets: vec![],
        };
        basis.digest = checkpoint::compute(
            &basis,
            Limits {
                decoded_bytes: MAX_CHECKPOINT,
                preimage_bytes: checkpoint::MAX_PREIMAGE_BYTES,
            },
        )
        .unwrap()
        .full_digest()
        .into();
        let joined = json!({"type":"session_joined","request_id":"join","scope":scope,"session_id":"session","participant_ref":"participant","generation":"2","permission_version":"3","versions":versions,"gate_id":"gate"});
        let change: AtomicChange = serde_json::from_value(json!({"change_id":"atomic","membership_hash":format!("sha256:{}","0".repeat(64)),"operations":[{"op_id":"op-a","fingerprint":format!("sha256:{}","0".repeat(64)),"participant_ref":"participant","base_revision":"10","dependencies":[],"read_preconditions":[],"payload":{"kind":"text.insert","target_id":"p0","at":{"kind":"end"},"text":"a","properties":null}}]})).unwrap();
        let bound = fingerprint_atomic(&scope, &serde_json::to_vec(&change).unwrap())
            .unwrap()
            .with_fingerprints();
        let request = json!({"type":"submit_changes","request_id":"submit","scope":scope,"session_id":"session","versions":versions,"changes":[bound]});
        let receipt = json!({"type":"commit_result","request_id":"submit","scope":scope,"generation":"2","revision":"11","issuer":"crewship","durability":"node","changes":[{"change_id":"atomic","status":"saved","first_revision":"11","last_revision":"11","operations":[{"op_id":"op-a","status":"saved","revision":"11","issuer":"crewship","durability":"node"}]}]});
        (
            joined.to_string(),
            serde_json::to_string(&basis).unwrap(),
            format!("  {} \n", request),
            receipt,
        )
    }
    #[test]
    fn peer_data_uses_ordered_atomic_cuts_without_claiming_own_membership_or_outer_head() {
        let (joined, checkpoint, original, mut receipt) = fixture();
        let state = validate_host_bootstrap(&joined, &checkpoint).unwrap();
        let mut draft =
            crate::experimental_draft::WordCraftLocalDraft::new(&state, "17", "writer").unwrap();
        let proposal=draft.edit("peer-a",&json!({"kind":"text.insert","target_id":"p0","at":{"kind":"start"},"text":"peer","properties":null}).to_string()).unwrap();
        let data =
            mooring_wordcraft::document_update::encode(&proposal.update(), &["p0".into()]).unwrap();
        let first = receipt["changes"][0].clone();
        let mut second = first.clone();
        second["change_id"] = json!("atomic-b");
        second["first_revision"] = json!("12");
        second["last_revision"] = json!("12");
        second["operations"][0]["op_id"] = json!("peer-b");
        second["operations"][0]["revision"] = json!("12");
        receipt["revision"] = json!("12");
        receipt["changes"] = json!([first, second]);
        let j: serde_json::Value = serde_json::from_str(&joined).unwrap();
        let frame = json!({"type":"document_update","request_id":"delivery","scope":j["scope"],"session_id":j["session_id"],"generation":j["generation"],"from_revision":"10","to_revision":"11","encoding":"json","data":data,"receipt":receipt});
        assert_eq!(
            validate_host_peer_document_update(&joined, "10", &frame.to_string()).unwrap(),
            data
        );
        let mut next = frame.clone();
        next["from_revision"] = json!("11");
        next["to_revision"] = json!("12");
        assert!(validate_host_peer_document_update(&joined, "11", &next.to_string()).is_ok());
        for mode in [
            "skip-peer",
            "from",
            "scope",
            "session",
            "generation",
            "receipt-scope",
            "receipt-generation",
            "issuer",
            "model",
            "encoding",
            "data",
            "missing-cut",
            "unknown",
            "duplicate",
        ] {
            let mut f = frame.clone();
            let mut own_join = j.clone();
            match mode {
                "skip-peer" => f["to_revision"] = json!("12"),
                "from" => f["from_revision"] = json!("9"),
                "scope" => f["scope"]["artifact_id"] = json!("other"),
                "session" => f["session_id"] = json!("other"),
                "generation" => f["generation"] = json!("3"),
                "receipt-scope" => f["receipt"]["scope"]["artifact_id"] = json!("other"),
                "receipt-generation" => f["receipt"]["generation"] = json!("3"),
                "issuer" => f["receipt"]["issuer"] = json!("runtime"),
                "model" => own_join["versions"]["model_version"] = json!("0.1.0"),
                "encoding" => f["encoding"] = json!("base64"),
                "data" => f["data"] = json!("{}"),
                "missing-cut" => f["receipt"]["changes"] = json!([]),
                "unknown" => f["authenticated"] = json!(true),
                _ => {}
            }
            let raw = if mode == "duplicate" {
                f.to_string()
                    .replacen("{", "{\"type\":\"document_update\",", 1)
            } else {
                f.to_string()
            };
            assert!(
                validate_host_peer_document_update(&own_join.to_string(), "10", &raw).is_err(),
                "{mode}"
            );
        }
        // OWN helper deliberately remains unchanged until actual runtime wire
        // qualification supplies the final paired per-atomic correlation proof.
        let own = WordCraftPendingCommit::new(&joined, &checkpoint, &original).unwrap();
        assert!(own.check_host_document_update(&frame.to_string()).is_err());
        assert_eq!(
            own.own_operation_for_host_peer_cut("10", &frame.to_string())
                .unwrap(),
            Some("op-a".into())
        );
        assert_eq!(
            own.own_operation_for_host_peer_cut("11", &next.to_string())
                .unwrap(),
            None
        );
        // Classification never changes the full original-request ACK contract.
        assert!(own.check_host_receipt(&receipt.to_string()).is_err());
        for mode in [
            "op",
            "duplicate-op",
            "durability",
            "op-issuer",
            "op-revision",
            "duplicate-cut",
            "session",
        ] {
            let mut bad = frame.clone();
            match mode {
                "op" => bad["receipt"]["changes"][0]["operations"][0]["op_id"] = json!("foreign"),
                "duplicate-op" => {
                    let op = bad["receipt"]["changes"][0]["operations"][0].clone();
                    bad["receipt"]["changes"][0]["operations"]
                        .as_array_mut()
                        .unwrap()
                        .push(op);
                }
                "durability" => {
                    bad["receipt"]["changes"][0]["operations"][0]["durability"] = json!("memory")
                }
                "op-issuer" => {
                    bad["receipt"]["changes"][0]["operations"][0]["issuer"] = json!("runtime")
                }
                "op-revision" => {
                    bad["receipt"]["changes"][0]["operations"][0]["revision"] = json!("10")
                }
                "duplicate-cut" => {
                    let cut = bad["receipt"]["changes"][0].clone();
                    bad["receipt"]["changes"].as_array_mut().unwrap().push(cut);
                }
                "session" => bad["session_id"] = json!("other"),
                _ => unreachable!(),
            }
            assert!(
                own.own_operation_for_host_peer_cut("10", &bad.to_string())
                    .is_err(),
                "{mode}"
            );
            assert_eq!(own.original_request_json(), original);
        }
    }
    #[test]
    fn host_bootstrap_verifies_outer_identity_before_returning_original_state() {
        let (joined, checkpoint, _, _) = fixture();
        let state = validate_host_bootstrap(&joined, &checkpoint).unwrap();
        let original: Checkpoint = serde_json::from_str(&checkpoint).unwrap();
        assert_eq!(
            state,
            checkpoint::verify(
                &original,
                Limits {
                    decoded_bytes: MAX_CHECKPOINT,
                    preimage_bytes: checkpoint::MAX_PREIMAGE_BYTES
                }
            )
            .unwrap()
            .state()
        );
        for mode in [
            "scope",
            "generation",
            "versions",
            "digest",
            "format",
            "unknown",
            "duplicate",
        ] {
            let mut j: serde_json::Value = serde_json::from_str(&joined).unwrap();
            let mut cp: serde_json::Value = serde_json::from_str(&checkpoint).unwrap();
            match mode {
                "scope" => j["scope"]["artifact_id"] = json!("other"),
                "generation" => j["generation"] = json!("1"),
                "versions" => j["versions"]["model_version"] = json!("0.1.0"),
                "digest" => cp["digest"] = json!(format!("sha256:{}", "0".repeat(64))),
                "format" => cp["format_version"] = json!("unknown"),
                "unknown" => j["authority"] = json!(true),
                _ => {}
            }
            let raw = if mode == "duplicate" {
                joined.replacen("{", "{\"type\":\"session_joined\",", 1)
            } else {
                j.to_string()
            };
            assert!(
                validate_host_bootstrap(&raw, &cp.to_string()).is_err(),
                "{mode}"
            );
        }
    }
    #[test]
    fn existing_receipt_boundary_rejects_swaps_unknown_and_duplicate_members_without_mutating_original(
    ) {
        let (joined, basis, request, receipt) = fixture();
        let pending = WordCraftPendingCommit::new(&joined, &basis, &request).unwrap();
        assert_eq!(pending.original_request_json(), request);
        let checked = pending.check_host_receipt(&receipt.to_string()).unwrap();
        assert_eq!(
            pending.check_host_receipt(&receipt.to_string()).unwrap(),
            checked,
            "exact duplicate receipt is idempotent DATA, not a second state install"
        );
        for mode in [
            "member",
            "revision",
            "duplicate",
            "unknown",
            "request",
            "issuer",
            "generation",
            "scope",
        ] {
            let mut bad = receipt.clone();
            match mode {
                "member" => bad["changes"][0]["operations"][0]["op_id"] = json!("other"),
                "revision" => bad["changes"][0]["operations"][0]["revision"] = json!("10"),
                "duplicate" => {
                    let op = bad["changes"][0]["operations"][0].clone();
                    bad["changes"][0]["operations"]
                        .as_array_mut()
                        .unwrap()
                        .push(op);
                }
                "unknown" => bad["changes"][0]["operations"][0]["status"] = json!("unknown"),
                "request" => bad["request_id"] = json!("other"),
                "issuer" => bad["issuer"] = json!("runtime"),
                "generation" => bad["generation"] = json!("3"),
                "scope" => bad["scope"]["artifact_id"] = json!("other"),
                _ => unreachable!(),
            }
            assert!(
                pending.check_host_receipt(&bad.to_string()).is_err(),
                "accepted {mode}"
            );
            assert_eq!(pending.original_request_json(), request);
        }
        let mut duplicate: Value = serde_json::from_str(&request).unwrap();
        let same_change = duplicate["changes"][0].clone();
        duplicate["changes"]
            .as_array_mut()
            .unwrap()
            .push(same_change);
        assert!(WordCraftPendingCommit::new(&joined, &basis, &duplicate.to_string()).is_err());
        let mut changed: Value = serde_json::from_str(&request).unwrap();
        changed["changes"][0]["operations"][0]["payload"]["text"] = json!("rewritten");
        assert!(WordCraftPendingCommit::new(&joined, &basis, &changed.to_string()).is_err());
    }
    #[test]
    fn own_document_update_binds_session_and_revision_cut_without_inventing_saved_checkpoint() {
        let (joined, basis, request, receipt) = fixture();
        let pending = WordCraftPendingCommit::new(&joined, &basis, &request).unwrap();
        let frame = json!({"type":"document_update","request_id":"delivery","scope":receipt["scope"],"session_id":"session","generation":"2","from_revision":"10","to_revision":"11","encoding":"json","data":"opaque-app-data","receipt":receipt});
        assert_eq!(
            pending
                .check_host_document_update(&frame.to_string())
                .unwrap(),
            "opaque-app-data"
        );
        for mode in ["session", "cut", "from", "generation"] {
            let mut bad = frame.clone();
            match mode {
                "session" => bad["session_id"] = json!("other"),
                "cut" => bad["to_revision"] = json!("12"),
                "from" => bad["from_revision"] = json!("12"),
                "generation" => bad["generation"] = json!("3"),
                _ => unreachable!(),
            };
            assert!(pending
                .check_host_document_update(&bad.to_string())
                .is_err());
        }
    }
}
