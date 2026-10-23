//! Ephemeral host-supplied preparation labels and target counters, never a
//! Rust revision allocator or durable ledger. Authority is established by the
//! verified private dispatcher before this validation boundary.
use crate::adapter::PreparedFailure;
use mooring_protocol::{
    wire::valid_identifier, AtomicChange, Counter, ErrorCode, Operation, PrepareContext,
    PrepareContextReadPolicy, PreparedOperationRevision, TargetEffect, TargetIdentity,
    TargetVersion,
};
use std::collections::{BTreeMap, BTreeSet};

#[derive(Debug)]
pub enum ContextError {
    /// Malformed/incomplete host context is NONTERMINAL, not a guessed rejection.
    Invalid,
    /// A qualified immutable member has an invalid confirmed observation.
    Read(PreparedFailure),
}

pub struct ConfirmedContext<'a> {
    context: &'a PrepareContext,
    operations: &'a [Operation],
}

impl<'a> ConfirmedContext<'a> {
    pub fn qualify(
        context: &'a PrepareContext,
        change: &'a AtomicChange,
        inventory: &[TargetIdentity],
    ) -> Result<Self, ContextError> {
        // Exhaustive on purpose: adding a future policy requires review here.
        match context.read_policy {
            PrepareContextReadPolicy::AgentConfirmedCurrent => {}
        }
        if change.operations.is_empty()
            || change.operations.len() > 256
            || inventory.len() > 65_536
            || context.targets.len() != inventory.len()
            || context.operation_revisions.len() != change.operations.len()
        {
            return Err(ContextError::Invalid);
        }
        let mut identities = BTreeMap::new();
        for identity in inventory {
            if !valid_identifier(&identity.target_id)
                || identities
                    .insert(&identity.target_id, identity.deleted)
                    .is_some()
            {
                return Err(ContextError::Invalid);
            }
        }
        let mut targets = BTreeMap::new();
        for target in &context.targets {
            if identities.get(&target.target_id) != Some(&target.deleted)
                || target.structure_revision > target.revision
                || target.revision > context.document_revision
                || targets
                    .insert(target.target_id.clone(), target.clone())
                    .is_some()
            {
                return Err(ContextError::Invalid);
            }
        }
        let mut ids = BTreeSet::new();
        let mut revision = context.document_revision.0;
        for (operation, label) in change.operations.iter().zip(&context.operation_revisions) {
            revision = revision.checked_add(1).ok_or(ContextError::Invalid)?;
            if !valid_identifier(&operation.op_id)
                || !ids.insert(&operation.op_id)
                || label.op_id != operation.op_id
                || label.revision.0 != revision
            {
                return Err(ContextError::Invalid);
            }
        }
        // The first capability accepts only confirmed entry observations, never
        // guessed future host labels or reads of not-yet-created targets.
        for operation in &change.operations {
            if operation.base_revision > context.document_revision {
                return Err(ContextError::Read(failure(
                    operation,
                    ErrorCode::PreconditionFailed,
                )));
            }
            check_reads(operation, &targets).map_err(ContextError::Read)?;
        }
        Ok(Self {
            context,
            operations: &change.operations,
        })
    }
    pub fn document_revision(&self) -> Counter {
        self.context.document_revision
    }
    pub fn working(&self) -> WorkingTargets<'_> {
        WorkingTargets {
            targets: self
                .context
                .targets
                .iter()
                .map(|target| (target.target_id.clone(), target.clone()))
                .collect(),
            labels: &self.context.operation_revisions,
            next: 0,
            operations: self.operations,
            checked: false,
        }
    }
}

pub struct WorkingTargets<'a> {
    targets: BTreeMap<String, TargetVersion>,
    labels: &'a [PreparedOperationRevision],
    next: usize,
    operations: &'a [Operation],
    checked: bool,
}
impl WorkingTargets<'_> {
    pub fn check_current_reads(&mut self, op_id: &str) -> Result<(), ContextError> {
        self.checked = false;
        let operation = self
            .operations
            .get(self.next)
            .ok_or(ContextError::Invalid)?;
        if operation.op_id != op_id {
            return Err(ContextError::Invalid);
        }
        check_reads(operation, &self.targets).map_err(ContextError::Read)?;
        self.checked = true;
        Ok(())
    }
    /// Validate the complete per-operation effect set BEFORE changing any
    /// working counter. Labels are consumed in exact immutable operation order.
    pub fn advance_effects(
        &mut self,
        op_id: &str,
        effects: &[TargetEffect],
    ) -> Result<(), ContextError> {
        let label = self.labels.get(self.next).ok_or(ContextError::Invalid)?;
        if !self.checked || label.op_id != op_id || effects.len() > 4096 {
            return Err(ContextError::Invalid);
        }
        let mut ids = BTreeSet::new();
        let mut created = 0usize;
        for effect in effects {
            if !valid_identifier(&effect.target_id)
                || !ids.insert(&effect.target_id)
                || !(effect.content_changed || effect.structure_changed)
                || (effect.created && effect.deleted)
                || ((effect.created || effect.deleted) && !effect.structure_changed)
            {
                return Err(ContextError::Invalid);
            }
            match self.targets.get(&effect.target_id) {
                None if effect.created => created += 1,
                Some(target) if !effect.created && !target.deleted => {}
                _ => return Err(ContextError::Invalid),
            }
        }
        if self.targets.len() + created > 65_536 {
            return Err(ContextError::Invalid);
        }
        for effect in effects {
            if effect.created {
                self.targets.insert(
                    effect.target_id.clone(),
                    TargetVersion {
                        target_id: effect.target_id.clone(),
                        revision: label.revision,
                        structure_revision: label.revision,
                        deleted: false,
                    },
                );
            } else {
                let target = self
                    .targets
                    .get_mut(&effect.target_id)
                    .expect("validated target");
                target.revision = label.revision;
                if effect.structure_changed {
                    target.structure_revision = label.revision;
                }
                target.deleted = effect.deleted;
            }
        }
        self.next += 1;
        self.checked = false;
        Ok(())
    }
    pub fn inventory(&self) -> Vec<TargetIdentity> {
        self.targets
            .values()
            .map(|target| TargetIdentity {
                target_id: target.target_id.clone(),
                deleted: target.deleted,
            })
            .collect()
    }
    pub fn versions(&self) -> Vec<TargetVersion> {
        self.targets.values().cloned().collect()
    }
    pub fn complete(&self) -> bool {
        self.next == self.labels.len()
    }
}

fn check_reads(
    operation: &Operation,
    targets: &BTreeMap<String, TargetVersion>,
) -> Result<(), PreparedFailure> {
    let mut seen = BTreeSet::new();
    for read in &operation.read_preconditions {
        if !valid_identifier(&read.target_id) || !seen.insert(&read.target_id) {
            return Err(failure(operation, ErrorCode::InvalidRequest));
        }
        let Some(target) = targets.get(&read.target_id) else {
            return Err(failure(operation, ErrorCode::InvalidRequest));
        };
        if target.deleted {
            return Err(failure(operation, ErrorCode::TargetDeleted));
        }
        if read.revision != target.revision
            || read
                .structure_revision
                .is_some_and(|revision| revision != target.structure_revision)
        {
            return Err(failure(operation, ErrorCode::PreconditionFailed));
        }
    }
    Ok(())
}
fn failure(operation: &Operation, code: ErrorCode) -> PreparedFailure {
    PreparedFailure {
        operation_id: operation.op_id.clone(),
        code,
        message: "confirmed read precondition failed".into(),
    }
}
