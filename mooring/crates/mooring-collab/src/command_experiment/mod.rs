//! Opt-in C1 experiment only. No listener, persistence, SQL Saved, or production
//! authority wiring. The caller supplies fresh trusted admission at BOTH ends.
use serde::{Deserialize, Serialize};
use serde_json::{json, Value};
use std::collections::{BTreeMap, BTreeSet};
pub mod native_channel;
pub mod process;
mod staging;
pub use staging::StagedCommit;

const FRAME: usize = 4 * 1024 * 1024;
const INPUT: usize = 65_536;
const HISTORY: usize = 32;
const HISTORY_BYTES: usize = 16 * 1024 * 1024;
const OPERATIONS: usize = 256;
type Result<T> = std::result::Result<T, &'static str>;

/// Constructed by a trusted host, NEVER decoded from command JSON.
#[derive(Clone)]
pub struct Admission {
    participant: String,
    author: String,
    time: String,
    epoch: u64,
    writer: bool,
}
impl Admission {
    pub fn new(
        participant: &str,
        author: &str,
        time: &str,
        epoch: u64,
        writer: bool,
    ) -> Result<Self> {
        if !id(participant)
            || author.is_empty()
            || author.len() > 200
            || time.len() != 20
            || epoch == 0
        {
            return Err("invalid_admission");
        }
        Ok(Self {
            participant: participant.into(),
            author: author.into(),
            time: time.into(),
            epoch,
            writer,
        })
    }
}
fn id(s: &str) -> bool {
    !s.is_empty() && s.len() <= 200 && !s.contains('\0')
}
#[derive(Deserialize, Serialize, Clone)]
#[serde(deny_unknown_fields)]
struct Target {
    block_id: String,
    unit: Unit,
    start: usize,
    end: usize,
}
#[derive(Deserialize, Serialize, Clone)]
enum Unit {
    #[serde(rename = "utf8_bytes")]
    Utf8Bytes,
}
#[derive(Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
struct InputEnvelope {
    op_id: String,
    generation: String,
    participant: String,
    base_revision: u64,
    target: Option<Target>,
    command: String,
    params: Value,
}
#[derive(Clone, Serialize, Deserialize, PartialEq)]
#[serde(deny_unknown_fields)]
struct Snapshot {
    native: Value,
    ids: Vec<String>,
}
#[derive(Clone, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
struct Undo {
    participant: String,
    op_id: String,
    before: Snapshot,
    after: Snapshot,
    undone: bool,
}
#[derive(Clone, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
struct Seen {
    input: Vec<u8>,
    result: Value,
}
#[derive(Clone, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
struct Checkpoint {
    engine_identity: Value,
    format: String,
    document: String,
    generations: BTreeSet<String>,
    generation: String,
    revision: u64,
    p: Snapshot,
    history: Vec<Undo>,
    seen: BTreeMap<String, Seen>,
}
struct Candidate {
    envelope: InputEnvelope,
    input: Vec<u8>,
    epoch: u64,
    author: String,
    time: String,
    snapshot: Snapshot,
    undo: Option<usize>,
    selection: Option<Value>,
}
/// The actual child implementation speaks only an inherited private FD.
/// Test doubles alone cannot establish engine qualification.
pub trait Engine {
    fn call(&mut self, request: Value) -> Result<Value>;
}
#[derive(Debug, PartialEq)]
pub enum Prepared {
    Read(Value),
    Pending { op_id: String },
    Known(Value),
}

