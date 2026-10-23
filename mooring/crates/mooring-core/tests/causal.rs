use mooring_core::{
    causal::{CausalContext, CausalPrepareContext, EntryVersion, QualifiedRead},
    prepare::{ConfirmedContext, ContextError},
};
use mooring_protocol::{
    canonical, AtomicChange, Counter, ErrorCode, Operation, PrepareContext,
    PrepareContextReadPolicy, PreparedOperationRevision, ReadPrecondition, TargetEffect,
    TargetIdentity, TargetVersion,
};
fn counter(value: &str) -> Counter {
    Counter(value.parse().unwrap())
}
fn outcome(error: ContextError) -> (&'static str, Option<String>) {
    match error {
        ContextError::Invalid => ("nonterminal", None),
        ContextError::Read(failure) => (
            match failure.code {
                ErrorCode::InvalidRequest => "invalid_request",
                ErrorCode::TargetDeleted => "target_deleted",
                ErrorCode::PreconditionFailed => "precondition_failed",
                _ => panic!("unexpected reason"),
            },
            Some(failure.operation_id),
        ),
    }
}

#[test]
fn shared_compact_bindings_and_original_working_reads() {
    let fixtures = canonical::normalize(include_bytes!(
        "../../../protocol/causal-read-fixtures.json"
    ))
    .unwrap();
    let cases = fixtures.value()["cases"].as_array().unwrap();
    assert_eq!(cases.len(), 29);
    let mut consumed = 0;
    let mut pure_consumed = 0;
    for case in cases {
        let id = case["id"].as_str().unwrap();
        let expected = case["expected"]["rust"].as_str().unwrap();
        if expected == "not_called" {
            continue;
        }
        consumed += 1;
        let targets: Vec<_> = case["targets"]
            .as_array()
            .unwrap()
            .iter()
            .map(|v| TargetVersion {
                target_id: v["target_id"].as_str().unwrap().into(),
                revision: counter(v["revision"].as_str().unwrap()),
                structure_revision: counter(v["structure_revision"].as_str().unwrap()),
                deleted: v["deleted"].as_bool().unwrap(),
            })
            .collect();
        let inventory: Vec<_> = targets
            .iter()
            .map(|t| TargetIdentity {
                target_id: t.target_id.clone(),
                deleted: t.deleted,
            })
            .collect();
        let change = AtomicChange {
            change_id: "pure-change".into(),
            membership_hash: "not-fingerprinted-in-pure-test".into(),
            task_id: None,
            operations: case["operations"]
                .as_array()
                .unwrap()
                .iter()
                .map(|v| Operation {
                    op_id: v["op_id"].as_str().unwrap().into(),
                    fingerprint: "not-fingerprinted-in-pure-test".into(),
                    participant_ref: "host-bound-agent".into(),
                    base_revision: counter(v["base_revision"].as_str().unwrap()),
                    dependencies: v["dependencies"]
                        .as_array()
                        .unwrap()
                        .iter()
                        .map(|v| v.as_str().unwrap().into())
                        .collect(),
                    payload: Default::default(),
                    read_preconditions: v["read_preconditions"]
                        .as_array()
                        .unwrap()
                        .iter()
                        .map(|r| ReadPrecondition {
                            target_id: r["target_id"].as_str().unwrap().into(),
                            revision: counter(r["revision"].as_str().unwrap()),
                            structure_revision: r
                                .get("structure_revision")
                                .map(|v| counter(v.as_str().unwrap())),
                        })
                        .collect(),
                })
                .collect(),
        };
        let labels: Vec<_> = case["operation_revisions"]
            .as_array()
            .unwrap()
            .iter()
            .map(|v| PreparedOperationRevision {
                op_id: v["op_id"].as_str().unwrap().into(),
                revision: counter(v["revision"].as_str().unwrap()),
            })
            .collect();
        let mut supplied = case["qualified_reads"].as_array().unwrap().clone();
        if let Some(tamper) = case.get("tamper") {
            if let Some(index) = tamper.get("qualification_index") {
                let index: usize = index.as_str().unwrap().parse().unwrap();
                supplied[index][tamper["field"].as_str().unwrap()] = tamper["value"].clone();
            }
            if let Some(index) = tamper.get("append_qualification_index") {
                let index: usize = index.as_str().unwrap().parse().unwrap();
                supplied.push(supplied[index].clone());
            }
            if let Some(index) = tamper.get("remove_qualification_index") {
                let index: usize = index.as_str().unwrap().parse().unwrap();
                supplied.remove(index);
            }
        }
        // Optional-null is a future wire shape negative, not representable by these
        // non-deserializable pure types. Do not claim their constructor proves JSON.
        if supplied.iter().any(|v| {
            v.get("observed_structure_revision")
                .is_some_and(|v| v.is_null())
        }) {
            assert_eq!(expected, "nonterminal", "{id}");
            continue;
        }
        pure_consumed += 1;
        let qualifications: Vec<_> = supplied
            .iter()
            .map(|v| QualifiedRead {
                op_id: v["op_id"].as_str().unwrap().into(),
                read_index: v["read_index"].as_str().unwrap().parse().unwrap(),
                target_id: v["target_id"].as_str().unwrap().into(),
                observed_revision: counter(v["observed_revision"].as_str().unwrap()),
                observed_structure_revision: v
                    .get("observed_structure_revision")
                    .map(|v| counter(v.as_str().unwrap())),
                entry: if v["entry"].is_null() {
                    None
                } else {
                    let e = &v["entry"];
                    Some(EntryVersion {
                        revision: counter(e["revision"].as_str().unwrap()),
                        structure_revision: counter(e["structure_revision"].as_str().unwrap()),
                        deleted: e["deleted"].as_bool().unwrap(),
                    })
                },
                covered: v["covered"].as_bool().unwrap(),
            })
            .collect();
        let context = CausalPrepareContext {
            document_revision: counter(case["document_revision"].as_str().unwrap()),
            targets: targets.clone(),
            operation_revisions: labels.clone(),
            qualified_reads: qualifications,
        };
        let checked = CausalContext::qualify(&context, &change, &inventory);
        let actual = match checked {
            Err(error) => outcome(error),
            Ok(checked) => {
                if id == "same-atomic-prefix-covered" {
                    let mut probe = checked.working();
                    probe.check_current_reads("a").unwrap();
                    let before = probe.versions();
                    let valid = TargetEffect {
                        target_id: "p".into(),
                        content_changed: true,
                        structure_changed: false,
                        created: false,
                        deleted: false,
                    };
                    let invalid = TargetEffect {
                        target_id: "not-existing".into(),
                        ..valid.clone()
                    };
                    assert!(matches!(
                        probe.advance_effects("a", &[valid.clone(), invalid]),
                        Err(ContextError::Invalid)
                    ));
                    assert_eq!(probe.versions(), before, "partial target mutation");
                    probe.check_current_reads("a").unwrap();
                    probe.advance_effects("a", &[valid]).unwrap();
                    probe.check_current_reads("b").unwrap();
                    probe.advance_effects("b", &[]).unwrap();
                    assert!(probe.complete());
                }
                let mut working = checked.working();
                let mut failure = None;
                for operation in &change.operations {
                    if let Err(error) = working.check_current_reads(&operation.op_id) {
                        failure = Some(outcome(error));
                        break;
                    }
                    let step = case["steps"]
                        .as_array()
                        .unwrap()
                        .iter()
                        .find(|v| v["op_id"].as_str().unwrap() == operation.op_id);
                    let effects: Vec<_> = step
                        .map(|step| {
                            step["effects"]
                                .as_array()
                                .unwrap()
                                .iter()
                                .map(|e| TargetEffect {
                                    target_id: e["target_id"].as_str().unwrap().into(),
                                    content_changed: e["content_changed"].as_bool().unwrap(),
                                    structure_changed: e["structure_changed"].as_bool().unwrap(),
                                    created: e["created"].as_bool().unwrap(),
                                    deleted: e["deleted"].as_bool().unwrap(),
                                })
                                .collect()
                        })
                        .unwrap_or_default();
                    if let Err(error) = working.advance_effects(&operation.op_id, &effects) {
                        failure = Some(outcome(error));
                        break;
                    }
                }
                if failure.is_none() {
                    assert!(working.complete(), "{id}");
                }
                failure.unwrap_or(("eligible", None))
            }
        };
        assert_eq!(actual.0, expected, "{id}");
        if let Some(root) = case["expected"].get("op_id") {
            assert_eq!(actual.1.as_deref(), root.as_str(), "{id}");
        }
        if id == "saved-a-stale-b-covered" {
            let strict = PrepareContext {
                read_policy: PrepareContextReadPolicy::AgentConfirmedCurrent,
                document_revision: context.document_revision,
                targets,
                operation_revisions: labels,
            };
            assert!(
                matches!(
                    ConfirmedContext::qualify(&strict, &change, &inventory),
                    Err(ContextError::Read(_))
                ),
                "strict policy silently relaxed"
            );
        }
    }
    assert_eq!(consumed, 24);
    assert_eq!(pure_consumed, 23);
}
