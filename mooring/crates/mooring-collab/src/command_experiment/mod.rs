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
        let (snapshot, undo) = if env.command == "participant.undo" {
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
            (snapshot, Some(index))
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
            (Snapshot { native, ids }, None)
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
fn validate_params(command: &str, p: &Value) -> Result<()> {
    match command {
        "text.insert" => {
            keys(p, &["text"])?;
            let t = p
                .get("text")
                .and_then(Value::as_str)
                .ok_or("invalid_text")?;
            if t.is_empty() || t.len() > INPUT / 2 || t.contains(['\n', '\r', '\0', '\u{fffc}']) {
                return Err("invalid_text");
            }
        }
        "text.delete" | "text.backspace" | "text.newParagraph" | "document.read" => keys(p, &[])?,
        "format.bold" => {
            keys(p, &["value"])?;
            if p.get("value").is_some_and(|v| !v.is_boolean()) {
                return Err("invalid_params");
            }
        }
        "format.font" => {
            keys(p, &["name"])?;
            let s = p
                .get("name")
                .and_then(Value::as_str)
                .ok_or("invalid_params")?;
            if s.is_empty() || s.len() > 200 {
                return Err("invalid_params");
            }
        }
        "format.size" => {
            keys(p, &["size"])?;
            let n = p
                .get("size")
                .and_then(Value::as_f64)
                .ok_or("invalid_params")?;
            if !(1.0..=200.0).contains(&n) {
                return Err("invalid_params");
            }
        }
        "para.align" => {
            keys(p, &["value"])?;
            if !matches!(
                p.get("value").and_then(Value::as_str),
                Some("left" | "center" | "right" | "justify")
            ) {
                return Err("invalid_params");
            }
        }
        "participant.undo" => {
            keys(p, &["op_id"])?;
            if p.get("op_id").is_some_and(|v| !v.as_str().is_some_and(id)) {
                return Err("invalid_params");
            }
        }
        _ => return Err("command_denied"),
    }
    Ok(())
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
    json!({"revision":source["rev"],"patch_sha256":source["patch_sha256"],"catalog":"wordcraft-c1-10-v1"})
}
