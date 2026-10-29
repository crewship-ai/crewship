//! Shared planner intersection. EligibleForHost is NOT a saved host receipt.
use mooring_core::resolve::*;
use mooring_protocol::canonical;
use std::collections::BTreeMap;

fn code(raw: &str) -> Code {
    match raw {
        "dependency_cycle" => Code::DependencyCycle,
        "dependency_rejected" => Code::DependencyRejected,
        "invalid_request" => Code::InvalidRequest,
        "target_deleted" => Code::TargetDeleted,
        "task_cancelled" => Code::TaskCancelled,
        "unknown_dependency" => Code::UnknownDependency,
        other => panic!("new fixture code needs explicit coverage: {other}"),
    }
}
#[test]
fn all_thirteen_shared_atomic_dependency_and_root_vectors() {
    let raw = include_bytes!("../../../protocol/batch-resolution-fixtures.json");
    let parsed = canonical::normalize(raw).unwrap();
    let cases = parsed.value()["cases"].as_array().unwrap();
    assert_eq!(cases.len(), 13);
    for case in cases {
        let id = case["id"].as_str().unwrap();
        let changes: Vec<_> = case["changes"]
            .as_array()
            .unwrap()
            .iter()
            .map(|change| AtomicChange {
                change_id: change["change_id"].as_str().unwrap().into(),
                task_id: None,
                encoded_bytes: 100,
                operations: change["operations"]
                    .as_array()
                    .unwrap()
                    .iter()
                    .map(|operation| {
                        let op_id = operation["op_id"].as_str().unwrap().to_string();
                        Operation {
                            fingerprint: format!("fixture:{op_id}"),
                            op_id,
                            dependencies: operation["dependencies"]
                                .as_array()
                                .unwrap()
                                .iter()
                                .map(|v| v.as_str().unwrap().into())
                                .collect(),
                        }
                    })
                    .collect(),
            })
            .collect();
        let mut prior = BTreeMap::new();
        if let Some(outcomes) = case["prior_outcomes"].as_array() {
            for outcome in outcomes {
                let op_id = outcome["op_id"].as_str().unwrap();
                assert_eq!(outcome["status"], "rejected");
                prior.insert(
                    op_id.to_string(),
                    PriorOperation {
                        fingerprint: format!("fixture:{op_id}"),
                        change_id: format!("prior:{op_id}"),
                        member_ids: vec![op_id.into()],
                        task_id: None,
                        outcome: HostOutcome::Rejected(Rejection::direct(
                            op_id,
                            code(outcome["code"].as_str().unwrap()),
                            "original host reason",
                        )),
                    },
                );
            }
        }
        if let Some(known) = case["known_change_ids"].as_array() {
            for change_id in known {
                let change = changes
                    .iter()
                    .find(|c| c.change_id == change_id.as_str().unwrap())
                    .unwrap();
                for op in &change.operations {
                    let expected = case["expected"]
                        .as_array()
                        .unwrap()
                        .iter()
                        .find(|v| v["op_id"] == op.op_id)
                        .unwrap();
                    prior.insert(
                        op.op_id.clone(),
                        PriorOperation {
                            fingerprint: op.fingerprint.clone(),
                            change_id: change.change_id.clone(),
                            member_ids: change
                                .operations
                                .iter()
                                .map(|op| op.op_id.clone())
                                .collect(),
                            task_id: None,
                            outcome: HostOutcome::Rejected(Rejection::direct(
                                &op.op_id,
                                code(expected["code"].as_str().unwrap()),
                                "immutable original rejection",
                            )),
                        },
                    );
                }
            }
        }
        let mut failures = BTreeMap::new();
        // Qualified adapter failures here are pure supplied planner inputs, not
        // execution of an app engine or host SQL mutation.
        for field in ["direct_failures", "adapter_failures"] {
            if let Some(items) = case[field].as_array() {
                for item in items {
                    let op_id = item["op_id"].as_str().unwrap();
                    failures.insert(
                        op_id.to_string(),
                        Rejection::direct(
                            op_id,
                            code(item["code"].as_str().unwrap()),
                            "qualified reason",
                        ),
                    );
                }
            }
        }
        let actual = plan(&changes, &prior, &failures, Limits::default()).unwrap();
        let operations: Vec<_> = actual.changes.iter().flat_map(|c| &c.operations).collect();
        let expected = case["expected"].as_array().unwrap();
        assert_eq!(operations.len(), expected.len(), "{id}");
        for expected in expected {
            let op_id = expected["op_id"].as_str().unwrap();
            let actual = operations.iter().find(|v| v.op_id == op_id).unwrap();
            match &actual.decision {
                Decision::EligibleForHost => assert_eq!(
                    expected["status"], "saved",
                    "{id}/{op_id}: only eligible, never host-Saved"
                ),
                Decision::Rejected(reason) | Decision::Replay(HostOutcome::Rejected(reason)) => {
                    assert_eq!(expected["status"], "rejected", "{id}/{op_id}");
                    assert_eq!(
                        reason.code,
                        code(expected["code"].as_str().unwrap()),
                        "{id}/{op_id}"
                    );
                    assert_eq!(
                        reason.root.op_id,
                        expected["root_op_id"].as_str().unwrap(),
                        "{id}/{op_id}"
                    );
                    assert_eq!(
                        reason.root.code,
                        code(expected["root_code"].as_str().unwrap()),
                        "{id}/{op_id}"
                    );
                }
                Decision::Replay(HostOutcome::PreviouslyConfirmed) => panic!(
                    "new previously-saved fixture needs explicit qualification: {id}/{op_id}"
                ),
            }
        }
    }
}
