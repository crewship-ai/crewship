//! Same minimal graph vectors as the host. Saved maps ONLY to EligibleForHost:
//! this pure planner cannot issue receipts or prove adapter/transaction effects.
use mooring_core::resolve::*;
use serde_json::{json, Value};
use std::collections::BTreeMap;
fn code(value: &Value) -> Code {
    serde_json::from_value(value.clone()).unwrap()
}
#[test]
fn shared_host_rust_resolution_vectors_match_the_planning_intersection() {
    let fixtures: Value = serde_json::from_str(include_str!(
        "../../../protocol/batch-resolution-fixtures.json"
    ))
    .unwrap();
    for case in fixtures["cases"].as_array().unwrap() {
        let changes: Vec<_> = case["changes"]
            .as_array()
            .unwrap()
            .iter()
            .map(|change| AtomicChange {
                change_id: change["change_id"].as_str().unwrap().into(),
                task_id: Some("fixture-task".into()),
                encoded_bytes: 100,
                operations: change["operations"]
                    .as_array()
                    .unwrap()
                    .iter()
                    .map(|operation| {
                        let id = operation["op_id"].as_str().unwrap();
                        Operation {
                            op_id: id.into(),
                            fingerprint: format!("fixture:{id}"),
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
        let expected = case["expected"].as_array().unwrap();
        let mut prior = BTreeMap::new();
        for old in case["prior_outcomes"].as_array().into_iter().flatten() {
            let id = old["op_id"].as_str().unwrap();
            let outcome = if old["status"] == "saved" {
                HostOutcome::PreviouslyConfirmed
            } else {
                HostOutcome::Rejected(Rejection::direct(
                    id,
                    code(&old["code"]),
                    "stored fixture outcome",
                ))
            };
            prior.insert(
                id.into(),
                PriorOperation {
                    fingerprint: format!("fixture:{id}"),
                    change_id: format!("stored:{id}"),
                    member_ids: vec![id.into()],
                    task_id: Some("fixture-task".into()),
                    outcome,
                },
            );
        }
        for known in case["known_change_ids"].as_array().into_iter().flatten() {
            let change = changes
                .iter()
                .find(|change| change.change_id == known.as_str().unwrap())
                .unwrap();
            let members: Vec<_> = change
                .operations
                .iter()
                .map(|op| op.op_id.clone())
                .collect();
            for op in &change.operations {
                let expected = expected
                    .iter()
                    .find(|item| item["op_id"] == op.op_id)
                    .unwrap();
                let outcome = if expected["status"] == "saved" {
                    HostOutcome::PreviouslyConfirmed
                } else {
                    HostOutcome::Rejected(Rejection::direct(
                        &op.op_id,
                        code(&expected["code"]),
                        "immutable stored fixture rejection",
                    ))
                };
                prior.insert(
                    op.op_id.clone(),
                    PriorOperation {
                        fingerprint: op.fingerprint.clone(),
                        change_id: change.change_id.clone(),
                        member_ids: members.clone(),
                        task_id: change.task_id.clone(),
                        outcome,
                    },
                );
            }
        }
        let mut direct = BTreeMap::new();
        for failure in case["direct_failures"]
            .as_array()
            .into_iter()
            .flatten()
            .chain(case["adapter_failures"].as_array().into_iter().flatten())
        {
            let id = failure["op_id"].as_str().unwrap();
            direct.insert(
                id.into(),
                Rejection::direct(
                    id,
                    code(&failure["code"]),
                    "supplied actual fixture failure",
                ),
            );
        }
        let result = plan(&changes, &prior, &direct, Limits::default()).unwrap();
        let mut actual = Vec::new();
        for change in result.changes {
            for operation in change.operations {
                let reason = match operation.decision {
                    Decision::EligibleForHost
                    | Decision::Replay(HostOutcome::PreviouslyConfirmed) => {
                        actual.push(json!({"op_id":operation.op_id,"status":"eligible_for_host"}));
                        continue;
                    }
                    Decision::Rejected(reason)
                    | Decision::Replay(HostOutcome::Rejected(reason)) => reason,
                };
                actual.push(json!({"op_id":operation.op_id,"status":"rejected","code":reason.code,"root_op_id":reason.root.op_id,"root_code":reason.root.code}));
            }
        }
        let expected: Vec<_> = expected
            .iter()
            .map(|value| {
                let mut value = value.clone();
                if value["status"] == "saved" {
                    value["status"] = json!("eligible_for_host");
                }
                value
            })
            .collect();
        assert_eq!(actual, expected, "{}", case["id"]);
    }
}
