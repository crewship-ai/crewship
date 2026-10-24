//! Default-off pure WordCraft WASM model facade. No network, authoritative
//! install, durable receipt or human CRDT update admission is implemented here.
mod experimental_draft;

use mooring_core::adapter::{PrepareAdapter, Prepared, PreparedCandidate};
use mooring_protocol::{
    canonical::fingerprint_atomic, wire::normalize_bounded, PrepareContext, Scope,
};
use mooring_wordcraft::{
    adapter::{Candidate, PrepareError, WordCraftAdapter},
    read, AdapterError, Document,
};
use serde::de::DeserializeOwned;
use serde_json::json;
use wasm_bindgen::prelude::*;

const MAX_JSON: usize = 262144;
const MAX_CHECKPOINT: usize = 1572864;

fn parse<T: DeserializeOwned>(raw: &str) -> Result<T, String> {
    let normalized = normalize_bounded(raw.as_bytes()).map_err(|_| "invalid_request")?;
    serde_json::from_value(normalized.value().clone()).map_err(|_| "invalid_request".into())
}
fn scope(raw: &str) -> Result<Scope, String> {
    let scope: Scope = parse(raw)?;
    if scope.application_id != mooring_wordcraft::profile::APPLICATION_ID {
        return Err("version_mismatch".into());
    }
    Ok(scope)
}
fn state_error(error: AdapterError) -> String {
    match error {
        AdapterError::Limit => "quota_exceeded",
        AdapterError::InvalidIdentity => "invalid_identity",
        AdapterError::InvalidNative => "invalid_native",
        AdapterError::InvalidCheckpoint => "invalid_checkpoint",
    }
    .into()
}
fn bound(raw: &str) -> Result<usize, String> {
    if raw.len() > 20 {
        return Err("invalid_request".into());
    }
    let counter =
        mooring_protocol::Counter::try_from(raw.to_owned()).map_err(|_| "invalid_request")?;
    usize::try_from(counter.0).map_err(|_| "invalid_request".into())
}
fn read_error(error: read::ReadError) -> String {
    match error {
        read::ReadError::InvalidLimit => "invalid_limit",
        read::ReadError::UnknownCursor => "unknown_cursor",
        read::ReadError::InvalidState => "invalid_state",
        read::ReadError::ItemTooLarge => "item_too_large",
    }
    .into()
}
fn encode<T: serde::Serialize + ?Sized>(value: &T) -> Result<String, String> {
    serde_json::to_string(value).map_err(|_| "invalid_state".into())
}

