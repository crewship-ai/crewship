use mooring_core::resolve::*;
use std::collections::BTreeMap;
fn op(id: &str, deps: &[&str]) -> Operation {
    Operation {
        op_id: id.into(),
        fingerprint: format!("fingerprint:{id}"),
        dependencies: deps.iter().map(|s| (*s).into()).collect(),
    }
}
fn change(id: &str, operations: Vec<Operation>) -> AtomicChange {
    AtomicChange {
        change_id: id.into(),
        task_id: Some("task".into()),
        operations,
        encoded_bytes: 100,
    }
}
fn run(
    changes: &[AtomicChange],
    prior: &BTreeMap<String, PriorOperation>,
    direct: &BTreeMap<String, Rejection>,
) -> Plan {
    plan(changes, prior, direct, Limits::default()).unwrap()
}
fn rejected(decision: &Decision) -> &Rejection {
    match decision {
        Decision::Rejected(reason) => reason,
        _ => panic!("expected rejection: {decision:?}"),
    }
}

#[test]
fn d4_atomic_failure_cascades_transitively_but_independent_change_survives() {
    let changes = [
        change("a", vec![op("A1", &[]), op("A2", &[])]),
        change("b", vec![op("B", &["A1"])]),
        change("c", vec![op("C", &["B"])]),
        change("d", vec![op("D", &[])]),
    ];
    let failures = BTreeMap::from([(
        "A2".into(),
        Rejection::direct("A2", Code::PermissionRevoked, "revoked while batched"),
    )]);
    let result = run(&changes, &BTreeMap::new(), &failures);
    for op in &result.changes[0].operations {
        assert_eq!(rejected(&op.decision).code, Code::PermissionRevoked);
    }
    for index in [1, 2] {
        let reason = rejected(&result.changes[index].operations[0].decision);
        assert_eq!(reason.code, Code::DependencyRejected);
        assert_eq!(reason.root.op_id, "A2");
        assert_eq!(reason.root.code, Code::PermissionRevoked);
    }
    assert_eq!(
        result.changes[3].operations[0].decision,
        Decision::EligibleForHost
    );
    assert_eq!(result.execution_order, vec!["d"]);
}

#[test]
fn d4_forward_dependencies_get_execution_order_and_cycles_fail_closed() {
    let changes = [
        change("b", vec![op("B", &["A"])]),
        change("a", vec![op("A", &[])]),
    ];
    assert_eq!(
        run(&changes, &BTreeMap::new(), &BTreeMap::new()).execution_order,
        vec!["a", "b"]
    );
    let cycle = [
        change("a", vec![op("A", &["B"])]),
        change("b", vec![op("B", &["A"])]),
        change("c", vec![op("C", &[])]),
    ];
    let result = run(&cycle, &BTreeMap::new(), &BTreeMap::new());
    assert_eq!(
        rejected(&result.changes[0].operations[0].decision)
            .root
            .code,
        Code::DependencyCycle
    );
    assert_eq!(
        rejected(&result.changes[1].operations[0].decision)
            .root
            .code,
        Code::DependencyCycle
    );
    assert_eq!(result.execution_order, vec!["c"]);
}