pub struct Host<E: Engine> {
    engine: E,
    state: Checkpoint,
    k: Option<Candidate>,
    issued: u64,
    rejected: BTreeMap<String, Seen>,
    // Exact last canonical SQL checkpoint identity; generation rotation in RAM
    // must not change the base digest used by the next SQL compare-and-swap.
    canonical_digest: String,
}
impl<E: Engine> Host<E> {
    pub fn new(
        mut engine: E,
        document: &str,
        generation: &str,
        native: Option<Value>,
    ) -> Result<Self> {
        if !id(document) || !id(generation) || document.contains(':') || generation.contains(':') {
            return Err("invalid_identity_namespace");
        }
        let out=engine.call(json!({"command":"init","params":{},"native":native,"target":null,"author":"host","time":"2001-01-01T00:00:00Z"}))?;
        if out.get("engine_identity") != Some(&expected_engine()) {
            return Err("engine_identity_mismatch");
        }
        let native = out.get("native").cloned().ok_or("engine_reply")?;
        let count = body(&native)?.len();
        let mut host = Self {
            engine,
            state: Checkpoint {
                engine_identity: expected_engine(),
                format: "wordcraft-c1-v2".into(),
                document: document.into(),
                generations: BTreeSet::from([generation.into()]),
                generation: generation.into(),
                revision: 0,
                p: Snapshot {
                    native,
                    ids: vec![],
                },
                history: vec![],
                seen: BTreeMap::new(),
            },
            k: None,
            issued: 0,
            rejected: BTreeMap::new(),
            canonical_digest: String::new(),
        };
        for _ in 0..count {
            let value = host.allocate()?;
            host.state.p.ids.push(value)
        }
        host.validate()?;
        host.canonical_digest = staging::digest(&host.checkpoint()?);
        Ok(host)
    }
    pub fn restore(engine: E, raw: &[u8], generation: &str) -> Result<Self> {
        if raw.len() > 32 * 1024 * 1024 || !id(generation) || generation.contains(':') {
            return Err("checkpoint_limit");
        }
        let mut state: Checkpoint =
            serde_json::from_slice(raw).map_err(|_| "invalid_checkpoint")?;
        if state.generations.contains(generation) || state.generations.len() >= 32 {
            return Err("generation_reuse");
        }
        state.generations.insert(generation.into());
        state.generation = generation.into();
        let mut host = Self {
            engine,
            state,
            k: None,
            issued: 0,
            rejected: BTreeMap::new(),
            canonical_digest: staging::digest(raw),
        };
        host.validate()?;
        let reply=host.engine.call(json!({"command":"init","params":{},"native":host.state.p.native,"target":null,"author":"host","time":"2001-01-01T00:00:00Z"}))?;
        if reply.get("engine_identity") != Some(&expected_engine()) {
            return Err("engine_identity_mismatch");
        }
        if reply.get("native") != Some(&host.state.p.native) {
            return Err("checkpoint_changed");
        }
        Ok(host)
    }
    fn allocate(&mut self) -> Result<String> {
        self.issued = self.issued.checked_add(1).ok_or("identity_exhausted")?;
        Ok(format!(
            "{}:{}:{}",
            self.state.document, self.state.generation, self.issued
        ))
    }
    fn validate(&self) -> Result<()> {
        if self.state.engine_identity != expected_engine()
            || self.state.format != "wordcraft-c1-v2"
            || self.state.seen.len() > OPERATIONS
            || self.state.history.len() > HISTORY
            || history_size(&self.state.history) > HISTORY_BYTES
        {
            return Err("checkpoint_limit");
        }
        snapshot_valid(&self.state.p)?;
        for record in &self.state.history {
            snapshot_valid(&record.before)?;
            snapshot_valid(&record.after)?;
        }
        Ok(())
    }
    pub fn generation(&self) -> &str {
        &self.state.generation
    }
    pub fn revision(&self) -> u64 {
        self.state.revision
    }
    pub fn ids(&self) -> &[String] {
        &self.state.p.ids
    }
    pub fn native(&self) -> &Value {
        &self.state.p.native
    }
    pub fn counts(&self) -> (usize, usize, usize) {
        (
            self.state.history.len(),
            usize::from(self.k.is_some()),
            self.state.seen.len() + self.rejected.len(),
        )
    }
    pub fn canonical_checkpoint_digest(&self) -> &str {
        &self.canonical_digest
    }
    pub fn checkpoint(&self) -> Result<Vec<u8>> {
        serde_json::to_vec(&self.state).map_err(|_| "checkpoint_encode")
    }
    pub fn prepare(&mut self, raw: &[u8], admission: &Admission) -> Result<Prepared> {
        if raw.is_empty() || raw.len() > INPUT {
            return Err("input_limit");
        }
        let normalized =
            mooring_protocol::wire::normalize_bounded(raw).map_err(|_| "invalid_input")?;
        let env: InputEnvelope =
            serde_json::from_slice(normalized.bytes()).map_err(|_| "invalid_input")?;
        if !id(&env.op_id) || env.participant != admission.participant {
            return Err("participant_denied");
        }
        let prefix = format!("{}:", env.generation);
        let local = env
            .op_id
            .strip_prefix(&prefix)
            .ok_or("operation_generation_mismatch")?;
        if !id(&env.generation) || env.generation.contains(':') || !id(local) || local.contains(':')
        {
            return Err("operation_generation_mismatch");
        }
        let effective = json!({"engine":self.state.engine_identity,"envelope":env});
        let normalized = mooring_protocol::wire::normalize_bounded(
            &serde_json::to_vec(&effective).map_err(|_| "invalid_input")?,
        )
        .map_err(|_| "invalid_input")?;
        let input = normalized.digest().into_bytes();
        if let Some(old) = self
            .state
            .seen
            .get(&env.op_id)
            .or_else(|| self.rejected.get(&env.op_id))
        {
            return if old.input == input {
                Ok(Prepared::Known(old.result.clone()))
            } else {
                Err("op_id_conflict")
            };
        }
        // A prior-generation operation with no confirmed outcome cannot be
        // prepared again. Known committed receipts above remain replayable.
        if env.generation != self.state.generation {
            return Err("stale_generation");
        }
        if let Some(old) = &self.k {
            return if old.envelope.op_id == env.op_id
                && old.input == input
                && old.epoch == admission.epoch
            {
                Ok(Prepared::Pending { op_id: env.op_id })
            } else {
                Err("pending_limit")
            };
        }
        if env.base_revision != self.state.revision {
            return Err("stale_revision");
        }
        validate_params(&env.command, &env.params)?;
        if env.command == "document.read" {
            if env.target.is_some() {
                return Err("invalid_target");
            }
            let out=self.engine.call(json!({"command":"document.read","params":{},"native":self.state.p.native,"target":null,"author":admission.author,"time":admission.time}))?;
            return Ok(Prepared::Read(
                json!({"generation":self.state.generation,"revision":self.state.revision,"ids":self.state.p.ids,"result":out.get("result").ok_or("engine_reply")?}),
            ));
        }
        if !admission.writer {
            return Err("reader_denied");
        }
        if self.state.seen.len() + self.rejected.len() >= OPERATIONS {
            return Err("operation_limit");
        }
        let before = self.state.p.clone();
        let (snapshot, undo, selection) = if env.command == "participant.undo" {
            if env.target.is_some() {
                return Err("invalid_target");
            }
            let requested = env.params.get("op_id").and_then(Value::as_str);
            let index = if let Some(requested) = requested {
                self.state
                    .history
                    .iter()
                    .position(|x| x.op_id == requested && !x.undone)
                    .ok_or("undo_unavailable")?
            } else {
                self.state
                    .history
                    .iter()
                    .rposition(|x| x.participant == admission.participant && !x.undone)
                    .ok_or("undo_unavailable")?
            };
            let record = &self.state.history[index];
            if record.participant != admission.participant {
                return Err("foreign_undo_denied");
            }
            let snapshot = reverse(&before, record)?;
            let reply=self.engine.call(json!({"command":"document.read","params":{},"native":snapshot.native,"target":null,"author":admission.author,"time":admission.time}))?;
            if reply.get("native") != Some(&snapshot.native) {
                return Err("undo_projection_changed");
            }
            (snapshot, Some(index), None)
        } else {
            let target = env.target.as_ref().ok_or("missing_target")?;
            if target.start > target.end {
                return Err("invalid_target");
            }
            let index = before
                .ids
                .iter()
                .position(|x| x == &target.block_id)
                .ok_or("target_gone")?;
            let reply=self.engine.call(json!({"command":env.command,"params":env.params,"native":before.native,"target":{"block":index,"start":target.start,"end":target.end,"unit":"utf8_bytes"},"author":admission.author,"time":admission.time}))?;
            let native = reply.get("native").cloned().ok_or("engine_reply")?;
            let selection =
                process::validated_selection(reply.get("result").ok_or("engine_reply")?, &native)?;
            let count = body(&native)?.len();
            let mut ids = before.ids.clone();
            match (env.command.as_str(), count.cmp(&ids.len())) {
                ("text.newParagraph", std::cmp::Ordering::Greater) if count == ids.len() + 1 => {
                    ids.insert(index + 1, self.allocate()?);
                }
                ("text.backspace", std::cmp::Ordering::Less)
                    if count + 1 == ids.len()
                        && index > 0
                        && target.start == 0
                        && target.end == 0 =>
                {
                    ids.remove(index);
                }
                ("text.delete", std::cmp::Ordering::Less)
                    if count + 1 == ids.len() && index + 1 < ids.len() =>
                {
                    ids.remove(index + 1);
                }
                (_, std::cmp::Ordering::Equal) => {}
                _ => return Err("unexpected_structure"),
            }
            (Snapshot { native, ids }, None, Some(selection))
        };
        snapshot_valid(&snapshot)?;
        if snapshot == before {
            return Err("no_change");
        }
        if serde_json::to_vec(&before).unwrap().len() + serde_json::to_vec(&snapshot).unwrap().len()
            > HISTORY_BYTES
        {
            return Err("history_limit");
        }
        let op_id = env.op_id.clone();
        self.k = Some(Candidate {
            envelope: env,
            input,
            epoch: admission.epoch,
            author: admission.author.clone(),
            time: admission.time.clone(),
            snapshot,
            undo,
            selection,
        });
        Ok(Prepared::Pending { op_id })
    }
    /// Only a definitive host rejection/rollback may discard K. Unknown SQL
    /// outcomes MUST retain K and reconcile the same op_id with canonical SQL.
    /// Rejection changes neither confirmed P nor its participant undo history.
    pub fn reject(&mut self, op_id: &str) -> Result<Value> {
        let k = self.k.take().ok_or("no_pending")?;
        if k.envelope.op_id != op_id {
            self.k = Some(k);
            return Err("wrong_pending");
        }
        let result = json!({"state":"rejected","op_id":op_id,"revision":self.state.revision});
        self.rejected.insert(
            op_id.into(),
            Seen {
                input: k.input,
                result: result.clone(),
            },
        );
        Ok(result)
    }
}
fn body(native: &Value) -> Result<&Vec<Value>> {
    native
        .get("body")
        .and_then(Value::as_array)
        .filter(|b| !b.is_empty() && b.len() <= 4096)
        .ok_or("invalid_document")
}
fn snapshot_valid(s: &Snapshot) -> Result<()> {
    if serde_json::to_vec(&s.native)
        .map_err(|_| "invalid_document")?
        .len()
        > FRAME
        || body(&s.native)?.len() != s.ids.len()
        || s.ids.iter().any(|x| !id(x))
        || s.ids.iter().collect::<BTreeSet<_>>().len() != s.ids.len()
    {
        return Err("invalid_document");
    }
    Ok(())
}
fn history_size(history: &[Undo]) -> usize {
    history
        .iter()
        .map(|r| serde_json::to_vec(r).map_or(HISTORY_BYTES + 1, |v| v.len()))
        .sum()
}
fn keys(params: &Value, allowed: &[&str]) -> Result<()> {
    let fields = params.as_object().ok_or("invalid_params")?;
    if fields.keys().any(|key| !allowed.contains(&key.as_str())) {
        return Err("unknown_parameter");
    }
    Ok(())
}
const CATALOG_JSON: &str =
    include_str!("../../../../../tools/wordcraft-c1-engine/src/capabilities.json");
