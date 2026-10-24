//! Concrete private session preparation. All source fields are caller-trusted
//! host DATA; only actual negotiated FD/session binding permits this path.
use crate::dispatch::invalid;
use mooring_core::{
    adapter::{CausalPrepareAdapter, Prepared},
    causal::CausalPrepareContext,
    checkpoint_description::DescribeCheckpoint,
};
use mooring_protocol::{
    self as protocol, boundary::decode_base64, canonical::fingerprint_atomic, checkpoint, control,
    *,
};
use mooring_wordcraft::{
    adapter::{Candidate, WordCraftAdapter},
    experimental_draft::Proposal,
    experimental_human_basis as basis,
    experimental_human_current::HostCommittedOwnOperation,
};
use std::{collections::BTreeSet, io};

const RAW: usize = 262_144;
const MAX53: u64 = (1u64 << 53) - 1;
#[derive(serde::Deserialize)]
#[serde(deny_unknown_fields)]
struct Payload {
    kind: String,
    operation_id: String,
    origin: String,
    update: String,
    authored_frontier: String,
    targets: Vec<String>,
    authored_checkpoint_state_digest: String,
}
struct Original {
    proposal: Proposal,
    authored: String,
    base: u64,
}
struct Delta {
    wire: SessionSavedNativeDelta,
    bytes: Vec<u8>,
}
struct Own {
    wire: SessionSavedHumanPrefix,
    original: Original,
}
struct PreparedDelta {
    wire: SessionPreparedNativeDelta,
    bytes: Vec<u8>,
}
struct PreparedOwn {
    wire: SessionPreparedHumanPrefix,
    original: Original,
}
pub(crate) struct Human {
    wire: SessionHumanPrepareContext,
    original: Original,
    basis: Vec<u8>,
    replay: Option<Vec<u8>>,
    saved: Vec<Delta>,
    own: Vec<Own>,
    history: Vec<Delta>,
    prepared: Vec<PreparedDelta>,
    prepared_own: Vec<PreparedOwn>,
}
fn counter(text: &str) -> io::Result<u64> {
    let n = text
        .parse::<u64>()
        .map_err(|_| invalid("invalid human source counter"))?;
    if n == 0 || n > MAX53 || n.to_string() != text {
        return Err(invalid("invalid human source counter"));
    }
    Ok(n)
}
fn source_counter(n: Counter) -> io::Result<u64> {
    if n.0 == 0 || n.0 > MAX53 {
        return Err(invalid("human source counter range"));
    }
    Ok(n.0)
}
fn digest(text: &str) -> io::Result<()> {
    if text.len() != 71
        || !text.starts_with("sha256:")
        || !text.as_bytes()[7..]
            .iter()
            .all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(b))
    {
        return Err(invalid("invalid raw-state digest"));
    }
    Ok(())
}
fn decode_original(
    change: &AtomicChange,
    scope: &Scope,
    origin: u64,
    participant: &str,
) -> io::Result<Original> {
    let bytes = serde_json::to_vec(change).map_err(|_| invalid("original atomic encoding"))?;
    if !fingerprint_atomic(scope, &bytes)
        .map_err(|_| invalid("original fingerprint"))?
        .matches_wire()
        || change.operations.len() != 1
    {
        return Err(invalid("invalid whole human original"));
    }
    let op = &change.operations[0];
    let p: Payload = serde_json::from_value(serde_json::Value::Object(op.payload.clone()))
        .map_err(|_| invalid("closed human payload"))?;
    if p.kind != "humancrdt.update"
        || p.operation_id != op.op_id
        || op.participant_ref != participant
        || counter(&p.origin)? != origin
    {
        return Err(invalid("human original source binding"));
    }
    digest(&p.authored_checkpoint_state_digest)?;
    let update = decode_base64(&p.update, RAW).map_err(|_| invalid("human original update"))?;
    let frontier =
        decode_base64(&p.authored_frontier, RAW).map_err(|_| invalid("human original frontier"))?;
    if update.is_empty() || frontier.is_empty() || p.targets.is_empty() || p.targets.len() > 256 {
        return Err(invalid("human original capacity"));
    }
    let targets: BTreeSet<_> = p.targets.iter().collect();
    if targets.len() != p.targets.len() {
        return Err(invalid("duplicate human target"));
    }
    Ok(Original {
        proposal: Proposal {
            origin,
            operation_id: p.operation_id,
            update,
            authored_frontier: frontier,
            targets: p.targets,
        },
        authored: p.authored_checkpoint_state_digest,
        base: op.base_revision.0,
    })
}
fn checkpoint_bytes(cp: &Checkpoint, open: &SessionOpen) -> io::Result<Vec<u8>> {
    if cp.scope != open.scope || cp.versions != open.versions {
        return Err(invalid("human checkpoint scope/tuple"));
    }
    let verified = checkpoint::verify(
        cp,
        checkpoint::Limits {
            decoded_bytes: control::MAX_CHECKPOINT_BYTES,
            preimage_bytes: control::MAX_CONTROL_BYTES,
        },
    )
    .map_err(|_| invalid("human full checkpoint identity"))?;
    let metadata = WordCraftAdapter
        .describe_checkpoint(verified.state())
        .map_err(|_| invalid("unqualified human checkpoint metadata"))?;
    if metadata.format_version != cp.format_version
        || metadata.versions != cp.versions
        || metadata.assets != cp.assets
    {
        return Err(invalid("human checkpoint metadata binding"));
    }
    Ok(verified.state().to_vec())
}
fn delta(wire: &SessionSavedNativeDelta) -> io::Result<Delta> {
    source_counter(wire.writer_origin)?;
    source_counter(wire.source_order)?;
    digest(&wire.checkpoint_state_digest)?;
    let bytes = decode_base64(&wire.update, RAW).map_err(|_| invalid("Saved original encoding"))?;
    if bytes.is_empty() {
        return Err(invalid("empty Saved original"));
    }
    Ok(Delta {
        wire: wire.clone(),
        bytes,
    })
}
impl Human {
    /// Bind EVERYTHING before the generic causal dispatcher can return a
    /// terminal read decision. This authenticates no JSON: `open`/`fence` must
    /// come from this actual inherited session's correlated startup.
    pub(crate) fn parse(
        request: &PrepareAtomicCausal,
        open: &SessionOpen,
        fence: &Fence,
    ) -> io::Result<Option<Self>> {
        let human =
            request.change.operations.iter().any(|op| {
                op.payload.get("kind").and_then(|v| v.as_str()) == Some("humancrdt.update")
            });
        let Some(wire) = request.human_context.as_ref() else {
            return if human {
                Err(invalid("missing human source context"))
            } else {
                Ok(None)
            };
        };
        if !human
            || wire.session_id != open.session_id
            || wire.runtime_ref != open.runtime_ref
            || wire.fence != *fence
            || request.scope != open.scope
            || request.selected != open.versions
            || request.change.operations.len() != 1
            || wire.op_id != request.change.operations[0].op_id
        {
            return Err(invalid("human session binding"));
        }
        let origin = source_counter(wire.origin)?;
        source_counter(wire.source_order)?;
        source_counter(wire.next_unsettled_order)?;
        let current = &request.change.operations[0];
        let original = decode_original(
            &request.change,
            &open.scope,
            origin,
            &current.participant_ref,
        )?;
        if wire.original_basis.revision.0 != original.base
            || original.base > request.context.document_revision.0
        {
            return Err(invalid("original observed revision binding"));
        }
        let basis = checkpoint_bytes(&wire.original_basis, open)?;
        let replay = wire
            .replay_basis
            .as_ref()
            .map(|cp| {
                if cp.revision.0 > original.base {
                    return Err(invalid("replay basis order"));
                }
                checkpoint_bytes(cp, open)
            })
            .transpose()?;
        let saved = wire
            .saved_transcript
            .iter()
            .map(delta)
            .collect::<io::Result<Vec<_>>>()?;
        let history = wire
            .own_history
            .iter()
            .map(delta)
            .collect::<io::Result<Vec<_>>>()?;
        let mut own = Vec::new();
        for entry in &wire.own_prefix {
            if entry.source_ref != wire.source_ref {
                return Err(invalid("own Saved source binding"));
            }
            source_counter(entry.source_order)?;
            own.push(Own {
                wire: entry.clone(),
                original: decode_original(
                    &entry.change,
                    &open.scope,
                    origin,
                    &current.participant_ref,
                )?,
            });
        }
        let mut prepared = Vec::new();
        let mut prepared_own = Vec::new();
        if let Some(group) = &wire.prepared_prefix {
            digest(&group.transaction_state_digest)?;
            if group.transaction_revision.0 > request.context.document_revision.0 {
                return Err(invalid("transaction prefix revision"));
            }
            for entry in &group.deltas {
                source_counter(entry.writer_origin)?;
                source_counter(entry.source_order)?;
                digest(&entry.checkpoint_state_digest)?;
                let bytes = decode_base64(&entry.update, RAW)
                    .map_err(|_| invalid("prepared original encoding"))?;
                if bytes.is_empty() {
                    return Err(invalid("empty prepared original"));
                }
                prepared.push(PreparedDelta {
                    wire: entry.clone(),
                    bytes,
                });
            }
            for entry in &group.own_prefix {
                if entry.source_ref != wire.source_ref {
                    return Err(invalid("own prepared source binding"));
                }
                source_counter(entry.source_order)?;
                prepared_own.push(PreparedOwn {
                    wire: entry.clone(),
                    original: decode_original(
                        &entry.change,
                        &open.scope,
                        origin,
                        &current.participant_ref,
                    )?,
                });
            }
        }
        let transaction_revision = wire
            .prepared_prefix
            .as_ref()
            .map(|p| p.transaction_revision.0)
            .unwrap_or(request.context.document_revision.0);
        if original.base > transaction_revision {
            return Err(invalid("original basis is not a durable observed cut"));
        }
        let replay_revision = wire
            .replay_basis
            .as_ref()
            .unwrap_or(&wire.original_basis)
            .revision
            .0;
        let mut prior = replay_revision;
        for entry in &saved {
            if entry.wire.revision.0 <= prior
                || entry.wire.revision.0 > transaction_revision
                || entry.wire.reference == wire.original_reference
                || (entry.wire.writer_origin.0 == origin
                    && entry.wire.source_ref != wire.source_ref)
            {
                return Err(invalid("actual Saved history order/source"));
            }
            prior = entry.wire.revision.0;
        }
        if prior != transaction_revision {
            return Err(invalid(
                "actual Saved transcript does not reach transaction start",
            ));
        }
        for entry in &history {
            if entry.wire.revision.0 == 0
                || entry.wire.revision.0 > transaction_revision
                || entry.wire.reference == wire.original_reference
                || entry.wire.writer_origin.0 != origin
                || entry.wire.source_ref != wire.source_ref
                || entry.wire.source_order.0 >= wire.source_order.0
            {
                return Err(invalid("committed own history source/order"));
            }
        }
        let mut prior = transaction_revision;
        for entry in &prepared {
            if entry.wire.prospective_revision.0 <= prior
                || entry.wire.prospective_revision.0 > request.context.document_revision.0
                || entry.wire.reference == wire.original_reference
                || (entry.wire.writer_origin.0 == origin
                    && entry.wire.source_ref != wire.source_ref)
            {
                return Err(invalid("prepared candidate order/source"));
            }
            prior = entry.wire.prospective_revision.0;
        }
        if prior != request.context.document_revision.0 {
            return Err(invalid(
                "prepared prefix does not reach current working revision",
            ));
        }
        let count = saved
            .len()
            .checked_add(history.len())
            .and_then(|n| n.checked_add(prepared.len()))
            .ok_or_else(|| invalid("human history count"))?;
        let bytes = saved
            .iter()
            .map(|v| v.bytes.len())
            .chain(history.iter().map(|v| v.bytes.len()))
            .chain(prepared.iter().map(|v| v.bytes.len()))
            .try_fold(0usize, |n, v| n.checked_add(v))
            .ok_or_else(|| invalid("human history bytes"))?;
        if count > 256 || bytes > RAW || own.len() + prepared_own.len() > 256 {
            return Err(invalid("combined human history capacity"));
        }
        Ok(Some(Self {
            wire: wire.as_ref().clone(),
            original,
            basis,
            replay,
            saved,
            own,
            history,
            prepared,
            prepared_own,
        }))
    }
}

