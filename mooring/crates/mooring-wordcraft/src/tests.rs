use super::*;
use serde_json::json;

fn d1() -> Value {
    serde_json::from_str(include_str!("../tests/fixtures/d1-native.json")).unwrap()
}
fn ids(native: &Value) -> Vec<String> {
    // Explicit caller-assigned fixture IDs. Production imports must supply fresh
    // IDs once and durably retain them, never regenerate indexes on reopen.
    (0..native["body"].as_array().unwrap().len())
        .map(|i| format!("fixture-paragraph-{i}"))
        .collect()
}
#[test]
fn native_d1_preserves_every_field_through_crdt_and_storage_only_restore() {
    let native = d1();
    let stable = ids(&native);
    let doc = Document::import(native.clone(), stable.clone()).unwrap();
    assert!(!doc.is_read_only());
    assert_eq!(doc.native().unwrap(), native);
    let persisted = doc.checkpoint().unwrap();
    drop(doc);
    let restored = Document::restore(&persisted).unwrap();
    assert_eq!(restored.block_ids().unwrap(), stable);
    assert_eq!(restored.native().unwrap(), native);
    assert!(!restored.is_read_only());
}
#[test]
fn unicode_boundaries_are_checked_in_both_directions() {
    let text = "Č😀e\u{301}";
    for (byte, utf16) in [(0, 0), (2, 1), (6, 3), (7, 4), (9, 5)] {
        assert_eq!(byte_to_utf16(text, byte), Ok(utf16));
        assert_eq!(utf16_to_byte(text, utf16), Ok(byte));
    }
    for byte in [1, 3, 4, 5, 8, 10] {
        assert_eq!(byte_to_utf16(text, byte), Err(AdapterError::InvalidNative));
    }
    for utf16 in [2, 6] {
        assert_eq!(utf16_to_byte(text, utf16), Err(AdapterError::InvalidNative));
    }
}
#[test]
fn synthetic_unicode_empty_paragraph_and_explicit_false_are_lossless() {
    let mut native = d1();
    native["body"] = json!([
        {"kind":"para","text":"Č😀e\u{301}","runs":[
            {"len":2},{"len":4,"props":{"bold":true,"boldCs":false}},
            {"len":3,"props":{"italic":false,"italicCs":true}}],"props":{"style":"Normal"}},
        {"kind":"para","text":"","runs":[],"mark":{"bold":false,"italic":true},"props":{"style":"Normal"}}
    ]);
    let doc = Document::import(native.clone(), ids(&native)).unwrap();
    assert!(!doc.is_read_only());
    assert_eq!(doc.native().unwrap(), native);
    assert_eq!(
        Document::restore(&doc.checkpoint().unwrap())
            .unwrap()
            .native()
            .unwrap(),
        native
    );
}
#[test]
fn unsupported_blocks_and_inline_semantics_open_entire_document_read_only() {
    let variants = [
        json!({"kind":"table","rows":[]}),
        json!({"kind":"para","text":"x","runs":[{"len":1,"obj":{"kind":"image"}}]}),
        json!({"kind":"para","text":"x","runs":[{"len":1,"props":{"unknown_future_property":true}}]}),
        json!({"kind":"para","text":"\u{fffc}","runs":[{"len":3}]}),
    ];
    for unsupported in variants {
        let mut native = d1();
        native["body"][0] = unsupported;
        let doc = Document::import(native.clone(), ids(&native)).unwrap();
        assert!(doc.is_read_only());
        assert_eq!(doc.native().unwrap(), native);
        let restored = Document::restore(&doc.checkpoint().unwrap()).unwrap();
        assert!(restored.is_read_only());
        assert_eq!(restored.native().unwrap(), native);
    }
}
#[test]
fn duplicate_ids_and_broken_native_utf8_run_boundaries_are_refused() {
    let native = d1();
    let mut stable = ids(&native);
    stable[1] = stable[0].clone();
    assert!(matches!(
        Document::import(native.clone(), stable),
        Err(AdapterError::InvalidIdentity)
    ));
    let mut bad = native;
    bad["body"][0]["runs"] = json!([{"len":1},{"len":26}]);
    assert!(matches!(
        Document::import(bad.clone(), ids(&bad)),
        Err(AdapterError::InvalidNative)
    ));
}
#[test]
fn checkpoint_cannot_pair_new_native_content_with_an_older_crdt() {
    let native = d1();
    let doc = Document::import(native.clone(), ids(&native)).unwrap();
    let mut checkpoint: Value = serde_json::from_slice(&doc.checkpoint().unwrap()).unwrap();
    checkpoint["native"]["body"][0]["text"] = json!("changed without matching CRDT");
    assert!(matches!(
        Document::restore(&serde_json::to_vec(&checkpoint).unwrap()),
        Err(AdapterError::InvalidCheckpoint)
    ));
}

