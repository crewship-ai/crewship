use mooring_core::adapter::*;
use mooring_protocol::{AtomicChange, Counter, ErrorCode, Operation};

fn original() -> AtomicChange {
    AtomicChange {
        change_id: "change".into(),
        task_id: None,
        membership_hash: "sha256:placeholder".into(),
        operations: vec![Operation {
            op_id: "actual".into(),
            participant_ref: "host-bound".into(),
            base_revision: Counter(0),
            dependencies: vec![],
            read_preconditions: vec![],
            payload: Default::default(),
            fingerprint: "sha256:placeholder".into(),
        }],
    }
}
fn failure(id: &str, code: ErrorCode, message: &str) -> PreparedFailure {
    PreparedFailure {
        operation_id: id.into(),
        code,
        message: message.into(),
    }
}
#[test]
fn malformed_or_uncertain_adapter_failure_cannot_become_terminal() {
    for id in ["", "foreign", "actual\0"] {
        assert_eq!(
            failure(id, ErrorCode::TargetDeleted, "missing")
                .qualify(&original())
                .unwrap_err(),
            InvalidPreparedFailure::Member
        );
    }
    for code in [ErrorCode::UnknownOutcome, ErrorCode::DependencyRejected] {
        assert_eq!(
            failure("actual", code, "uncertain")
                .qualify(&original())
                .unwrap_err(),
            InvalidPreparedFailure::Nonterminal
        );
    }
    let mut duplicate = original();
    duplicate.operations.push(duplicate.operations[0].clone());
    assert_eq!(
        failure("actual", ErrorCode::TargetDeleted, "missing")
            .qualify(&duplicate)
            .unwrap_err(),
        InvalidPreparedFailure::Member
    );
    assert_eq!(
        failure("actual", ErrorCode::InvalidRequest, &"😀".repeat(1001))
            .qualify(&original())
            .unwrap_err(),
        InvalidPreparedFailure::Message
    );
    let qualified = failure("actual", ErrorCode::TargetDeleted, &"😀".repeat(1000))
        .qualify(&original())
        .unwrap();
    assert_eq!(qualified.operation_id(), "actual");
    let reason = qualified.into_planning_rejection();
    assert_eq!(reason.root.op_id, "actual");
    assert_eq!(reason.root.code, ErrorCode::TargetDeleted);
}
