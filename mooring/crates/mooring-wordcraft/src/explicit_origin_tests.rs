//! Pure construction/candidate allocation, not host issuance or agent admission.
use crate::*;
use mooring_text::{yrs::ClientID, CheckedClientOrigin};
use std::collections::BTreeMap;

fn origin(value: u64) -> CheckedClientOrigin {
    CheckedClientOrigin::new(value).unwrap()
}
fn fixture(name: &str) -> Value {
    serde_json::from_str(match name {
        "canonical" => include_str!("../tests/fixtures/d1-engine-canonical.json"),
        "raw" => include_str!("../tests/fixtures/d1-native.json"),
        "d3" => include_str!("../tests/fixtures/d3-engine-canonical.json"),
        _ => unreachable!(),
    })
    .unwrap()
}
fn stable(native: &Value) -> Vec<String> {
    (0..native["body"].as_array().unwrap().len())
        .map(|n| format!("original-stable-{n}"))
        .collect()
}
fn insert() -> command::Command {
    command::Command::Insert {
        target_id: "original-stable-0".into(),
        at: command::Anchor::End {},
        text: " + origin".into(),
        properties: Some(BTreeMap::from([
            ("bold".into(), false),
            ("italic".into(), true),
        ])),
    }
}

#[test]
fn explicit_import_allocates_only_the_selected_origin_and_preserves_full_native() {
    let native = fixture("canonical");
    let ids = stable(&native);
    let document = Document::import_with_origin(native.clone(), ids.clone(), origin(17)).unwrap();
    assert!(!document.is_read_only());
    assert_eq!(document.native().unwrap(), native);
    assert_eq!(document.block_ids().unwrap(), ids);
    assert_eq!(document.doc.client_id().get(), 17);
    let tx = document.doc.transact();
    assert_eq!(tx.state_vector().iter().count(), 1);
    assert!(tx.state_vector().get(&ClientID::new(17)) > 0);
}

#[test]
fn fresh_restore_retains_ids_and_allocates_actual_candidate_only_at_requested_origin() {
    let native = fixture("canonical");
    let ids = stable(&native);
    let initial = Document::import_with_origin(native.clone(), ids.clone(), origin(17)).unwrap();
    let checkpoint = initial.checkpoint().unwrap();
    let initial_clock = initial
        .doc
        .transact()
        .state_vector()
        .get(&ClientID::new(17));
    let mut document = Document::restore_with_fresh_origin(&checkpoint, origin(31)).unwrap();
    assert_eq!(document.doc.client_id().get(), 31);
    assert_eq!(document.native().unwrap(), native);
    assert_eq!(document.block_ids().unwrap(), ids);
    assert_eq!(document.checkpoint().unwrap(), checkpoint);
    candidate::apply(&mut document, &insert()).unwrap();
    let mut expected = native;
    let text = expected["body"][0]["text"].as_str().unwrap().to_owned();
    expected["body"][0]["text"] = json!(text + " + origin");
    expected["body"][0]["runs"]
        .as_array_mut()
        .unwrap()
        .push(json!({
            "len": 9, "props": {"bold": false, "italic": true}
        }));
    assert_eq!(document.native().unwrap(), expected);
    let after = document.doc.transact().state_vector();
    assert_eq!(after.get(&ClientID::new(17)), initial_clock);
    assert!(after.get(&ClientID::new(31)) > 0);
    let checkpoint = document.checkpoint().unwrap();
    assert!(Document::restore_with_fresh_origin(&checkpoint, origin(31)).is_err());
    let restored = Document::restore_with_fresh_origin(&checkpoint, origin(47)).unwrap();
    assert_eq!(restored.native().unwrap(), expected);
    assert_eq!(restored.block_ids().unwrap(), ids);
}

#[test]
fn fresh_restore_denies_initial_origin_even_after_text_deletion() {
    let native = fixture("canonical");
    let mut document =
        Document::import_with_origin(native.clone(), stable(&native), origin(17)).unwrap();
    candidate::apply(
        &mut document,
        &command::Command::Delete {
            target_id: "original-stable-0".into(),
            start: command::Anchor::Start {},
            end: command::Anchor::End {},
        },
    )
    .unwrap();
    assert!(
        Document::restore_with_fresh_origin(&document.checkpoint().unwrap(), origin(17)).is_err()
    );
}

#[test]
fn explicit_origins_never_promote_readonly_raw_d1_or_section_d3() {
    for name in ["raw", "d3"] {
        let native = fixture(name);
        let ids = stable(&native);
        let document =
            Document::import_with_origin(native.clone(), ids.clone(), origin(17)).unwrap();
        assert!(document.is_read_only(), "{name}");
        assert_eq!(document.native().unwrap(), native);
        let checkpoint = document.checkpoint().unwrap();
        let mut restored = Document::restore_with_fresh_origin(&checkpoint, origin(31)).unwrap();
        assert!(restored.is_read_only());
        assert_eq!(restored.native().unwrap(), native);
        assert_eq!(restored.block_ids().unwrap(), ids);
        assert!(matches!(
            candidate::apply(&mut restored, &insert()),
            Err(candidate::CandidateError::ReadOnly)
        ));
        assert_eq!(restored.checkpoint().unwrap(), checkpoint);
    }
}
