//! Actual CRDT ownership/allocation tests; host source/settlement are synthetic.
//! No receipt controller, source issuance, status or permission is qualified here.
use super::*;
use mooring_protocol::{
    Counter, Operation, PrepareContextReadPolicy, PreparedOperationRevision, ReadPrecondition,
    TargetVersion,
};
use mooring_text::{
    yrs::{ClientID, ReadTxn, Transact},
    CheckedClientOrigin,
};
use serde_json::{json, Value};
fn origin(n: u64) -> CheckedClientOrigin {
    CheckedClientOrigin::new(n).unwrap()
}
fn source() -> (Vec<u8>, Document) {
    let native: Value =
        serde_json::from_str(include_str!("../tests/fixtures/d1-engine-canonical.json")).unwrap();
    let ids = (0..native["body"].as_array().unwrap().len())
        .map(|i| format!("stable-{i}"))
        .collect();
    let baseline = Document::import_with_origin(native, ids, origin(17))
        .unwrap()
        .checkpoint()
        .unwrap();
    let document = Document::restore_with_fresh_origin(&baseline, origin(31)).unwrap();
    (baseline, document)
}
fn change(id: &str, rev: u64) -> AtomicChange {
    AtomicChange {change_id:format!("atomic-{id}"),membership_hash:"b".repeat(64),task_id:None,operations:vec![Operation {op_id:id.into(),fingerprint:"a".repeat(64),participant_ref:"synthetic-source".into(),base_revision:Counter(rev),dependencies:vec![],read_preconditions:vec![ReadPrecondition {target_id:"stable-0".into(),revision:Counter(rev),structure_revision:Some(Counter(0))}],payload:json!({"kind":"text.insert","target_id":"stable-0","at":{"kind":"end"},"text":id,"properties":{"bold":false,"italic":true}}).as_object().unwrap().clone()}]}
}
fn context(document: &Document, change: &AtomicChange, rev: u64) -> PrepareContext {
    PrepareContext {
        read_policy: PrepareContextReadPolicy::AgentConfirmedCurrent,
        document_revision: Counter(rev),
        targets: inventory(document)
            .unwrap()
            .into_iter()
            .map(|id| TargetVersion {
                target_id: id.target_id,
                revision: Counter(rev),
                structure_revision: Counter(0),
                deleted: id.deleted,
            })
            .collect(),
        operation_revisions: vec![PreparedOperationRevision {
            op_id: change.operations[0].op_id.clone(),
            revision: Counter(rev + 1),
        }],
    }
}
fn prepare(document: Document, change: &AtomicChange, rev: u64) -> Candidate {
    let context = context(&document, change, rev);
    let Prepared::Candidate(candidate) = WordCraftAdapter
        .prepare_owned_atomic(document, change, &context)
        .unwrap()
    else {
        panic!("unexpected rejection")
    };
    candidate
}
fn clock(doc: &Document, id: u64) -> u32 {
    doc.doc.transact().state_vector().get(&ClientID::new(id))
}
#[test]
fn consuming_candidate_preserves_the_actual_selected_origin_and_complete_model() {
    let (_baseline, document) = source();
    let before = document.native().unwrap();
    let ids = document.block_ids().unwrap();
    let request = change("A", 0);
    let original = serde_json::to_vec(&request).unwrap();
    let candidate = prepare(document, &request, 0);
    let checkpoint = candidate.checkpoint_bytes().to_vec();
    let document = candidate.into_document();
    assert_eq!(document.doc.client_id().get(), 31);
    assert_eq!(document.checkpoint().unwrap(), checkpoint);
    assert_eq!(document.block_ids().unwrap(), ids);
    let mut expected = before;
    expected["body"][0]["text"] =
        json!(expected["body"][0]["text"].as_str().unwrap().to_owned() + "A");
    expected["body"][0]["runs"]
        .as_array_mut()
        .unwrap()
        .push(json!({"len":1,"props":{"bold":false,"italic":true}}));
    assert_eq!(document.native().unwrap(), expected);
    assert_eq!(serde_json::to_vec(&request).unwrap(), original);
    assert!(clock(&document, 31) > 0);
}
#[test]
fn synthetic_accepted_a_unknown_b_retained_then_rejected_b_requires_fresh_recovery() {
    let (_baseline, document) = source();
    let a = change("A", 0);
    let candidate_a = prepare(document, &a, 0);
    // Synthetic settlement fixture only: actual native owner controller must
    // verify the complete receipt, identity and bytes BEFORE consuming this.
    let accepted_a = candidate_a.checkpoint_bytes().to_vec();
    let document = candidate_a.into_document();
    let a_clock = clock(&document, 31);
    assert!(a_clock > 0);
    let a_native = document.native().unwrap();
    let ids = document.block_ids().unwrap();
    let b = change("B", 1);
    let original_b = serde_json::to_vec(&b).unwrap();
    let candidate_b = prepare(document, &b, 1);
    let original_candidate = candidate_b.checkpoint_bytes().to_vec();
    let b_document = Document::restore(&original_candidate).unwrap();
    let b_clock = clock(&b_document, 31);
    assert!(b_clock > a_clock);
    // Unknown keeps the exact original and actual owned candidate: no retry
    // preparation, new allocation, receipt or implicit promotion occurs.
    let pending = (candidate_b, b);
    assert_eq!(pending.0.checkpoint_bytes(), original_candidate);
    assert_eq!(serde_json::to_vec(&pending.1).unwrap(), original_b);
    // Explicit terminal-failed policy in the synthetic fixture retires B's
    // source Doc. It does not roll it back and resume the same client clock.
    drop(pending.0);
    let retained_b = pending.1;
    assert!(Document::restore_with_fresh_origin(&accepted_a, origin(31)).is_err());
    let recovered = Document::restore_with_fresh_origin(&accepted_a, origin(47)).unwrap();
    assert_eq!(clock(&recovered, 31), a_clock);
    assert_eq!(clock(&recovered, 47), 0);
    assert_eq!(recovered.native().unwrap(), a_native);
    assert_eq!(recovered.block_ids().unwrap(), ids);
    assert_eq!(serde_json::to_vec(&retained_b).unwrap(), original_b);
    let c = change("C", 1);
    let candidate_c = prepare(recovered, &c, 1);
    let document = candidate_c.into_document();
    assert_eq!(document.doc.client_id().get(), 47);
    assert!(clock(&document, 47) > 0);
    assert_eq!(clock(&document, 31), a_clock);
    assert!(clock(&document, 31) < b_clock);
    assert_eq!(serde_json::to_vec(&retained_b).unwrap(), original_b);
}

