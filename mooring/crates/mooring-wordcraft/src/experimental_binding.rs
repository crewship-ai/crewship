//! Default-off native-state binding experiment. DATA only: no authority, transport,
//! durable receipt, or production checkpoint compatibility is implied.
//! Reuses the WordCraft stable-ID order/blocks/texts model. The separate checkpoint
//! format isolates general property actions from the qualified boolean-only model.
use crate::{string, validate_ids, AdapterError, Document, MAX_CHECKPOINT_BYTES, MAX_NATIVE_BYTES};
use mooring_text::yrs::{
    types::{text::YChange, Attrs},
    updates::decoder::Decode,
    Any, Array, ClientID, Doc, Map, MapPrelim, MapRef, OffsetKind, Options, Origin, Out, ReadTxn,
    StateVector, Text, TextPrelim, TextRef, Transact, UndoManager, Update,
};
use serde::{Deserialize, Serialize};
use serde_json::{json, Map as JsonMap, Value};
use std::{
    collections::{BTreeMap, BTreeSet, HashSet},
    sync::Arc,
};

const FORMAT: &str = "wordcraft.yrs-binding-experiment.v1";
const STRUCTURE: &str = "wordcraft.binding.structure";
const IDS: &str = "wordcraft.binding.ids";
const PARAGRAPHS: &str = "wordcraft.binding.paragraphs";
const PREFIX: &str = "wcb:";
const PRESENT: &str = "@present";
const MAX_UPDATE: usize = 262144;
const MAX_CELLS: usize = 65536;

#[derive(Debug, PartialEq, Eq)]
pub enum Error {
    InvalidState,
    Unsupported,
    InvalidOrigin,
    Limit,
    NoChange,
    StaleStructure,
}
impl From<AdapterError> for Error {
    fn from(_: AdapterError) -> Self {
        Self::InvalidState
    }
}
#[derive(Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
struct Checkpoint {
    format: String,
    update: Vec<u8>,
}
#[derive(Clone, PartialEq)]
struct Cell {
    ch: char,
    props: JsonMap<String, Value>,
}

/// Untrusted DATA. The host must bind the basis to its observed document;
/// these fields grant no authentication, authorization or durable outcome.
#[derive(Clone, Debug, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Change {
    pub update: Vec<u8>,
    pub basis_state: [u8; 32],
    pub basis_ids: Vec<String>,
    pub basis_structure: String,
}
struct UndoSafety {
    destructive: bool,
    remote_generation: u64,
}
pub struct Binding {
    document: Document,
    undo: UndoManager,
    origin: u64,
    remote_generation: u64,
    undo_safety: Vec<UndoSafety>,
}

impl Binding {
    pub fn import(native: Value, ids: Vec<String>, origin: u64) -> Result<Self, Error> {
        validate_native(&native)?;
        // Retain the established header, stable-ID order and opaque native blocks.
        let imported = Document::import(native.clone(), ids.clone())?;
        let document = clone_document(&imported, origin)?;
        let texts = document.doc.get_or_insert_map("wordcraft.texts");
        let paragraphs = document.doc.get_or_insert_map(PARAGRAPHS);
        let allocated = document.doc.get_or_insert_map(IDS);
        let structure = document.doc.get_or_insert_map(STRUCTURE);
        {
            let mut txn = document.doc.transact_mut();
            structure.insert(&mut txn, "epoch", "0");
            for (id, block) in ids.iter().zip(native["body"].as_array().unwrap()) {
                allocated.insert(&mut txn, id.as_str(), "1");
                if editable(block) {
                    let text = texts.insert(&mut txn, id.as_str(), TextPrelim::default());
                    let props = paragraphs.insert(&mut txn, id.as_str(), MapPrelim::default());
                    let mut offset = 0;
                    for cell in cells(block)? {
                        let attrs = attributes(&cell.props, 0, 0);
                        text.insert_with_attributes(&mut txn, offset, &cell.ch.to_string(), attrs);
                        offset += cell.ch.len_utf16() as u32;
                    }
                    for (key, value) in property_object(block.get("props"))? {
                        props.insert(&mut txn, action_key(&key, 0, 0), value.to_string());
                    }
                } else {
                    // The production importer may have projected this paragraph.
                    // This experiment retains unsupported blocks entirely opaquely.
                    texts.remove(&mut txn, id);
                }
                let blocks = txn.get_map("wordcraft.blocks").ok_or(Error::InvalidState)?;
                blocks.insert(&mut txn, id.as_str(), block.to_string());
            }
        }
        Self::attach(document, origin)
    }

