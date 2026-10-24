//! Separate opt-in causal-agent preparation. The inherited host alone qualifies
//! durable history and commits outcomes. No participant or human delta routing.
use crate::dispatch::{
    budget_check, direct_failure, invalid, inventory_set, BoundedWriter, Registration,
};
use mooring_core::{
    adapter::{CausalPrepareAdapter, Prepared, PreparedCandidate},
    causal::{CausalContext, CausalPrepareContext as HostContext, EntryVersion, QualifiedRead},
    prepare::ContextError,
};
use mooring_protocol::{
    self as protocol,
    canonical::fingerprint_atomic,
    control::{self, Peer},
    *,
};
use std::io;

impl<A: CausalPrepareAdapter> Registration<A> {
    /// Caller must establish verified inherited-host provenance and exact
    /// profile selection first. The raw JSON context carries no authority.
    pub fn respond_causal(&self, raw: &[u8]) -> io::Result<Vec<u8>> {
        self.respond_causal_selected(
            raw,
            false,
            |adapter, bytes, change, context| {
                adapter
                    .prepare_atomic_causal(bytes, change, context)
                    .map_err(|_| invalid("uncertain causal application preparation"))
            },
            |_, _, _| Ok(None),
        )
    }

    pub(crate) fn respond_causal_selected<P, D>(
        &self,
        raw: &[u8],
        session_native: bool,
        mut prepare: P,
        mut delta: D,
    ) -> io::Result<Vec<u8>>
    where
        P: FnMut(&A, &[u8], &AtomicChange, &HostContext) -> io::Result<Prepared<A::Candidate>>,
        D: FnMut(&A::Candidate, &[u8], &AtomicChange) -> io::Result<Option<SessionNativeDelta>>,
    {
        let validate = if session_native {
            control::validate_session_native_prepare
        } else {
            control::validate_causal
        };
        let normalized =
            validate(raw, Peer::InheritedHost).map_err(|_| invalid("invalid causal control"))?;
        match normalized.value()["type"].as_str() {
            Some("validate_checkpoint_causal") => {
                let request: ValidateCheckpointCausal =
                    serde_json::from_value(normalized.value().clone())
                        .map_err(|_| invalid("invalid causal checkpoint request"))?;
                self.check_binding(&request.scope, &request.selected)?;
                let bytes = control::decode_checkpoint(&request.checkpoint)
                    .map_err(|_| invalid("invalid checkpoint envelope"))?;
                let inventory = self
                    .adapter
                    .validate_checkpoint(&bytes)
                    .map_err(|_| invalid("invalid application checkpoint"))?;
                inventory_set(&inventory)?;
                response_bytes(
                    &CheckpointValidatedCausal {
                        r#type: CheckpointValidatedCausalType::CheckpointValidatedCausal,
                        request_id: request.request_id,
                        scope: request.scope,
                        selected: self.selected.clone(),
                        input_checkpoint_digest: request.checkpoint.digest,
                        inventory,
                        control_profile:
                            CheckpointValidatedCausalControlProfile::MooringPrepareCausalV1,
                    },
                    session_native,
                )
            }
            Some("prepare_atomic_causal") => {
                let request: PrepareAtomicCausal =
                    serde_json::from_value(normalized.value().clone())
                        .map_err(|_| invalid("invalid causal prepare request"))?;
                self.check_binding(&request.scope, &request.selected)?;
                let bytes = control::decode_checkpoint(&request.checkpoint)
                    .map_err(|_| invalid("invalid checkpoint envelope"))?;
                let atomic_bytes =
                    serde_json::to_vec(&request.change).map_err(|_| invalid("atomic encoding"))?;
                let fingerprinted = fingerprint_atomic(&request.scope, &atomic_bytes)
                    .map_err(|_| invalid("invalid atomic identity"))?;
                if !fingerprinted.matches_wire() {
                    return Err(invalid("atomic identity mismatch"));
                }
                let change = fingerprinted.change();
                let inventory = self
                    .adapter
                    .validate_checkpoint(&bytes)
                    .map_err(|_| invalid("invalid application checkpoint"))?;
                inventory_set(&inventory)?;
                let context = host_context(&request.context)?;
                // Validate ALL original bindings against the real complete app
                // inventory before returning any terminal read classification.
                let prepared = match CausalContext::qualify(&context, change, &inventory) {
                    Err(ContextError::Invalid) => return Err(invalid("invalid causal context")),
                    Err(ContextError::Read(failure)) => Prepared::Rejected(failure),
                    Ok(_) => prepare(&self.adapter, &bytes, change, &context)?,
                };
                let mut native_delta = None;
                let outcome = match prepared {
                    Prepared::Rejected(failure) => {
                        AtomicPreparedCausalOutcome::PreparedDirectFailure(direct_failure(
                            failure, change,
                        )?)
                    }
                    Prepared::Candidate(candidate) => {
                        budget_check(&candidate.effects())?;
                        budget_check(&candidate.inventory())?;
                        let checkpoint = control::encode_checkpoint(candidate.checkpoint_bytes())
                            .map_err(|_| invalid("candidate checkpoint limit"))?;
                        let actual_inventory = self
                            .adapter
                            .validate_checkpoint(candidate.checkpoint_bytes())
                            .map_err(|_| invalid("invalid candidate checkpoint"))?;
                        let qualified = CausalContext::qualify(&context, change, &inventory)
                            .map_err(|_| invalid("invalid candidate context"))?;
                        let mut working = qualified.working();
                        if candidate.effects().len() != change.operations.len() {
                            return Err(invalid("incomplete operation effects"));
                        }
                        for effect in candidate.effects() {
                            working
                                .check_current_reads(&effect.op_id)
                                .map_err(|_| invalid("candidate bypassed causal reads"))?;
                            working
                                .advance_effects(&effect.op_id, &effect.targets)
                                .map_err(|_| invalid("invalid causal candidate effects"))?;
                        }
                        if !working.complete()
                            || inventory_set(candidate.inventory())?
                                != inventory_set(&actual_inventory)?
                            || inventory_set(candidate.inventory())?
                                != inventory_set(&working.inventory())?
                        {
                            return Err(invalid("candidate inventory mismatch"));
                        }
                        native_delta = delta(&candidate, &bytes, change)?;
                        AtomicPreparedCausalOutcome::PreparedCandidate(
                            protocol::PreparedCandidate {
                                kind: PreparedCandidateKind::Candidate,
                                checkpoint,
                                effects: candidate.effects().to_vec(),
                                inventory: candidate.inventory().to_vec(),
                            },
                        )
                    }
                };
                response_bytes(
                    &AtomicPreparedCausal {
                        r#type: AtomicPreparedCausalType::AtomicPreparedCausal,
                        request_id: request.request_id,
                        scope: request.scope,
                        selected: self.selected.clone(),
                        input_checkpoint_digest: request.checkpoint.digest,
                        change_id: change.change_id.clone(),
                        membership_hash: change.membership_hash.clone(),
                        outcome,
                        native_delta,
                        control_profile: AtomicPreparedCausalControlProfile::MooringPrepareCausalV1,
                    },
                    session_native,
                )
            }
            _ => Err(invalid("unsupported causal control")),
        }
    }
}

