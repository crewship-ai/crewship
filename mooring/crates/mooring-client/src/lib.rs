//! Minimal experimental client used by native/WASM conformance fixtures. This
//! is not a network session or an application adapter and cannot issue "Saved".
use mooring_protocol::{negotiate, Counter, Hello, Versions};
use mooring_text::yrs::updates::decoder::Decode;
use mooring_text::yrs::{GetString, ReadTxn, StateVector, Text, Transact, Update};
use mooring_text::{new_document, recover, UnsavedDraft};
use std::collections::BTreeMap;
use wasm_bindgen::prelude::*;

#[wasm_bindgen]
pub struct TextClient {
    document: mooring_text::yrs::Doc,
    unsaved: Vec<UnsavedDraft>,
    target_ids: BTreeMap<String, String>,
}

#[wasm_bindgen]
impl TextClient {
    #[wasm_bindgen(constructor)]
    pub fn new() -> Self {
        Self {
            document: new_document(),
            unsaved: Vec::new(),
            target_ids: BTreeMap::new(),
        }
    }

    /// Optimistic local insertion, with the origin's stable operation ID kept
    /// in its draft. It is not a durable receipt or automatic retry mechanism.
    pub fn insert(
        &mut self,
        op_id: &str,
        block_id: &str,
        index: u32,
        value: &str,
    ) -> Result<Vec<u8>, String> {
        if !mooring_protocol::wire::valid_identifier(op_id)
            || !mooring_protocol::wire::valid_identifier(block_id)
            || value.len() > 64 * 1024
            || self.unsaved.len() >= 256
        {
            return Err("invalid_request".into());
        }
        if self.unsaved.iter().any(|draft| draft.op_id == op_id) {
            return Err("op_id_conflict".into());
        }
        // Validate offsets before creating a new root or starting a mutation.
        let existing = self.document.transact().get_text(block_id);
        let body = existing
            .map(|text| text.get_string(&self.document.transact()))
            .unwrap_or_default();
        let mut offset = 0;
        let mut boundary = index == 0;
        for character in body.chars() {
            offset += character.len_utf16() as u32;
            boundary |= offset == index;
        }
        if !boundary {
            return Err("invalid_utf16_offset".into());
        }
        let predicted = body
            .len()
            .checked_add(value.len())
            .ok_or("quota_exceeded")?;
        let existing_drafts: usize = self.unsaved.iter().map(|draft| draft.text.len()).sum();
        if predicted > 4 * 1024 * 1024
            || existing_drafts
                .checked_add(predicted)
                .ok_or("quota_exceeded")?
                > 4 * 1024 * 1024
        {
            return Err("quota_exceeded".into());
        }
        let text = self.document.get_or_insert_text(block_id);
        let before = self.document.transact().state_vector();
        text.insert(&mut self.document.transact_mut(), index, value);
        let draft = text.get_string(&self.document.transact());
        self.target_ids.insert(op_id.into(), block_id.into());
        self.unsaved.push(UnsavedDraft {
            op_id: op_id.into(),
            text: draft,
            reason: "unconfirmed".into(),
        });
        Ok(self.document.transact().encode_state_as_update_v1(&before))
    }

    pub fn text(&self, block_id: &str) -> String {
        self.document
            .transact()
            .get_text(block_id)
            .map(|text| text.get_string(&self.document.transact()))
            .unwrap_or_default()
    }

    /// CRDT state for test/transport use, never a claim of persisted state.
    pub fn snapshot(&self) -> Vec<u8> {
        self.document
            .transact()
            .encode_state_as_update_v1(&StateVector::default())
    }

    /// Caller must authenticate the host and check scope/revision before this
    /// entry. A bounded snapshot replaces optimistic state; all drafts remain.
    pub fn recover(&mut self, snapshot: &[u8], reason: &str) -> Result<(), String> {
        if snapshot.len() > 4 * 1024 * 1024 || reason.len() > 1000 {
            return Err("quota_exceeded".into());
        }
        let pending = std::mem::take(&mut self.unsaved);
        match recover(snapshot, pending) {
            Ok(recovery) => {
                self.document = recovery.document;
                self.unsaved = recovery.unsaved;
                for draft in &mut self.unsaved {
                    draft.reason = reason.into();
                }
                Ok(())
            }
            Err(error) => {
                self.unsaved = error.unsaved;
                Err(error.reason)
            }
        }
    }

