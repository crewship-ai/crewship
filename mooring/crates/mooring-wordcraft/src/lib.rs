//! Default-off WordCraft native model boundary; no transport or durable receipts.
//! The caller supplies host-authenticated, scoped checkpoints and stable IDs.
//! An export is a candidate, never a durable Saved receipt.
use std::collections::HashSet;

use mooring_text::yrs::types::text::YChange;
use mooring_text::yrs::types::Attrs;
use mooring_text::yrs::{
    Any, Array, Doc, Map, Out, ReadTxn, StateVector, Text, TextPrelim, Transact,
};
use serde::{Deserialize, Serialize};
use serde_json::{json, Map as JsonMap, Value};

const MAX_NATIVE_BYTES: usize = 16 * 1024 * 1024;
const MAX_CHECKPOINT_BYTES: usize = 64 * 1024 * 1024;
const MAX_BLOCKS: usize = 8192;
const PROPS_PRESENT: &str = "wordcraft.props";
const CHARACTER_PREFIX: &str = "wordcraft.chr.";
const CHARACTER_FLAGS: &[&str] = &["bold", "boldCs", "italic", "italicCs"];
const ROOT_FIELDS: &[&str] = &[
    "body",
    "lastSection",
    "parts",
    "styles",
    "numbering",
    "comments",
    "revisions",
    "settings",
    "core",
    "sources",
    "customProps",
];

#[derive(Debug, PartialEq, Eq)]
pub enum AdapterError {
    InvalidNative,
    InvalidIdentity,
    InvalidCheckpoint,
    Limit,
}

#[derive(Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
struct Checkpoint {
    format: String,
    native: Value,
    block_ids: Vec<String>,
    update: Vec<u8>,
}

impl std::fmt::Display for AdapterError {
    fn fmt(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        formatter.write_str(match self {
            Self::InvalidNative => "invalid native WordCraft document",
            Self::InvalidIdentity => "invalid stable block identity",
            Self::InvalidCheckpoint => "inconsistent WordCraft CRDT checkpoint",
            Self::Limit => "WordCraft adapter resource limit",
        })
    }
}
impl std::error::Error for AdapterError {}

pub struct Document {
    doc: Doc,
    read_only: bool,
}

impl Document {
    /// IDs must be assigned once by the host during first import and included
    /// in its confirmed checkpoint. They are not regenerated from body indexes.
    pub fn import(native: Value, ids: Vec<String>) -> Result<Self, AdapterError> {
        let read_only = classify(&native)?;
        let body = native["body"]
            .as_array()
            .ok_or(AdapterError::InvalidNative)?;
        validate_ids(&ids, body.len())?;
        let doc = mooring_text::new_document();
        let metadata = doc.get_or_insert_map("wordcraft.metadata");
        let blocks = doc.get_or_insert_map("wordcraft.blocks");
        let order = doc.get_or_insert_array("wordcraft.order");
        let mut header = native.clone();
        header.as_object_mut().unwrap().remove("body");
        {
            let mut txn = doc.transact_mut();
            metadata.insert(&mut txn, "document", header.to_string());
            order.insert_range(&mut txn, 0, ids.iter().cloned());
            for (id, block) in ids.iter().zip(body) {
                let mut metadata = block.clone();
                if !read_only {
                    // Text lives only in Y.Text, not an obsolete plaintext
                    // copy in paragraph metadata. Original run segmentation
                    // contains formatting/lengths, never a second text copy.
                    metadata.as_object_mut().unwrap().remove("text");
                }
                blocks.insert(&mut txn, id.as_str(), metadata.to_string());
            }
        }
        if !read_only {
            let texts = doc.get_or_insert_map("wordcraft.texts");
            for (id, block) in ids.iter().zip(body) {
                let mut txn = doc.transact_mut();
                // Nested shared types persist their identity even when empty;
                // an empty named root alone is absent from a Yrs state update.
                let text = texts.insert(&mut txn, id.as_str(), TextPrelim::default());
                let source = block["text"].as_str().unwrap();
                let mut byte = 0;
                let mut utf16 = 0;
                for run in block["runs"].as_array().unwrap() {
                    let length = run["len"].as_u64().unwrap() as usize;
                    let end = byte + length;
                    // Independent properties are independent CRDT attributes;
                    // one JSON blob would make concurrent bold/italic conflict.
                    let mut attrs = Attrs::new();
                    if let Some(properties) = run.get("props") {
                        attrs.insert(PROPS_PRESENT.into(), Any::Bool(true));
                        for (key, value) in properties.as_object().unwrap() {
                            attrs.insert(
                                format!("{CHARACTER_PREFIX}{key}").into(),
                                Any::Bool(value.as_bool().unwrap()),
                            );
                        }
                    }
                    text.insert_with_attributes(&mut txn, utf16, &source[byte..end], attrs);
                    utf16 += source[byte..end].encode_utf16().count() as u32;
                    byte = end;
                }
            }
        }
        Ok(Self { doc, read_only })
    }