    pub fn restore(bytes: &[u8], origin: u64) -> Result<Self, Error> {
        if bytes.len() > MAX_CHECKPOINT_BYTES {
            return Err(Error::Limit);
        }
        let cp: Checkpoint = serde_json::from_slice(bytes).map_err(|_| Error::InvalidState)?;
        if cp.format != FORMAT {
            return Err(Error::InvalidState);
        }
        let document = decode_document(&cp.update, origin)?;
        let result = Self::attach(document, origin)?;
        result.validate()?;
        Ok(result)
    }
    fn attach(document: Document, origin: u64) -> Result<Self, Error> {
        let mut undo = UndoManager::with_options(mooring_text::yrs::undo::Options {
            capture_timeout_millis: 0,
            tracked_origins: HashSet::from([Origin::from(ClientID::new(origin))]),
            capture_transaction: None,
            // Every outer transaction is a separate undo item; no platform
            // wall-clock implementation is required (including on WASM).
            timestamp: Arc::new(|| 0_u64),
            init_undo_stack: vec![],
            init_redo_stack: vec![],
        });
        for name in ["wordcraft.blocks", "wordcraft.texts", PARAGRAPHS] {
            let root = document.doc.get_or_insert_map(name);
            undo.expand_scope(&document.doc, &root);
        }
        let order = document.doc.get_or_insert_array("wordcraft.order");
        undo.expand_scope(&document.doc, &order);
        Ok(Self {
            document,
            undo,
            origin,
            remote_generation: 0,
            undo_safety: Vec::new(),
        })
    }
    /// All allocated stable IDs, including retired paragraphs. This is DATA,
    /// not permission. Native-value differences alone do not prove all CRDT effects.
    pub fn inventory_ids(&self) -> Result<Vec<String>, Error> {
        let txn = self.document.doc.transact();
        let map = txn.get_map(IDS).ok_or(Error::InvalidState)?;
        let mut ids = map.keys(&txn).map(str::to_string).collect::<Vec<_>>();
        ids.sort();
        validate_ids(&ids, ids.len())?;
        Ok(ids)
    }
    pub fn stable_ids(&self) -> Result<Vec<String>, Error> {
        Ok(self.document.block_ids()?)
    }
    pub fn native(&self) -> Result<Value, Error> {
        project(&self.document)
    }
    pub fn checkpoint(&self) -> Result<Vec<u8>, Error> {
        let cp = Checkpoint {
            format: FORMAT.into(),
            update: full_update(&self.document.doc),
        };
        let bytes = serde_json::to_vec(&cp).map_err(|_| Error::InvalidState)?;
        if bytes.len() > MAX_CHECKPOINT_BYTES {
            return Err(Error::Limit);
        }
        Ok(bytes)
    }
    /// Compare the engine post-state by stable IDs. Missing existing IDs retire
    /// paragraphs; newly supplied IDs are inserted once. Existing IDs cannot move.
    /// A split retains the left ID; a join retains its chosen surviving ID.
    pub fn reconcile(&mut self, after: &Value, ids: Vec<String>) -> Result<Change, Error> {
        validate_native(after)?;
        validate_ids(&ids, after["body"].as_array().unwrap().len())?;
        let before = self.native()?;
        if before == *after && ids == self.stable_ids()? {
            return Err(Error::NoChange);
        }
        allowed_transition(&before, &self.stable_ids()?, after, &ids)?;
        let mut change = self.basis()?;
        let destructive =
            ids != self.stable_ids()? || deleted_text(&before, &self.stable_ids()?, after, &ids)?;
        let candidate = clone_document(&self.document, self.origin)?;
        let vector = self.document.doc.transact().state_vector();
        reconcile(&candidate, after, &ids, self.origin)?;
        if project(&candidate)? != *after {
            return Err(Error::Unsupported);
        }
        validate_document(&candidate)?;
        let update = candidate.doc.transact().encode_state_as_update_v1(&vector);
        if update.len() > MAX_UPDATE {
            return Err(Error::Limit);
        }
        apply(&self.document.doc, &update, ClientID::new(self.origin))?;
        self.undo.reset();
        self.undo_safety.push(UndoSafety {
            destructive,
            remote_generation: self.remote_generation,
        });
        change.update = update;
        Ok(change)
    }
    /// Validate in an isolated candidate before touching this replica or its undo
    /// stack. Caller must separately authenticate and authorize actual effects.
    pub fn apply_remote(&mut self, change: &Change) -> Result<(), Error> {
        if change.update.len() > MAX_UPDATE {
            return Err(Error::Limit);
        }
        validate_ids(&change.basis_ids, change.basis_ids.len())?;
        if change.basis_structure.len() > 128 {
            return Err(Error::InvalidState);
        }
        let candidate = clone_document(&self.document, self.origin)?;
        apply(&candidate.doc, &change.update, "binding-remote")?;
        validate_document(&candidate)?;
        if full_update(&candidate.doc) == full_update(&self.document.doc) {
            return Ok(());
        }
        let structure_changed = epoch(&candidate)? != epoch(&self.document)?;
        if candidate.block_ids()? != self.stable_ids()? && !structure_changed {
            return Err(Error::InvalidState);
        }
        if change.basis_structure != epoch(&self.document)?
            || change.basis_ids != self.stable_ids()?
            || (structure_changed && change.basis_state != state_digest(&self.document.doc))
        {
            return Err(Error::StaleStructure);
        }
        let txn = candidate.doc.transact();
        let allocated = txn.get_map(IDS).ok_or(Error::InvalidState)?;
        if self
            .inventory_ids()?
            .iter()
            .any(|id| allocated.get(&txn, id).is_none())
        {
            return Err(Error::InvalidState);
        }
        drop(txn);
        allowed_transition(
            &self.native()?,
            &self.stable_ids()?,
            &project(&candidate)?,
            &candidate.block_ids()?,
        )?;
        apply(&self.document.doc, &change.update, "binding-remote")?;
        self.remote_generation = self.remote_generation.checked_add(1).ok_or(Error::Limit)?;
        Ok(())
    }
    pub fn undo(&mut self) -> Result<Change, Error> {
        let safety = self.undo_safety.last().ok_or(Error::NoChange)?;
        // State comparison cannot prove delete ownership. Refuse destructive
        // undo after a peer update rather than resurrect or hide peer content.
        if safety.destructive && safety.remote_generation != self.remote_generation {
            return Err(Error::Unsupported);
        }
        let mut change = self.basis()?;
        let vector = self.document.doc.transact().state_vector();
        if !self.undo.undo_blocking() {
            return Err(Error::NoChange);
        }
        self.undo_safety.pop();
        if self.stable_ids()? != change.basis_ids {
            advance_epoch(&self.document, self.origin)?;
        }
        self.validate()?;
        change.update = self
            .document
            .doc
            .transact()
            .encode_state_as_update_v1(&vector);
        Ok(change)
    }
    fn basis(&self) -> Result<Change, Error> {
        Ok(Change {
            update: Vec::new(),
            basis_state: state_digest(&self.document.doc),
            basis_ids: self.stable_ids()?,
            basis_structure: epoch(&self.document)?,
        })
    }
    fn validate(&self) -> Result<(), Error> {
        validate_document(&self.document)
    }
}

