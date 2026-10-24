//! Stateless private preparation: input checkpoint -> owned candidate. No Rust
//! outcome ledger, speculative installation, participant routing or Saved.
use mooring_core::{
    adapter::{PrepareAdapter, Prepared, PreparedCandidate, PreparedFailure},
    prepare::{ConfirmedContext, ContextError},
};
use mooring_protocol::{
    self as protocol,
    canonical::fingerprint_atomic,
    control::{self, Peer},
    *,
};
use std::{collections::BTreeMap, io};

/// Registered semantic identity comes from the qualified application, not
/// the bootstrap crate or Yrs version. Source/binary/WASM provenance is separate.
pub fn wordcraft_versions() -> Versions {
    mooring_wordcraft::profile::qualified_versions()
}

pub fn wordcraft_registration() -> Registration<mooring_wordcraft::adapter::WordCraftAdapter> {
    Registration {
        application_id: "wordcraft".into(),
        selected: wordcraft_versions(),
        adapter: mooring_wordcraft::adapter::WordCraftAdapter,
    }
}

pub struct Registration<A> {
    pub application_id: String,
    pub selected: Versions,
    pub adapter: A,
}

impl<A: PrepareAdapter> Registration<A> {
    pub fn respond(&self, raw: &[u8]) -> io::Result<Vec<u8>> {
        let normalized = control::validate(raw, Peer::InheritedHost)
            .map_err(|_| invalid("invalid private control"))?;
        match normalized.value()["type"].as_str() {
            Some("validate_checkpoint") => {
                let request: ValidateCheckpoint =
                    serde_json::from_value(normalized.value().clone())
                        .map_err(|_| invalid("invalid checkpoint request"))?;
                self.check_binding(&request.scope, &request.selected)?;
                let bytes = control::decode_checkpoint(&request.checkpoint)
                    .map_err(|_| invalid("invalid checkpoint envelope"))?;
                let inventory = self
                    .adapter
                    .validate_checkpoint(&bytes)
                    .map_err(|_| invalid("invalid application checkpoint"))?;
                inventory_set(&inventory)?;
                let response = CheckpointValidated {
                    r#type: CheckpointValidatedType::CheckpointValidated,
                    request_id: request.request_id,
                    scope: request.scope,
                    selected: self.selected.clone(),
                    input_checkpoint_digest: request.checkpoint.digest,
                    inventory,
                    control_profile: CheckpointValidatedControlProfile::MooringPrepareV1,
                };
                response_bytes(&response)
            }
            Some("prepare_atomic") => {
                let request: PrepareAtomic = serde_json::from_value(normalized.value().clone())
                    .map_err(|_| invalid("invalid prepare request"))?;
                self.check_binding(&request.scope, &request.selected)?;
                let bytes = control::decode_checkpoint(&request.checkpoint)
                    .map_err(|_| invalid("invalid checkpoint envelope"))?;
                // Full atomic membership and payload numbers are normalized BEFORE
                // adapter interpretation; the public atomic byte bound still applies.
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
                let qualified = ConfirmedContext::qualify(&request.context, change, &inventory);
                let prepared = match qualified {
                    Err(ContextError::Invalid) => {
                        return Err(invalid("invalid host preparation context"))
                    }
                    Err(ContextError::Read(failure)) => Prepared::Rejected(failure),
                    Ok(_) => self
                        .adapter
                        .prepare_atomic(&bytes, change, &request.context)
                        .map_err(|_| invalid("uncertain application preparation"))?,
                };
                let outcome = match prepared {
                    Prepared::Rejected(failure) => rejection(failure, change)?,
                    Prepared::Candidate(candidate) => {
                        // Revalidate actual opaque state, not just candidate metadata.
                        // Neither this restoration nor effect simulation installs it.
                        budget_check(&candidate.effects())?;
                        budget_check(&candidate.inventory())?;
                        let checkpoint = control::encode_checkpoint(candidate.checkpoint_bytes())
                            .map_err(|_| invalid("candidate checkpoint limit"))?;
                        let actual_inventory = self
                            .adapter
                            .validate_checkpoint(candidate.checkpoint_bytes())
                            .map_err(|_| invalid("invalid candidate checkpoint"))?;
                        let qualified =
                            ConfirmedContext::qualify(&request.context, change, &inventory)
                                .map_err(|_| invalid("invalid candidate context"))?;
                        let mut working = qualified.working();
                        if candidate.effects().len() != change.operations.len() {
                            return Err(invalid("incomplete operation effects"));
                        }
                        for effect in candidate.effects() {
                            working
                                .check_current_reads(&effect.op_id)
                                .map_err(|_| invalid("candidate bypassed confirmed reads"))?;
                            working
                                .advance_effects(&effect.op_id, &effect.targets)
                                .map_err(|_| invalid("invalid candidate effects"))?;
                        }
                        if !working.complete()
                            || inventory_set(candidate.inventory())?
                                != inventory_set(&actual_inventory)?
                            || inventory_set(candidate.inventory())?
                                != inventory_set(&working.inventory())?
                        {
                            return Err(invalid("candidate inventory mismatch"));
                        }
                        AtomicPreparedOutcome::PreparedCandidate(protocol::PreparedCandidate {
                            kind: PreparedCandidateKind::Candidate,
                            checkpoint,
                            effects: candidate.effects().to_vec(),
                            inventory: candidate.inventory().to_vec(),
                        })
                    }
                };
                let response = AtomicPrepared {
                    r#type: AtomicPreparedType::AtomicPrepared,
                    request_id: request.request_id,
                    scope: request.scope,
                    selected: self.selected.clone(),
                    input_checkpoint_digest: request.checkpoint.digest,
                    change_id: change.change_id.clone(),
                    membership_hash: change.membership_hash.clone(),
                    outcome,
                    control_profile: AtomicPreparedControlProfile::MooringPrepareV1,
                };
                response_bytes(&response)
            }
            _ => Err(invalid("unsupported private control")),
        }
    }
    pub(crate) fn check_binding(&self, scope: &Scope, selected: &Versions) -> io::Result<()> {
        if !protocol::wire::valid_identifier(&self.application_id)
            || scope.application_id != self.application_id
            || selected != &self.selected
        {
            return Err(invalid("unregistered application or engine"));
        }
        Ok(())
    }
}