#[wasm_bindgen]
pub struct WordCraftModel {
    document: Document,
}
#[wasm_bindgen]
impl WordCraftModel {
    #[wasm_bindgen(constructor)]
    pub fn new(native_json: &str, ids_json: &str) -> Result<WordCraftModel, String> {
        if native_json.len() > MAX_JSON {
            return Err("quota_exceeded".into());
        }
        // Preflight duplicate/depth/token/byte bounds, but parse ORIGINAL native
        // bytes afterwards: opaque native integers must not be binary64-rounded.
        normalize_bounded(native_json.as_bytes()).map_err(|_| "invalid_request")?;
        let native = serde_json::from_str(native_json).map_err(|_| "invalid_native")?;
        let ids: Vec<String> = parse(ids_json)?;
        Ok(Self {
            document: Document::import(native, ids).map_err(state_error)?,
        })
    }
    /// Caller authenticates host scope/revision/digest before loading storage
    /// bytes. This is not an authoritative server acknowledgement API.
    pub fn restore(checkpoint: &[u8]) -> Result<WordCraftModel, String> {
        if checkpoint.len() > MAX_CHECKPOINT {
            return Err("quota_exceeded".into());
        }
        Ok(Self {
            document: Document::restore(checkpoint).map_err(state_error)?,
        })
    }
    pub fn native_json(&self) -> Result<String, String> {
        encode(&self.document.native().map_err(state_error)?)
    }
    /// Observed CRDT state only; never authority or a durable receipt.
    pub fn state_vector(&self) -> Vec<u8> {
        self.document.state_vector()
    }
    pub fn stable_ids_json(&self) -> Result<String, String> {
        encode(&self.document.block_ids().map_err(state_error)?)
    }
    pub fn is_read_only(&self) -> bool {
        self.document.is_read_only()
    }
    pub fn checkpoint(&self) -> Result<Vec<u8>, String> {
        let bytes = self.document.checkpoint().map_err(state_error)?;
        if bytes.len() > MAX_CHECKPOINT {
            return Err("quota_exceeded".into());
        }
        Ok(bytes)
    }
    pub fn header_json(&self, max_bytes: &str) -> Result<String, String> {
        encode(&read::header(&self.document, bound(max_bytes)?).map_err(read_error)?)
    }
    /// Stable after-ID only; a host must bind remote cursors to the same
    /// immutable authenticated checkpoint, revision and scope.
    pub fn page_json(
        &self,
        after_id: Option<String>,
        max_targets: &str,
        max_bytes: &str,
    ) -> Result<String, String> {
        encode(
            &read::page(
                &self.document,
                after_id.as_deref(),
                bound(max_targets)?,
                bound(max_bytes)?,
            )
            .map_err(read_error)?,
        )
    }
    /// Local agent candidate only. Supplied context is NOT authority; an actual
    /// host must independently bind/qualify its current context before commit.
    /// The original model remains unchanged even when a candidate is returned.
    pub fn preview_agent(
        &self,
        scope_json: &str,
        change_json: &str,
        context_json: &str,
    ) -> Result<UnconfirmedWordCraftCandidate, String> {
        if self.document.is_read_only() {
            return Err("forbidden".into());
        }
        let scope = scope(scope_json)?;
        normalize_bounded(change_json.as_bytes()).map_err(|_| "invalid_request")?;
        let fingerprinted =
            fingerprint_atomic(&scope, change_json.as_bytes()).map_err(|_| "invalid_request")?;
        if !fingerprinted.matches_wire() {
            return Err("op_id_conflict".into());
        }
        let context: PrepareContext = parse(context_json)?;
        match WordCraftAdapter.prepare_atomic(&self.checkpoint()?, fingerprinted.change(), &context)
        {
            Ok(Prepared::Candidate(candidate)) => {
                if candidate.checkpoint_bytes().len() > MAX_CHECKPOINT {
                    return Err("quota_exceeded".into());
                }
                Ok(UnconfirmedWordCraftCandidate { candidate })
            }
            Ok(Prepared::Rejected(failure)) => Err(encode(
                &json!({"kind":"rejected","op_id":failure.operation_id,"code":failure.code,"message":failure.message}),
            )?),
            Err(PrepareError::Context) => Err("invalid_context".into()),
            Err(PrepareError::State(error)) => Err(state_error(error)),
        }
    }
}
#[wasm_bindgen]
pub struct UnconfirmedWordCraftCandidate {
    candidate: Candidate,
}
#[wasm_bindgen]
impl UnconfirmedWordCraftCandidate {
    pub fn checkpoint(&self) -> Vec<u8> {
        self.candidate.checkpoint_bytes().to_vec()
    }
    pub fn native_json(&self) -> Result<String, String> {
        encode(
            &Document::restore(self.candidate.checkpoint_bytes())
                .map_err(state_error)?
                .native()
                .map_err(state_error)?,
        )
    }
    pub fn effects_json(&self) -> Result<String, String> {
        encode(self.candidate.effects())
    }
    pub fn inventory_json(&self) -> Result<String, String> {
        encode(self.candidate.inventory())
    }
    pub fn state(&self) -> String {
        "unconfirmed".into()
    }
}
/// Explicit client construction. Unlike preview, this intentionally computes
/// fingerprints over normalized original membership/read observations. It does
/// not authenticate any participant or repair an already admitted retry.
#[wasm_bindgen]
pub fn bind_agent_change(scope_json: &str, change_json: &str) -> Result<String, String> {
    let scope = scope(scope_json)?;
    normalize_bounded(change_json.as_bytes()).map_err(|_| "invalid_request")?;
    let fingerprinted =
        fingerprint_atomic(&scope, change_json.as_bytes()).map_err(|_| "invalid_request")?;
    encode(&fingerprinted.with_fingerprints())
}
#[wasm_bindgen]
pub fn profile_json() -> String {
    let profile = mooring_wordcraft::profile::qualified_versions();
    json!({"application_id":mooring_wordcraft::profile::APPLICATION_ID,"selected":profile,"checkpoint_format":mooring_wordcraft::profile::CHECKPOINT_FORMAT,"upstream_source":mooring_wordcraft::profile::SOURCE_PIN,"upstream_git_describe":mooring_wordcraft::profile::SOURCE_DESCRIBE,"facade_package_version":env!("CARGO_PKG_VERSION"),"authoritative_install":false,"human_raw_updates":false,"experimental_local_drafts":true,"human_causal_witness":false,"qualification_artifacts":mooring_wordcraft::profile::qualification_artifacts()}).to_string()
}
#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::{json, Value};
    fn input() -> (String, String) {
        let native =
            include_str!("../../mooring-wordcraft/tests/fixtures/d1-engine-canonical.json");
        let parsed: Value = serde_json::from_str(native).unwrap();
        let ids = (0..parsed["body"].as_array().unwrap().len())
            .map(|i| format!("stable-{i}"))
            .collect::<Vec<_>>();
        (native.into(), serde_json::to_string(&ids).unwrap())
    }
    #[test]
    fn full_native_and_stable_ids_survive_facade_checkpoint_and_fresh_restore() {
        let (native, ids) = input();
        let model = WordCraftModel::new(&native, &ids).unwrap();
        assert_eq!(
            serde_json::from_str::<Value>(&model.native_json().unwrap()).unwrap(),
            serde_json::from_str::<Value>(&native).unwrap()
        );
        let checkpoint = model.checkpoint().unwrap();
        drop(model);
        let restored = WordCraftModel::restore(&checkpoint).unwrap();
        assert_eq!(
            serde_json::from_str::<Value>(&restored.stable_ids_json().unwrap()).unwrap(),
            serde_json::from_str::<Value>(&ids).unwrap()
        );
        assert_eq!(
            serde_json::from_str::<Value>(&restored.native_json().unwrap()).unwrap(),
            serde_json::from_str::<Value>(&native).unwrap()
        );
    }
    #[test]
    fn malformed_duplicate_and_oversized_inputs_are_rejected() {
        let (native, ids) = input();
        assert!(WordCraftModel::new(&(native + "garbage"), &ids).is_err());
        assert!(WordCraftModel::new("{\"body\":[],\"body\":[]}", "[]").is_err());
        assert!(WordCraftModel::restore(&vec![0; 1572865]).is_err());
        assert!(WordCraftModel::new(&json!({"body":[]}).to_string(), "[]").is_err());
    }
    fn scope_json() -> String {
        json!({"workspace_id":"qualification-workspace","artifact_id":"qualification-d1","application_id":"wordcraft"}).to_string()
    }
    fn packet() -> (String, String) {
        let (native, ids) = input();
        let ids: Vec<String> = serde_json::from_str(&ids).unwrap();
        let _ = native;
        let change = json!({"change_id":"candidate-change","operations":[{"op_id":"candidate-op","fingerprint":"0".repeat(64),"participant_ref":"candidate-agent","base_revision":"0","dependencies":[],"read_preconditions":[{"target_id":ids[0],"revision":"0","structure_revision":"0"}],"payload":{"kind":"text.insert","target_id":ids[0],"at":{"kind":"end"},"text":" + agent","properties":{"italic":true,"bold":false}}}],"membership_hash":"0".repeat(64)});
        let mut targets = ids;
        targets.extend([
            "wordcraft:document-structure".into(),
            "wordcraft:document-metadata".into(),
        ]);
        let context = json!({"read_policy":"agent_confirmed_current","document_revision":"0","targets":targets.into_iter().map(|id|json!({"target_id":id,"revision":"0","structure_revision":"0","deleted":false})).collect::<Vec<_>>(),"operation_revisions":[{"op_id":"candidate-op","revision":"1"}]});
        (
            bind_agent_change(&scope_json(), &change.to_string()).unwrap(),
            context.to_string(),
        )
    }
    #[test]
    fn typed_candidate_is_unconfirmed_and_leaves_original_model_unchanged() {
        let (native, ids) = input();
        let model = WordCraftModel::new(&native, &ids).unwrap();
        let before = model.checkpoint().unwrap();
        let (change, context) = packet();
        let preview = model
            .preview_agent(&scope_json(), &change, &context)
            .unwrap();
        assert_eq!(preview.state(), "unconfirmed");
        assert_eq!(model.checkpoint().unwrap(), before);
        let mut expected: Value = serde_json::from_str(&native).unwrap();
        expected["body"][0]["text"] =
            json!(expected["body"][0]["text"].as_str().unwrap().to_owned() + " + agent");
        expected["body"][0]["runs"]
            .as_array_mut()
            .unwrap()
            .push(json!({"len":8,"props":{"italic":true,"bold":false}}));
        assert_eq!(
            serde_json::from_str::<Value>(&preview.native_json().unwrap()).unwrap(),
            expected
        );
        assert_eq!(
            WordCraftModel::restore(&preview.checkpoint())
                .unwrap()
                .native_json()
                .unwrap(),
            preview.native_json().unwrap()
        );
        assert_eq!(
            serde_json::from_str::<Value>(&preview.effects_json().unwrap()).unwrap()[0]["op_id"],
            "candidate-op"
        );
    }
    #[test]
    fn immutable_fingerprint_missing_reads_and_context_are_not_silently_repaired() {
        let (native, ids) = input();
        let model = WordCraftModel::new(&native, &ids).unwrap();
        let (change, context) = packet();
        let mut tampered: Value = serde_json::from_str(&change).unwrap();
        tampered["operations"][0]["payload"]["text"] = json!("changed");
        assert!(
            matches!(model.preview_agent(&scope_json(),&tampered.to_string(),&context),Err(error) if error=="op_id_conflict")
        );
        tampered["operations"][0]["read_preconditions"] = json!([]);
        let rebuilt = bind_agent_change(&scope_json(), &tampered.to_string()).unwrap();
        let Err(error) = model.preview_agent(&scope_json(), &rebuilt, &context) else {
            panic!("missing reads accepted")
        };
        assert_eq!(
            serde_json::from_str::<Value>(&error).unwrap()["code"],
            "invalid_request"
        );
        let mut invalid: Value = serde_json::from_str(&context).unwrap();
        invalid["targets"] = json!([]);
        assert!(
            matches!(model.preview_agent(&scope_json(),&change,&invalid.to_string()),Err(error) if error=="invalid_context")
        );
        let mut foreign: Value = serde_json::from_str(&scope_json()).unwrap();
        foreign["application_id"] = json!("other");
        assert!(bind_agent_change(&foreign.to_string(), &change).is_err());
    }
    #[test]
    fn native_opaque_integer_is_preserved_without_binary64_rounding() {
        let (native, ids) = input();
        let mut value: Value = serde_json::from_str(&native).unwrap();
        value["unknown_native_field"] = json!({"integer":9007199254740993u64});
        let model = WordCraftModel::new(&value.to_string(), &ids).unwrap();
        assert!(model.is_read_only());
        assert_eq!(
            serde_json::from_str::<Value>(&model.native_json().unwrap()).unwrap(),
            value
        );
        assert_eq!(
            WordCraftModel::restore(&model.checkpoint().unwrap())
                .unwrap()
                .native_json()
                .unwrap(),
            model.native_json().unwrap()
        );
    }
    #[test]
    fn page_budgets_use_canonical_decimal_without_js_uint32_wrapping() {
        let (native, ids) = input();
        let model = WordCraftModel::new(&native, &ids).unwrap();
        for value in ["01", "-1", "1.5", "4294967297", "18446744073709551616"] {
            assert!(model.page_json(None, value, "262144").is_err());
        }
        assert!(model
            .page_json(Some("foreign".into()), "1", "262144")
            .is_err());
        let page: Value =
            serde_json::from_str(&model.page_json(None, "1", "262144").unwrap()).unwrap();
        assert_eq!(page["paragraphs"].as_array().unwrap().len(), 1);
        assert!(model.header_json("262144").is_ok());
    }
}