#[test]
fn owned_preparation_keeps_complete_context_read_and_readonly_gates() {
    let (_, document) = source();
    let request = change("missing-context", 0);
    let mut ctx = context(&document, &request, 0);
    ctx.targets.pop();
    assert!(matches!(
        WordCraftAdapter.prepare_owned_atomic(document, &request, &ctx),
        Err(PrepareError::Context)
    ));

    let (_, document) = source();
    let request = change("stale-read", 0);
    let ctx = context(&document, &request, 1);
    let Prepared::Rejected(failure) = WordCraftAdapter
        .prepare_owned_atomic(document, &request, &ctx)
        .unwrap()
    else {
        panic!("stale read accepted")
    };
    assert_eq!(failure.operation_id, "stale-read");
    assert_eq!(failure.code, ErrorCode::PreconditionFailed);

    let native: Value =
        serde_json::from_str(include_str!("../tests/fixtures/d1-native.json")).unwrap();
    let ids = (0..native["body"].as_array().unwrap().len())
        .map(|i| format!("stable-{i}"))
        .collect();
    let document = Document::import_with_origin(native, ids, origin(17)).unwrap();
    assert!(document.is_read_only());
    let request = change("readonly", 0);
    let ctx = context(&document, &request, 0);
    let Prepared::Rejected(failure) = WordCraftAdapter
        .prepare_owned_atomic(document, &request, &ctx)
        .unwrap()
    else {
        panic!("readonly write accepted")
    };
    assert_eq!(failure.operation_id, "readonly");
    assert_eq!(failure.code, ErrorCode::Forbidden);
}
