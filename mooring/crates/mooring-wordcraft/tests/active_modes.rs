use mooring_wordcraft::Document;
use serde_json::Value;
use wordcraft_doc::{
    numbering::{AbstractNum, Level},
    props::{NumChange, PropChange},
};

fn native() -> wordcraft_doc::Document {
    serde_json::from_str(include_str!("fixtures/d1-engine-canonical.json")).unwrap()
}
fn import(native: Value) -> Document {
    let ids = (0..native["body"].as_array().unwrap().len())
        .map(|i| format!("mode_{i}"))
        .collect();
    Document::import(native, ids).unwrap()
}
fn assert_readonly(typed: wordcraft_doc::Document) {
    // Serialize the actual pinned application types, including their omission rules.
    let native = serde_json::to_value(typed).unwrap();
    let doc = import(native.clone());
    assert!(doc.is_read_only(), "unsupported native semantics admitted");
    assert_eq!(doc.native().unwrap(), native);
    let ids = doc.block_ids().unwrap();
    let cp = doc.checkpoint().unwrap();
    let restored = Document::restore(&cp).unwrap();
    assert!(restored.is_read_only());
    assert_eq!(restored.native().unwrap(), native);
    assert_eq!(restored.block_ids().unwrap(), ids);
    assert_eq!(restored.checkpoint().unwrap(), cp);
}
#[test]
fn native_settings_with_review_or_protection_semantics_are_readonly() {
    assert!(!import(serde_json::to_value(native()).unwrap()).is_read_only());
    let mut value = native();
    value.settings.track_changes = true;
    assert_readonly(value);
    let mut value = native();
    value.settings.protection = Some("readOnly".into());
    assert_readonly(value);
    let mut value = native();
    value.settings.protection = Some("none".into());
    assert_readonly(value);
    let mut value = native();
    value.settings.protect_formatting = true;
    assert_readonly(value);
    let mut value = native();
    value
        .settings
        .protect_hash
        .push(("hash".into(), "opaque".into()));
    assert_readonly(value);
}
#[test]
fn native_retained_review_metadata_is_readonly_even_without_revision_table() {
    // Orphan revision references are unqualified too; no application authorization is inferred.
    let mut value = native();
    value.last_section.fmt_change = PropChange::boxed(0, value.last_section.clone());
    assert_readonly(value);
    let mut value = native();
    value.styles.default_chr.ins = Some(0);
    assert_readonly(value);
    let mut value = native();
    value.styles.styles[0].chr.del = Some(0);
    assert_readonly(value);
    let mut value = native();
    value.styles.styles[0].para.num_change = Some(Box::new(NumChange {
        rev: 0,
        original: "1.".into(),
    }));
    assert_readonly(value);
    let mut value = native();
    let mut level = Level::default();
    level.chr.fmt_change = PropChange::boxed(0, level.chr.clone());
    value.numbering.abstracts.push(AbstractNum {
        id: 900,
        levels: vec![level],
        ..Default::default()
    });
    assert_readonly(value);
}
#[test]
fn native_numbering_without_review_metadata_remains_qualified() {
    let mut value = native();
    value.numbering.abstracts.push(AbstractNum {
        id: 900,
        levels: vec![Level::default()],
        ..Default::default()
    });
    assert!(!import(serde_json::to_value(value).unwrap()).is_read_only());
}

#[test]
fn actual_old_protected_projected_checkpoint_restores_readonly_byte_exact() {
    let original = include_bytes!("fixtures/protected-old-v2-checkpoint.json");
    let envelope: Value = serde_json::from_slice(original).unwrap();
    let doc = Document::restore(original).unwrap();
    assert!(doc.is_read_only());
    assert_eq!(doc.native().unwrap(), envelope["native"]);
    assert_eq!(
        serde_json::to_value(doc.block_ids().unwrap()).unwrap(),
        envelope["block_ids"]
    );
    assert_eq!(doc.checkpoint().unwrap(), original);
}
