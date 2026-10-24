use mooring_text::yrs::{
    updates::decoder::Decode, ClientID, Doc, Map, MapPrelim, OffsetKind, Options, ReadTxn,
    StateVector, Text, Transact, Update,
};
use mooring_text::{recover, UnsavedDraft};
fn doc(origin: u64) -> Doc {
    Doc::with_options(Options {
        client_id: ClientID::new(origin),
        offset_kind: OffsetKind::Utf16,
        skip_gc: false,
        ..Options::default()
    })
}
fn full(doc: &Doc) -> Vec<u8> {
    doc.transact()
        .encode_state_as_update_v1(&StateVector::default())
}
fn drafts() -> Vec<UnsavedDraft> {
    vec![
        UnsavedDraft {
            op_id: "own_A".into(),
            text: "původní 😀".into(),
            reason: "pending".into(),
        },
        UnsavedDraft {
            op_id: "independent_B".into(),
            text: "nedotčený".into(),
            reason: "uncertain".into(),
        },
    ]
}
#[test]
fn independent_hidden_map_allocation_after_missing_same_origin_prefix_is_not_full_recovery() {
    let base = doc(31);
    base.get_or_insert_map("metadata")
        .insert(&mut base.transact_mut(), "base", "retained");
    let initial = full(&base);
    let author = doc(17);
    author
        .transact_mut()
        .apply_update(Update::decode_v1(&initial).unwrap())
        .unwrap();
    author
        .get_or_insert_text("pending")
        .insert(&mut author.transact_mut(), 0, "A😀");
    let after_a = author.transact().state_vector();
    author
        .get_or_insert_map("metadata")
        .insert(&mut author.transact_mut(), "hidden_flag", true);
    let b = author.transact().encode_state_as_update_v1(&after_a);
    let incomplete =
        mooring_text::yrs::merge_updates_v1([initial.as_slice(), b.as_slice()]).unwrap();
    let original = drafts();
    match recover(&incomplete, original.clone()) {
        Err(error) => assert_eq!(error.unsaved, original),
        Ok(_) => panic!("unmaterialized hidden-map history silently reported full recovery"),
    }
}
#[test]
fn independent_complete_nested_map_gc_preserves_allocated_clock_and_original_drafts() {
    let source = doc(17);
    let root = source.get_or_insert_map("root");
    let nested = root.insert(&mut source.transact_mut(), "gone", MapPrelim::default());
    nested.insert(&mut source.transact_mut(), "payload", "😀 original content");
    root.remove(&mut source.transact_mut(), "gone");
    let expected = source.transact().state_vector();
    assert!(expected.get(&ClientID::new(17)) > 0);
    let encoded = full(&source);
    let recovered = recover(&encoded, drafts()).unwrap();
    assert_eq!(recovered.document.transact().state_vector(), expected);
    assert_eq!(recovered.unsaved, drafts());
    let cold = recover(&full(&recovered.document), drafts()).unwrap();
    assert_eq!(cold.document.transact().state_vector(), expected);
    assert_eq!(cold.unsaved, drafts());
}
#[test]
fn independent_delete_of_unknown_future_history_preserves_every_unsaved_draft() {
    let source = doc(31);
    source
        .get_or_insert_text("known")
        .insert(&mut source.transact_mut(), 0, "known");
    let initial = full(&source);
    let orphan = [0, 1, 17, 1, 0, 1];
    let incomplete =
        mooring_text::yrs::merge_updates_v1([initial.as_slice(), orphan.as_slice()]).unwrap();
    let original = drafts();
    match recover(&incomplete, original.clone()) {
        Err(error) => assert_eq!(error.unsaved, original),
        Ok(_) => panic!("unknown future delete was accepted as materialized full history"),
    }
}