const CATALOG_PROVENANCE: &str =
    include_str!("../../../../../tools/wordcraft-c1-engine/src/catalog-provenance.json");
// BEGIN shared catalog validation: byte-identical in host and private helper.
fn supported_pattern(pattern: &str, text: &str) -> bool {
    match pattern {
        r"^[0-9A-Fa-f]{6}(?![\s\S])" => {
            text.len() == 6 && text.bytes().all(|b| b.is_ascii_hexdigit())
        }
        r"^[\x20-\x7e]{1,2}(?![\s\S])" => {
            (1..=2).contains(&text.len()) && text.bytes().all(|b| (0x20..=0x7e).contains(&b))
        }
        r"^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?(?![\s\S])" => {
            let data = text.trim_end_matches('=');
            text.len() % 4 == 0
                && text.len() - data.len() <= 2
                && data
                    .bytes()
                    .all(|b| b.is_ascii_alphanumeric() || b == b'+' || b == b'/')
        }
        _ => false,
    }
}
fn schema_accepts(schema: &Value, value: &Value, depth: usize) -> bool {
    if depth > 24 {
        return false;
    }
    let Some(s) = schema.as_object() else {
        return false;
    };
    if let Some(choices) = s.get("anyOf") {
        return s.len() == 1
            && choices.as_array().is_some_and(|items| {
                !items.is_empty()
                    && items.len() <= 32
                    && items
                        .iter()
                        .any(|item| schema_accepts(item, value, depth + 1))
            });
    }
    let Some(kind) = s.get("type").and_then(Value::as_str) else {
        return false;
    };
    let allowed: &[&str] = match kind {
        "object" => &["type", "properties", "required", "additionalProperties"],
        "array" => &["type", "items", "minItems", "maxItems"],
        "string" => &["type", "enum", "minLength", "maxLength", "pattern"],
        "number" | "integer" => &["type", "enum", "minimum", "maximum"],
        "boolean" | "null" => &["type", "enum"],
        _ => return false,
    };
    if s.keys().any(|key| !allowed.contains(&key.as_str())) {
        return false;
    }
    if let Some(choices) = s.get("enum") {
        if !choices
            .as_array()
            .is_some_and(|items| items.contains(value))
        {
            return false;
        }
    }
    match kind {
        "object" => {
            let (Some(object), Some(properties)) = (
                value.as_object(),
                s.get("properties").and_then(Value::as_object),
            ) else {
                return false;
            };
            if s.get("additionalProperties") != Some(&Value::Bool(false)) {
                return false;
            }
            if let Some(required) = s.get("required") {
                if !required.as_array().is_some_and(|keys| {
                    keys.iter().all(|key| {
                        key.as_str().is_some_and(|name| {
                            properties.contains_key(name) && object.contains_key(name)
                        })
                    })
                }) {
                    return false;
                }
            }
            object.iter().all(|(key, item)| {
                properties
                    .get(key)
                    .is_some_and(|rule| schema_accepts(rule, item, depth + 1))
            })
        }
        "array" => value.as_array().is_some_and(|items| {
            ["minItems", "maxItems"].iter().all(|key| {
                !s.contains_key(*key)
                    || s[*key].as_u64().is_some_and(|limit| {
                        if *key == "minItems" {
                            items.len() as u64 >= limit
                        } else {
                            items.len() as u64 <= limit
                        }
                    })
            }) && s.get("items").is_some_and(|rule| {
                items
                    .iter()
                    .all(|item| schema_accepts(rule, item, depth + 1))
            })
        }),
        "string" => value.as_str().is_some_and(|text| {
            let count = text.chars().count() as u64;
            ["minLength", "maxLength"].iter().all(|key| {
                !s.contains_key(*key)
                    || s[*key].as_u64().is_some_and(|limit| {
                        if *key == "minLength" {
                            count >= limit
                        } else {
                            count <= limit
                        }
                    })
            }) && (!s.contains_key("pattern")
                || s["pattern"]
                    .as_str()
                    .is_some_and(|pattern| supported_pattern(pattern, text)))
        }),
        "number" | "integer" => value.as_f64().is_some_and(|n| {
            n.is_finite()
                && (kind != "integer" || n.fract() == 0.0)
                && ["minimum", "maximum"].iter().all(|key| {
                    !s.contains_key(*key)
                        || s[*key].as_f64().is_some_and(|bound| {
                            bound.is_finite()
                                && if *key == "minimum" {
                                    n >= bound
                                } else {
                                    n <= bound
                                }
                        })
                })
        }),
        "boolean" => value.is_boolean(),
        "null" => value.is_null(),
        _ => false,
    }
}
fn catalog() -> &'static Value {
    static CATALOG: std::sync::OnceLock<Value> = std::sync::OnceLock::new();
    CATALOG.get_or_init(|| serde_json::from_str(CATALOG_JSON).expect("generated pinned catalog"))
}
fn catalog_entry(command: &str) -> Option<&'static Value> {
    let catalog = catalog();
    if catalog["engine"]["sha"] != "7ef2633a7e84956522d1ca47b99fe6feef16c1db"
        || catalog["schemaVersion"] != 1
    {
        return None;
    }
    let entries = catalog["commands"].as_array()?;
    let mut matches = entries
        .iter()
        .filter(|entry| entry["id"].as_str() == Some(command));
    let entry = matches.next()?;
    if matches.next().is_some() {
        return None;
    }
    Some(entry)
}
/// Explicit default-off product test profile: only the generated 72 isolated
/// fixture-qualified IDs. Product qualification flags remain unchanged. Current
/// participant authority and every existing stage/commit guard remain mandatory.
fn validate_catalog_candidate(
    command: &str,
    params: &Value,
) -> std::result::Result<(), &'static str> {
    let entry = catalog_entry(command).ok_or("command_denied")?;
    if entry["engineQualification"]["status"] != "qualified"
        || entry["engineQualification"]["scope"] != "isolated-c1-fixture"
        || entry["permanentlyForbidden"] != false
        || entry["schemaStatus"] != "exact"
        || entry["effect"] != "document-candidate"
        || !matches!(
            entry["group"].as_str(),
            Some("text-enter" | "character-format" | "paragraphs" | "lists")
        )
    {
        return Err("command_denied");
    }
    if !schema_accepts(&entry["parameterSchema"], params, 0) {
        return Err("invalid_params");
    }
    // Structural effect guard retained: multi-paragraph insertion has no reviewed
    // stable-ID mapping. Enter remains the explicit structural operation below.
    if command == "text.insert"
        && params["text"].as_str().is_some_and(|text| {
            text.is_empty() || text.len() > 32768 || text.contains(['\n', '\r', '\0', '\u{fffc}'])
        })
    {
        return Err("invalid_text");
    }
    Ok(())
}
fn catalog_identity() -> Value {
    let provenance: Value =
        serde_json::from_str(CATALOG_PROVENANCE).expect("generated catalog provenance");
    json!(format!(
        "wordcraft-c1-qualified72-v5:{}",
        provenance["catalogSha256"].as_str().expect("catalog SHA")
    ))
}
// END shared catalog validation.
fn validate_params(command: &str, p: &Value) -> Result<()> {
    // Host controls are not engine registry commands.
    match command {
        "document.read" => keys(p, &[]),
        "participant.undo" => {
            keys(p, &["op_id"])?;
            if p.get("op_id").is_some_and(|v| !v.as_str().is_some_and(id)) {
                return Err("invalid_params");
            }
            Ok(())
        }
        _ => validate_catalog_candidate(command, p),
    }
}