    pub fn unsaved_json(&self) -> String {
        let values: Vec<_> = self.unsaved.iter().map(|draft| serde_json::json!({"op_id":draft.op_id,"target_id":self.target_ids.get(&draft.op_id),"text":draft.text,"reason":draft.reason})).collect();
        serde_json::to_string(&values).expect("string-only draft serialization")
    }
}
impl Default for TextClient {
    fn default() -> Self {
        Self::new()
    }
}

#[wasm_bindgen]
pub fn negotiate_json(input: &str) -> Result<String, String> {
    let normalized = mooring_protocol::wire::normalize_bounded(input.as_bytes())
        .map_err(|_| "invalid_request")?;
    let hello: Hello =
        serde_json::from_value(normalized.value().clone()).map_err(|_| "invalid_request")?;
    let versions = Versions {
        protocol_version: "0.4.0".into(),
        model_version: "1.0.0".into(),
        engine_version: env!("CARGO_PKG_VERSION").into(),
    };
    let supported = [versions];
    if hello.control_profiles.is_some() {
        return Err("invalid_request".into());
    }
    let selected = negotiate(&hello, &supported)?;
    serde_json::to_string(selected).map_err(|error| error.to_string())
}

/// The same fixture runner executes natively and through the actual WASM ABI.
#[wasm_bindgen]
pub fn conformance_case(input: &str) -> Result<String, String> {
    if input.len() > 64 * 1024 {
        return Err("fixture_too_large".into());
    }
    let case: serde_json::Value = serde_json::from_str(input).map_err(|e| e.to_string())?;
    let value = match case["kind"].as_str().ok_or("fixture kind absent")? {
        "identifier" => {
            let count = case["repeat"].as_u64().ok_or("repeat absent")?;
            let unit = case["unit"].as_str().ok_or("unit absent")?;
            if count > 201 || unit.len() > 4 {
                return Err("fixture_too_large".into());
            }
            let id = unit.repeat(count as usize);
            let mut client = TextClient::new();
            let ok = client.insert(&id, "p1", 0, "text").is_ok();
            if !ok && !client.text("p1").is_empty() {
                return Err("invalid identifier mutated text".into());
            }
            serde_json::json!({"ok":ok})
        }
        "counter" => {
            match serde_json::from_str::<Counter>(case["input"].as_str().ok_or("input absent")?) {
                Ok(counter) => serde_json::json!({"ok":true,"value":String::from(counter)}),
                Err(_) => serde_json::json!({"ok":false}),
            }
        }
        "reader" => {
            let frame: Vec<u8> =
                serde_json::from_value(case["frame"].clone()).map_err(|e| e.to_string())?;
            let result = mooring_text::decode_input(&frame, mooring_text::Access::Reader, 4096);
            serde_json::json!({"ok":result.is_ok()})
        }
        "recovery" => {
            let mut authoritative = TextClient::new();
            authoritative.insert(
                "confirmed",
                "p1",
                0,
                case["confirmed"].as_str().ok_or("confirmed absent")?,
            )?;
            // Another participant's provisional A creates CRDT text IDs. B
            // then edits after those IDs; B alone must remain causally pending.
            let provisional_a = new_document();
            provisional_a
                .get_or_insert_text("rejected-A")
                .push(&mut provisional_a.transact_mut(), "A:");
            let bytes_a = provisional_a
                .transact()
                .encode_state_as_update_v1(&StateVector::default());
            let mut optimistic = TextClient::new();
            optimistic
                .document
                .transact_mut()
                .apply_update(Update::decode_v1(&bytes_a).map_err(|e| e.to_string())?)
                .map_err(|e| e.to_string())?;
            let bytes_b = optimistic.insert(
                "B",
                "rejected-A",
                2,
                case["draft"].as_str().ok_or("draft absent")?,
            )?;
            let isolated_b = new_document();
            isolated_b
                .transact_mut()
                .apply_update(Update::decode_v1(&bytes_b).map_err(|e| e.to_string())?)
                .map_err(|e| e.to_string())?;
            if !isolated_b.transact().has_missing_updates() {
                return Err("fixture B must depend on A CRDT IDs".into());
            }
            optimistic.recover(
                &authoritative.snapshot(),
                "dependency_rejected:A:permission_revoked",
            )?;
            serde_json::json!({"ok":optimistic.text("p1")==authoritative.text("p1") && optimistic.text("rejected-A").is_empty(),"drafts":serde_json::from_str::<serde_json::Value>(&optimistic.unsaved_json()).map_err(|e| e.to_string())?})
        }
        _ => return Err("unknown fixture".into()),
    };
    serde_json::to_string(&value).map_err(|e| e.to_string())
}