    pub fn native(&self) -> Result<Value, AdapterError> {
        let txn = self.doc.transact();
        let metadata = txn
            .get_map("wordcraft.metadata")
            .ok_or(AdapterError::InvalidCheckpoint)?;
        let mut native: Value = serde_json::from_str(&string(metadata.get(&txn, "document"))?)
            .map_err(|_| AdapterError::InvalidCheckpoint)?;
        let object = native
            .as_object_mut()
            .ok_or(AdapterError::InvalidCheckpoint)?;
        if object.contains_key("body") {
            return Err(AdapterError::InvalidCheckpoint);
        }
        let mut body = Vec::new();
        for id in self.block_ids()? {
            let blocks = txn
                .get_map("wordcraft.blocks")
                .ok_or(AdapterError::InvalidCheckpoint)?;
            let mut block: Value = serde_json::from_str(&string(blocks.get(&txn, &id))?)
                .map_err(|_| AdapterError::InvalidCheckpoint)?;
            if !self.read_only {
                let texts = txn
                    .get_map("wordcraft.texts")
                    .ok_or(AdapterError::InvalidCheckpoint)?;
                let Some(Out::YText(text)) = texts.get(&txn, &id) else {
                    return Err(AdapterError::InvalidCheckpoint);
                };
                let mut content = String::new();
                let mut runs = Vec::new();
                for chunk in text.diff(&txn, YChange::identity) {
                    let value = string(Some(chunk.insert))?;
                    let attrs = chunk.attributes.unwrap_or_default();
                    let mut properties = JsonMap::new();
                    let present = matches!(attrs.get(PROPS_PRESENT), Some(Any::Bool(true)));
                    for (key, value) in attrs.iter() {
                        if key.as_ref() == PROPS_PRESENT {
                            if !matches!(value, Any::Bool(true)) {
                                return Err(AdapterError::InvalidCheckpoint);
                            }
                            continue;
                        }
                        let flag = key
                            .strip_prefix(CHARACTER_PREFIX)
                            .ok_or(AdapterError::InvalidCheckpoint)?;
                        if !CHARACTER_FLAGS.contains(&flag) || !present {
                            return Err(AdapterError::InvalidCheckpoint);
                        }
                        let Any::Bool(value) = value else {
                            return Err(AdapterError::InvalidCheckpoint);
                        };
                        properties.insert(flag.into(), json!(value));
                    }
                    let mut run = JsonMap::new();
                    if present {
                        run.insert("props".into(), Value::Object(properties));
                    }
                    run.insert("len".into(), json!(value.len()));
                    content.push_str(&value);
                    runs.push(Value::Object(run));
                }
                // Yrs coalesces adjacent identical attributes and has no
                // zero-length text item. Retain native segmentation/empty-run
                // formatting only when its complete semantics still agree.
                if normalize_runs(&runs)?
                    == normalize_runs(
                        block["runs"]
                            .as_array()
                            .ok_or(AdapterError::InvalidCheckpoint)?,
                    )?
                {
                    // Exact original run layout is retained, including empty runs.
                } else {
                    block["runs"] = json!(runs);
                }
                block["text"] = json!(content);
            }
            body.push(block);
        }
        object.insert("body".into(), json!(body));
        Ok(native)
    }

    pub fn block_ids(&self) -> Result<Vec<String>, AdapterError> {
        let txn = self.doc.transact();
        // An empty root array has no Yrs items to encode. Its implicit root
        // name denotes the same empty sequence; native/ID equality is checked
        // when restoring, so dropping a nonempty order cannot pass validation.
        let Some(order) = txn.get_array("wordcraft.order") else {
            return Ok(Vec::new());
        };
        let ids = order
            .iter(&txn)
            .map(|item| string(Some(item)))
            .collect::<Result<Vec<_>, _>>()?;
        validate_ids(&ids, ids.len()).map_err(|_| AdapterError::InvalidCheckpoint)?;
        Ok(ids)
    }

    /// This is a document-wide gate, not a per-block lossy fallback. No public
    /// mutation or raw Yrs update entrypoint is exposed by this initial crate.
    pub fn is_read_only(&self) -> bool {
        self.read_only
    }

