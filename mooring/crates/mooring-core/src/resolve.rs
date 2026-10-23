//! Dependency/atomicity planning, never durable confirmation. Callers supply
//! host-owned prior outcomes and fingerprints calculated from canonical full
//! operation semantics; this module neither authenticates nor stores outcomes.
use std::collections::{BTreeMap, BTreeSet};

pub use mooring_protocol::ErrorCode as Code;

#[derive(Clone, Debug, PartialEq)]
pub struct RootCause {
    pub op_id: String,
    pub code: Code,
    pub message: String,
}
#[derive(Clone, Debug, PartialEq)]
pub struct Rejection {
    pub code: Code,
    pub root: RootCause,
}
impl Rejection {
    pub fn direct(op_id: &str, code: Code, message: &str) -> Self {
        Self {
            code: code.clone(),
            root: RootCause {
                op_id: op_id.into(),
                code,
                message: message.into(),
            },
        }
    }
    fn dependent(root: RootCause) -> Self {
        Self {
            code: Code::DependencyRejected,
            root,
        }
    }
}
#[derive(Clone, Debug, PartialEq)]
pub enum HostOutcome {
    PreviouslyConfirmed,
    Rejected(Rejection),
}
#[derive(Clone, Debug)]
pub struct PriorOperation {
    pub fingerprint: String,
    pub change_id: String,
    pub member_ids: Vec<String>,
    pub task_id: Option<String>,
    pub outcome: HostOutcome,
}
#[derive(Clone, Debug)]
pub struct Operation {
    pub op_id: String,
    pub fingerprint: String,
    pub dependencies: Vec<String>,
}
#[derive(Clone, Debug)]
pub struct AtomicChange {
    pub change_id: String,
    pub task_id: Option<String>,
    pub operations: Vec<Operation>,
    /// Complete encoded change envelope bytes, calculated by the caller. The
    /// fixed batch envelope must also be reserved in the configured byte limit.
    pub encoded_bytes: usize,
}
#[derive(Clone, Copy, Debug)]
pub struct Limits {
    pub max_operations: usize,
    pub max_bytes: usize,
}
impl Default for Limits {
    fn default() -> Self {
        Self {
            max_operations: 256,
            max_bytes: 256 * 1024,
        }
    }
}
#[derive(Debug, PartialEq)]
pub enum InvalidBatch {
    Empty,
    TooLarge,
    InvalidLimits,
    DuplicateId,
    InvalidId,
    InconsistentAtomicHistory,
}
#[derive(Clone, Debug, PartialEq)]
pub enum Decision {
    EligibleForHost,
    Replay(HostOutcome),
    Rejected(Rejection),
}
#[derive(Clone, Debug, PartialEq)]
pub struct OperationResult {
    pub op_id: String,
    pub decision: Decision,
}
#[derive(Debug, PartialEq)]
pub struct ChangeResult {
    pub change_id: String,
    pub operations: Vec<OperationResult>,
}
#[derive(Debug, PartialEq)]
pub struct Plan {
    pub changes: Vec<ChangeResult>,
    /// Dependency order for NEW eligible changes only. Replay never executes.
    pub execution_order: Vec<String>,
}
#[derive(Clone)]
enum AtomicDecision {
    Eligible,
    Replay(Vec<HostOutcome>),
    Rejected(Rejection),
}
impl AtomicDecision {
    fn failure(&self) -> Option<&Rejection> {
        match self {
            Self::Rejected(reason) => Some(reason),
            Self::Replay(outcomes) => outcomes.iter().find_map(|outcome| match outcome {
                HostOutcome::Rejected(reason) => Some(reason),
                _ => None,
            }),
            Self::Eligible => None,
        }
    }
}
fn valid_id(id: &str) -> bool {
    mooring_protocol::wire::valid_identifier(id)
}

