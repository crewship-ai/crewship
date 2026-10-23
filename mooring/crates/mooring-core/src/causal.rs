//! Proposed causal-read inputs, separate from the strict-current wire policy.
//!
//! These values attest host-owned SQL history, not authority or durability. A
//! future explicitly negotiated private dispatcher must bind them to its exact
//! request, scope, input digest and immutable change membership before use. They
//! intentionally have no deserializer and cannot enable a protocol capability.
use mooring_protocol::{Counter, PreparedOperationRevision, TargetVersion};

/// Compact original-read qualification made by the trusted SQL host after it
/// checks ALL intervening effects and the complete immutable dependency closure.
/// Missing history is a nonterminal host error, never `covered = false`.
#[derive(Clone, Debug, PartialEq)]
pub struct QualifiedRead {
    pub op_id: String,
    /// Original per-operation read index. Future wire form is a canonical
    /// decimal string 0..255; this pure representation is not a JSON schema.
    pub read_index: u32,
    pub target_id: String,
    pub observed_revision: Counter,
    /// Exact original optional observation: absence cannot become JSON null.
    pub observed_structure_revision: Option<Counter>,
    /// Required nullable entry state: None means a qualified complete inventory
    /// has no such target. This does not invent a provisional revision zero.
    pub entry: Option<EntryVersion>,
    pub covered: bool,
}

#[derive(Clone, Debug, PartialEq)]
pub struct EntryVersion {
    pub revision: Counter,
    pub structure_revision: Counter,
    pub deleted: bool,
}

/// Not accepted by the existing `agent_confirmed_current` preparation API.
/// Historical chain length does not increase this compact context's wire size.
#[derive(Clone, Debug, PartialEq)]
pub struct CausalPrepareContext {
    pub document_revision: Counter,
    pub targets: Vec<TargetVersion>,
    pub operation_revisions: Vec<PreparedOperationRevision>,
    pub qualified_reads: Vec<QualifiedRead>,
}

/// Local SQL traversal limits; distinct from frame size or generic PRD maxima.
/// Exceeding any work budget is nonterminal and preserves pending drafts.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct HistoryBudget {
    pub operations: usize,
    pub dependency_edges: usize,
    pub effects: usize,
}
impl Default for HistoryBudget {
    fn default() -> Self {
        Self {
            operations: 10_000,
            dependency_edges: 65_536,
            effects: 100_000,
        }
    }
}

use crate::{
    adapter::PreparedFailure,
    prepare::{apply_effects, validate_layout, ContextError},
};
use mooring_protocol::{
    wire::valid_identifier, AtomicChange, ErrorCode, Operation, TargetEffect, TargetIdentity,
};
use std::collections::{BTreeMap, BTreeSet};