#[test]
fn unsupported_document_metadata_and_unknown_paragraph_fields_are_read_only() {
    for (field, value) in [
        ("parts", json!({"image1":{"kind":"image"}})),
        ("comments", json!({"comment1":{"text":"retain"}})),
        ("revisions", json!([{"author":"retain"}])),
        ("future_native_feature", json!({"data":"retain"})),
    ] {
        let mut native = d1();
        native[field] = value;
        let doc = Document::import(native.clone(), ids(&native)).unwrap();
        assert!(doc.is_read_only());
        assert_eq!(doc.native().unwrap(), native);
    }
    let mut native = d1();
    native["body"][0]["future_anchor"] = json!({"retain":true});
    assert!(Document::import(native.clone(), ids(&native))
        .unwrap()
        .is_read_only());
}

#[test]
fn invalid_native_container_types_cannot_be_classified_writable() {
    for (field, value) in [
        ("comments", json!([])),
        ("revisions", json!({})),
        ("styles", json!(false)),
    ] {
        let mut native = d1();
        native[field] = value;
        assert!(matches!(
            Document::import(native.clone(), ids(&native)),
            Err(AdapterError::InvalidNative)
        ));
    }
}

#[test]
fn empty_document_has_a_storage_only_roundtrip_without_invented_paragraphs() {
    let mut native = d1();
    native["body"] = json!([]);
    let doc = Document::import(native.clone(), Vec::new()).unwrap();
    let restored = Document::restore(&doc.checkpoint().unwrap()).unwrap();
    assert!(restored.block_ids().unwrap().is_empty());
    assert_eq!(restored.native().unwrap(), native);
}

#[test]
fn independent_concurrent_inline_flags_merge_in_either_delivery_order() {
    use mooring_text::yrs::types::Attrs;
    use mooring_text::yrs::updates::decoder::Decode;
    use mooring_text::yrs::{Any, Map, Out, ReadTxn, StateVector, Text, Transact};
    let mut native = d1();
    native["body"] =
        json!([{"kind":"para","text":"Č😀e\u{301}","runs":[{"len":9}],"props":{"style":"Normal"}}]);
    let base = Document::import(native.clone(), vec!["stable-paragraph".into()])
        .unwrap()
        .checkpoint()
        .unwrap();
    let a = Document::restore(&base).unwrap();
    let b = Document::restore(&base).unwrap();
    for (replica, flag) in [(&a, "bold"), (&b, "italic")] {
        let txn = replica.doc.transact();
        let Out::YText(text) = txn
            .get_map("wordcraft.texts")
            .unwrap()
            .get(&txn, "stable-paragraph")
            .unwrap()
        else {
            panic!("missing paragraph")
        };
        drop(txn);
        let attrs = Attrs::from([
            ("wordcraft.props".into(), Any::Bool(true)),
            (format!("wordcraft.chr.{flag}").into(), Any::Bool(true)),
        ]);
        text.format(&mut replica.doc.transact_mut(), 0, 5, attrs);
    }
    let updates = [&a, &b].map(|d| {
        d.doc
            .transact()
            .encode_state_as_update_v1(&StateVector::default())
    });
    let mut expected = native;
    expected["body"][0]["runs"] = json!([{"len":9,"props":{"bold":true,"italic":true}}]);
    for order in [[0, 1], [1, 0]] {
        let replica = Document::restore(&base).unwrap();
        for index in order {
            replica
                .doc
                .transact_mut()
                .apply_update(mooring_text::yrs::Update::decode_v1(&updates[index]).unwrap())
                .unwrap();
        }
        assert_eq!(replica.native().unwrap(), expected);
        assert_eq!(
            Document::restore(&replica.checkpoint().unwrap())
                .unwrap()
                .native()
                .unwrap(),
            expected
        );
    }
}

#[test]
fn zero_length_inline_runs_with_ambiguous_insertion_semantics_are_read_only() {
    let mut native = d1();
    native["body"][0]["runs"] = json!([
        {"len":0,"props":{"bold":true}},
        {"len":27}
    ]);
    let doc = Document::import(native.clone(), ids(&native)).unwrap();
    assert!(doc.is_read_only());
    assert!(doc.read_only_reason().is_some());
    assert_eq!(doc.native().unwrap(), native);
}