fn blocks(snapshot: &Snapshot) -> Result<BTreeMap<String, Value>> {
    Ok(snapshot
        .ids
        .iter()
        .cloned()
        .zip(body(&snapshot.native)?.iter().cloned())
        .collect())
}
fn header(native: &Value) -> Value {
    let mut h = native.clone();
    h.as_object_mut().unwrap().remove("body");
    h
}
fn reverse(current: &Snapshot, record: &Undo) -> Result<Snapshot> {
    let before = blocks(&record.before)?;
    let after = blocks(&record.after)?;
    let mut present = blocks(current)?;
    let keys: BTreeSet<_> = before.keys().chain(after.keys()).cloned().collect();
    for key in keys {
        if before.get(&key) != after.get(&key) {
            if present.get(&key) != after.get(&key) {
                return Err("undo_overlap");
            }
            if let Some(value) = before.get(&key) {
                present.insert(key, value.clone());
            } else {
                present.remove(&key);
            }
        }
    }
    let structural = record.before.ids != record.after.ids;
    if structural && current.ids != record.after.ids {
        return Err("undo_structure_overlap");
    }
    let ids = if structural {
        record.before.ids.clone()
    } else {
        current.ids.clone()
    };
    let mut native = current.native.clone();
    if header(&record.before.native) != header(&record.after.native) {
        if header(&current.native) != header(&record.after.native) {
            return Err("undo_metadata_overlap");
        }
        native = record.before.native.clone();
    }
    native["body"] = Value::Array(
        ids.iter()
            .map(|id| present.get(id).cloned().ok_or("undo_target_gone"))
            .collect::<Result<Vec<_>>>()?,
    );
    Ok(Snapshot { native, ids })
}