/// Plans bounded atomic changes without modifying them or retaining state. A
/// matching retry returns the original host outcome even if validation would
/// now differ. Callers still authorize status access and every new commit in
/// the host's writer transaction; EligibleForHost is not a saved receipt.
pub fn plan(
    changes: &[AtomicChange],
    prior: &BTreeMap<String, PriorOperation>,
    direct: &BTreeMap<String, Rejection>,
    limits: Limits,
) -> Result<Plan, InvalidBatch> {
    if changes.is_empty() {
        return Err(InvalidBatch::Empty);
    }
    if limits.max_operations == 0 || limits.max_operations > 256 || limits.max_bytes == 0 {
        return Err(InvalidBatch::InvalidLimits);
    }
    let mut op_to_change = BTreeMap::new();
    let mut change_ids = BTreeSet::new();
    let (mut operations, mut bytes) = (0usize, 0usize);
    for (index, change) in changes.iter().enumerate() {
        if !valid_id(&change.change_id)
            || change.task_id.as_ref().is_some_and(|id| !valid_id(id))
            || change.operations.is_empty()
            || change.encoded_bytes == 0
        {
            return Err(InvalidBatch::InvalidId);
        }
        if !change_ids.insert(&change.change_id) {
            return Err(InvalidBatch::DuplicateId);
        }
        operations = operations
            .checked_add(change.operations.len())
            .ok_or(InvalidBatch::TooLarge)?;
        bytes = bytes
            .checked_add(change.encoded_bytes)
            .ok_or(InvalidBatch::TooLarge)?;
        if operations > limits.max_operations || bytes > limits.max_bytes {
            return Err(InvalidBatch::TooLarge);
        }
        let known = change
            .operations
            .iter()
            .filter(|op| prior.contains_key(&op.op_id))
            .count();
        let member_ids: Vec<_> = change
            .operations
            .iter()
            .map(|op| op.op_id.clone())
            .collect();
        let identity_conflict = change.operations.iter().any(|op| {
            prior.get(&op.op_id).is_some_and(|old| {
                old.fingerprint != op.fingerprint
                    || old.change_id != change.change_id
                    || old.member_ids != member_ids
                    || old.task_id != change.task_id
            })
        });
        if !identity_conflict && known != 0 && known != change.operations.len() {
            return Err(InvalidBatch::InconsistentAtomicHistory);
        }
        if !identity_conflict && known != 0 {
            let confirmed = change
                .operations
                .iter()
                .filter(|op| matches!(prior[&op.op_id].outcome, HostOutcome::PreviouslyConfirmed))
                .count();
            if confirmed != 0 && confirmed != known {
                return Err(InvalidBatch::InconsistentAtomicHistory);
            }
        }
        for op in &change.operations {
            if !valid_id(&op.op_id)
                || op.fingerprint.is_empty()
                || op.fingerprint.len() > 200
                || op.dependencies.len() > limits.max_operations
                || op.dependencies.iter().any(|dep| !valid_id(dep))
            {
                return Err(InvalidBatch::InvalidId);
            }
            if op_to_change.insert(op.op_id.clone(), index).is_some() {
                return Err(InvalidBatch::DuplicateId);
            }
        }
    }
    if direct.keys().any(|id| !op_to_change.contains_key(id)) {
        return Err(InvalidBatch::InvalidId);
    }
    let mut context = Context {
        changes,
        prior,
        direct,
        op_to_change,
        visiting: vec![false; changes.len()],
        outcomes: vec![None; changes.len()],
        order: Vec::new(),
    };
    for index in 0..changes.len() {
        context.evaluate(index);
    }
    let results = changes
        .iter()
        .zip(&context.outcomes)
        .map(|(change, outcome)| {
            let outcome = outcome.as_ref().expect("every bounded change evaluated");
            ChangeResult {
                change_id: change.change_id.clone(),
                operations: change
                    .operations
                    .iter()
                    .enumerate()
                    .map(|(index, op)| OperationResult {
                        op_id: op.op_id.clone(),
                        decision: match outcome {
                            AtomicDecision::Eligible => Decision::EligibleForHost,
                            AtomicDecision::Rejected(reason) => Decision::Rejected(reason.clone()),
                            AtomicDecision::Replay(outcomes) => {
                                Decision::Replay(outcomes[index].clone())
                            }
                        },
                    })
                    .collect(),
            }
        })
        .collect();
    Ok(Plan {
        changes: results,
        execution_order: context.order,
    })
}
struct Context<'a> {
    changes: &'a [AtomicChange],
    prior: &'a BTreeMap<String, PriorOperation>,
    direct: &'a BTreeMap<String, Rejection>,
    op_to_change: BTreeMap<String, usize>,
    visiting: Vec<bool>,
    outcomes: Vec<Option<AtomicDecision>>,
    order: Vec<String>,
}
impl Context<'_> {
    fn evaluate(&mut self, index: usize) -> AtomicDecision {
        if let Some(outcome) = &self.outcomes[index] {
            return outcome.clone();
        }
        if self.visiting[index] {
            return AtomicDecision::Rejected(Rejection::direct(
                &self.changes[index].operations[0].op_id,
                Code::DependencyCycle,
                "atomic dependency cycle",
            ));
        }
        self.visiting[index] = true;
        let decision = self.evaluate_new(index);
        self.visiting[index] = false;
        if matches!(decision, AtomicDecision::Eligible) {
            self.order.push(self.changes[index].change_id.clone());
        }
        self.outcomes[index] = Some(decision.clone());
        decision
    }
    fn evaluate_new(&mut self, index: usize) -> AtomicDecision {
        let change = &self.changes[index];
        let members: Vec<_> = change
            .operations
            .iter()
            .map(|op| op.op_id.clone())
            .collect();
        for op in &change.operations {
            if let Some(old) = self.prior.get(&op.op_id) {
                if old.fingerprint != op.fingerprint
                    || old.change_id != change.change_id
                    || old.member_ids != members
                    || old.task_id != change.task_id
                {
                    return AtomicDecision::Rejected(Rejection::direct(
                        &op.op_id,
                        Code::OpIdConflict,
                        "operation content, atomic membership or task changed",
                    ));
                }
            }
        }
        if self.prior.contains_key(&change.operations[0].op_id) {
            let outcomes: Vec<_> = change
                .operations
                .iter()
                .map(|op| self.prior[&op.op_id].outcome.clone())
                .collect();
            let confirmed = outcomes
                .iter()
                .filter(|outcome| matches!(outcome, HostOutcome::PreviouslyConfirmed))
                .count();
            // Mixed confirmed/rejected history cannot be a valid atomic commit.
            // This condition is checked separately at the public boundary below.
            debug_assert!(confirmed == 0 || confirmed == outcomes.len());
            return AtomicDecision::Replay(outcomes);
        }
        for op in &change.operations {
            if let Some(reason) = self.direct.get(&op.op_id) {
                return AtomicDecision::Rejected(reason.clone());
            }
        }
        if let Some(root) = internal_cycle(change) {
            return AtomicDecision::Rejected(Rejection::direct(
                &root,
                Code::DependencyCycle,
                "operation dependency cycle inside atomic change",
            ));
        }
        // Atomic membership is fingerprinted in application order. Reordering
        // operations here would change the authenticated command semantics.
        for (position, op) in change.operations.iter().enumerate() {
            if op.dependencies.iter().any(|dependency| {
                change.operations[position..]
                    .iter()
                    .any(|later| later.op_id == *dependency)
            }) {
                return AtomicDecision::Rejected(Rejection::direct(
                    &op.op_id,
                    Code::InvalidRequest,
                    "dependency must precede its operation inside atomic change",
                ));
            }
        }
        for op in &change.operations {
            for dependency in &op.dependencies {
                let failure = if let Some(&owner) = self.op_to_change.get(dependency) {
                    if owner == index {
                        continue;
                    }
                    self.evaluate(owner).failure().cloned()
                } else if let Some(old) = self.prior.get(dependency) {
                    match &old.outcome {
                        HostOutcome::PreviouslyConfirmed => None,
                        HostOutcome::Rejected(reason) => Some(reason.clone()),
                    }
                } else {
                    return AtomicDecision::Rejected(Rejection::direct(
                        &op.op_id,
                        Code::UnknownDependency,
                        &format!("unknown dependency {dependency}"),
                    ));
                };
                if let Some(reason) = failure {
                    if reason.root.code == Code::DependencyCycle
                        && members.contains(&reason.root.op_id)
                    {
                        return AtomicDecision::Rejected(Rejection {
                            code: Code::DependencyCycle,
                            root: reason.root,
                        });
                    }
                    return AtomicDecision::Rejected(Rejection::dependent(reason.root));
                }
            }
        }
        AtomicDecision::Eligible
    }
}
fn internal_cycle(change: &AtomicChange) -> Option<String> {
    fn visit<'a>(
        id: &'a str,
        operations: &BTreeMap<&'a str, &'a Operation>,
        active: &mut BTreeSet<&'a str>,
        done: &mut BTreeSet<&'a str>,
    ) -> Option<String> {
        if active.contains(id) {
            return Some(id.into());
        }
        if done.contains(id) {
            return None;
        }
        active.insert(id);
        for dependency in &operations[id].dependencies {
            if operations.contains_key(dependency.as_str()) {
                if let Some(root) = visit(dependency, operations, active, done) {
                    return Some(root);
                }
            }
        }
        active.remove(id);
        done.insert(id);
        None
    }
    let operations: BTreeMap<_, _> = change
        .operations
        .iter()
        .map(|op| (op.op_id.as_str(), op))
        .collect();
    let (mut active, mut done) = (BTreeSet::new(), BTreeSet::new());
    for op in &change.operations {
        if let Some(root) = visit(&op.op_id, &operations, &mut active, &mut done) {
            return Some(root);
        }
    }
    None
}