#[test]
fn unqualified_empty_run_encoding_opens_whole_document_read_only() {
    for run in [json!({"len":0}), json!({"len":0,"props":{"bold":false}})] {
        let mut native = d1();
        native["body"][0] =
            json!({"kind":"para","text":"","runs":[run],"props":{"style":"Normal"}});
        let doc = Document::import(native.clone(), ids(&native)).unwrap();
        assert!(doc.is_read_only());
        assert!(doc.read_only_reason().is_some());
        assert_eq!(doc.native().unwrap(), native);
        let restored = Document::restore(&doc.checkpoint().unwrap()).unwrap();
        assert!(restored.is_read_only());
        assert_eq!(restored.native().unwrap(), native);
    }
}

#[test]
fn v2_checkpoint_packs_original_update_and_preserves_opaque_native_integer() {
    let mut native = d1();
    native["opaque_future_native"] = json!({"integer":9_007_199_254_740_993_u64});
    let document = Document::import(native.clone(), ids(&native)).unwrap();
    let bytes = document.checkpoint().unwrap();
    let stored: Value = serde_json::from_slice(&bytes).unwrap();
    assert_eq!(stored["format"], profile::CHECKPOINT_FORMAT);
    assert!(stored.get("update").is_none());
    assert!(stored["update_base64"].is_string());
    drop(document);
    let restored = Document::restore(&bytes).unwrap();
    assert_eq!(restored.native().unwrap(), native);
    assert!(serde_json::to_string(&restored.native().unwrap())
        .unwrap()
        .contains("9007199254740993"));
    assert!(restored.is_read_only());
}
#[test]
fn legacy_checkpoint_keeps_original_native_and_ids_but_never_becomes_writable() {
    let native = d1();
    let stable = ids(&native);
    let source = Document::import(native.clone(), stable.clone()).unwrap();
    let legacy = Checkpoint {
        format: profile::LEGACY_CHECKPOINT_FORMAT.into(),
        native: native.clone(),
        block_ids: stable.clone(),
        update: source
            .doc
            .transact()
            .encode_state_as_update_v1(&StateVector::default()),
    };
    let bytes = serde_json::to_vec(&legacy).unwrap();
    drop(source);
    let mut restored = Document::restore(&bytes).unwrap();
    assert_eq!(restored.native().unwrap(), native);
    assert_eq!(restored.block_ids().unwrap(), stable);
    assert!(restored.is_read_only());
    assert!(restored.read_only_reason().unwrap().contains("migration"));
    let command = command::Command::Insert {
        target_id: stable[0].clone(),
        at: command::Anchor::Start {},
        text: "unsafe".into(),
        properties: None,
    };
    assert_eq!(
        candidate::apply(&mut restored, &command),
        Err(candidate::CandidateError::ReadOnly)
    );
    let exported: Value = serde_json::from_slice(&restored.checkpoint().unwrap()).unwrap();
    assert_eq!(exported["format"], profile::LEGACY_CHECKPOINT_FORMAT);
    assert!(exported.get("update").is_some() && exported.get("update_base64").is_none());
}
#[test]
fn packed_checkpoint_rejects_noncanonical_base64_and_cross_format_wrappers() {
    let native = d1();
    let document = Document::import(native.clone(), ids(&native)).unwrap();
    let stored: Value = serde_json::from_slice(&document.checkpoint().unwrap()).unwrap();
    for encoded in ["AB==", "AAB=", " AA==", "AA==\n", "AA", "====", "", "é=="] {
        let mut bad = stored.clone();
        bad["update_base64"] = json!(encoded);
        assert!(
            Document::restore(&serde_json::to_vec(&bad).unwrap()).is_err(),
            "accepted {encoded:?}"
        );
    }
    let mut bad = stored.clone();
    bad["format"] = json!(profile::LEGACY_CHECKPOINT_FORMAT);
    assert!(Document::restore(&serde_json::to_vec(&bad).unwrap()).is_err());
    let mut bad = stored;
    bad["update"] = json!([0, 0]);
    assert!(Document::restore(&serde_json::to_vec(&bad).unwrap()).is_err());
}
#[test]
fn checkpoint_rejects_hidden_roots_and_unprojected_metadata_keys() {
    let native = d1();
    let document = Document::import(native.clone(), ids(&native)).unwrap();
    document.doc.get_or_insert_text("hidden.root").insert(
        &mut document.doc.transact_mut(),
        0,
        "invisible",
    );
    let bytes = document.checkpoint().unwrap();
    assert!(Document::restore(&bytes).is_err());
    let document = Document::import(native.clone(), ids(&native)).unwrap();
    document.doc.get_or_insert_map("wordcraft.metadata").insert(
        &mut document.doc.transact_mut(),
        "hidden",
        "invisible",
    );
    assert!(Document::restore(&document.checkpoint().unwrap()).is_err());
}