fn validate_native(native: &Value) -> Result<(), Error> {
    if serde_json::to_vec(native)
        .map_err(|_| Error::InvalidState)?
        .len()
        > MAX_NATIVE_BYTES
    {
        return Err(Error::Limit);
    }
    // Structural validity uses the pinned actual WordCraft model. Unknown native
    // data remains readable/opaque, but is never silently normalized on writes.
    crate::classify(native)?;
    Ok(())
}
fn editable(block: &Value) -> bool {
    block["kind"] == "para"
        && block["text"]
            .as_str()
            .is_some_and(|s| !s.contains('\u{fffc}'))
        && block.as_object().is_some_and(|b| {
            b.keys()
                .all(|k| ["kind", "text", "runs", "props", "mark"].contains(&k.as_str()))
        })
        && block["runs"].as_array().is_some_and(|runs| {
            runs.iter().all(|run| {
                run.as_object()
                    .is_some_and(|r| r.keys().all(|k| ["len", "props"].contains(&k.as_str())))
            })
        })
}
fn property_object(value: Option<&Value>) -> Result<JsonMap<String, Value>, Error> {
    let mut props = match value {
        None => JsonMap::new(),
        Some(Value::Object(p)) => p.clone(),
        _ => return Err(Error::InvalidState),
    };
    if props
        .keys()
        .any(|k| k.contains(':') || k.starts_with('@') || k.len() > 128)
    {
        return Err(Error::Unsupported);
    }
    props.insert(PRESENT.into(), json!(value.is_some()));
    Ok(props)
}
fn cells(block: &Value) -> Result<Vec<Cell>, Error> {
    let source = block["text"].as_str().ok_or(Error::InvalidState)?;
    if source.chars().count() > MAX_CELLS {
        return Err(Error::Limit);
    }
    let mut result = Vec::new();
    let mut byte = 0usize;
    for run in block["runs"].as_array().ok_or(Error::InvalidState)? {
        let len = usize::try_from(run["len"].as_u64().ok_or(Error::InvalidState)?)
            .map_err(|_| Error::Limit)?;
        let end = byte.checked_add(len).ok_or(Error::Limit)?;
        let text = source.get(byte..end).ok_or(Error::InvalidState)?;
        let props = property_object(run.get("props"))?;
        result.extend(text.chars().map(|ch| Cell {
            ch,
            props: props.clone(),
        }));
        byte = end;
    }
    if byte != source.len() {
        return Err(Error::InvalidState);
    }
    Ok(result)
}
fn action_key(field: &str, stamp: u64, origin: u64) -> String {
    format!("{PREFIX}{field}:{stamp}:{origin}")
}
fn parse_key(key: &str) -> Result<(&str, u64, u64), Error> {
    let mut parts = key
        .strip_prefix(PREFIX)
        .ok_or(Error::InvalidState)?
        .split(':');
    let field = parts.next().ok_or(Error::InvalidState)?;
    let stamp = parts
        .next()
        .ok_or(Error::InvalidState)?
        .parse::<u64>()
        .map_err(|_| Error::InvalidState)?;
    let origin = parts
        .next()
        .ok_or(Error::InvalidState)?
        .parse::<u64>()
        .map_err(|_| Error::InvalidState)?;
    if parts.next().is_some()
        || field.is_empty()
        || field.len() > 128
        || (field.starts_with('@') && field != PRESENT)
    {
        return Err(Error::InvalidState);
    }
    Ok((field, stamp, origin))
}
fn attributes(props: &JsonMap<String, Value>, stamp: u64, origin: u64) -> Attrs {
    props
        .iter()
        .map(|(k, v)| {
            (
                action_key(k, stamp, origin).into(),
                Any::String(v.to_string().into()),
            )
        })
        .collect()
}
fn resolved<'a>(
    entries: impl Iterator<Item = (&'a str, Any)>,
) -> Result<JsonMap<String, Value>, Error> {
    let mut winners: BTreeMap<&str, ((u64, u64), Value)> = BTreeMap::new();
    for (key, value) in entries {
        let (field, stamp, origin) = parse_key(key)?;
        let Any::String(raw) = value else {
            return Err(Error::InvalidState);
        };
        let value = serde_json::from_str(&raw).map_err(|_| Error::InvalidState)?;
        if winners
            .get(field)
            .is_none_or(|(old, _)| (stamp, origin) > *old)
        {
            winners.insert(field, ((stamp, origin), value));
        }
    }
    Ok(winners
        .into_iter()
        .filter_map(|(k, (_, v))| (!v.is_null()).then_some((k.into(), v)))
        .collect())
}
fn write_props(target: &mut Value, mut props: JsonMap<String, Value>) -> Result<(), Error> {
    let present = props
        .remove(PRESENT)
        .and_then(|v| v.as_bool())
        .ok_or(Error::InvalidState)?;
    if present || !props.is_empty() {
        target["props"] = Value::Object(props);
    } else {
        target
            .as_object_mut()
            .ok_or(Error::InvalidState)?
            .remove("props");
    }
    Ok(())
}
fn text_ref(document: &Document, id: &str) -> Result<TextRef, Error> {
    let txn = document.doc.transact();
    match txn.get_map("wordcraft.texts").and_then(|m| m.get(&txn, id)) {
        Some(Out::YText(t)) => Ok(t),
        _ => Err(Error::InvalidState),
    }
}
fn paragraph_ref(document: &Document, id: &str) -> Result<MapRef, Error> {
    let txn = document.doc.transact();
    match txn.get_map(PARAGRAPHS).and_then(|m| m.get(&txn, id)) {
        Some(Out::YMap(m)) => Ok(m),
        _ => Err(Error::InvalidState),
    }
}
fn project(document: &Document) -> Result<Value, Error> {
    let txn = document.doc.transact();
    let metadata = txn
        .get_map("wordcraft.metadata")
        .ok_or(Error::InvalidState)?;
    let mut native: Value = serde_json::from_str(&string(metadata.get(&txn, "document"))?)
        .map_err(|_| Error::InvalidState)?;
    let blocks = txn.get_map("wordcraft.blocks").ok_or(Error::InvalidState)?;
    let mut body = Vec::new();
    for id in document.block_ids()? {
        let mut block: Value = serde_json::from_str(&string(blocks.get(&txn, &id))?)
            .map_err(|_| Error::InvalidState)?;
        if editable(&block) {
            let text = text_ref(document, &id)?;
            let mut content = String::new();
            let mut runs: Vec<Value> = Vec::new();
            for chunk in text.diff(&txn, YChange::identity) {
                let value = string(Some(chunk.insert))?;
                let attrs = chunk.attributes.unwrap_or_default();
                let props = resolved(attrs.iter().map(|(k, v)| (k.as_ref(), v.clone())))?;
                let mut run = json!({"len":value.len()});
                write_props(&mut run, props)?;
                if let Some(last) = runs
                    .last_mut()
                    .filter(|last| last.get("props") == run.get("props"))
                {
                    last["len"] = json!(last["len"].as_u64().unwrap() + value.len() as u64);
                } else {
                    runs.push(run);
                }
                content.push_str(&value);
            }
            block["text"] = json!(content);
            block["runs"] = json!(runs);
            let props = paragraph_ref(document, &id)?;
            write_props(
                &mut block,
                resolved(props.iter(&txn).map(|(k, v)| match v {
                    Out::Any(a) => (k, a),
                    _ => (k, Any::Null),
                }))?,
            )?;
        }
        body.push(block);
    }
    native["body"] = json!(body);
    Ok(native)
}
fn full_update(doc: &Doc) -> Vec<u8> {
    doc.transact()
        .encode_state_as_update_v1(&StateVector::default())
}
fn decode_document(update: &[u8], origin: u64) -> Result<Document, Error> {
    if origin == 0 || origin >= (1 << 53) {
        return Err(Error::InvalidOrigin);
    }
    if update.len() > MAX_NATIVE_BYTES {
        return Err(Error::Limit);
    }
    preflight(update)?;
    let doc = Doc::with_options(Options {
        client_id: ClientID::new(origin),
        offset_kind: OffsetKind::Utf16,
        skip_gc: true,
        ..Default::default()
    });
    apply(&doc, update, "binding-restore")?;
    Ok(Document {
        doc,
        read_only: false,
        project_text: true,
        model: crate::format::Model::V2,
    })
}
fn clone_document(document: &Document, origin: u64) -> Result<Document, Error> {
    decode_document(&full_update(&document.doc), origin)
}
fn apply(doc: &Doc, update: &[u8], origin: impl Into<Origin>) -> Result<(), Error> {
    preflight(update)?;
    doc.transact_mut_with(origin)
        .apply_update(Update::decode_v1(update).map_err(|_| Error::InvalidState)?)
        .map_err(|_| Error::InvalidState)?;
    if doc.transact().has_missing_updates() {
        return Err(Error::InvalidState);
    }
    Ok(())
}
fn validate_document(document: &Document) -> Result<(), Error> {
    let txn = document.doc.transact();
    let allowed = [
        "wordcraft.metadata",
        "wordcraft.blocks",
        "wordcraft.texts",
        "wordcraft.order",
        PARAGRAPHS,
        IDS,
        STRUCTURE,
    ];
    if txn.root_refs().any(|(name, _)| !allowed.contains(&name)) {
        return Err(Error::InvalidState);
    }
    let metadata = txn
        .get_map("wordcraft.metadata")
        .ok_or(Error::InvalidState)?;
    if metadata.len(&txn) != 1 {
        return Err(Error::InvalidState);
    }
    let blocks = txn.get_map("wordcraft.blocks").ok_or(Error::InvalidState)?;
    let allocated = txn.get_map(IDS).ok_or(Error::InvalidState)?;
    let inventory = allocated.keys(&txn).map(str::to_string).collect::<Vec<_>>();
    validate_ids(&inventory, inventory.len())?;
    if allocated
        .iter(&txn)
        .any(|(_, value)| !matches!(value,Out::Any(Any::String(s)) if s.as_ref()=="1"))
    {
        return Err(Error::InvalidState);
    }
    let all_ids = blocks.keys(&txn).map(str::to_string).collect::<Vec<_>>();
    if all_ids.iter().any(|id| !inventory.contains(id)) {
        return Err(Error::InvalidState);
    }
    validate_ids(&all_ids, all_ids.len())?;
    let mut expected = BTreeSet::new();
    for id in &all_ids {
        let block: Value = serde_json::from_str(&string(blocks.get(&txn, id))?)
            .map_err(|_| Error::InvalidState)?;
        if editable(&block) {
            expected.insert(id.as_str());
        }
    }
    for name in ["wordcraft.texts", PARAGRAPHS] {
        let map = txn.get_map(name).ok_or(Error::InvalidState)?;
        if map.keys(&txn).collect::<BTreeSet<_>>() != expected {
            return Err(Error::InvalidState);
        }
    }
    epoch(document)?;
    let native = project(document)?;
    validate_native(&native)?;
    validate_ids(
        &document.block_ids()?,
        native["body"].as_array().unwrap().len(),
    )?;
    Ok(())
}
fn allowed_transition(
    before: &Value,
    before_ids: &[String],
    after: &Value,
    after_ids: &[String],
) -> Result<(), Error> {
    let mut a = before.clone();
    let mut b = after.clone();
    a.as_object_mut().unwrap().remove("body");
    b.as_object_mut().unwrap().remove("body");
    if a != b {
        return Err(Error::Unsupported);
    }
    let old: BTreeMap<_, _> = before_ids
        .iter()
        .zip(before["body"].as_array().unwrap())
        .collect();
    let new: BTreeMap<_, _> = after_ids
        .iter()
        .zip(after["body"].as_array().unwrap())
        .collect();
    let surviving_old: Vec<_> = before_ids
        .iter()
        .filter(|id| new.contains_key(id))
        .collect();
    let surviving_new: Vec<_> = after_ids.iter().filter(|id| old.contains_key(id)).collect();
    if surviving_old != surviving_new {
        return Err(Error::Unsupported);
    }
    for (id, block) in &old {
        let Some(next) = new.get(id) else {
            if !editable(block) {
                return Err(Error::Unsupported);
            }
            continue;
        };
        if block == next {
            continue;
        }
        if !editable(block) || !editable(next) {
            return Err(Error::Unsupported);
        }
        let mut a = (*block).clone();
        let mut b = (*next).clone();
        for field in ["text", "runs", "props"] {
            a.as_object_mut().unwrap().remove(field);
            b.as_object_mut().unwrap().remove(field);
        }
        if a != b || block["props"].get("style") != next["props"].get("style") {
            return Err(Error::Unsupported);
        }
    }
    for (id, block) in &new {
        if !old.contains_key(id) && !editable(block) {
            return Err(Error::Unsupported);
        }
    }
    // Actual application validation is required for changed documents; readonly
    // opaque input may be imported/restored without permitting a lossy rewrite.
    if before != after && !crate::native_qualification::lossless(after) {
        return Err(Error::Unsupported);
    }
    Ok(())
}
fn reconcile(document: &Document, after: &Value, ids: &[String], origin: u64) -> Result<(), Error> {
    let before = project(document)?;
    let old_ids = document.block_ids()?;
    let old: BTreeMap<_, _> = old_ids
        .iter()
        .zip(before["body"].as_array().unwrap())
        .collect();
    let blocks = document.doc.get_or_insert_map("wordcraft.blocks");
    let texts = document.doc.get_or_insert_map("wordcraft.texts");
    let paragraphs = document.doc.get_or_insert_map(PARAGRAPHS);
    let allocated = document.doc.get_or_insert_map(IDS);
    let structure = document.doc.get_or_insert_map(STRUCTURE);
    let order = document.doc.get_or_insert_array("wordcraft.order");
    // The frontier sum is strictly greater than every previously allocated
    // action stamp. Independent attributes retain superseded actions for undo.
    let stamp = document
        .doc
        .transact()
        .state_vector()
        .iter()
        .try_fold(1u64, |sum, (_, c)| sum.checked_add(*c as u64))
        .ok_or(Error::Limit)?;
    let mut txn = document.doc.transact_mut();
    if old_ids != ids {
        structure.insert(&mut txn, "epoch", format!("{origin}:{stamp}"));
    }
    for (id, block) in ids.iter().zip(after["body"].as_array().unwrap()) {
        if old.get(id).is_some_and(|old| *old == block) {
            continue;
        }
        let (text, props) = if !old.contains_key(id) {
            if allocated.get(&txn, id).is_some() {
                return Err(Error::Unsupported);
            }
            allocated.insert(&mut txn, id.as_str(), "1");
            blocks.insert(&mut txn, id.as_str(), block.to_string());
            (
                texts.insert(&mut txn, id.as_str(), TextPrelim::default()),
                paragraphs.insert(&mut txn, id.as_str(), MapPrelim::default()),
            )
        } else {
            let text = match texts.get(&txn, id) {
                Some(Out::YText(t)) => t,
                _ => return Err(Error::InvalidState),
            };
            let props = match paragraphs.get(&txn, id) {
                Some(Out::YMap(p)) => p,
                _ => return Err(Error::InvalidState),
            };
            (text, props)
        };
        let old_cells = old
            .get(id)
            .map(|b| cells(b))
            .transpose()?
            .unwrap_or_default();
        let new_cells = cells(block)?;
        let prefix = old_cells
            .iter()
            .zip(&new_cells)
            .take_while(|(a, b)| a.ch == b.ch)
            .count();
        let suffix = old_cells[prefix..]
            .iter()
            .rev()
            .zip(new_cells[prefix..].iter().rev())
            .take_while(|(a, b)| a.ch == b.ch)
            .count();
        let start = old_cells[..prefix]
            .iter()
            .map(|c| c.ch.len_utf16() as u32)
            .sum();
        let removed = old_cells[prefix..old_cells.len() - suffix]
            .iter()
            .map(|c| c.ch.len_utf16() as u32)
            .sum();
        if removed > 0 {
            text.remove_range(&mut txn, start, removed);
        }
        let mut offset = start;
        for cell in &new_cells[prefix..new_cells.len() - suffix] {
            text.insert_with_attributes(
                &mut txn,
                offset,
                &cell.ch.to_string(),
                attributes(&cell.props, stamp, origin),
            );
            offset += cell.ch.len_utf16() as u32;
        }
        let mut offset = 0;
        for (index, cell) in new_cells.iter().enumerate() {
            let prior = if index < prefix {
                Some(&old_cells[index])
            } else if index >= new_cells.len() - suffix {
                Some(&old_cells[old_cells.len() - (new_cells.len() - index)])
            } else {
                None
            };
            if let Some(prior) = prior {
                let keys: BTreeSet<_> = prior.props.keys().chain(cell.props.keys()).collect();
                let attrs: Attrs = keys
                    .into_iter()
                    .filter(|k| prior.props.get(*k) != cell.props.get(*k))
                    .map(|k| {
                        (
                            action_key(k, stamp, origin).into(),
                            Any::String(
                                cell.props.get(k).unwrap_or(&Value::Null).to_string().into(),
                            ),
                        )
                    })
                    .collect();
                if !attrs.is_empty() {
                    text.format(&mut txn, offset, cell.ch.len_utf16() as u32, attrs);
                }
            }
            offset += cell.ch.len_utf16() as u32;
        }
        let prior = property_object(old.get(id).and_then(|b| b.get("props")))?;
        let next = property_object(block.get("props"))?;
        let keys: BTreeSet<_> = prior.keys().chain(next.keys()).collect();
        for key in keys {
            if !old.contains_key(id) || prior.get(key) != next.get(key) {
                props.insert(
                    &mut txn,
                    action_key(key, stamp, origin),
                    next.get(key).unwrap_or(&Value::Null).to_string(),
                );
            }
        }
    }
    // Diff the ID sequence without replacing surviving entries. Removed blocks
    // remain as retired opaque metadata; IDs cannot be reused for new content.
    let mut current = old_ids;
    for index in (0..current.len()).rev() {
        if !ids.contains(&current[index]) {
            order.remove_range(&mut txn, index as u32, 1);
            current.remove(index);
        }
    }
    for (index, id) in ids.iter().enumerate() {
        if current.get(index) != Some(id) {
            order.insert(&mut txn, index as u32, id.clone());
            current.insert(index, id.clone());
        }
    }
    Ok(())
}

