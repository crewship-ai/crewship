use mooring_wordcraft::experimental_binding::Binding;
use serde_json::{json, Value};
fn native(block: Value) -> Value {
    let mut v: Value =
        serde_json::from_str(include_str!("fixtures/d1-engine-canonical.json")).unwrap();
    v["body"] = json!([block]);
    v
}
#[test]
fn review_zero_length_run_data_conservation() {
    let v = native(json!({"kind":"para","text":"","runs":[{"len":0,"props":{"bold":true}}]}));
    let original = mooring_wordcraft::Document::import(v.clone(), vec!["p_one".into()]).unwrap();
    assert_eq!(original.native().unwrap(), v);
    let b = Binding::import(v.clone(), vec!["p_one".into()], 17).unwrap();
    assert_eq!(b.native().unwrap()["body"], v["body"]);
}
#[test]
fn review_null_property_data_conservation() {
    let v = native(json!({"kind":"para","text":"a","runs":[{"len":1,"props":{"color":null}}]}));
    let original = mooring_wordcraft::Document::import(v.clone(), vec!["p_one".into()]).unwrap();
    assert_eq!(original.native().unwrap(), v);
    let b = Binding::import(v.clone(), vec!["p_one".into()], 17).unwrap();
    assert_eq!(b.native().unwrap()["body"], v["body"]);
}
#[test]
fn review_adjacent_run_data_conservation() {
    let v = native(json!({"kind":"para","text":"ab","runs":[{"len":1},{"len":1}]}));
    let original = mooring_wordcraft::Document::import(v.clone(), vec!["p_one".into()]).unwrap();
    assert_eq!(original.native().unwrap(), v);
    let b = Binding::import(v.clone(), vec!["p_one".into()], 17).unwrap();
    assert_eq!(b.native().unwrap()["body"], v["body"]);
}

#[test]
fn noncanonical_paragraphs_remain_opaque_through_restore_and_refuse_changes() {
    use mooring_wordcraft::experimental_binding::Error;
    for block in [
        json!({"kind":"para","text":"","runs":[{"len":0,"props":{"bold":true}}]}),
        json!({"kind":"para","text":"a","runs":[{"len":1,"props":{"color":null}}]}),
        json!({"kind":"para","text":"a","runs":[{"len":1}],"props":{"align":null}}),
        json!({"kind":"para","text":"ab","runs":[{"len":1},{"len":1}]}),
        json!({"kind":"para","text":"a","runs":[{"len":1,"props":{"futureFlag":true}}]}),
    ] {
        let initial = native(block);
        let mut binding = Binding::import(initial.clone(), vec!["p_one".into()], 17).unwrap();
        assert_eq!(binding.native().unwrap(), initial);
        let checkpoint = binding.checkpoint().unwrap();
        let restored = Binding::restore(&checkpoint, 18).unwrap();
        assert_eq!(restored.native().unwrap(), initial);
        assert_eq!(restored.stable_ids().unwrap(), vec!["p_one".to_string()]);
        let mut edited = initial.clone();
        edited["body"][0]["text"] = json!("edited");
        edited["body"][0]["runs"] = json!([{"len":6}]);
        assert!(matches!(
            binding.reconcile(&edited, vec!["p_one".into()]),
            Err(Error::Unsupported)
        ));
        assert_eq!(binding.checkpoint().unwrap(), checkpoint);
        // No projection namespaces may be synthesized for an opaque paragraph.
        let cp: Value = serde_json::from_slice(&checkpoint).unwrap();
        let update: Vec<u8> = serde_json::from_value(cp["update"].clone()).unwrap();
        use mooring_text::yrs::{updates::decoder::Decode, Doc, Map, ReadTxn, Transact, Update};
        let doc = Doc::new();
        doc.transact_mut()
            .apply_update(Update::decode_v1(&update).unwrap())
            .unwrap();
        let txn = doc.transact();
        for name in ["wordcraft.texts", "wordcraft.binding.paragraphs"] {
            assert!(txn
                .get_map(name)
                .is_none_or(|map| map.get(&txn, "p_one").is_none()));
        }
    }
}
