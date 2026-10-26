use mooring_wordcraft::experimental_binding::{Binding, Error};
use serde_json::{json, Value};

fn native(texts: &[&str]) -> Value {
    let mut value: Value =
        serde_json::from_str(include_str!("fixtures/d1-engine-canonical.json")).unwrap();
    value["body"] = Value::Array(texts.iter().map(|text| json!({"kind":"para","text":text,"runs":if text.is_empty(){json!([])}else{json!([{"len":text.len()}])}})).collect());
    value
}
fn ids(ids: &[&str]) -> Vec<String> {
    ids.iter().map(|id| id.to_string()).collect()
}

#[test]
fn sequential_split_join_preserves_surviving_ids_and_peer_paragraph_changes() {
    let initial = native(&["Příliš žluťoučký", "peer"]);
    let mut jana = Binding::import(initial, ids(&["p_one", "p_peer"]), 17).unwrap();
    let mut petr = Binding::restore(&jana.checkpoint().unwrap(), 18).unwrap();
    let split = native(&["Příliš ", "žluťoučký", "peer"]);
    let a = jana
        .reconcile(&split, ids(&["p_one", "p_new", "p_peer"]))
        .unwrap();
    petr.apply_remote(&a).unwrap();
    let b = petr
        .reconcile(
            &native(&["Příliš ", "žluťoučký", "peer!"]),
            ids(&["p_one", "p_new", "p_peer"]),
        )
        .unwrap();
    jana.apply_remote(&b).unwrap();
    assert_eq!(jana.native().unwrap(), petr.native().unwrap());
    assert_eq!(
        jana.stable_ids().unwrap(),
        ids(&["p_one", "p_new", "p_peer"])
    );
    let joined = native(&["Příliš žluťoučký", "peer!"]);
    let update = jana.reconcile(&joined, ids(&["p_one", "p_peer"])).unwrap();
    petr.apply_remote(&update).unwrap();
    assert_eq!(jana.native().unwrap(), joined);
    assert_eq!(petr.native().unwrap(), joined);
    assert_eq!(jana.stable_ids().unwrap(), ids(&["p_one", "p_peer"]));
}

#[test]
fn undo_own_color_does_not_replace_later_peer_color() {
    let initial = native(&["čau"]);
    let mut jana = Binding::import(initial.clone(), ids(&["p_one"]), 17).unwrap();
    let mut petr = Binding::restore(&jana.checkpoint().unwrap(), 18).unwrap();
    let mut red = initial.clone();
    red["body"][0]["runs"][0]["props"] = json!({"color":{"Rgb":[255,0,0]}, "size":12.0});
    let a = jana.reconcile(&red, ids(&["p_one"])).unwrap();
    petr.apply_remote(&a).unwrap();
    let mut blue = red.clone();
    blue["body"][0]["runs"][0]["props"]["color"] = json!({"Rgb":[0,0,255]});
    let b = petr.reconcile(&blue, ids(&["p_one"])).unwrap();
    jana.apply_remote(&b).unwrap();
    let undo = jana.undo().unwrap();
    petr.apply_remote(&undo).unwrap();
    let result = jana.native().unwrap();
    assert_eq!(
        result["body"][0]["runs"][0]["props"]["color"],
        json!({"Rgb":[0,0,255]})
    );
    assert!(result["body"][0]["runs"][0]["props"].get("size").is_none());
    assert_eq!(result, petr.native().unwrap());
}

#[test]
fn paragraph_fields_merge_and_undo_independently() {
    let initial = native(&["Ahoj"]);
    let mut jana = Binding::import(initial.clone(), ids(&["p_one"]), 17).unwrap();
    let mut petr = Binding::restore(&jana.checkpoint().unwrap(), 18).unwrap();
    let mut left = initial.clone();
    left["body"][0]["props"] = json!({"align":"center"});
    let mut right = initial.clone();
    right["body"][0]["props"] = json!({"spaceAfter":12.0});
    let a = jana.reconcile(&left, ids(&["p_one"])).unwrap();
    let b = petr.reconcile(&right, ids(&["p_one"])).unwrap();
    jana.apply_remote(&b).unwrap();
    petr.apply_remote(&a).unwrap();
    assert_eq!(jana.native().unwrap(), petr.native().unwrap());
    assert_eq!(
        jana.native().unwrap()["body"][0]["props"],
        json!({"align":"center","spaceAfter":12.0})
    );
    let a = jana.undo().unwrap();
    petr.apply_remote(&a).unwrap();
    assert_eq!(
        jana.native().unwrap()["body"][0]["props"],
        json!({"spaceAfter":12.0})
    );
    assert_eq!(jana.native().unwrap(), petr.native().unwrap());
}

