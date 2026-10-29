use mooring_core::{adapter::PrepareAdapter, checkpoint_description::DescribeCheckpoint};
use mooring_wordcraft::{adapter::WordCraftAdapter, Document};
use serde_json::{json, Value};

#[test]
fn borrowed_views_match_byte_traits_without_changing_full_d1_or_readonly_d3() {
    for (raw, readonly) in [
        (include_str!("fixtures/d1-engine-canonical.json"), false),
        (include_str!("fixtures/d3-engine-canonical.json"), true),
    ] {
        let native: Value = serde_json::from_str(raw).unwrap();
        let ids: Vec<_> = (0..native["body"].as_array().unwrap().len())
            .map(|i| format!("borrowed_{i}"))
            .collect();
        let original = Document::import(native.clone(), ids.clone()).unwrap();
        let checkpoint = original.checkpoint().unwrap();
        let checked = Document::restore(&checkpoint).unwrap();
        let old_description = WordCraftAdapter.describe_checkpoint(&checkpoint).unwrap();
        let description = WordCraftAdapter.describe_document(&checked).unwrap();
        assert_eq!(description.format_version, old_description.format_version);
        assert_eq!(description.versions, old_description.versions);
        assert_eq!(description.assets, old_description.assets);
        assert_eq!(
            WordCraftAdapter.validate_document(&checked).unwrap(),
            WordCraftAdapter.validate_checkpoint(&checkpoint).unwrap()
        );
        assert_eq!(checked.native().unwrap(), native);
        assert_eq!(checked.block_ids().unwrap(), ids);
        assert_eq!(checked.is_read_only(), readonly);
        assert_eq!(checked.checkpoint().unwrap(), checkpoint);
        assert_eq!(original.checkpoint().unwrap(), checkpoint);
    }
}

#[test]
fn borrowed_description_keeps_closed_graph_check_independent_of_writability() {
    let mut native: Value =
        serde_json::from_str(include_str!("fixtures/d1-engine-canonical.json")).unwrap();
    native["settings"]["attachedTemplate"] = json!("https://example.invalid/external.dotx");
    let ids: Vec<_> = (0..native["body"].as_array().unwrap().len())
        .map(|i| format!("external_{i}"))
        .collect();
    let document = Document::import(native.clone(), ids.clone()).unwrap();
    let checkpoint = document.checkpoint().unwrap();
    assert!(!document.is_read_only());
    assert!(WordCraftAdapter.describe_document(&document).is_err());
    assert!(WordCraftAdapter.describe_checkpoint(&checkpoint).is_err());
    assert_eq!(
        WordCraftAdapter.validate_document(&document).unwrap(),
        WordCraftAdapter.validate_checkpoint(&checkpoint).unwrap()
    );
    assert_eq!(document.native().unwrap(), native);
    assert_eq!(document.block_ids().unwrap(), ids);
    assert_eq!(document.checkpoint().unwrap(), checkpoint);
}
