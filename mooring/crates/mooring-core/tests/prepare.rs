use mooring_core::prepare::{ConfirmedContext, ContextError};
use mooring_protocol::{
    canonical, AtomicChange, Counter, ErrorCode, Operation, PrepareContext,
    PrepareContextReadPolicy, PreparedOperationRevision, ReadPrecondition, TargetEffect,
    TargetIdentity, TargetVersion,
};

fn code(value: &str) -> ErrorCode {
    match value {
        "invalid_request" => ErrorCode::InvalidRequest,
        "target_deleted" => ErrorCode::TargetDeleted,
        "precondition_failed" => ErrorCode::PreconditionFailed,
        _ => panic!("unknown expected code"),
    }
}

#[test]
fn shared_confirmed_entry_working_effects_and_actual_member_vectors() {
    let fixture = canonical::normalize(include_bytes!(
        "../../../protocol/prepare-context-fixtures.json"
    ))
    .unwrap();
    let cases = fixture.value()["cases"].as_array().unwrap();
    assert_eq!(cases.len(), 31);
    for case in cases {
        let id = case["id"].as_str().unwrap();
        let inventory: Vec<_> = case["inventory"]
            .as_array()
            .unwrap()
            .iter()
            .map(|value| TargetIdentity {
                target_id: value["target_id"].as_str().unwrap().into(),
                deleted: value["deleted"].as_bool().unwrap(),
            })
            .collect();
        let targets: Vec<_> = case["targets"]
            .as_array()
            .unwrap()
            .iter()
            .map(|value| TargetVersion {
                target_id: value["target_id"].as_str().unwrap().into(),
                deleted: value["deleted"].as_bool().unwrap(),
                revision: Counter(value["revision"].as_str().unwrap().parse().unwrap()),
                structure_revision: Counter(
                    value["structure_revision"]
                        .as_str()
                        .unwrap()
                        .parse()
                        .unwrap(),
                ),
            })
            .collect();
        let change = AtomicChange {
            change_id: "change".into(),
            task_id: None,
            membership_hash: "sha256:not-tested-here".into(),
            operations: case["operations"]
                .as_array()
                .unwrap()
                .iter()
                .map(|value| Operation {
                    op_id: value["op_id"].as_str().unwrap().into(),
                    fingerprint: "sha256:not-tested-here".into(),
                    participant_ref: "host-bound-agent".into(),
                    base_revision: Counter(
                        value["base_revision"].as_str().unwrap().parse().unwrap(),
                    ),
                    dependencies: vec![],
                    payload: Default::default(),
                    read_preconditions: value["read_preconditions"]
                        .as_array()
                        .unwrap()
                        .iter()
                        .map(|read| ReadPrecondition {
                            target_id: read["target_id"].as_str().unwrap().into(),
                            revision: Counter(read["revision"].as_str().unwrap().parse().unwrap()),
                            structure_revision: read.get("structure_revision").map(|revision| {
                                Counter(revision.as_str().unwrap().parse().unwrap())
                            }),
                        })
                        .collect(),
                })
                .collect(),
        };
        let context = PrepareContext {
            read_policy: PrepareContextReadPolicy::AgentConfirmedCurrent,
            document_revision: Counter(
                case["document_revision"].as_str().unwrap().parse().unwrap(),
            ),
            targets,
            operation_revisions: case["operation_revisions"]
                .as_array()
                .unwrap()
                .iter()
                .map(|label| PreparedOperationRevision {
                    op_id: label["op_id"].as_str().unwrap().into(),
                    revision: Counter(label["revision"].as_str().unwrap().parse().unwrap()),
                })
                .collect(),
        };
        let expected = &case["expected"];
        let qualified = ConfirmedContext::qualify(&context, &change, &inventory);
        if expected["context_valid"] == false {
            assert!(matches!(qualified, Err(ContextError::Invalid)), "{id}");
            continue;
        }
        if let Some(failure) = expected.get("entry_failure") {
            match qualified {
                Err(ContextError::Read(actual)) => {
                    assert_eq!(
                        actual.operation_id,
                        failure["op_id"].as_str().unwrap(),
                        "{id}"
                    );
                    assert_eq!(actual.code, code(failure["code"].as_str().unwrap()), "{id}");
                }
                _ => panic!("missing entry failure: {id}"),
            }
            continue;
        }
        let qualified = qualified.unwrap_or_else(|error| panic!("{id}: {error:?}"));
        let mut working = qualified.working();
        if id == "current-confirmed-read" {
            assert!(matches!(
                working.advance_effects("a", &[]),
                Err(ContextError::Invalid)
            ));
            assert!(matches!(
                working.check_current_reads("foreign"),
                Err(ContextError::Invalid)
            ));
        }
        let mut stopped = false;
        for step in case["steps"].as_array().unwrap() {
            let op_id = step["op_id"].as_str().unwrap();
            let operation = change
                .operations
                .iter()
                .find(|operation| operation.op_id == op_id)
                .unwrap_or(&change.operations[0]);
            let checked = working.check_current_reads(&operation.op_id);
            assert!(
                !matches!(checked, Err(ContextError::Invalid)),
                "invalid working order: {id}"
            );
            if let Err(ContextError::Read(actual)) = checked {
                let failure = expected
                    .get("working_failure")
                    .unwrap_or_else(|| panic!("unexpected working failure: {id}"));
                assert_eq!(
                    actual.operation_id,
                    failure["op_id"].as_str().unwrap(),
                    "{id}"
                );
                assert_eq!(actual.code, code(failure["code"].as_str().unwrap()), "{id}");
                stopped = true;
                break;
            }
            let effects: Vec<_> = step["effects"]
                .as_array()
                .unwrap()
                .iter()
                .map(|effect| TargetEffect {
                    target_id: effect["target_id"].as_str().unwrap().into(),
                    content_changed: effect["content_changed"].as_bool().unwrap(),
                    structure_changed: effect["structure_changed"].as_bool().unwrap(),
                    created: effect["created"].as_bool().unwrap(),
                    deleted: effect["deleted"].as_bool().unwrap(),
                })
                .collect();
            let before = working.versions();
            let result = working.advance_effects(op_id, &effects);
            if expected["effects_valid"] == false {
                assert!(matches!(result, Err(ContextError::Invalid)), "{id}");
                assert_eq!(
                    working.versions(),
                    before,
                    "invalid effects partly changed targets: {id}"
                );
                working
                    .advance_effects(&change.operations[0].op_id, &[])
                    .unwrap();
                stopped = true;
                break;
            }
            result.unwrap_or_else(|error| panic!("{id}: {error:?}"));
        }
        if expected.get("working_failure").is_some() || expected["effects_valid"] == false {
            assert!(stopped, "missing expected failure: {id}");
            continue;
        }
        assert!(working.complete(), "{id}");
        let final_targets: Vec<_> = expected["final_targets"]
            .as_array()
            .unwrap()
            .iter()
            .map(|target| TargetVersion {
                target_id: target["target_id"].as_str().unwrap().into(),
                revision: Counter(target["revision"].as_str().unwrap().parse().unwrap()),
                structure_revision: Counter(
                    target["structure_revision"]
                        .as_str()
                        .unwrap()
                        .parse()
                        .unwrap(),
                ),
                deleted: target["deleted"].as_bool().unwrap(),
            })
            .collect();
        assert_eq!(working.versions(), final_targets, "{id}");
    }
}