#[test]
fn malformed_update_leaves_replica_and_undo_unchanged() {
    let mut binding = Binding::import(native(&["č"]), ids(&["p_one"]), 17).unwrap();
    let mut change = binding
        .reconcile(&native(&["č!"]), ids(&["p_one"]))
        .unwrap();
    let before = binding.checkpoint().unwrap();
    change.update = vec![255];
    assert!(binding.apply_remote(&change).is_err());
    assert_eq!(before, binding.checkpoint().unwrap());
}

#[test]
fn concurrent_czech_unicode_insertions_and_duplicate_delivery_converge() {
    let initial = native(&["A🙂Z"]);
    let mut jana = Binding::import(initial, ids(&["p_one"]), 17).unwrap();
    let mut petr = Binding::restore(&jana.checkpoint().unwrap(), 18).unwrap();
    let a = jana
        .reconcile(&native(&["Ač🙂Z"]), ids(&["p_one"]))
        .unwrap();
    let b = petr
        .reconcile(&native(&["Až🙂Z"]), ids(&["p_one"]))
        .unwrap();
    jana.apply_remote(&b).unwrap();
    jana.apply_remote(&b).unwrap();
    petr.apply_remote(&a).unwrap();
    assert_eq!(jana.native().unwrap(), petr.native().unwrap());
    let result = jana.native().unwrap();
    let text = result["body"][0]["text"].as_str().unwrap();
    assert!(text.contains('č') && text.contains('ž') && text.contains('🙂'));
    let restored = Binding::restore(&jana.checkpoint().unwrap(), 19).unwrap();
    assert_eq!(restored.native().unwrap(), result);
}

#[test]
fn unsupported_blocks_and_styles_are_preserved_and_cannot_be_mutated() {
    let mut initial = native(&["editable"]);
    initial["body"]
        .as_array_mut()
        .unwrap()
        .push(json!({"kind":"future-block","opaque":{"value":12345678901234567890u64}}));
    let mut binding = Binding::import(initial.clone(), ids(&["p_one", "opaque"]), 17).unwrap();
    assert_eq!(binding.native().unwrap(), initial);
    let restored = Binding::restore(&binding.checkpoint().unwrap(), 18).unwrap();
    assert_eq!(restored.native().unwrap(), initial);
    let mut after = initial.clone();
    after["body"][1]["opaque"]["value"] = json!(4);
    assert!(binding
        .reconcile(&after, ids(&["p_one", "opaque"]))
        .is_err());
    let mut after = initial.clone();
    after["styles"]["new"] = json!({});
    assert!(binding
        .reconcile(&after, ids(&["p_one", "opaque"]))
        .is_err());
    assert_eq!(binding.native().unwrap(), initial);
}

#[test]
fn split_empty_paragraph_then_undo_restores_original_id() {
    let initial = native(&[""]);
    let mut binding = Binding::import(initial.clone(), ids(&["p_one"]), 17).unwrap();
    binding
        .reconcile(&native(&["", ""]), ids(&["p_one", "p_two"]))
        .unwrap();
    assert_eq!(binding.stable_ids().unwrap(), ids(&["p_one", "p_two"]));
    binding.undo().unwrap();
    assert_eq!(binding.native().unwrap(), initial);
    assert_eq!(binding.stable_ids().unwrap(), ids(&["p_one"]));
}

