//! Bounded, connection-owned immutable projections. A cursor is never a grant:
//! the actor checks the original live participant before lookup and before send.
use crate::session_startup::ConfirmedDocument;
use mooring_protocol::{Counter, ReadRequest, Scope, TargetVersion};
use mooring_wordcraft::read::{ReadError, Snapshot};
use serde_json::{json, Value};
use std::collections::BTreeMap;

const RETAINED_BYTES: usize = 4 * 1024 * 1024;
const PARAGRAPHS_PER_PAGE: usize = 32;

#[derive(Debug, PartialEq, Eq)]
pub(crate) enum Failure {
    Invalid,
    Unavailable,
    Capacity,
    State,
}

#[cfg(test)]
mod tests {
    use super::*;
    use mooring_wordcraft::{adapter::WordCraftAdapter, Document};

    fn confirmed(revision: u64, marker: &str) -> ConfirmedDocument {
        let mut native: Value = serde_json::from_str(include_str!(
            "../../mooring-wordcraft/tests/fixtures/d1-native.json"
        ))
        .unwrap();
        // Unsupported native metadata makes this the complete read-only path,
        // including opaque integers which cannot survive a binary64 wire value.
        native["opaque"] = json!(u64::MAX);
        native["body"] = Value::Array((0..70).map(|index| {
            let text = format!("{marker} paragraph {index} Č😀 {}", "q\\\"".repeat(50));
            json!({"kind":"para", "text":text, "runs":[{"len":text.len()}], "section":{"opaque":u64::MAX}})
        }).collect());
        let document =
            Document::import(native, (0..70).map(|i| format!("stable-{i}")).collect()).unwrap();
        assert!(document.is_read_only());
        let identities = WordCraftAdapter.validate_document(&document).unwrap();
        let targets = identities
            .into_iter()
            .map(|identity| TargetVersion {
                target_id: identity.target_id,
                revision: Counter(3),
                structure_revision: Counter(1),
                deleted: false,
            })
            .collect();
        let checkpoint = serde_json::from_value(json!({
            "scope":{"workspace_id":"workspace","artifact_id":"artifact","application_id":"wordcraft"},
            "revision":revision.to_string(), "generation":"1",
            "format_version":mooring_wordcraft::profile::CHECKPOINT_FORMAT,
            "versions":crate::dispatch::wordcraft_versions(),
            "encoding":"base64", "state":"", "digest":"sha256:fixture", "assets":[]
        })).unwrap();
        ConfirmedDocument::committed(document, checkpoint, targets)
    }

    fn request(current: &ConfirmedDocument) -> ReadRequest {
        serde_json::from_value(json!({"type":"read_request","request_id":"first-read",
            "scope":current.checkpoint().scope,"session_id":"session",
            "revision":current.checkpoint().revision,"max_bytes":"65536"}))
        .unwrap()
    }

    #[test]
    fn readonly_pages_reassemble_one_lossless_revision_while_live_document_changes() {
        let original = confirmed(10, "original");
        let next = confirmed(11, "CHANGED");
        let mut request = request(&original);
        let mut reads = Reads::default();
        let mut body = Vec::new();
        let mut reconstructed = Value::Null;
        let mut ids = Vec::new();
        let mut page_count = 0;
        loop {
            let current = if page_count == 0 { &original } else { &next };
            let response = reads
                .respond(
                    "connection",
                    &format!("host-{page_count}"),
                    &request,
                    current,
                    65536,
                )
                .unwrap();
            assert!(serde_json::to_vec(&response).unwrap().len() <= 65536);
            let _: mooring_protocol::ReadResult = serde_json::from_value(response.clone()).unwrap();
            assert_eq!(response["revision"], "10");
            for item in response["items"].as_array().unwrap() {
                assert_eq!(
                    item["revision"], "3",
                    "target revision is not document revision"
                );
                let native: Value =
                    serde_json::from_str(item["data"]["native_json"].as_str().unwrap()).unwrap();
                if item["model_type"] == "wordcraft.native-header-json.v1" {
                    assert_eq!(native["opaque"].as_u64(), Some(u64::MAX));
                    reconstructed = native;
                } else {
                    assert_eq!(native["section"]["opaque"].as_u64(), Some(u64::MAX));
                    ids.push(item["target_id"].as_str().unwrap().to_owned());
                    body.push(native);
                }
            }
            page_count += 1;
            assert!(page_count < 100, "cursor did not advance");
            let Some(cursor) = response.get("next_cursor") else {
                // Last-page retries remain exact despite newer current DATA.
                assert_eq!(
                    reads
                        .respond("connection", "retry", &request, &next, 65536)
                        .unwrap(),
                    response
                );
                break;
            };
            request.cursor = Some(cursor.as_str().unwrap().into());
        }
        assert!(page_count > 2);
        reconstructed["body"] = Value::Array(body);
        assert_eq!(reconstructed, original.document().native().unwrap());
        assert_eq!(ids, original.document().block_ids().unwrap());
    }