/// Validated compact host attestations. This API does not itself negotiate the
/// future causal capability or authenticate the caller's host provenance.
pub struct CausalContext<'a> {
    context: &'a CausalPrepareContext,
    operations: &'a [Operation],
    internal_closures: Vec<BTreeSet<usize>>,
}
impl<'a> CausalContext<'a> {
    pub fn qualify(
        context: &'a CausalPrepareContext,
        change: &'a AtomicChange,
        inventory: &[TargetIdentity],
    ) -> Result<Self, ContextError> {
        let targets = validate_layout(
            context.document_revision,
            &context.targets,
            &context.operation_revisions,
            change,
            inventory,
        )?;
        let mut ids = BTreeMap::new();
        for (index, operation) in change.operations.iter().enumerate() {
            ids.insert(operation.op_id.clone(), index);
        }
        let mut closures: Vec<BTreeSet<usize>> = Vec::with_capacity(change.operations.len());
        let mut expected_reads = 0usize;
        for (index, operation) in change.operations.iter().enumerate() {
            if operation.dependencies.len() > 256 || operation.read_preconditions.len() > 256 {
                return Err(ContextError::Invalid);
            }
            expected_reads += operation.read_preconditions.len();
            let mut closure = BTreeSet::new();
            let mut dependencies = BTreeSet::new();
            for dependency in &operation.dependencies {
                if !valid_identifier(dependency) || !dependencies.insert(dependency) {
                    return Err(ContextError::Invalid);
                }
                if let Some(prior) = ids.get(dependency) {
                    if *prior >= index {
                        return Err(ContextError::Invalid);
                    }
                    closure.insert(*prior);
                    closure.extend(closures[*prior].iter().copied());
                }
            }
            closures.push(closure);
        }
        if context.qualified_reads.len() != expected_reads {
            return Err(ContextError::Invalid);
        }
        // First validate ALL attestation bindings. A malformed later binding
        // must not turn an earlier stale observation into a terminal outcome.
        let mut supplied = BTreeMap::new();
        for qualification in &context.qualified_reads {
            let Some(index) = ids.get(&qualification.op_id) else {
                return Err(ContextError::Invalid);
            };
            let Some(read) = change.operations[*index]
                .read_preconditions
                .get(qualification.read_index as usize)
            else {
                return Err(ContextError::Invalid);
            };
            if qualification.read_index > 255
                || qualification.target_id != read.target_id
                || qualification.observed_revision != read.revision
                || qualification.observed_structure_revision != read.structure_revision
                || supplied
                    .insert((*index, qualification.read_index as usize), qualification)
                    .is_some()
            {
                return Err(ContextError::Invalid);
            }
            let expected = targets.get(&read.target_id).map(|target| EntryVersion {
                revision: target.revision,
                structure_revision: target.structure_revision,
                deleted: target.deleted,
            });
            if qualification.entry != expected {
                return Err(ContextError::Invalid);
            }
            if let Some(entry) = &qualification.entry {
                if !entry.deleted
                    && read.revision == entry.revision
                    && read
                        .structure_revision
                        .is_none_or(|revision| revision == entry.structure_revision)
                    && !qualification.covered
                {
                    return Err(ContextError::Invalid);
                }
            }
            if qualification.covered {
                let Some(entry) = &qualification.entry else {
                    return Err(ContextError::Invalid);
                };
                if entry.deleted
                    || read.revision > entry.revision
                    || read
                        .structure_revision
                        .is_some_and(|structure| structure > read.revision)
                {
                    return Err(ContextError::Invalid);
                }
                if read.revision == entry.revision
                    && read
                        .structure_revision
                        .is_some_and(|structure| structure != entry.structure_revision)
                {
                    return Err(ContextError::Invalid);
                }
            }
        }
        for (index, operation) in change.operations.iter().enumerate() {
            if operation.base_revision > context.document_revision {
                return Err(read_failure(operation, ErrorCode::PreconditionFailed));
            }
            let mut seen = BTreeSet::new();
            for (read_index, read) in operation.read_preconditions.iter().enumerate() {
                if !valid_identifier(&read.target_id) || !seen.insert(&read.target_id) {
                    return Err(read_failure(operation, ErrorCode::InvalidRequest));
                }
                let qualification = supplied
                    .get(&(index, read_index))
                    .ok_or(ContextError::Invalid)?;
                let Some(entry) = &qualification.entry else {
                    return Err(read_failure(operation, ErrorCode::InvalidRequest));
                };
                if entry.deleted {
                    return Err(read_failure(operation, ErrorCode::TargetDeleted));
                }
                if !qualification.covered {
                    return Err(read_failure(operation, ErrorCode::PreconditionFailed));
                }
            }
        }
        Ok(Self {
            context,
            operations: &change.operations,
            internal_closures: closures,
        })
    }
    pub fn working(&self) -> CausalWorkingTargets<'_> {
        CausalWorkingTargets {
            targets: self
                .context
                .targets
                .iter()
                .map(|target| (target.target_id.clone(), target.clone()))
                .collect(),
            labels: &self.context.operation_revisions,
            operations: self.operations,
            internal_closures: &self.internal_closures,
            prefix: BTreeMap::new(),
            next: 0,
            checked: false,
        }
    }
    pub fn document_revision(&self) -> Counter {
        self.context.document_revision
    }
}

/// Keeps original confirmed observations intact; prior candidate effects carry
/// only immutable member indices and supplied host labels, never Saved status.
pub struct CausalWorkingTargets<'a> {
    targets: BTreeMap<String, TargetVersion>,
    labels: &'a [PreparedOperationRevision],
    operations: &'a [Operation],
    internal_closures: &'a [BTreeSet<usize>],
    prefix: BTreeMap<String, Vec<usize>>,
    next: usize,
    checked: bool,
}
impl CausalWorkingTargets<'_> {
    pub fn check_current_reads(&mut self, op_id: &str) -> Result<(), ContextError> {
        self.checked = false;
        let operation = self
            .operations
            .get(self.next)
            .ok_or(ContextError::Invalid)?;
        if operation.op_id != op_id {
            return Err(ContextError::Invalid);
        }
        for read in &operation.read_preconditions {
            let Some(target) = self.targets.get(&read.target_id) else {
                return Err(read_failure(operation, ErrorCode::InvalidRequest));
            };
            if target.deleted {
                return Err(read_failure(operation, ErrorCode::TargetDeleted));
            }
            if self.prefix.get(&read.target_id).is_some_and(|effects| {
                effects
                    .iter()
                    .any(|index| !self.internal_closures[self.next].contains(index))
            }) {
                return Err(read_failure(operation, ErrorCode::PreconditionFailed));
            }
        }
        self.checked = true;
        Ok(())
    }
    pub fn advance_effects(
        &mut self,
        op_id: &str,
        effects: &[TargetEffect],
    ) -> Result<(), ContextError> {
        let label = self.labels.get(self.next).ok_or(ContextError::Invalid)?;
        if !self.checked || label.op_id != op_id {
            return Err(ContextError::Invalid);
        }
        apply_effects(&mut self.targets, label.revision, effects)?;
        for effect in effects {
            self.prefix
                .entry(effect.target_id.clone())
                .or_default()
                .push(self.next);
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
fn read_failure(operation: &Operation, code: ErrorCode) -> ContextError {
    ContextError::Read(PreparedFailure {
        operation_id: operation.op_id.clone(),
        code,
        message: "confirmed causal read precondition failed".into(),
    })
}