fn expected_engine() -> Value {
    let source: Value = serde_json::from_str(include_str!(
        "../../../../../tools/wordcraft-c1-engine/source.json"
    ))
    .expect("bundled public source identity");
    json!({"revision":source["rev"],"patch_sha256":source["patch_sha256"],"catalog":catalog_identity()})
}

#[cfg(test)]
const CATALOG_TEST_VECTORS: &str =
    include_str!("../../../../../tools/wordcraft-c1-engine/src/catalog-validation-vectors.json");

#[cfg(test)]
mod catalog_validation_tests {
    use super::*;
    #[test]
    fn all_reviewed_profile_examples_match_on_both_sides() {
        let vectors: Value = serde_json::from_str(CATALOG_TEST_VECTORS).unwrap();
        assert_eq!(vectors.as_array().unwrap().len(), 44);
        for vector in vectors.as_array().unwrap() {
            let entry = catalog_entry(vector["id"].as_str().unwrap()).unwrap();
            for params in vector["valid"].as_array().unwrap() {
                assert!(
                    schema_accepts(&entry["parameterSchema"], params, 0),
                    "valid {}: {}",
                    vector["id"],
                    params
                );
                let mut unknown = params.clone();
                unknown
                    .as_object_mut()
                    .unwrap()
                    .insert("unexpected".into(), json!(true));
                assert!(!schema_accepts(&entry["parameterSchema"], &unknown, 0));
            }
            for params in vector["invalid"].as_array().unwrap() {
                assert!(
                    !schema_accepts(&entry["parameterSchema"], params, 0),
                    "invalid {}: {}",
                    vector["id"],
                    params
                );
            }
        }
    }
    #[test]
    fn catalog_filters_effects_and_closed_parameters() {
        for command in [
            "file.open",
            "font.add",
            "tools.macros",
            "review.addToDictionary",
            "caret.set",
            "edit.undo",
            "unknown.command",
        ] {
            assert_eq!(
                validate_catalog_candidate(command, &json!({})),
                Err("command_denied")
            );
        }
        assert!(validate_catalog_candidate("para.bullets", &json!({"kind":"bullet"})).is_ok());
        assert!(validate_catalog_candidate("format.italic", &json!({"value":true})).is_ok());
        assert!(validate_catalog_candidate(
            "format.italic",
            &json!({"value":true,"command":"file.open"})
        )
        .is_err());
        assert!(validate_catalog_candidate("text.insert", &json!({"text":"a\nb"})).is_err());
        assert!(catalog()["commands"]
            .as_array()
            .unwrap()
            .iter()
            .all(|entry| entry["qualification"] == "unqualified"));
    }
    #[test]
    fn schema_combinations_and_unknown_rules_fail_closed() {
        assert!(!schema_accepts(
            &json!({"type":"number","enum":[1],"minimum":2}),
            &json!(1),
            0
        ));
        assert!(!schema_accepts(
            &json!({"type":"number","oneOf":[]}),
            &json!(1),
            0
        ));
        assert!(!schema_accepts(
            &json!({"anyOf":[{"type":"boolean"}],"enum":[false]}),
            &json!(true),
            0
        ));
        assert!(!schema_accepts(
            &json!({"type":"string","pattern":"unreviewed"}),
            &json!("unreviewed"),
            0
        ));
        assert!(!schema_accepts(
            &json!({"type":"string","pattern":r"^[0-9A-Fa-f]{6}(?![\s\S])"}),
            &json!("aabbcc\n"),
            0
        ));
        assert!(!schema_accepts(
            &json!({"type":"object","properties":{}}),
            &json!({}),
            0
        ));
    }
    #[test]
    fn exactly_fixture_qualified_ids_are_admitted_without_promoting_product_flags() {
        let mut admitted = 0;
        for entry in catalog()["commands"].as_array().unwrap() {
            let command = entry["id"].as_str().unwrap();
            assert_eq!(entry["qualification"], "unqualified");
            if entry["engineQualification"]["status"] == "qualified" {
                let params = &entry["engineQualification"]["testedParameters"];
                assert!(
                    validate_catalog_candidate(command, params).is_ok(),
                    "{command}: {params}"
                );
                let mut extra = params.clone();
                extra
                    .as_object_mut()
                    .unwrap()
                    .insert("unexpected".into(), json!(true));
                assert!(
                    validate_catalog_candidate(command, &extra).is_err(),
                    "{command}"
                );
                admitted += 1;
            } else {
                assert_eq!(
                    validate_catalog_candidate(command, &json!({})),
                    Err("command_denied"),
                    "{command}"
                );
            }
        }
        assert_eq!(admitted, 72);
        assert_eq!(catalog_identity(), json!("wordcraft-c1-qualified72-v5:9f1cb61f8ffb8e4890001aab1a9a222e327a8e98f4531ef50a7b66e28cc0f0cb"));
    }
}