fn preflight(update: &[u8]) -> Result<(), Error> {
    crate::checkpoint_wire::preflight_with_format(update, |key, value| {
        crate::checkpoint_wire::standard_attribute(key, value)
            || (parse_key(key).is_ok()
                && (value == "null" || serde_json::from_str::<String>(value).is_ok()))
    })?;
    Ok(())
}

fn epoch(document: &Document) -> Result<String, Error> {
    let txn = document.doc.transact();
    let map = txn.get_map(STRUCTURE).ok_or(Error::InvalidState)?;
    if map.len(&txn) != 1 {
        return Err(Error::InvalidState);
    }
    let value = string(map.get(&txn, "epoch"))?;
    if value.is_empty() || value.len() > 128 {
        return Err(Error::InvalidState);
    }
    Ok(value)
}
fn advance_epoch(document: &Document, origin: u64) -> Result<(), Error> {
    let stamp = document
        .doc
        .transact()
        .state_vector()
        .iter()
        .try_fold(1u64, |sum, (_, c)| sum.checked_add(*c as u64))
        .ok_or(Error::Limit)?;
    let map = document.doc.get_or_insert_map(STRUCTURE);
    map.insert(
        &mut document.doc.transact_mut_with("binding-structure-epoch"),
        "epoch",
        format!("{origin}:{stamp}"),
    );
    Ok(())
}
fn deleted_text(
    before: &Value,
    before_ids: &[String],
    after: &Value,
    after_ids: &[String],
) -> Result<bool, Error> {
    for (id, block) in before_ids.iter().zip(before["body"].as_array().unwrap()) {
        let Some(index) = after_ids.iter().position(|next| next == id) else {
            return Ok(true);
        };
        if !editable(block) {
            continue;
        }
        let old = cells(block)?;
        let new = cells(&after["body"][index])?;
        let mut current = 0;
        for cell in &new {
            if old.get(current).is_some_and(|old| old.ch == cell.ch) {
                current += 1;
            }
        }
        if current != old.len() {
            return Ok(true);
        }
    }
    Ok(false)
}

fn state_digest(doc: &Doc) -> [u8; 32] {
    use sha2::{Digest, Sha256};
    Sha256::digest(full_update(doc)).into()
}

/// Checks only newly allocated Yrs client IDs against a host-authenticated
/// origin. Delete-only updates allocate no IDs. This is NOT complete authorship
/// qualification: inherited property-action keys/history still require a trusted
/// provenance witness. It grants no production admission or block mandate.
pub fn validate_authorship(change: &Change, host_origin: u64) -> Result<(), Error> {
    if host_origin == 0 || host_origin >= (1 << 53) {
        return Err(Error::InvalidOrigin);
    }
    if change.update.len() > MAX_UPDATE {
        return Err(Error::Limit);
    }
    preflight(&change.update)?;
    let update = Update::decode_v1(&change.update).map_err(|_| Error::InvalidState)?;
    if update
        .insertions(true)
        .iter()
        .any(|(client, _)| client.get() != host_origin)
    {
        return Err(Error::InvalidOrigin);
    }
    Ok(())
}
