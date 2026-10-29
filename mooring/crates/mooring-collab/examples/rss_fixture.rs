//! EXPLICIT TEST FIXTURE: synthetic read-only authority. Never production-selected.
use mooring_collab::{private_rpc::bootstrap, read_frame, write_frame, MAX_FRAME};
use mooring_core::lease::{boottime, DeliveryGate};
use mooring_protocol::{LifecycleRequest, ReadRequest, Scope, SessionJoin, SessionLeave};
use serde_json::{json, Value};
use std::{
    collections::{BTreeMap, VecDeque},
    io,
    time::Duration,
};
use yrs::{Array, Doc, GetString, Map, ReadTxn, Text, TextPrelim, Transact};

const MAX_PARTICIPANTS: usize = 100;
const MAX_DOCUMENTS: usize = 100;
const MAX_QUEUE_FRAMES: usize = 4;
const MAX_QUEUE_BYTES: usize = MAX_FRAME;
fn invalid() -> io::Error {
    io::Error::new(io::ErrorKind::InvalidData, "invalid fixture input")
}
fn key(scope: &Scope) -> String {
    format!(
        "{}\0{}\0{}",
        scope.application_id, scope.workspace_id, scope.artifact_id
    )
}
struct Document {
    doc: Doc,
    scope: Scope,
}
struct Participant {
    document: String,
    gate: DeliveryGate,
    queue: VecDeque<Vec<u8>>,
    queued_bytes: usize,
}
fn load(scope: Scope, source: &str) -> io::Result<Document> {
    let source: Value = serde_json::from_str(source).map_err(|_| invalid())?;
    let paragraphs = source["paragraphs"].as_array().ok_or_else(invalid)?;
    if paragraphs.len() > 4096 {
        return Err(invalid());
    }
    let doc = mooring_text::new_document();
    let order = doc.get_or_insert_array("order");
    let blocks = doc.get_or_insert_map("blocks");
    let metadata = doc.get_or_insert_map("paragraph_metadata");
    let mut ids = std::collections::BTreeSet::new();
    {
        let mut tx = doc.transact_mut();
        for paragraph in paragraphs {
            let id = paragraph["id"]
                .as_str()
                .filter(|id| !id.is_empty() && id.len() <= 200)
                .ok_or_else(invalid)?;
            if !ids.insert(id) {
                return Err(invalid());
            }
            let text = paragraph["text"].as_str().ok_or_else(invalid)?;
            order.push_back(&mut tx, id.to_owned());
            metadata.insert(&mut tx, id, paragraph["metadata"].to_string());
            let shared = blocks.insert(&mut tx, id, TextPrelim::new(text));
            let length = text.encode_utf16().count() as u64;
            for run in paragraph["runs"].as_array().into_iter().flatten() {
                let start = run["start"].as_u64().ok_or_else(invalid)?;
                let len = run["length"].as_u64().ok_or_else(invalid)?;
                if start > length || len > length - start {
                    return Err(invalid());
                }
                let mut attrs = yrs::types::Attrs::new();
                for (name, value) in run["attrs"].as_object().ok_or_else(invalid)? {
                    attrs.insert(
                        name.as_str().into(),
                        yrs::Any::from_json(&value.to_string()).map_err(|_| invalid())?,
                    );
                }
                shared.format(&mut tx, start as u32, len as u32, attrs);
            }
        }
    }
    Ok(Document { doc, scope })
}
fn snapshot(document: &Document) -> Value {
    let tx = document.doc.transact();
    let blocks = tx.get_map("blocks").unwrap();
    let order = tx.get_array("order").unwrap();
    let paragraphs: Vec<_> = order
        .iter(&tx)
        .map(|id| {
            let id = id.to_string(&tx);
            let text = match blocks.get(&tx, &id) {
                Some(yrs::Out::YText(text)) => text.get_string(&tx),
                _ => String::new(),
            };
            json!({"id":id,"text":text})
        })
        .collect();
    json!({"paragraphs":paragraphs})
}
fn error(request: &str, code: &str) -> Value {
    json!({"type":"protocol_error","request_id":request,"reason":{"code":code,"message":"explicit mock fixture rejection","retry":"do_not_retry"}})
}
#[cfg(target_os = "linux")]
fn run() -> io::Result<()> {
    use std::os::{fd::FromRawFd, unix::net::UnixStream};
    if std::env::args().skip(1).collect::<Vec<_>>() != ["--rpc-fd", "3"] {
        return Err(invalid());
    }
    let flags = unsafe { libc::fcntl(3, libc::F_GETFD) };
    if flags == -1 || unsafe { libc::fcntl(3, libc::F_SETFD, flags | libc::FD_CLOEXEC) } == -1 {
        return Err(io::Error::last_os_error());
    }
    let mut stream = unsafe { UnixStream::from_raw_fd(3) };
    stream.peer_addr()?;
    bootstrap(&mut stream, Duration::from_secs(5))?;
    let mut documents = BTreeMap::<String, Document>::new();
    let mut participants = BTreeMap::<String, Participant>::new();
    let mut next_id = 0u64;
    while let Some(raw) = read_frame(&mut stream)? {
        let value: Value = serde_json::from_slice(&raw).map_err(|_| invalid())?;
        let response = match value["type"].as_str() {
            Some("lifecycle_request") => {
                let request: LifecycleRequest =
                    serde_json::from_slice(&raw).map_err(|_| invalid())?;
                let document_key = key(&request.scope);
                let state = match request.action {
                    mooring_protocol::LifecycleRequestAction::Open => {
                        if documents.len() >= MAX_DOCUMENTS || documents.contains_key(&document_key)
                        {
                            return Err(invalid());
                        }
                        let checkpoint = request.checkpoint.ok_or_else(invalid)?;
                        if checkpoint.scope != request.scope {
                            return Err(invalid());
                        }
                        documents.insert(
                            document_key,
                            load(request.scope.clone(), &checkpoint.state)?,
                        );
                        "ready"
                    }
                    mooring_protocol::LifecycleRequestAction::Close => {
                        participants.retain(|_, participant| participant.document != document_key);
                        documents.remove(&document_key);
                        "closed"
                    }
                    _ => return Err(invalid()),
                };
                json!({"type":"lifecycle_result","request_id":request.request_id,"scope":request.scope,"session_id":request.session_id,"generation":"1","state":state,"revision":"0"})
            }
            Some("session_join") => {
                let request: SessionJoin = serde_json::from_slice(&raw).map_err(|_| invalid())?;
                // Fixed synthetic ticket names an existing generic document. This
                // is NOT ticket validation or a production authority boundary.
                let document_key = request
                    .ticket
                    .strip_prefix("MOCK-READ-ONLY:")
                    .ok_or_else(invalid)?;
                let document = documents.get(document_key).ok_or_else(invalid)?;
                if participants.len() >= MAX_PARTICIPANTS
                    || request.versions != mooring_collab::private_rpc::versions()
                {
                    return Err(invalid());
                }
                next_id = next_id.checked_add(1).ok_or_else(invalid)?;
                let session = format!("mock-session-{next_id}");
                let now = boottime()?;
                let mut gate = DeliveryGate::default();
                let renewal = gate.request(now).map_err(|_| invalid())?;
                // Immediate mock host grant, measured from the actual request
                // send tick. Real admission/lease callbacks remain unimplemented.
                gate.renew(renewal, Duration::from_secs(60), now)
                    .map_err(|_| invalid())?;
                participants.insert(
                    session.clone(),
                    Participant {
                        document: document_key.into(),
                        gate,
                        queue: VecDeque::with_capacity(MAX_QUEUE_FRAMES),
                        queued_bytes: 0,
                    },
                );
                json!({"type":"session_joined","request_id":request.request_id,"scope":document.scope,"session_id":session,"participant_ref":format!("mock-participant-{next_id}"),"generation":"1","permission_version":"1","versions":request.versions,"gate_id":format!("mock-gate-{next_id}")})
            }
            Some("read_request") => {
                let request: ReadRequest = serde_json::from_slice(&raw).map_err(|_| invalid())?;
                let participant = participants
                    .get_mut(&request.session_id)
                    .ok_or_else(invalid)?;
                if participant.document != key(&request.scope) {
                    return Err(invalid());
                }
                if !participant.gate.may_deliver(boottime()?) {
                    error(&request.request_id, "lease_expired")
                } else {
                    let document = &documents[&participant.document];
                    let result = json!({"type":"read_result","request_id":request.request_id,"scope":document.scope,"revision":"0","items":[{"target_id":"generic-document","model_type":"fixture.paragraphs","revision":"0","data":snapshot(document)}]});
                    let encoded = serde_json::to_vec(&result).map_err(|_| invalid())?;
                    if encoded.len() > request.max_bytes.0 as usize
                        || encoded.len() > MAX_QUEUE_BYTES - participant.queued_bytes
                        || participant.queue.len() >= MAX_QUEUE_FRAMES
                    {
                        error(&request.request_id, "capacity_exhausted")
                    } else {
                        participant.queued_bytes += encoded.len();
                        participant.queue.push_back(encoded);
                        // Recheck immediately before actual content send, after
                        // encoding/queueing; no stale grant can bypass the gate.
                        if !participant.gate.may_deliver(boottime()?) {
                            participant.queue.clear();
                            participant.queued_bytes = 0;
                            error(&request.request_id, "lease_expired")
                        } else {
                            let frame = participant.queue.pop_front().unwrap();
                            participant.queued_bytes -= frame.len();
                            write_frame(&mut stream, &frame)?;
                            continue;
                        }
                    }
                }
            }
            Some("session_leave") => {
                let request: SessionLeave = serde_json::from_slice(&raw).map_err(|_| invalid())?;
                let participant = participants.get(&request.session_id).ok_or_else(invalid)?;
                if participant.document != key(&request.scope) {
                    return Err(invalid());
                }
                participants.remove(&request.session_id);
                continue; // one-way transport disconnect notification
            }
            _ => return Err(invalid()), // no writes, grants, or Saved dispatcher
        };
        write_frame(
            &mut stream,
            &serde_json::to_vec(&response).map_err(|_| invalid())?,
        )?;
    }
    Ok(())
}
#[cfg(not(target_os = "linux"))]
fn run() -> io::Result<()> {
    Err(invalid())
}
fn main() {
    if run().is_err() {
        std::process::exit(1);
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use mooring_core::batch::Tick;
    #[test]
    fn generic_projection_keeps_text_and_rejects_duplicate_ids_or_bad_run_bounds() {
        let scope = Scope {
            application_id: "fixture".into(),
            workspace_id: "workspace".into(),
            artifact_id: "artifact".into(),
        };
        let source = json!({"paragraphs":[{"id":"p1","text":"Žluťoučký 🦀","metadata":{"style":"heading"},"runs":[{"start":0,"length":2,"attrs":{"bold":true}}]}]});
        let document = load(scope.clone(), &source.to_string()).unwrap();
        assert_eq!(snapshot(&document)["paragraphs"][0]["text"], "Žluťoučký 🦀");
        let mut duplicate = source.clone();
        duplicate["paragraphs"]
            .as_array_mut()
            .unwrap()
            .push(source["paragraphs"][0].clone());
        assert!(load(scope.clone(), &duplicate.to_string()).is_err());
        let mut invalid = source;
        invalid["paragraphs"][0]["runs"][0]["length"] = json!(u64::MAX);
        assert!(load(scope, &invalid.to_string()).is_err());
    }
    #[test]
    fn deterministic_expiry_closes_mock_delivery_at_exclusive_deadline() {
        let mut gate = DeliveryGate::default();
        let request = gate.request(Tick(Duration::from_secs(1))).unwrap();
        gate.renew(
            request,
            Duration::from_secs(60),
            Tick(Duration::from_secs(1)),
        )
        .unwrap();
        assert!(gate.may_deliver(Tick(Duration::from_secs(60))));
        assert!(!gate.may_deliver(Tick(Duration::from_secs(61))));
        assert!(!gate.may_deliver(Tick(Duration::from_secs(62))));
    }
}
