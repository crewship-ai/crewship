use mooring_text::{
    new_document_with_origin, recover_with_origin, CheckedClientOrigin, UnsavedDraft,
    MAX_CLIENT_ORIGIN,
};
use yrs::{
    ClientID, Doc, GetString, Map, OffsetKind, Options, ReadTxn, StateVector, Text, Transact,
};

fn snapshot(origin: u64, deleted: bool) -> Vec<u8> {
    let document = Doc::with_options(Options {
        client_id: ClientID::new(origin),
        offset_kind: OffsetKind::Utf16,
        ..Options::default()
    });
    let text = document.get_or_insert_text("paragraph");
    let mut tx = document.transact_mut();
    text.push(&mut tx, "a😀b");
    if deleted {
        text.remove_range(&mut tx, 0, 4);
    }
    tx.encode_state_as_update_v1(&StateVector::default())
}

fn drafts() -> Vec<UnsavedDraft> {
    vec![UnsavedDraft {
        op_id: "original-pending-operation".into(),
        text: "keep this original draft".into(),
        reason: "unknown host outcome".into(),
    }]
}

#[test]
fn explicit_origin_rejects_zero_and_values_that_yrs_would_alias() {
    for invalid in [0, MAX_CLIENT_ORIGIN + 1, (1_u64 << 53) + 1, u64::MAX] {
        assert!(CheckedClientOrigin::new(invalid).is_err());
    }
    for valid in [1, MAX_CLIENT_ORIGIN] {
        let origin = CheckedClientOrigin::new(valid).unwrap();
        assert_eq!(origin.get(), valid);
        let doc = new_document_with_origin(origin);
        assert_eq!(doc.client_id().get(), valid);
        let text = doc.get_or_insert_text("paragraph");
        let mut tx = doc.transact_mut();
        text.push(&mut tx, "a😀b");
        assert_eq!(text.len(&tx), 4);
        text.remove_range(&mut tx, 1, 2);
        assert_eq!(text.get_string(&tx), "ab");
        assert!(tx.state_vector().get(&ClientID::new(valid)) > 0);
    }
}

#[test]
fn fresh_origin_recovery_preserves_original_ids_utf16_and_drafts() {
    let input = snapshot(17, false);
    let recovered = recover_with_origin(&input, drafts(), CheckedClientOrigin::new(31).unwrap())
        .unwrap_or_else(|e| panic!("{}", e.reason));
    assert_eq!(recovered.unsaved, drafts());
    assert_eq!(recovered.document.client_id().get(), 31);
    let text = recovered.document.get_or_insert_text("paragraph");
    let mut tx = recovered.document.transact_mut();
    assert_eq!(text.get_string(&tx), "a😀b");
    let original_clock = tx.state_vector().get(&ClientID::new(17));
    assert!(original_clock > 0);
    assert_eq!(tx.state_vector().get(&ClientID::new(31)), 0);
    text.insert(&mut tx, 3, "X");
    assert_eq!(text.get_string(&tx), "a😀Xb");
    assert_eq!(tx.state_vector().get(&ClientID::new(17)), original_clock);
    assert!(tx.state_vector().get(&ClientID::new(31)) > 0);
}

#[test]
fn fresh_origin_recovery_denies_allocated_even_fully_deleted_history() {
    for deleted in [false, true] {
        let input = snapshot(17, deleted);
        let error =
            match recover_with_origin(&input, drafts(), CheckedClientOrigin::new(17).unwrap()) {
                Ok(_) => panic!("allocated origin reused, deleted={deleted}"),
                Err(error) => error,
            };
        assert_eq!(error.unsaved, drafts());
    }
}

#[test]
fn fresh_origin_recovery_keeps_drafts_on_invalid_or_partial_snapshot() {
    let author = Doc::with_client_id(17);
    author
        .get_or_insert_text("a")
        .push(&mut author.transact_mut(), "unconfirmed A");
    let after_a = author.transact().state_vector();
    author
        .get_or_insert_text("b")
        .push(&mut author.transact_mut(), "independent B");
    let only_b = author.transact().encode_state_as_update_v1(&after_a);
    for input in [vec![255], vec![0, 0, 1], only_b] {
        let error =
            match recover_with_origin(&input, drafts(), CheckedClientOrigin::new(31).unwrap()) {
                Ok(_) => panic!("malformed or trailing snapshot accepted"),
                Err(error) => error,
            };
        assert_eq!(error.unsaved, drafts());
    }
}

#[test]
fn fresh_origin_recovery_denies_gc_of_deleted_nested_shared_type() {
    let author = Doc::with_client_id(17);
    let parent = author.get_or_insert_map("container");
    parent.insert(
        &mut author.transact_mut(),
        "nested",
        yrs::TextPrelim::new("a😀b"),
    );
    parent.remove(&mut author.transact_mut(), "nested");
    let full = author
        .transact()
        .encode_state_as_update_v1(&StateVector::default());
    let error = match recover_with_origin(&full, drafts(), CheckedClientOrigin::new(17).unwrap()) {
        Ok(_) => panic!("garbage-collected nested allocation reused its origin"),
        Err(error) => error,
    };
    assert_eq!(error.unsaved, drafts());
    let recovered = recover_with_origin(&full, drafts(), CheckedClientOrigin::new(31).unwrap())
        .unwrap_or_else(|e| panic!("{}", e.reason));
    assert_eq!(recovered.unsaved, drafts());
    let tx = recovered.document.transact();
    assert!(tx.state_vector().get(&ClientID::new(17)) > 0);
    assert_eq!(tx.get_map("container").unwrap().len(&tx), 0);
}