pub(crate) fn inventory_set(inventory: &[TargetIdentity]) -> io::Result<BTreeMap<&str, bool>> {
    if inventory.len() > 65_536 {
        return Err(invalid("inventory limit"));
    }
    let mut targets = BTreeMap::new();
    for identity in inventory {
        if !protocol::wire::valid_identifier(&identity.target_id)
            || targets
                .insert(identity.target_id.as_str(), identity.deleted)
                .is_some()
        {
            return Err(invalid("invalid target inventory"));
        }
    }
    Ok(targets)
}
fn rejection(failure: PreparedFailure, change: &AtomicChange) -> io::Result<AtomicPreparedOutcome> {
    direct_failure(failure, change).map(AtomicPreparedOutcome::PreparedDirectFailure)
}
pub(crate) fn direct_failure(
    failure: PreparedFailure,
    change: &AtomicChange,
) -> io::Result<PreparedDirectFailure> {
    let failure = failure
        .qualify(change)
        .map_err(|_| invalid("invalid prepared failure"))?;
    Ok(PreparedDirectFailure {
        kind: PreparedDirectFailureKind::Failure,
        op_id: failure.operation_id().into(),
        reason: Rejection {
            code: failure.code().clone(),
            message: failure.message().into(),
            root_op_id: None,
            root_cause: None,
            retry: RejectionRetry::NewOperationAfterRefresh,
        },
    })
}
fn response_bytes(response: &impl serde::Serialize) -> io::Result<Vec<u8>> {
    let mut writer = BoundedWriter { bytes: Vec::new() };
    serde_json::to_writer(&mut writer, response)
        .map_err(|_| invalid("control response encoding or limit"))?;
    let raw = writer.bytes;
    control::validate(&raw, Peer::InheritedRuntime)
        .map(|value| value.bytes().to_vec())
        .map_err(|_| invalid("control response limit or shape"))
}
pub(crate) fn invalid(message: &'static str) -> io::Error {
    io::Error::new(io::ErrorKind::InvalidData, message)
}

pub(crate) fn budget_check(value: &impl serde::Serialize) -> io::Result<()> {
    let mut writer = CountWriter(0);
    serde_json::to_writer(&mut writer, value).map_err(|_| invalid("candidate metadata limit"))
}
struct CountWriter(usize);
impl io::Write for CountWriter {
    fn write(&mut self, bytes: &[u8]) -> io::Result<usize> {
        self.0 = self
            .0
            .checked_add(bytes.len())
            .filter(|size| *size <= control::MAX_CONTROL_BYTES)
            .ok_or_else(|| invalid("control response limit"))?;
        Ok(bytes.len())
    }
    fn flush(&mut self) -> io::Result<()> {
        Ok(())
    }
}
pub(crate) struct BoundedWriter {
    pub(crate) bytes: Vec<u8>,
}
impl io::Write for BoundedWriter {
    fn write(&mut self, bytes: &[u8]) -> io::Result<usize> {
        if bytes.len() > control::MAX_CONTROL_BYTES - self.bytes.len() {
            return Err(invalid("control response limit"));
        }
        self.bytes.extend_from_slice(bytes);
        Ok(bytes.len())
    }
    fn flush(&mut self) -> io::Result<()> {
        Ok(())
    }
}