#[test]
fn d4_internal_cycle_and_unknown_reference_are_explicit() {
    let changes = [
        change("a", vec![op("A1", &["A2"]), op("A2", &["A1"])]),
        change("b", vec![op("B", &["missing"])]),
    ];
    let result = run(&changes, &BTreeMap::new(), &BTreeMap::new());
    assert_eq!(
        rejected(&result.changes[0].operations[0].decision).code,
        Code::DependencyCycle
    );
    assert_eq!(
        rejected(&result.changes[1].operations[0].decision).code,
        Code::UnknownDependency
    );
}
fn known(change: &AtomicChange, outcome: HostOutcome) -> BTreeMap<String, PriorOperation> {
    change
        .operations
        .iter()
        .map(|op| {
            (
                op.op_id.clone(),
                PriorOperation {
                    fingerprint: op.fingerprint.clone(),
                    change_id: change.change_id.clone(),
                    member_ids: change
                        .operations
                        .iter()
                        .map(|op| op.op_id.clone())
                        .collect(),
                    task_id: change.task_id.clone(),
                    outcome: outcome.clone(),
                },
            )
        })
        .collect()
}
#[test]
fn d2_retry_replays_host_outcome_and_never_reexecutes_after_lost_reply() {
    let changes = [change("a", vec![op("A", &[])])];
    let prior = known(&changes[0], HostOutcome::PreviouslyConfirmed);
    let now_revoked = BTreeMap::from([(
        "A".into(),
        Rejection::direct("A", Code::PermissionRevoked, "current role changed"),
    )]);
    let result = run(&changes, &prior, &now_revoked);
    assert_eq!(
        result.changes[0].operations[0].decision,
        Decision::Replay(HostOutcome::PreviouslyConfirmed)
    );
    assert!(result.execution_order.is_empty());
    let original = Rejection::direct("A", Code::PreconditionFailed, "original failure");
    let result = run(
        &changes,
        &known(&changes[0], HostOutcome::Rejected(original.clone())),
        &BTreeMap::new(),
    );
    assert_eq!(
        result.changes[0].operations[0].decision,
        Decision::Replay(HostOutcome::Rejected(original))
    );
}
#[test]
fn p25_full_atomic_membership_and_task_cannot_change_on_retry() {
    let original = change("a", vec![op("A1", &[]), op("A2", &[])]);
    let prior = known(&original, HostOutcome::PreviouslyConfirmed);
    for variant in 0..5 {
        let mut incoming = original.clone();
        match variant {
            0 => incoming.operations.reverse(),
            1 => incoming.task_id = Some("different-task".into()),
            2 => incoming.operations[0].fingerprint = "changed".into(),
            3 => incoming.change_id = "regrouped".into(),
            _ => incoming.operations.push(op("new-member", &[])),
        }
        let result = run(&[incoming], &prior, &BTreeMap::new());
        assert_eq!(
            rejected(&result.changes[0].operations[0].decision).code,
            Code::OpIdConflict
        );
        assert!(result.execution_order.is_empty());
    }
}
#[test]
fn p42_mixed_replay_new_or_mixed_persisted_status_never_partially_executes() {
    let original = change("a", vec![op("A1", &[]), op("A2", &[])]);
    let mut prior = known(&original, HostOutcome::PreviouslyConfirmed);
    prior.remove("A2");
    assert_eq!(
        plan(
            std::slice::from_ref(&original),
            &prior,
            &BTreeMap::new(),
            Limits::default()
        ),
        Err(InvalidBatch::InconsistentAtomicHistory)
    );
    let mut prior = known(&original, HostOutcome::PreviouslyConfirmed);
    prior.get_mut("A2").unwrap().outcome = HostOutcome::Rejected(Rejection::direct(
        "A2",
        Code::QuotaExceeded,
        "impossible partial history",
    ));
    assert_eq!(
        plan(&[original], &prior, &BTreeMap::new(), Limits::default()),
        Err(InvalidBatch::InconsistentAtomicHistory)
    );
}
#[test]
fn p21_total_count_bytes_and_duplicate_ids_are_bounded() {
    let changes = [
        change("a", vec![op("A", &[])]),
        change("b", vec![op("B", &[])]),
    ];
    for limits in [
        Limits {
            max_operations: 1,
            max_bytes: 1000,
        },
        Limits {
            max_operations: 10,
            max_bytes: 199,
        },
    ] {
        assert_eq!(
            plan(&changes, &BTreeMap::new(), &BTreeMap::new(), limits),
            Err(InvalidBatch::TooLarge)
        );
    }
    let duplicate = [
        change("a", vec![op("A", &[])]),
        change("b", vec![op("A", &[])]),
    ];
    assert_eq!(
        plan(
            &duplicate,
            &BTreeMap::new(),
            &BTreeMap::new(),
            Limits::default()
        ),
        Err(InvalidBatch::DuplicateId)
    );
    assert_eq!(changes[0].operations[0].op_id, "A"); // Inputs still owned by caller.
}

#[test]
fn p21_invalid_configuration_cannot_remove_recursion_bound() {
    let changes = [change("a", vec![op("A", &[])])];
    assert_eq!(
        plan(
            &changes,
            &BTreeMap::new(),
            &BTreeMap::new(),
            Limits {
                max_operations: usize::MAX,
                max_bytes: usize::MAX
            }
        ),
        Err(InvalidBatch::InvalidLimits)
    );
}

#[test]
fn d4_atomic_order_is_preserved_and_forward_internal_dependency_is_rejected() {
    let forward = change("forward", vec![op("B", &["A"]), op("A", &[])]);
    let result = run(&[forward], &BTreeMap::new(), &BTreeMap::new());
    assert!(result.execution_order.is_empty());
    for operation in &result.changes[0].operations {
        let reason = rejected(&operation.decision);
        assert_eq!(reason.code, Code::InvalidRequest);
        assert_eq!(reason.root.op_id, "B");
    }
    let backward = change("ordered", vec![op("A", &[]), op("B", &["A"])]);
    let result = run(&[backward], &BTreeMap::new(), &BTreeMap::new());
    assert_eq!(result.execution_order, vec!["ordered"]);
    assert!(result.changes[0]
        .operations
        .iter()
        .all(|op| op.decision == Decision::EligibleForHost));
}
