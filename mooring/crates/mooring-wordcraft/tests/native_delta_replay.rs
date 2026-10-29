//! Synthetic trusted-host fixtures exercise actual app/Yrs replay, not admission.
use mooring_text::yrs::{updates::decoder::Decode, Update};
use mooring_wordcraft::{
    command::Command,
    document_update,
    experimental_draft::{DraftAccess, DraftReplica, Proposal},
    experimental_human_basis::{
        self as basis, HostConfirmedBasis, HostSavedDelta, HostSealedOriginal,
    },
    experimental_human_current::{self as current, HostWriter},
    AdapterError, Document,
};
use serde_json::{json, Value};
fn initial() -> Vec<u8> {
    let mut native: Value =
        serde_json::from_str(include_str!("fixtures/d1-engine-canonical.json")).unwrap();
    native["body"] = json!([{"kind":"para","text":"alpha","runs":[{"len":5}]},{"kind":"para","text":"beta","runs":[{"len":4}]}]);
    Document::import(native, vec!["p0".into(), "p1".into()])
        .unwrap()
        .checkpoint()
        .unwrap()
}
fn insert(text: &str) -> Command {
    serde_json::from_value(json!({"kind":"text.insert","target_id":"p0","at":{"kind":"end"},"text":text,"properties":null})).unwrap()
}
fn prepare(checkpoint: &[u8], proposal: &Proposal) -> Vec<u8> {
    current::prepare_current(
        current::Authority::Writer(HostWriter::current(checkpoint, proposal.origin).unwrap()),
        &proposal.authored_frontier,
        &proposal.update,
        &proposal.targets,
    )
    .unwrap()
    .checkpoint()
    .unwrap()
}
struct Case {
    initial: Vec<u8>,
    current: Vec<u8>,
    candidate: Document,
    a: Proposal,
    c: Proposal,
}
fn concurrent() -> Case {
    let initial = initial();
    let mut own = DraftReplica::restore(&initial, 17, DraftAccess::Writer).unwrap();
    let a = own.edit("original-A", insert(" A😀")).unwrap();
    let mut peer = DraftReplica::restore(&initial, 18, DraftAccess::Writer).unwrap();
    let c=peer.edit("saved-peer-C",serde_json::from_value(json!({"kind":"text.delete","target_id":"p1","start":{"kind":"start"},"end":{"kind":"end"}})).unwrap()).unwrap();
    assert!(document_update::allocation_origins(&c.update)
        .unwrap()
        .is_empty());
    let current = prepare(&initial, &c);
    let history = [HostSavedDelta::from_trusted_committed_store(
        b"synthetic-saved-C",
        c.origin,
        &c.update,
        &c.targets,
    )
    .unwrap()];
    let sealed =
        HostSealedOriginal::from_trusted_original_store(&a, b"synthetic-original-A", 1).unwrap();
    let host = HostConfirmedBasis::from_trusted_confirmed_store(
        &initial, &current, 17, sealed, 1, &history,
    )
    .unwrap();
    let checkpoint = basis::prepare_confirmed_basis(basis::Authority::Writer(host))
        .unwrap()
        .checkpoint()
        .unwrap();
    Case {
        initial,
        current,
        candidate: Document::restore(&checkpoint).unwrap(),
        a,
        c,
    }
}
#[test]
fn original_human_delta_replays_exact_candidate_after_unobserved_delete_only_peer() {
    let case = concurrent();
    let original = case.a.update.clone();
    let previous = case.current.clone();
    let before = case.candidate.checkpoint().unwrap();
    let delivery = case.candidate.update_since(&case.current).unwrap();
    assert_ne!(
        delivery, original,
        "cumulative delivery DeleteSet is not the immutable original packet"
    );
    let original_ds = Update::decode_v1(&original).unwrap().delete_set().clone();
    let mut union = original_ds;
    union.merge_with(
        Update::decode_v1(&case.c.update)
            .unwrap()
            .delete_set()
            .clone(),
    );
    assert_eq!(Update::decode_v1(&delivery).unwrap().delete_set(), &union);
    case.candidate
        .verify_update_since(&case.current, &original)
        .unwrap();
    case.candidate
        .verify_update_since(&case.current, &delivery)
        .unwrap();
    assert_eq!(case.a.update, original);
    assert_eq!(case.current, previous);
    assert_eq!(case.candidate.checkpoint().unwrap(), before);
    let mut reader = DraftReplica::restore(&case.current, 47, DraftAccess::Reader).unwrap();
    reader
        .apply_verified_document_update(
            &document_update::encode(&original, &case.a.targets).unwrap(),
        )
        .unwrap();
    assert_eq!(reader.native().unwrap(), case.candidate.native().unwrap());
    assert_eq!(reader.native().unwrap()["body"][0]["text"], "alpha A😀");
    assert_eq!(reader.native().unwrap()["body"][1]["text"], "");
}
#[test]
fn derived_delta_without_peer_remains_exact_original() {
    let initial = initial();
    let mut own = DraftReplica::restore(&initial, 17, DraftAccess::Writer).unwrap();
    let a = own.edit("A", insert(" A")).unwrap();
    let candidate = Document::restore(&prepare(&initial, &a)).unwrap();
    let derived = candidate.update_since(&initial).unwrap();
    assert_eq!(derived, a.update);
    candidate.verify_update_since(&initial, &derived).unwrap();
}
#[test]
fn missing_original_and_wrong_current_delete_set_cannot_prove_candidate() {
    let case = concurrent();
    let unchanged = case.candidate.checkpoint().unwrap();
    assert!(case
        .candidate
        .verify_update_since(&case.current, &[])
        .is_err());
    assert!(case
        .candidate
        .verify_update_since(&case.current, &[0, 0])
        .is_err());
    // Delete-only C changes no state-vector clock. Exact checkpoint comparison
    // must still reject this earlier current state without C's deletion.
    assert!(case
        .candidate
        .verify_update_since(&case.initial, &case.a.update)
        .is_err());
    let wrong = Document::restore(&case.current).unwrap();
    assert!(wrong
        .verify_update_since(&case.current, &case.a.update)
        .is_err());
    assert_eq!(case.candidate.checkpoint().unwrap(), unchanged);
}
#[test]
fn actual_orphan_original_is_denied_without_mutating_candidate() {
    let case = concurrent();
    let mut orphan = DraftReplica::restore(&case.initial, 29, DraftAccess::Writer).unwrap();
    orphan.edit("missing-predecessor", insert(" H")).unwrap();
    let b = orphan.edit("orphan-B", insert(" B")).unwrap();
    let original = b.update.clone();
    let unchanged = case.candidate.checkpoint().unwrap();
    assert!(case
        .candidate
        .verify_update_since(&case.current, &b.update)
        .is_err());
    assert_eq!(b.update, original);
    assert_eq!(case.candidate.checkpoint().unwrap(), unchanged);
}
#[test]
fn malformed_and_oversize_update_are_denied_before_upstream_decode() {
    let case = concurrent();
    assert!(case
        .candidate
        .verify_update_since(&case.current, &[255])
        .is_err());
    assert!(matches!(
        case.candidate
            .verify_update_since(&case.current, &vec![0; 262145]),
        Err(AdapterError::Limit)
    ));
    assert!(case
        .candidate
        .verify_update_since(&[255], &case.a.update)
        .is_err());
}