impl Human {
    fn with_authority<T>(
        &self,
        current: &[u8],
        consume: impl FnOnce(basis::HostConfirmedBasis<'_>) -> io::Result<T>,
    ) -> io::Result<T> {
        let own_origin = source_counter(self.wire.origin)?;
        let saved = self
            .saved
            .iter()
            .map(|entry| {
                basis::HostSavedDelta::from_trusted_committed_store(
                    entry.wire.reference.as_bytes(),
                    source_counter(entry.wire.writer_origin)?,
                    &entry.bytes,
                    &entry.wire.targets,
                )
                .and_then(|delta| delta.with_saved_revision(entry.wire.revision.0))
                .map_err(|_| invalid("unqualified Saved native witness"))
            })
            .collect::<io::Result<Vec<_>>>()?;
        let own = self
            .own
            .iter()
            .map(|entry| {
                let matching = self
                    .saved
                    .iter()
                    .find(|saved| saved.wire.reference == entry.wire.reference)
                    .ok_or_else(|| invalid("missing own Saved record"))?;
                if matching.wire.source_ref != self.wire.source_ref
                    || matching.wire.source_order != entry.wire.source_order
                    || source_counter(matching.wire.writer_origin)? != own_origin
                    || matching.bytes != entry.original.proposal.update
                    || matching.wire.targets != entry.original.proposal.targets
                {
                    return Err(invalid("own Saved provenance binding"));
                }
                let sealed = basis::HostSealedOriginal::from_trusted_original_store(
                    &entry.original.proposal,
                    entry.wire.reference.as_bytes(),
                    source_counter(entry.wire.source_order)?,
                )
                .map_err(|_| invalid("own Saved original reference"))?
                .with_original_base_revision(entry.original.base);
                basis::HostSavedOwnPrefix::from_trusted_live_slot(
                    sealed,
                    &entry.original.authored,
                    &matching.wire.checkpoint_state_digest,
                )
                .map_err(|_| invalid("own Saved cut binding"))
            })
            .collect::<io::Result<Vec<_>>>()?;
        let mut views = basis::HostBasisViews::from_trusted_live_store(
            &self.basis,
            self.wire.original_basis.revision.0,
        );
        if let (Some(bytes), Some(cp)) = (&self.replay, &self.wire.replay_basis) {
            views = views
                .with_replay_basis(bytes, cp.revision.0)
                .map_err(|_| invalid("unqualified replay basis"))?;
        }
        let prepared = self
            .prepared
            .iter()
            .map(|entry| {
                basis::HostPreparedDelta::from_trusted_writer_candidate(
                    entry.wire.reference.as_bytes(),
                    source_counter(entry.wire.writer_origin)?,
                    &entry.bytes,
                    &entry.wire.targets,
                    entry.wire.prospective_revision.0,
                    &entry.wire.checkpoint_state_digest,
                )
                .map_err(|_| invalid("unqualified prospective candidate witness"))
            })
            .collect::<io::Result<Vec<_>>>()?;
        let prepared_own = self
            .prepared_own
            .iter()
            .map(|entry| {
                let matching = self
                    .prepared
                    .iter()
                    .find(|candidate| candidate.wire.reference == entry.wire.reference)
                    .ok_or_else(|| invalid("missing own prepared candidate"))?;
                if matching.wire.source_ref != self.wire.source_ref
                    || matching.wire.source_order != entry.wire.source_order
                    || source_counter(matching.wire.writer_origin)? != own_origin
                    || matching.bytes != entry.original.proposal.update
                    || matching.wire.targets != entry.original.proposal.targets
                {
                    return Err(invalid("own prepared provenance binding"));
                }
                let sealed = basis::HostSealedOriginal::from_trusted_original_store(
                    &entry.original.proposal,
                    entry.wire.reference.as_bytes(),
                    source_counter(entry.wire.source_order)?,
                )
                .map_err(|_| invalid("own prepared original reference"))?
                .with_original_base_revision(entry.original.base);
                basis::HostPreparedOwnPrefix::from_trusted_writer_original(
                    sealed,
                    &entry.original.authored,
                )
                .map_err(|_| invalid("own prepared authored cut binding"))
            })
            .collect::<io::Result<Vec<_>>>()?;
        let prepared_group = self
            .wire
            .prepared_prefix
            .as_ref()
            .map(|group| {
                basis::HostPreparedPrefix::from_trusted_writer_candidates(
                    group.transaction_revision.0,
                    &group.transaction_state_digest,
                    &prepared,
                    &prepared_own,
                )
                .map_err(|_| invalid("unqualified prospective prefix"))
            })
            .transpose()?;
        if let Some(group) = prepared_group.as_ref() {
            views = views
                .with_prepared_prefix(group)
                .map_err(|_| invalid("unqualified prepared basis view"))?;
        }
        let original = basis::HostSealedOriginal::from_trusted_original_store(
            &self.original.proposal,
            self.wire.original_reference.as_bytes(),
            source_counter(self.wire.source_order)?,
        )
        .map_err(|_| invalid("current original host reference"))?
        .with_original_base_revision(self.original.base);
        let history = self
            .history
            .iter()
            .map(|entry| {
                if entry.wire.source_ref != self.wire.source_ref
                    || source_counter(entry.wire.writer_origin)? != own_origin
                    || source_counter(entry.wire.source_order)?
                        >= source_counter(self.wire.source_order)?
                {
                    return Err(invalid("own committed history provenance"));
                }
                Ok(
                    HostCommittedOwnOperation::from_trusted_committed_host_store(
                        own_origin,
                        &entry.bytes,
                    ),
                )
            })
            .collect::<io::Result<Vec<_>>>()?;
        let authority = basis::HostConfirmedBasis::from_trusted_live_store_with_basis_views(
            views,
            current,
            own_origin,
            original,
            source_counter(self.wire.next_unsettled_order)?,
            &saved,
            &own,
        )
        .and_then(|basis| basis.with_committed_own_history(&history))
        .map_err(|_| invalid("unqualified human basis"))?;
        consume(authority)
    }
}

impl Human {
    pub(crate) fn preflight(&self, current: &[u8]) -> io::Result<()> {
        self.with_authority(current, |authority| {
            let candidate = basis::prepare_confirmed_basis_with_authored_cut(
                basis::Authority::Writer(authority),
                &self.original.authored,
            )
            .map_err(|_| invalid("unqualified human history or authored cut"))?;
            let bytes = candidate
                .checkpoint()
                .map_err(|_| invalid("human preflight checkpoint"))?;
            if bytes.len() > control::MAX_CHECKPOINT_BYTES {
                return Err(invalid("human candidate capacity"));
            }
            Ok(())
        })
    }
    pub(crate) fn prepare(
        &self,
        adapter: &WordCraftAdapter,
        current: &[u8],
        change: &AtomicChange,
        context: &CausalPrepareContext,
    ) -> io::Result<Prepared<Candidate>> {
        self.with_authority(current, |authority| {
            adapter
                .prepare_human_atomic(
                    current,
                    change,
                    context,
                    basis::Authority::Writer(authority),
                )
                .map_err(|_| invalid("uncertain human application preparation"))
        })
    }
}

impl crate::dispatch::Registration<WordCraftAdapter> {
    pub(crate) fn respond_session_native(
        &self,
        raw: &[u8],
        open: &SessionOpen,
        fence: Option<&Fence>,
    ) -> io::Result<Vec<u8>> {
        if !open
            .required_capabilities
            .contains(&SessionOpenRequiredCapabilitiesItem::CheckpointIdentityV1)
            || !open
                .required_capabilities
                .contains(&SessionOpenRequiredCapabilitiesItem::HumanNativePrepareV1)
        {
            return Err(invalid("missing human session capability"));
        }
        let normalized =
            control::validate_session_native_prepare(raw, control::Peer::InheritedHost)
                .map_err(|_| invalid("invalid session-native control"))?;
        let human = if normalized.value()["type"] == "prepare_atomic_causal" {
            let request: PrepareAtomicCausal = serde_json::from_value(normalized.value().clone())
                .map_err(|_| invalid("invalid session prepare"))?;
            self.check_binding(&request.scope, &request.selected)?;
            if request.scope != open.scope || request.selected != open.versions {
                return Err(invalid("prepare escaped session binding"));
            }
            let fence = fence.ok_or_else(|| invalid("human preparation before owned startup"))?;
            let human = Human::parse(&request, open, fence)?;
            if let Some(human) = human.as_ref() {
                let current = control::decode_checkpoint(&request.checkpoint)
                    .map_err(|_| invalid("human current checkpoint"))?;
                // Unknown/incomplete human history is never converted to a
                // terminal explicit-read rejection. Pure bounded replay first.
                human.preflight(&current)?;
            }
            human
        } else {
            None
        };
        self.respond_causal_selected(
            raw,
            true,
            |adapter, bytes, change, context| {
                if let Some(human) = human.as_ref() {
                    human.prepare(adapter, bytes, change, context)
                } else {
                    adapter
                        .prepare_atomic_causal(bytes, change, context)
                        .map_err(|_| invalid("uncertain native agent preparation"))
                }
            },
            |candidate, bytes, _| {
                native_delta(
                    candidate,
                    bytes,
                    human.as_ref().map(|h| h.original.proposal.origin),
                )
                .map(Some)
            },
        )
    }
}

fn native_delta(
    candidate: &Candidate,
    previous: &[u8],
    human_origin: Option<u64>,
) -> io::Result<SessionNativeDelta> {
    use mooring_core::adapter::PreparedCandidate;
    let writer = match (human_origin, candidate.native_writer_origin()) {
        (Some(origin), None) => origin,
        (None, Some(origin)) => origin,
        _ => return Err(invalid("candidate writer DATA binding")),
    };
    if writer == 0 || writer > MAX53 {
        return Err(invalid("candidate writer range"));
    }
    let document = mooring_wordcraft::Document::restore(candidate.checkpoint_bytes())
        .map_err(|_| invalid("candidate delta restore"))?;
    let raw = document
        .update_since(previous)
        .map_err(|_| invalid("candidate exact native delta"))?;
    let origins = mooring_wordcraft::document_update::allocation_origins(&raw)
        .map_err(|_| invalid("candidate native allocation inventory"))?;
    if origins.iter().any(|origin| *origin != writer) {
        return Err(invalid("candidate contains foreign allocations"));
    }
    let targets: BTreeSet<_> = candidate
        .effects()
        .iter()
        .flat_map(|effect| &effect.targets)
        .map(|target| target.target_id.clone())
        .collect();
    let targets: Vec<_> = targets.into_iter().collect();
    // This enforces the app's bounded closed DATA grammar too. The eventual
    // public DocumentUpdate still needs its separate WHOLE-frame capacity.
    mooring_wordcraft::document_update::encode(&raw, &targets)
        .map_err(|_| invalid("candidate native DATA capacity"))?;
    Ok(SessionNativeDelta {
        writer_origin: Counter(writer),
        allocation_origins: origins.into_iter().map(Counter).collect(),
        update: protocol::boundary::encode_base64(&raw, RAW)
            .map_err(|_| invalid("candidate native encoding"))?,
        targets,
    })
}

#[cfg(test)]
mod tests {
    use super::*;
    use mooring_core::adapter::PrepareAdapter;
    use mooring_wordcraft::{
        experimental_draft::{DraftAccess, DraftReplica},
        Document,
    };
    use serde_json::{json, Value};
    fn hash(bytes: &[u8]) -> String {
        control::encode_checkpoint(bytes).unwrap().digest
    }
    fn full(bytes: &[u8], open: &SessionOpen, revision: u64) -> Checkpoint {
        let encoded = control::encode_checkpoint(bytes).unwrap();
        let mut cp:Checkpoint=serde_json::from_value(json!({"scope":open.scope,"revision":revision.to_string(),"generation":"1","format_version":mooring_wordcraft::profile::CHECKPOINT_FORMAT,"versions":open.versions,"encoding":"base64","state":encoded.state,"digest":encoded.digest,"assets":[]})).unwrap();
        cp.digest = checkpoint::compute(
            &cp,
            checkpoint::Limits {
                decoded_bytes: control::MAX_CHECKPOINT_BYTES,
                preimage_bytes: control::MAX_CONTROL_BYTES,
            },
        )
        .unwrap()
        .full_digest()
        .into();
        cp
    }
    fn change(p: &Proposal, authored: &[u8], scope: &Scope) -> AtomicChange {
        let encode = |b: &[u8]| protocol::boundary::encode_base64(b, RAW).unwrap();
        let value = json!({"change_id":format!("change-{}",p.operation_id),"membership_hash":format!("sha256:{}","0".repeat(64)),"operations":[{"op_id":p.operation_id,"fingerprint":format!("sha256:{}","0".repeat(64)),"participant_ref":"human-participant","base_revision":"0","dependencies":if p.operation_id=="B"{vec!["A"]}else{vec![]},"read_preconditions":[],"payload":{"kind":"humancrdt.update","operation_id":p.operation_id,"origin":p.origin.to_string(),"update":encode(&p.update),"authored_frontier":encode(&p.authored_frontier),"targets":p.targets,"authored_checkpoint_state_digest":hash(authored)}}]});
        fingerprint_atomic(scope, &serde_json::to_vec(&value).unwrap())
            .unwrap()
            .with_fingerprints()
    }
    fn request(
        open: &SessionOpen,
        fence: &Fence,
        basis: &Checkpoint,
        current: &[u8],
        change: &AtomicChange,
        revision: u64,
        order: u64,
    ) -> Value {
        let targets:Vec<_>=WordCraftAdapter.validate_checkpoint(current).unwrap().into_iter().map(|t|json!({"target_id":t.target_id,"revision":revision.to_string(),"structure_revision":"0","deleted":false})).collect();
        json!({"type":"prepare_atomic_causal","request_id":format!("prepare-{}",change.change_id),"scope":open.scope,"selected":open.versions,"checkpoint":control::encode_checkpoint(current).unwrap(),"control_profile":control::CAUSAL_PROFILE,"change":change,"context":{"read_policy":"agent_dependency_covered","document_revision":revision.to_string(),"targets":targets,"operation_revisions":[{"op_id":change.operations[0].op_id,"revision":(revision+1).to_string()}],"qualified_reads":[]},"human_context":{"session_id":open.session_id,"runtime_ref":open.runtime_ref,"fence":fence,"op_id":change.operations[0].op_id,"source_ref":"live-source","original_reference":format!("original-{}",change.operations[0].op_id),"origin":"17","source_order":order.to_string(),"next_unsettled_order":order.to_string(),"original_basis":basis,"saved_transcript":[],"own_prefix":[],"own_history":[]}})
    }
    fn setup() -> (SessionOpen, Fence, Vec<u8>, DraftReplica) {
        let open:SessionOpen=serde_json::from_value(json!({"type":"session_open","request_id":"open","required_capabilities":["checkpoint_identity_v1","human_native_prepare_v1"],"control_profile":"mooring.session-causal.v1","scope":{"workspace_id":"w","artifact_id":"a","application_id":"wordcraft"},"session_id":"session","runtime_ref":"actual-runtime","versions":crate::dispatch::wordcraft_versions()})).unwrap();
        let fence:Fence=serde_json::from_value(json!({"generation":"2","token":"0123456789012345678901234567890123456789012345678901234567890123"})).unwrap();
        let mut native: Value = serde_json::from_str(include_str!(
            "../../mooring-wordcraft/tests/fixtures/d1-engine-canonical.json"
        ))
        .unwrap();
        native["body"] = json!([{"kind":"para","text":"aa","runs":[{"len":2}]}]);
        let before = Document::import(native, vec!["p_one".into()])
            .unwrap()
            .checkpoint()
            .unwrap();
        let producer = DraftReplica::restore(&before, 17, DraftAccess::Writer).unwrap();
        (open, fence, before, producer)
    }
    fn insert() -> mooring_wordcraft::command::Command {
        serde_json::from_value(json!({"kind":"text.insert","target_id":"p_one","at":{"kind":"end"},"text":" human😀","properties":null})).unwrap()
    }
    fn respond(request: &Value, open: &SessionOpen, fence: &Fence) -> io::Result<Value> {
        let raw = crate::dispatch::wordcraft_registration().respond_session_native(
            &serde_json::to_vec(request).unwrap(),
            open,
            Some(fence),
        )?;
        serde_json::from_slice(&raw).map_err(|_| invalid("test response"))
    }
    fn staged_fixture() -> (SessionOpen, Fence, Value, Value) {
        let (open, fence, before, mut producer) = setup();
        let proposal_a = producer.edit("A", insert()).unwrap();
        let authored_a = producer.optimistic_checkpoint().unwrap();
        let a = change(&proposal_a, &authored_a, &open.scope);
        let initial = full(&before, &open, 0);
        let first = request(&open, &fence, &initial, &before, &a, 0, 1);
        let prepared_a = respond(&first, &open, &fence).unwrap();
        assert_eq!(prepared_a["outcome"]["kind"], "candidate");
        assert!(prepared_a.get("saved").is_none());
        let a_bytes = control::decode_checkpoint(
            &serde_json::from_value(prepared_a["outcome"]["checkpoint"].clone()).unwrap(),
        )
        .unwrap();
        let proposal_b = producer.edit("B", insert()).unwrap();
        let authored_b = producer.optimistic_checkpoint().unwrap();
        let b = change(&proposal_b, &authored_b, &open.scope);
        let mut second = request(&open, &fence, &initial, &a_bytes, &b, 1, 2);
        second["human_context"]["prepared_prefix"] = json!({"transaction_revision":"0","transaction_state_digest":hash(&before),"deltas":[{"reference":"original-A","source_ref":"live-source","source_order":"1","writer_origin":"17","update":protocol::boundary::encode_base64(&proposal_a.update,RAW).unwrap(),"targets":proposal_a.targets,"prospective_revision":"1","checkpoint_state_digest":hash(&a_bytes)}],"own_prefix":[{"reference":"original-A","source_ref":"live-source","source_order":"1","change":a}]});
        (open, fence, second, producer.native().unwrap())
    }
    #[test]
    fn concrete_human_preparation_keeps_same_writer_candidates_distinct_from_saved() {
        // Actual application/closed protocol; all host source, SQL and lifecycle
        // provenance are SYNTHETIC. No confirmed actor Doc is installed here.
        let (open, fence, request, expected) = staged_fixture();
        let response = respond(&request, &open, &fence).unwrap();
        assert_eq!(response["outcome"]["kind"], "candidate");
        assert_eq!(response["native_delta"]["writer_origin"], "17");
        let bytes = control::decode_checkpoint(
            &serde_json::from_value(response["outcome"]["checkpoint"].clone()).unwrap(),
        )
        .unwrap();
        assert_eq!(
            Document::restore(&bytes).unwrap().native().unwrap(),
            expected
        );
        assert!(response.get("receipt").is_none());
        assert!(control::validate_causal(
            &serde_json::to_vec(&request).unwrap(),
            control::Peer::InheritedHost
        )
        .is_err());
    }
    #[test]
    fn human_context_tampering_is_nonterminal_before_explicit_read_failure() {
        let (open, fence, request, _) = staged_fixture();
        for mode in [
            "fence",
            "session",
            "source",
            "order",
            "missing-original",
            "missing-prefix",
            "candidate-hash",
            "transaction-hash",
        ] {
            let mut bad = request.clone();
            match mode {
                "fence" => bad["human_context"]["fence"]["generation"] = json!("3"),
                "session" => bad["human_context"]["session_id"] = json!("foreign"),
                "source" => bad["human_context"]["source_ref"] = json!("foreign"),
                "order" => bad["human_context"]["next_unsettled_order"] = json!("3"),
                "missing-original" => {
                    bad.as_object_mut().unwrap().remove("human_context");
                }
                "missing-prefix" => {
                    bad["human_context"]["prepared_prefix"]["own_prefix"] = json!([])
                }
                "candidate-hash" => {
                    bad["human_context"]["prepared_prefix"]["deltas"][0]
                        ["checkpoint_state_digest"] = json!(format!("sha256:{}", "0".repeat(64)))
                }
                "transaction-hash" => {
                    bad["human_context"]["prepared_prefix"]["transaction_state_digest"] =
                        json!(format!("sha256:{}", "0".repeat(64)))
                }
                _ => unreachable!(),
            }
            assert!(respond(&bad, &open, &fence).is_err(), "{mode}");
        }
        let mut bad = request.clone();
        bad["change"]["operations"][0]["read_preconditions"] =
            json!([{"target_id":"missing","revision":"0"}]);
        let change = fingerprint_atomic(&open.scope, &serde_json::to_vec(&bad["change"]).unwrap())
            .unwrap()
            .with_fingerprints();
        bad["change"] = serde_json::to_value(change).unwrap();
        bad["context"]["qualified_reads"] = json!([{"op_id":"B","read_index":"0","target_id":"missing","observed_revision":"0","entry":null,"covered":false}]);
        assert_eq!(
            respond(&bad, &open, &fence).unwrap()["outcome"]["kind"],
            "failure"
        );
        bad["human_context"]["prepared_prefix"]["transaction_state_digest"] =
            json!(format!("sha256:{}", "0".repeat(64)));
        assert!(
            respond(&bad, &open, &fence).is_err(),
            "unknown basis became a terminal read result"
        );
    }
    #[test]
    fn inherited_fd_human_handler_preserves_confirmed_doc_until_host_commit() {
        use std::{os::unix::net::UnixStream, time::Duration};
        let (open, fence, before, mut producer) = setup();
        let expected = Document::restore(&before).unwrap().native().unwrap();
        let initial = full(&before, &open, 0);
        let targets:Vec<_>=WordCraftAdapter.validate_checkpoint(&before).unwrap().into_iter().map(|t|json!({"target_id":t.target_id,"revision":"0","structure_revision":"0","deleted":false})).collect();
        let a = producer.edit("A", insert()).unwrap();
        let authored_a = producer.optimistic_checkpoint().unwrap();
        let original_a = change(&a, &authored_a, &open.scope);
        let first = request(&open, &fence, &initial, &before, &original_a, 0, 1);
        let b = producer.edit("B", insert()).unwrap();
        let authored_b = producer.optimistic_checkpoint().unwrap();
        let original_b = change(&b, &authored_b, &open.scope);
        let expected_b = producer.native().unwrap();
        let (child, mut peer) = UnixStream::pair().unwrap();
        peer.set_read_timeout(Some(Duration::from_secs(4))).unwrap();
        let worker = std::thread::spawn(move || {
            let send = |peer: &mut UnixStream, value: &Value| {
                crate::write_frame(peer, &serde_json::to_vec(value).unwrap()).unwrap()
            };
            let recv = |peer: &mut UnixStream| {
                serde_json::from_slice::<Value>(&crate::read_frame(peer).unwrap().unwrap()).unwrap()
            };
            send(&mut peer, &serde_json::to_value(&open).unwrap());
            let acquire = recv(&mut peer);
            send(
                &mut peer,
                &json!({"type":"owner_grant","request_id":acquire["request_id"],"scope":open.scope,"session_id":open.session_id,"runtime_ref":open.runtime_ref,"fence":fence,"lease_duration_ms":"60000","revision":"0","renewal_nonce":acquire["renewal_nonce"],"permission_version":"1"}),
            );
            send(
                &mut peer,
                &json!({"type":"session_restore","request_id":"restore","control_profile":"mooring.session-causal.v1","scope":open.scope,"session_id":open.session_id,"runtime_ref":open.runtime_ref,"fence":fence,"versions":open.versions,"checkpoint":initial,"targets":targets}),
            );
            assert_eq!(recv(&mut peer)["type"], "session_restored");
            let opened = recv(&mut peer);
            assert_eq!(opened["type"], "session_opened");
            assert_eq!(
                opened["capabilities"],
                json!(["checkpoint_identity_v1", "human_native_prepare_v1"])
            );
            send(&mut peer, &first);
            let prepared = recv(&mut peer);
            assert_eq!(prepared["outcome"]["kind"], "candidate");
            let current = control::decode_checkpoint(
                &serde_json::from_value(prepared["outcome"]["checkpoint"].clone()).unwrap(),
            )
            .unwrap();
            let mut second = request(&open, &fence, &initial, &current, &original_b, 1, 2);
            second["human_context"]["prepared_prefix"] = json!({"transaction_revision":"0","transaction_state_digest":hash(&before),"deltas":[{"reference":"original-A","source_ref":"live-source","source_order":"1","writer_origin":"17","update":protocol::boundary::encode_base64(&a.update,RAW).unwrap(),"targets":a.targets,"prospective_revision":"1","checkpoint_state_digest":hash(&current)}],"own_prefix":[{"reference":"original-A","source_ref":"live-source","source_order":"1","change":original_a}]});
            send(&mut peer, &second);
            let prepared_b = recv(&mut peer);
            assert_eq!(prepared_b["outcome"]["kind"], "candidate");
            let current = control::decode_checkpoint(
                &serde_json::from_value(prepared_b["outcome"]["checkpoint"].clone()).unwrap(),
            )
            .unwrap();
            assert_eq!(
                Document::restore(&current).unwrap().native().unwrap(),
                expected_b
            );
            second["human_context"]["fence"]["generation"] = json!("3");
            send(&mut peer, &second);
            assert!(matches!(crate::read_frame(&mut peer), Ok(None) | Err(_)));
        });
        // Actual socket and application, explicitly MOCK host authority/clock.
        let host = protocol::boundary::Transport::AuthenticatedHost {
            issuer: "crewship",
            durabilities: &[protocol::boundary::Durability::Node],
            required: protocol::boundary::Durability::Node,
        };
        let driver =
            crate::session_driver::Driver::from_negotiated_inherited_stream_with_native_humans(
                child,
                &host,
                || Ok(mooring_core::batch::Tick(Duration::from_millis(1))),
            )
            .unwrap();
        worker.join().unwrap();
        assert_eq!(driver.confirmed().document().native().unwrap(), expected);
        assert_eq!(driver.confirmed().checkpoint().revision, Counter(0));
        drop(driver);
    }
}
