//! Inactive full-document preparation for the WordCraft binding experiment.
//! UNTRUSTED DATA preparation only: not a live-admission capability.
//! No production registration, participant admission, Saved, or persistence.
//! Effects are deliberately conservative, NOT precise CRDT block attribution.
use mooring_core::{
    adapter::{PrepareAdapter, Prepared, PreparedCandidate, PreparedFailure},
    prepare::{ConfirmedContext, ContextError},
};
use mooring_protocol::{
    AtomicChange, ErrorCode, PrepareContext, PreparedOperationEffects, TargetEffect, TargetIdentity,
};
use mooring_wordcraft::{
    adapter::{METADATA_TARGET, STRUCTURE_TARGET},
    experimental_binding::{Binding, Change},
};
use serde::Deserialize;
use std::collections::BTreeSet;

/// Trusted HOST configuration only. JSON scope claims never construct authority.
pub enum WriteScope {
    WholeDocument,
    Blocks(Vec<String>),
}
pub struct BindingAdapter {
    origin: u64,
}
#[derive(Debug)]
pub enum Error {
    Scope,
    Origin,
    Checkpoint,
    Context,
}
pub struct Candidate {
    checkpoint: Vec<u8>,
    effects: Vec<PreparedOperationEffects>,
    inventory: Vec<TargetIdentity>,
}
impl PreparedCandidate for Candidate {
    fn checkpoint_bytes(&self) -> &[u8] {
        &self.checkpoint
    }
    fn effects(&self) -> &[PreparedOperationEffects] {
        &self.effects
    }
    fn inventory(&self) -> &[TargetIdentity] {
        &self.inventory
    }
}
#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Payload {
    kind: String,
    update: String,
    basis_state: [u8; 32],
    basis_structure: String,
    basis_ids: Vec<String>,
}
impl BindingAdapter {
    /// Numeric origins and scope are host configuration DATA, not proof of
    /// original authoring basis, allocation ownership, or format-key history.
    /// Until those proofs are integrated this accepts trusted local fixtures only.
    /// Caller must authenticate and authorize whole-document writes separately,
    /// bind this adapter to the exact scope, and reserve a fresh CRDT origin.
    /// Partial block mandates are unsupported, even for single-block updates.
    pub fn new(origin: u64, scope: WriteScope) -> Result<Self, Error> {
        if !matches!(scope, WriteScope::WholeDocument) {
            return Err(Error::Scope);
        }
        if origin == 0 || origin >= (1 << 53) {
            return Err(Error::Origin);
        }
        Ok(Self { origin })
    }
    fn restore(&self, checkpoint: &[u8]) -> Result<Binding, Error> {
        Binding::restore(checkpoint, self.origin).map_err(|_| Error::Checkpoint)
    }
}
fn inventory(binding: &Binding) -> Result<Vec<TargetIdentity>, Error> {
    let live: BTreeSet<_> = binding
        .stable_ids()
        .map_err(|_| Error::Checkpoint)?
        .into_iter()
        .collect();
    let mut result: Vec<_> = binding
        .inventory_ids()
        .map_err(|_| Error::Checkpoint)?
        .into_iter()
        .map(|target_id| TargetIdentity {
            deleted: !live.contains(&target_id),
            target_id,
        })
        .collect();
    result.extend(
        [STRUCTURE_TARGET, METADATA_TARGET].map(|target_id| TargetIdentity {
            target_id: target_id.into(),
            deleted: false,
        }),
    );
    result.sort_by(|a, b| a.target_id.cmp(&b.target_id));
    Ok(result)
}
fn rejection(operation: &str, message: &str) -> Prepared<Candidate> {
    Prepared::Rejected(PreparedFailure {
        operation_id: operation.into(),
        code: ErrorCode::InvalidRequest,
        message: message.into(),
    })
}
impl PrepareAdapter for BindingAdapter {
    type Candidate = Candidate;
    type Error = Error;
    fn validate_checkpoint(&self, checkpoint: &[u8]) -> Result<Vec<TargetIdentity>, Error> {
        inventory(&self.restore(checkpoint)?)
    }
    fn prepare_atomic(
        &self,
        checkpoint: &[u8],
        change: &AtomicChange,
        context: &PrepareContext,
    ) -> Result<Prepared<Candidate>, Error> {
        let mut candidate = self.restore(checkpoint)?;
        let before = inventory(&candidate)?;
        if change.operations.len() != 1 {
            return Err(Error::Context);
        }
        let operation = &change.operations[0];
        let qualified = match ConfirmedContext::qualify(context, change, &before) {
            Ok(value) => value,
            Err(ContextError::Read(failure)) => return Ok(Prepared::Rejected(failure)),
            Err(ContextError::Invalid) => return Err(Error::Context),
        };
        let mut working = qualified.working();
        if let Err(error) = working.check_current_reads(&operation.op_id) {
            return match error {
                ContextError::Read(failure) => Ok(Prepared::Rejected(failure)),
                ContextError::Invalid => Err(Error::Context),
            };
        }
        // No native-only diff: equal-value attribute actions still change CRDT
        // history. Require the complete LIVE document and reserved target reads.
        if before.iter().filter(|t| !t.deleted).any(|target| {
            !operation
                .read_preconditions
                .iter()
                .any(|read| read.target_id == target.target_id && read.structure_revision.is_some())
        }) {
            return Ok(rejection(
                &operation.op_id,
                "whole-document read observations required",
            ));
        }
        let payload: Payload =
            match serde_json::from_value(serde_json::Value::Object(operation.payload.clone())) {
                Ok(value) => value,
                Err(_) => {
                    return Ok(rejection(
                        &operation.op_id,
                        "invalid experimental update payload",
                    ))
                }
            };
        if payload.kind != "wordcraft.binding.update" {
            return Ok(rejection(
                &operation.op_id,
                "unsupported experimental update payload",
            ));
        }
        let update = match mooring_protocol::boundary::decode_base64(&payload.update, 262144) {
            Ok(value) => value,
            Err(_) => {
                return Ok(rejection(
                    &operation.op_id,
                    "invalid bounded update encoding",
                ))
            }
        };
        let authored = Change {
            update,
            basis_state: payload.basis_state,
            basis_structure: payload.basis_structure,
            basis_ids: payload.basis_ids,
        };
        if mooring_wordcraft::experimental_binding::validate_authorship(&authored, self.origin)
            .is_err()
        {
            return Ok(rejection(
                &operation.op_id,
                "foreign experimental allocation origin",
            ));
        }
        if candidate.apply_remote(&authored).is_err() {
            return Ok(rejection(
                &operation.op_id,
                "invalid experimental binding update",
            ));
        }
        let after = inventory(&candidate)?;
        // IDs cannot be forgotten or resurrected. Binding enforces retired-ID
        // allocation; this also checks the host inventory representation.
        if before.iter().any(|old| {
            !after
                .iter()
                .any(|new| new.target_id == old.target_id && (!old.deleted || new.deleted))
        }) {
            return Err(Error::Context);
        }
        let mut effects = Vec::new();
        for target in &after {
            let old = before.iter().find(|old| old.target_id == target.target_id);
            if old.is_some_and(|old| old.deleted) {
                continue;
            }
            let created = old.is_none();
            effects.push(TargetEffect {
                target_id: target.target_id.clone(),
                content_changed: true,
                structure_changed: created
                    || target.deleted
                    || target.target_id == STRUCTURE_TARGET,
                created,
                deleted: target.deleted,
            });
        }
        working
            .advance_effects(&operation.op_id, &effects)
            .map_err(|_| Error::Context)?;
        if !working.complete() || working.inventory() != after {
            return Err(Error::Context);
        }
        let checkpoint = candidate.checkpoint().map_err(|_| Error::Checkpoint)?;
        Ok(Prepared::Candidate(Candidate {
            checkpoint,
            inventory: after,
            effects: vec![PreparedOperationEffects {
                op_id: operation.op_id.clone(),
                targets: effects,
            }],
        }))
    }
}