#[test]
fn actual_wordcraft_model_split_join_output_roundtrips() {
    let mut engine = wordcraft_doc::Document::from_text("Příliš žluťoučký");
    let initial = serde_json::to_value(&engine).unwrap();
    let mut binding = Binding::import(initial.clone(), ids(&["p_one"]), 17).unwrap();
    engine
        .split_paragraph(&wordcraft_doc::Pos::body(0, "Příliš ".len()))
        .unwrap();
    let split = serde_json::to_value(&engine).unwrap();
    binding.reconcile(&split, ids(&["p_one", "p_two"])).unwrap();
    assert_eq!(binding.native().unwrap(), split);
    engine
        .delete_range(
            &wordcraft_doc::Pos::body(0, "Příliš ".len()),
            &wordcraft_doc::Pos::body(1, 0),
        )
        .unwrap();
    let joined = serde_json::to_value(engine).unwrap();
    binding.reconcile(&joined, ids(&["p_one"])).unwrap();
    assert_eq!(binding.native().unwrap(), joined);
    assert_eq!(binding.inventory_ids().unwrap(), ids(&["p_one", "p_two"]));
    assert_eq!(binding.native().unwrap(), initial);
}

#[test]
fn split_racing_same_paragraph_typing_is_refused_without_mutation() {
    let initial = native(&["abcd"]);
    let mut jana = Binding::import(initial.clone(), ids(&["p_one"]), 17).unwrap();
    let mut petr = Binding::restore(&jana.checkpoint().unwrap(), 18).unwrap();
    let split = jana
        .reconcile(&native(&["ab", "cd"]), ids(&["p_one", "p_new"]))
        .unwrap();
    let typed = petr
        .reconcile(&native(&["abcXd"]), ids(&["p_one"]))
        .unwrap();
    let jana_cp = jana.checkpoint().unwrap();
    let petr_cp = petr.checkpoint().unwrap();
    assert_eq!(jana.apply_remote(&typed), Err(Error::StaleStructure));
    assert_eq!(petr.apply_remote(&split), Err(Error::StaleStructure));
    assert_eq!(jana.checkpoint().unwrap(), jana_cp);
    assert_eq!(petr.checkpoint().unwrap(), petr_cp);
    // The rejected originals remain intact; host recovery must retain them as
    // drafts and restore its confirmed checkpoint, never claim them Saved.
    assert!(!typed.update.is_empty() && !split.update.is_empty());
    assert_eq!(petr.native().unwrap()["body"][0]["text"], json!("abcXd"));
}

#[test]
fn stale_text_after_split_join_and_structural_undo_roundtrip_is_refused() {
    for undo in [false, true] {
        let initial = native(&["abcd"]);
        let mut jana = Binding::import(initial.clone(), ids(&["p_one"]), 17).unwrap();
        let mut petr = Binding::restore(&jana.checkpoint().unwrap(), 18).unwrap();
        let typed = petr
            .reconcile(&native(&["abcXd"]), ids(&["p_one"]))
            .unwrap();
        jana.reconcile(&native(&["ab", "cd"]), ids(&["p_one", "p_new"]))
            .unwrap();
        if undo {
            jana.undo().unwrap();
        } else {
            jana.reconcile(&initial, ids(&["p_one"])).unwrap();
        }
        assert_eq!(jana.stable_ids().unwrap(), ids(&["p_one"]));
        let before = jana.checkpoint().unwrap();
        assert_eq!(jana.apply_remote(&typed), Err(Error::StaleStructure));
        assert_eq!(jana.checkpoint().unwrap(), before);
    }
}

#[test]
fn delete_only_change_invalidates_structural_basis_even_without_new_clocks() {
    let initial = native(&["abcd"]);
    let mut jana = Binding::import(initial.clone(), ids(&["p_one"]), 17).unwrap();
    let mut petr = Binding::restore(&jana.checkpoint().unwrap(), 18).unwrap();
    let split = jana
        .reconcile(&native(&["ab", "cd"]), ids(&["p_one", "p_new"]))
        .unwrap();
    petr.reconcile(&native(&["abc"]), ids(&["p_one"])).unwrap();
    let before = petr.checkpoint().unwrap();
    assert_eq!(petr.apply_remote(&split), Err(Error::StaleStructure));
    assert_eq!(petr.checkpoint().unwrap(), before);
}

#[test]
fn new_allocation_origin_check_rejects_foreign_author_but_is_not_authority() {
    use mooring_wordcraft::experimental_binding::validate_authorship;
    let mut binding = Binding::import(native(&["a"]), ids(&["p_one"]), 17).unwrap();
    let change = binding
        .reconcile(&native(&["ač"]), ids(&["p_one"]))
        .unwrap();
    assert_eq!(validate_authorship(&change, 17), Ok(()));
    assert_eq!(validate_authorship(&change, 18), Err(Error::InvalidOrigin));
}