    #[test]
    fn cursors_cannot_cross_connections_revisions_replacement_or_restart() {
        let current = confirmed(10, "original");
        let mut request = request(&current);
        let mut reads = Reads::default();
        // The complete real fixture header is about 29 KiB before wire
        // escaping. A 4 KiB request must refuse it, never truncate metadata.
        let mut too_small = request.clone();
        too_small.max_bytes = Counter(4096);
        assert_eq!(
            reads.respond("alice", "undersized-first", &too_small, &current, 4096),
            Err(Failure::Capacity)
        );
        assert!(reads.connections.is_empty());
        assert_eq!(reads.bytes, 0);
        let response = reads
            .respond("alice", "snapshot-a", &request, &current, 65536)
            .unwrap();
        request.cursor = Some(response["next_cursor"].as_str().unwrap().into());
        assert_eq!(
            reads.respond("bob", "r", &request, &current, 65536),
            Err(Failure::Unavailable)
        );
        request.revision = Some(Counter(11));
        assert_eq!(
            reads.respond("alice", "r", &request, &current, 65536),
            Err(Failure::Unavailable)
        );
        request.revision = Some(Counter(10));
        assert_eq!(
            Reads::default().respond("alice", "r", &request, &current, 65536),
            Err(Failure::Unavailable)
        );
        let mut fresh = request.clone();
        fresh.cursor = None;
        assert_eq!(
            reads.respond("alice", "failed-replacement", &fresh, &current, 1),
            Err(Failure::Capacity)
        );
        assert!(reads
            .respond("alice", "r", &request, &current, 65536)
            .is_ok());
        reads
            .respond("alice", "snapshot-b", &fresh, &current, 65536)
            .unwrap();
        assert_eq!(
            reads.respond("alice", "r", &request, &current, 65536),
            Err(Failure::Unavailable)
        );
        reads.forget("alice");
        assert_eq!(reads.bytes, 0);
    }
}

impl From<ReadError> for Failure {
    fn from(error: ReadError) -> Self {
        match error {
            ReadError::InvalidLimit | ReadError::UnknownCursor => Self::Invalid,
            ReadError::ItemTooLarge => Self::Capacity,
            ReadError::InvalidState => Self::State,
        }
    }
}

struct Frozen {
    id: String,
    scope: Scope,
    revision: Counter,
    digest: String,
    native: Snapshot,
    targets: BTreeMap<String, TargetVersion>,
    bytes: usize,
}

#[derive(Default)]
pub(crate) struct Reads {
    connections: BTreeMap<String, Frozen>,
    bytes: usize,
}

impl Reads {
    pub(crate) fn forget(&mut self, connection: &str) {
        if let Some(snapshot) = self.connections.remove(connection) {
            self.bytes -= snapshot.bytes;
        }
    }

    pub(crate) fn clear(&mut self) {
        self.connections.clear();
        self.bytes = 0;
    }

