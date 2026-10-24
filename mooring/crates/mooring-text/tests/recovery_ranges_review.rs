use mooring_text::yrs::{
    updates::decoder::Decode, ClientID, Doc, Options, ReadTxn, StateVector, Text, Transact, Update,
};
use mooring_text::{recover, UnsavedDraft};

#[test]
fn full_snapshot_with_unmaterialized_encoded_origin_retains_all_drafts_as_error() {
    let baseline = Doc::with_options(Options {
        client_id: ClientID::new(31),
        ..Options::default()
    });
    baseline
        .get_or_insert_text("a")
        .insert(&mut baseline.transact_mut(), 0, "baseline a");
    baseline
        .get_or_insert_text("b")
        .insert(&mut baseline.transact_mut(), 0, "baseline b");
    let base = baseline
        .transact()
        .encode_state_as_update_v1(&StateVector::default());
    let author = Doc::with_options(Options {
        client_id: ClientID::new(17),
        ..Options::default()
    });
    author
        .transact_mut()
        .apply_update(Update::decode_v1(&base).unwrap())
        .unwrap();
    author
        .get_or_insert_text("a")
        .insert(&mut author.transact_mut(), 0, "unconfirmed A");
    let after_a = author.transact().state_vector();
    author
        .get_or_insert_text("b")
        .insert(&mut author.transact_mut(), 0, "independent B");
    let b = author.transact().encode_state_as_update_v1(&after_a);
    // This is not a complete authoritative snapshot: its B allocation follows
    // the absent same-origin A range. Native presence alone cannot qualify it.
    let incomplete = mooring_text::yrs::merge_updates_v1([base.as_slice(), b.as_slice()]).unwrap();
    let drafts = vec![
        UnsavedDraft {
            op_id: "a".into(),
            text: "A original intent".into(),
            reason: "pending".into(),
        },
        UnsavedDraft {
            op_id: "b".into(),
            text: "B independent intent".into(),
            reason: "pending".into(),
        },
        UnsavedDraft {
            op_id: "other".into(),
            text: "unrelated draft".into(),
            reason: "pending".into(),
        },
    ];
    match recover(&incomplete, drafts.clone()) {
        Err(error) => assert_eq!(error.unsaved, drafts),
        Ok(recovery) => {
            assert_eq!(
                recovery
                    .document
                    .transact()
                    .state_vector()
                    .get(&ClientID::new(17)),
                0
            );
            panic!("incomplete full snapshot reported successful recovery after ignoring encoded allocation");
        }
    }
}
