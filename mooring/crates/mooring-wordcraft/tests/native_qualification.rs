use mooring_wordcraft::Document;
use serde_json::{json, Value};
fn native() -> Value {
    serde_json::from_str(include_str!("fixtures/d1-engine-canonical.json")).unwrap()
}
fn import(value: Value) -> Document {
    let ids = (0..value["body"].as_array().unwrap().len())
        .map(|i| format!("qualified_{i}"))
        .collect();
    Document::import(value, ids).unwrap()
}
#[test]
fn engine_canonical_full_native_is_qualified_but_serde_loss_is_readonly() {
    let initial = native();
    assert!(!import(initial.clone()).is_read_only());
    for path in [
        "/settings",
        "/styles",
        "/styles/defaultChr",
        "/styles/defaultPara",
        "/styles/styles/0",
        "/numbering",
        "/lastSection",
    ] {
        let mut value = initial.clone();
        value
            .pointer_mut(path)
            .unwrap()
            .as_object_mut()
            .unwrap()
            .insert("opaqueAudit".into(), json!(9007199254740993_u64));
        let doc = import(value.clone());
        assert!(doc.is_read_only(), "lossy unknown metadata at {path}");
        assert_eq!(doc.native().unwrap(), value);
        let cp = doc.checkpoint().unwrap();
        let restored = Document::restore(&cp).unwrap();
        assert!(restored.is_read_only());
        assert_eq!(restored.native().unwrap(), value);
        assert_eq!(restored.checkpoint().unwrap(), cp);
    }
}
#[test]
fn engine_numeric_coercion_and_defaults_never_qualify_writing() {
    for (path, value) in [
        ("/settings/defaultTab", json!(36.1)),
        ("/lastSection/pageW", json!(612.1)),
        ("/styles/defaultPara/spaceAfter", json!(8.1)),
        ("/settings/compatMode", json!(9007199254740993_u64)),
    ] {
        let mut input = native();
        *input.pointer_mut(path).unwrap() = value;
        let doc = import(input.clone());
        assert!(doc.is_read_only(), "lossy numeric metadata at {path}");
        assert_eq!(doc.native().unwrap(), input);
    }
    for (path, key) in [
        ("/settings", "defaultTab"),
        ("/styles", "defaultChr"),
        ("/lastSection", "pageW"),
    ] {
        let mut input = native();
        input
            .pointer_mut(path)
            .unwrap()
            .as_object_mut()
            .unwrap()
            .remove(key);
        let doc = import(input.clone());
        assert!(
            doc.is_read_only(),
            "implicit native default at {path}/{key}"
        );
        assert_eq!(doc.native().unwrap(), input);
    }
}

#[test]
fn original_raw_source_stays_readonly_with_no_float_rewrite() {
    let source: Value = serde_json::from_str(include_str!("fixtures/d1-native.json")).unwrap();
    let doc = import(source.clone());
    assert!(doc.is_read_only());
    assert_eq!(doc.native().unwrap(), source);
    let cp = doc.checkpoint().unwrap();
    let restored = Document::restore(&cp).unwrap();
    assert!(restored.is_read_only());
    assert_eq!(restored.native().unwrap(), source);
    assert_eq!(restored.checkpoint().unwrap(), cp);
}

#[test]
fn actual_native_open_normalization_never_silently_changes_writable_input() {
    for body in [
        json!([]),
        json!([{"kind":"para","text":"aa","runs":[{"len":1},{"len":1}]}]),
    ] {
        let mut source = native();
        source["body"] = body;
        let doc = import(source.clone());
        assert!(doc.is_read_only());
        assert_eq!(doc.native().unwrap(), source);
        let cp = doc.checkpoint().unwrap();
        let restored = Document::restore(&cp).unwrap();
        assert!(restored.is_read_only());
        assert_eq!(restored.native().unwrap(), source);
        assert_eq!(restored.checkpoint().unwrap(), cp);
    }
}

#[test]
fn new_wordcraft_ui_document_uses_the_canonical_editable_blank() {
    let source = include_str!("../../../../lib/mooring/wordcraft-empty.ts");
    let literal = source
        .lines()
        .find_map(|line| line.strip_prefix("export const WORDCRAFT_EMPTY_NATIVE = "))
        .unwrap();
    let raw: String = serde_json::from_str(literal).unwrap();
    let native: Value = serde_json::from_str(&raw).unwrap();
    let mut blank = wordcraft_doc::Document::new();
    blank.normalize_runs();
    blank.ensure_nonempty();
    assert_eq!(native, serde_json::to_value(blank).unwrap());
    let document = Document::import(native.clone(), vec!["new-paragraph".into()]).unwrap();
    assert!(!document.is_read_only());
    let restored = Document::restore(&document.checkpoint().unwrap()).unwrap();
    assert!(!restored.is_read_only());
    assert_eq!(restored.native().unwrap(), native);
}