    pub(crate) fn respond(
        &mut self,
        connection: &str,
        original_request: &str,
        request: &ReadRequest,
        current: &ConfirmedDocument,
        envelope_budget: usize,
    ) -> Result<Value, Failure> {
        if request.scope != current.checkpoint().scope
            || request
                .query
                .as_ref()
                .is_some_and(|query| !query.is_empty())
            || request.max_bytes.0 == 0
            || request.max_bytes.0 > 262_144
        {
            return Err(Failure::Invalid);
        }
        let budget = (request.max_bytes.0 as usize).min(envelope_budget);
        if let Some(cursor) = &request.cursor {
            let [snapshot_id, after]: [String; 2] =
                serde_json::from_str(cursor).map_err(|_| Failure::Invalid)?;
            let frozen = self
                .connections
                .get(connection)
                .ok_or(Failure::Unavailable)?;
            if snapshot_id != frozen.id
                || request.revision != Some(frozen.revision)
                || frozen.scope != request.scope
            {
                return Err(Failure::Unavailable);
            }
            return frozen.response(
                request,
                (!after.is_empty()).then_some(after.as_str()),
                false,
                budget,
            );
        }
        if request
            .revision
            .is_some_and(|revision| revision != current.checkpoint().revision)
        {
            return Err(Failure::Unavailable);
        }
        let native = Snapshot::capture(current.document())?;
        let targets: BTreeMap<String, TargetVersion> = current
            .targets()
            .iter()
            .map(|target| (target.target_id.clone(), target.clone()))
            .collect();
        let target_bytes = serde_json::to_vec(&targets)
            .map_err(|_| Failure::State)?
            .len();
        let metadata_bytes = serde_json::to_vec(&(
            connection,
            &request.scope,
            original_request,
            current.checkpoint().revision,
            &current.checkpoint().digest,
        ))
        .map_err(|_| Failure::State)?
        .len();
        // Exact serialized accounting unit [native_projection, targets, metadata].
        // Includes duplicate target-map keys and the retained connection/scope.
        // The four extra bytes are outer brackets and two commas, not RSS.
        let bytes = native
            .encoded_bytes()
            .checked_add(target_bytes)
            .and_then(|size| size.checked_add(metadata_bytes))
            .and_then(|size| size.checked_add(4))
            .ok_or(Failure::Capacity)?;
        let previous = self.connections.get(connection).map_or(0, |old| old.bytes);
        let remaining = self.bytes.checked_sub(previous).ok_or(Failure::State)?;
        if bytes > RETAINED_BYTES.saturating_sub(remaining) {
            return Err(Failure::Capacity);
        }
        let frozen = Frozen {
            id: original_request.into(),
            scope: request.scope.clone(),
            revision: current.checkpoint().revision,
            digest: current.checkpoint().digest.clone(),
            native,
            targets,
            bytes,
        };
        // Failed first-page creation preserves the old read. A successful new
        // first page explicitly replaces it; an old cursor then fails closed.
        let response = frozen.response(request, None, true, budget)?;
        self.connections.insert(connection.into(), frozen);
        self.bytes = remaining + bytes;
        Ok(response)
    }
}

impl Frozen {
    fn cursor(&self, after: Option<&str>) -> Result<String, Failure> {
        serde_json::to_string(&[self.id.as_str(), after.unwrap_or("")]).map_err(|_| Failure::State)
    }

    fn response(
        &self,
        request: &ReadRequest,
        after: Option<&str>,
        include_header: bool,
        budget: usize,
    ) -> Result<Value, Failure> {
        let page = self.native.page(after, PARAGRAPHS_PER_PAGE, 262_144)?;
        let mut header = Vec::new();
        if include_header {
            let target = self
                .targets
                .get("wordcraft:document-metadata")
                .ok_or(Failure::State)?;
            // Native JSON stays a lossless string inside the JCS wire profile:
            // opaque application integers must not pass through binary64.
            header.push(json!({
                "target_id":"wordcraft:document-metadata",
                "model_type":"wordcraft.native-header-json.v1",
                "revision":target.revision,
                "data":{
                    "native_json":serde_json::to_string(&self.native.header(262_144)?).map_err(|_| Failure::State)?,
                    "checkpoint_digest":self.digest,
                    "target":target
                }
            }));
        }
        let mut paragraphs = Vec::new();
        for paragraph in &page.paragraphs {
            let target = self
                .targets
                .get(&paragraph.target_id)
                .ok_or(Failure::State)?;
            paragraphs.push(json!({
                "target_id":paragraph.target_id,
                "model_type":"wordcraft.native-paragraph-json.v1",
                "revision":target.revision,
                "data":{
                    "native_json":serde_json::to_string(&paragraph.native).map_err(|_| Failure::State)?,
                    "target":target
                }
            }));
        }
        // Check the complete serialized ReadResult, including escaping,
        // metadata and cursor. The caller has already reserved the exact
        // private-envelope overhead and checks the final frame before write.
        let total = paragraphs.len();
        for retained in (0..=total).rev() {
            if retained == 0 && header.is_empty() && total != 0 {
                return Err(Failure::Capacity);
            }
            let more = retained < total || page.next_after.is_some();
            let last = if retained == 0 {
                after
            } else {
                Some(page.paragraphs[retained - 1].target_id.as_str())
            };
            let mut items = header.clone();
            items.extend(paragraphs[..retained].iter().cloned());
            let mut response = json!({
                "type":"read_result", "request_id":request.request_id,
                "scope":self.scope, "revision":self.revision, "items":items
            });
            if more {
                response["next_cursor"] = Value::String(self.cursor(last)?);
            }
            if serde_json::to_vec(&response)
                .map_err(|_| Failure::State)?
                .len()
                <= budget
            {
                // Retain completed snapshots for exact last-page retry. They
                // are released on replacement, participant leave or close.
                return Ok(response);
            }
        }
        Err(Failure::Capacity)
    }
}
