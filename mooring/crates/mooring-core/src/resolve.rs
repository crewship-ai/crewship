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
    InvalidRejection,
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
    !id.is_empty() && id.len() <= 200 && !id.contains('\0')
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
    if direct.iter().any(|(id, reason)| {
        reason.root.op_id != *id
            || reason.code != reason.root.code
            || reason.code == Code::DependencyRejected
    }) {
        return Err(InvalidBatch::InvalidRejection);
    }
    let preflight = admission_failures(changes, prior, direct, &op_to_change);
    let cycles = cyclic_changes(changes, prior, &preflight, &op_to_change);
    let mut context = Context {
        changes,
        prior,
        direct: &preflight,
        op_to_change,
        cycles,
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
                            AtomicDecision::Rejected(reason) => {
                                Decision::Rejected(if op.op_id == reason.root.op_id {
                                    reason.clone()
                                } else {
                                    Rejection::dependent(reason.root.clone())
                                })
                            }
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
    cycles: Vec<Option<String>>,
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
        if let Some(root) = &self.cycles[index] {
            return AtomicDecision::Rejected(Rejection::direct(
                root,
                Code::DependencyCycle,
                "indivisible atomic changes form a dependency cycle",
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
                    return AtomicDecision::Rejected(Rejection::dependent(reason.root));
                }
            }
        }
        AtomicDecision::Eligible
    }
}
fn internal_cycle(change: &AtomicChange) -> Option<String> {
    let operations: BTreeMap<_, _> = change
        .operations
        .iter()
        .map(|operation| (operation.op_id.as_str(), operation))
        .collect();
    // Choose the first SUBMITTED actual cyclic member, not a DFS backedge
    // entry or an acyclic ancestor leading into another member's cycle.
    for operation in &change.operations {
        let mut pending: Vec<_> = operation
            .dependencies
            .iter()
            .filter(|dependency| operations.contains_key(dependency.as_str()))
            .map(String::as_str)
            .collect();
        let mut visited = BTreeSet::new();
        while let Some(id) = pending.pop() {
            if id == operation.op_id {
                return Some(operation.op_id.clone());
            }
            if !visited.insert(id) {
                continue;
            }
            pending.extend(
                operations[id]
                    .dependencies
                    .iter()
                    .filter(|dependency| operations.contains_key(dependency.as_str()))
                    .map(String::as_str),
            );
        }
    }
    None
}

/// Every vertex reachable back to itself is a complete SCC cycle member.
/// Bounded by at most 256 operations/changes; repeated iterative reachability
/// avoids recursive SCC bookkeeping and DFS "already black" misclassification.
fn cyclic_changes(
    changes: &[AtomicChange],
    prior: &BTreeMap<String, PriorOperation>,
    direct: &BTreeMap<String, Rejection>,
    owners: &BTreeMap<String, usize>,
) -> Vec<Option<String>> {
    let mut edges = vec![Vec::new(); changes.len()];
    for (index, change) in changes.iter().enumerate() {
        // Replays/conflicts and direct invalid atomic changes do not execute.
        // They terminate dependency propagation rather than creating new edges.
        if change
            .operations
            .iter()
            .any(|op| prior.contains_key(&op.op_id) || direct.contains_key(&op.op_id))
        {
            continue;
        }
        for operation in &change.operations {
            for dependency in &operation.dependencies {
                if let Some(&owner) = owners.get(dependency) {
                    if owner != index {
                        edges[index].push(owner);
                    }
                }
            }
        }
        edges[index].sort_unstable();
        edges[index].dedup();
    }
    let mut reachable = vec![vec![false; changes.len()]; changes.len()];
    for start in 0..changes.len() {
        let mut pending = edges[start].clone();
        while let Some(node) = pending.pop() {
            if reachable[start][node] {
                continue;
            }
            reachable[start][node] = true;
            pending.extend(&edges[node]);
        }
    }
    changes
        .iter()
        .enumerate()
        .map(|(index, change)| {
            if !reachable[index][index] {
                return None;
            }
            change
                .operations
                .iter()
                .find(|operation| {
                    operation.dependencies.iter().any(|dependency| {
                        owners
                            .get(dependency)
                            .is_some_and(|&owner| owner != index && reachable[owner][index])
                    })
                })
                .map(|operation| operation.op_id.clone())
        })
        .collect()
}

/// Gather definite nonexecuting outcomes before SCC. Propagation removes a
/// failed vertex's outgoing edges, so an apparent cycle cannot hide an already
/// definite admission/dependency rejection. No adapter is invoked here.
fn admission_failures(
    changes: &[AtomicChange],
    prior: &BTreeMap<String, PriorOperation>,
    direct: &BTreeMap<String, Rejection>,
    owners: &BTreeMap<String, usize>,
) -> BTreeMap<String, Rejection> {
    let mut failures = BTreeMap::new();
    for change in changes {
        let members: Vec<_> = change
            .operations
            .iter()
            .map(|op| op.op_id.clone())
            .collect();
        let conflict = change.operations.iter().find(|op| {
            prior.get(&op.op_id).is_some_and(|old| {
                old.fingerprint != op.fingerprint
                    || old.change_id != change.change_id
                    || old.member_ids != members
                    || old.task_id != change.task_id
            })
        });
        if let Some(op) = conflict {
            failures.insert(
                op.op_id.clone(),
                Rejection::direct(
                    &op.op_id,
                    Code::OpIdConflict,
                    "operation content, atomic membership or task changed",
                ),
            );
            continue;
        }
        if prior.contains_key(&change.operations[0].op_id) {
            if let Some(reason) =
                change
                    .operations
                    .iter()
                    .find_map(|op| match &prior[&op.op_id].outcome {
                        HostOutcome::Rejected(reason) => Some(reason),
                        _ => None,
                    })
            {
                failures.insert(change.operations[0].op_id.clone(), reason.clone());
            }
            continue; // committed/rejected matching retries never revalidate
        }
        if let Some((op, reason)) = change
            .operations
            .iter()
            .find_map(|op| direct.get(&op.op_id).map(|reason| (op, reason)))
        {
            failures.insert(op.op_id.clone(), reason.clone());
            continue;
        }
        'operations: for op in &change.operations {
            for dependency in &op.dependencies {
                if owners.contains_key(dependency) {
                    continue;
                }
                let reason = match prior.get(dependency) {
                    Some(old) => match &old.outcome {
                        HostOutcome::PreviouslyConfirmed => continue,
                        HostOutcome::Rejected(reason) => Rejection::dependent(reason.root.clone()),
                    },
                    None => Rejection::direct(
                        &op.op_id,
                        Code::UnknownDependency,
                        &format!("unknown dependency {dependency}"),
                    ),
                };
                failures.insert(op.op_id.clone(), reason);
                break 'operations;
            }
        }
    }
    // At most one new failed change per pass is needed for progress; the
    // operation/dependency iteration order makes selection deterministic.
    loop {
        let mut added = false;
        for change in changes {
            if change
                .operations
                .iter()
                .any(|op| failures.contains_key(&op.op_id) || prior.contains_key(&op.op_id))
            {
                continue;
            }
            'operations: for op in &change.operations {
                for dependency in &op.dependencies {
                    if let Some(&owner) = owners.get(dependency) {
                        if let Some(root) = changes[owner].operations.iter().find_map(|member| {
                            failures
                                .get(&member.op_id)
                                .map(|reason| reason.root.clone())
                        }) {
                            failures.insert(op.op_id.clone(), Rejection::dependent(root));
                            added = true;
                            break 'operations;
                        }
                    }
                }
            }
        }
        if !added {
            break;
        }
    }
    failures
}