    pub fn read_only_reason(&self) -> Option<&'static str> {
        self.read_only.then_some(
            "unsupported WordCraft block, inline feature, review content or native field",
        )
    }

    pub fn checkpoint(&self) -> Result<Vec<u8>, AdapterError> {
        let checkpoint = Checkpoint {
            format: "wordcraft-0.10.0/yrs-0.28.0/v1".into(),
            native: self.native()?,
            block_ids: self.block_ids()?,
            update: self
                .doc
                .transact()
                .encode_state_as_update_v1(&StateVector::default()),
        };
        let bytes = serde_json::to_vec(&checkpoint).map_err(|_| AdapterError::InvalidCheckpoint)?;
        if bytes.len() > MAX_CHECKPOINT_BYTES {
            return Err(AdapterError::Limit);
        }
        Ok(bytes)
    }

    /// Only host-authenticated, scope/revision-verified storage checkpoints may
    /// reach this function. It is not an arbitrary remote Yrs update endpoint.
    /// Decode into a fresh document so errors cannot mutate a live replica.
    pub fn restore(bytes: &[u8]) -> Result<Self, AdapterError> {
        if bytes.len() > MAX_CHECKPOINT_BYTES {
            return Err(AdapterError::Limit);
        }
        let checkpoint: Checkpoint =
            serde_json::from_slice(bytes).map_err(|_| AdapterError::InvalidCheckpoint)?;
        if checkpoint.format != "wordcraft-0.10.0/yrs-0.28.0/v1" {
            return Err(AdapterError::InvalidCheckpoint);
        }
        let read_only =
            classify(&checkpoint.native).map_err(|_| AdapterError::InvalidCheckpoint)?;
        let recovered = mooring_text::recover(&checkpoint.update, Vec::new())
            .map_err(|_| AdapterError::InvalidCheckpoint)?;
        let result = Self {
            doc: recovered.document,
            read_only,
        };
        if result.block_ids()? != checkpoint.block_ids || result.native()? != checkpoint.native {
            return Err(AdapterError::InvalidCheckpoint);
        }
        Ok(result)
    }
}

fn string(value: Option<Out>) -> Result<String, AdapterError> {
    match value {
        Some(Out::Any(Any::String(value))) => Ok(value.to_string()),
        _ => Err(AdapterError::InvalidCheckpoint),
    }
}
fn validate_ids(ids: &[String], count: usize) -> Result<(), AdapterError> {
    let mut unique = HashSet::new();
    if ids.len() != count
        || ids.len() > MAX_BLOCKS
        || ids.iter().any(|id| {
            id.is_empty()
                || id.len() > 128
                || !id
                    .bytes()
                    .all(|b| b.is_ascii_alphanumeric() || b == b'_' || b == b'-')
                || !unique.insert(id)
        })
    {
        return Err(AdapterError::InvalidIdentity);
    }
    Ok(())
}