// This conversion does not authenticate the host or qualify its SQL proof.
// Null/absence distinctions must already pass the raw schema validator above.
pub(crate) fn host_context(context: &protocol::CausalPrepareContext) -> io::Result<HostContext> {
    let qualified_reads = context
        .qualified_reads
        .iter()
        .map(|read| {
            let index = read
                .read_index
                .parse::<u8>()
                .map_err(|_| invalid("invalid causal read index"))?;
            if index.to_string() != read.read_index {
                return Err(invalid("noncanonical causal read index"));
            }
            Ok(QualifiedRead {
                op_id: read.op_id.clone(),
                read_index: u32::from(index),
                target_id: read.target_id.clone(),
                observed_revision: read.observed_revision,
                observed_structure_revision: read.observed_structure_revision,
                entry: match &read.entry {
                    QualifiedCausalReadEntry::CausalEntryVersion(entry) => Some(EntryVersion {
                        revision: entry.revision,
                        structure_revision: entry.structure_revision,
                        deleted: entry.deleted,
                    }),
                    QualifiedCausalReadEntry::Variant2(()) => None,
                },
                covered: read.covered,
            })
        })
        .collect::<io::Result<Vec<_>>>()?;
    Ok(HostContext {
        document_revision: context.document_revision,
        targets: context.targets.clone(),
        operation_revisions: context.operation_revisions.clone(),
        qualified_reads,
    })
}

fn response_bytes(response: &impl serde::Serialize, session_native: bool) -> io::Result<Vec<u8>> {
    let mut writer = BoundedWriter { bytes: Vec::new() };
    serde_json::to_writer(&mut writer, response)
        .map_err(|_| invalid("causal response encoding or limit"))?;
    let validate = if session_native {
        control::validate_session_native_prepare
    } else {
        control::validate_causal
    };
    validate(&writer.bytes, Peer::InheritedRuntime)
        .map(|value| value.bytes().to_vec())
        .map_err(|_| invalid("causal response limit or shape"))
}