/// Retains all native metadata; first D1 writes support paragraphs, basic
/// inline flags, paragraph style and numbering only. Everything else is read-only.
fn classify(native: &Value) -> Result<bool, AdapterError> {
    if serde_json::to_vec(native)
        .map_err(|_| AdapterError::InvalidNative)?
        .len()
        > MAX_NATIVE_BYTES
    {
        return Err(AdapterError::Limit);
    }
    let object = native.as_object().ok_or(AdapterError::InvalidNative)?;
    let body = native["body"]
        .as_array()
        .ok_or(AdapterError::InvalidNative)?;
    for field in [
        "lastSection",
        "parts",
        "styles",
        "numbering",
        "comments",
        "settings",
        "core",
    ] {
        if object.get(field).and_then(Value::as_object).is_none() {
            return Err(AdapterError::InvalidNative);
        }
    }
    for field in ["revisions", "sources", "customProps"] {
        if object.get(field).and_then(Value::as_array).is_none() {
            return Err(AdapterError::InvalidNative);
        }
    }
    if body.len() > MAX_BLOCKS {
        return Err(AdapterError::Limit);
    }
    let mut unsupported = object
        .keys()
        .any(|key| !ROOT_FIELDS.contains(&key.as_str()));
    for field in ["parts", "comments", "revisions"] {
        unsupported |= match object.get(field) {
            Some(Value::Object(v)) => !v.is_empty(),
            Some(Value::Array(v)) => !v.is_empty(),
            _ => true,
        };
    }
    for block in body {
        let block = block.as_object().ok_or(AdapterError::InvalidNative)?;
        if block.get("kind").and_then(Value::as_str) != Some("para") {
            unsupported = true;
            continue;
        }
        unsupported |= block
            .keys()
            .any(|k| !["kind", "text", "runs", "props", "mark"].contains(&k.as_str()));
        let source = block
            .get("text")
            .and_then(Value::as_str)
            .ok_or(AdapterError::InvalidNative)?;
        unsupported |= source.contains('\u{fffc}');
        let runs = block
            .get("runs")
            .and_then(Value::as_array)
            .ok_or(AdapterError::InvalidNative)?;
        let mut byte = 0usize;
        for run in runs {
            let run = run.as_object().ok_or(AdapterError::InvalidNative)?;
            unsupported |= run.keys().any(|k| !["len", "props"].contains(&k.as_str()));
            let length = usize::try_from(
                run.get("len")
                    .and_then(Value::as_u64)
                    .ok_or(AdapterError::InvalidNative)?,
            )
            .map_err(|_| AdapterError::InvalidNative)?;
            byte = byte
                .checked_add(length)
                .ok_or(AdapterError::InvalidNative)?;
            if !source.is_char_boundary(byte) {
                return Err(AdapterError::InvalidNative);
            }
            // Actual native and WASM WordCraft normalize empty runs away,
            // including their formatting. Until that encoding is qualified,
            // retain the entire document opaquely and forbid writes.
            unsupported |= length == 0;
            if let Some(props) = run.get("props") {
                unsupported |= classify_character(props)?;
            }
        }
        if byte != source.len() {
            return Err(AdapterError::InvalidNative);
        }
        if let Some(mark) = block.get("mark") {
            unsupported |= classify_character(mark)?;
        }
        if let Some(props) = block.get("props") {
            let props = props.as_object().ok_or(AdapterError::InvalidNative)?;
            unsupported |= props
                .keys()
                .any(|k| !["style", "numbering"].contains(&k.as_str()));
            if let Some(style) = props.get("style") {
                if style.as_str().is_none_or(|s| s.is_empty() || s.len() > 256) {
                    return Err(AdapterError::InvalidNative);
                }
            }
            if let Some(numbering) = props.get("numbering") {
                let numbering = numbering.as_object().ok_or(AdapterError::InvalidNative)?;
                unsupported |= numbering
                    .keys()
                    .any(|k| !["num", "level"].contains(&k.as_str()));
                if ["num", "level"].iter().any(|k| {
                    numbering
                        .get(*k)
                        .and_then(Value::as_u64)
                        .is_none_or(|n| n > u32::MAX.into())
                }) {
                    return Err(AdapterError::InvalidNative);
                }
            }
        }
    }
    Ok(unsupported)
}
fn classify_character(props: &Value) -> Result<bool, AdapterError> {
    let props = props.as_object().ok_or(AdapterError::InvalidNative)?;
    let mut unsupported = false;
    for (key, value) in props {
        if CHARACTER_FLAGS.contains(&key.as_str()) {
            if !value.is_boolean() {
                return Err(AdapterError::InvalidNative);
            }
        } else {
            unsupported = true;
        }
    }
    Ok(unsupported)
}
type RunSemantics = (JsonMap<String, Value>, usize);

fn normalize_runs(runs: &[Value]) -> Result<Vec<RunSemantics>, AdapterError> {
    let mut normalized: Vec<RunSemantics> = Vec::new();
    for run in runs {
        let mut props = run
            .as_object()
            .cloned()
            .ok_or(AdapterError::InvalidCheckpoint)?;
        let length = usize::try_from(
            props
                .remove("len")
                .and_then(|v| v.as_u64())
                .ok_or(AdapterError::InvalidCheckpoint)?,
        )
        .map_err(|_| AdapterError::InvalidCheckpoint)?;
        if length == 0 {
            continue;
        }
        if let Some((previous, bytes)) = normalized.last_mut() {
            if *previous == props {
                *bytes = bytes.checked_add(length).ok_or(AdapterError::Limit)?;
                continue;
            }
        }
        normalized.push((props, length));
    }
    Ok(normalized)
}

pub fn byte_to_utf16(text: &str, offset: usize) -> Result<u32, AdapterError> {
    if !text.is_char_boundary(offset) {
        return Err(AdapterError::InvalidNative);
    }
    u32::try_from(text[..offset].encode_utf16().count()).map_err(|_| AdapterError::Limit)
}
pub fn utf16_to_byte(text: &str, offset: u32) -> Result<usize, AdapterError> {
    let mut units = 0;
    for (byte, scalar) in text.char_indices() {
        if units == offset {
            return Ok(byte);
        }
        units += scalar.len_utf16() as u32;
        if units > offset {
            return Err(AdapterError::InvalidNative);
        }
    }
    if units == offset {
        Ok(text.len())
    } else {
        Err(AdapterError::InvalidNative)
    }
}

#[cfg(test)]
mod tests;
