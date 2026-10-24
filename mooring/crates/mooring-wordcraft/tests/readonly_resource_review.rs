use mooring_core::checkpoint_description::DescribeCheckpoint;
use mooring_wordcraft::{
    adapter::{PrepareError, WordCraftAdapter},
    Document,
};
use std::sync::Arc;
use wordcraft_doc::{control::ContentControl, Block};

#[test]
fn opaque_control_resources_cannot_be_declared_a_complete_empty_manifest() {
    let source: serde_json::Value =
        serde_json::from_str(include_str!("fixtures/d2-engine-canonical.json")).unwrap();
    for location in ["table", "row", "cell"] {
        let mut typed: wordcraft_doc::Document = serde_json::from_value(source.clone()).unwrap();
        let Block::Table(table) = Arc::make_mut(&mut typed.body[9]) else {
            panic!("actual D2 table missing")
        };
        let control = ContentControl {
            extra: vec!["<w:dataBinding w:storeItemID=\"unresolved-source\" w:xpath=\"/external/document\"/>".into()],
            ..Default::default()
        };
        let wrap = match location {
            "table" => &mut table.controls,
            "row" => &mut table.rows[0].controls,
            _ => &mut table.rows[0].cells[0].controls,
        };
        wrap.open.push(control);
        wrap.close = 1;
        let native = serde_json::to_value(&typed).unwrap();
        let ids = (0..native["body"].as_array().unwrap().len())
            .map(|i| format!("review_{i}"))
            .collect();
        let document = Document::import(native.clone(), ids).unwrap();
        assert!(document.is_read_only());
        let checkpoint = document.checkpoint().unwrap();
        assert!(
            matches!(
                WordCraftAdapter.describe_checkpoint(&checkpoint),
                Err(PrepareError::UnqualifiedDescription)
            ),
            "{location} opaque XML was qualified as empty external resources"
        );
        let restored = Document::restore(&checkpoint).unwrap();
        assert_eq!(restored.native().unwrap(), native);
        assert_eq!(restored.checkpoint().unwrap(), checkpoint);
    }
}

#[test]
fn empty_controls_remain_qualified_but_even_plain_or_unmatched_controls_are_unknown() {
    let source: serde_json::Value =
        serde_json::from_str(include_str!("fixtures/d2-engine-canonical.json")).unwrap();
    for location in ["table", "row", "cell"] {
        for kind in ["empty", "plain", "close-only"] {
            let mut typed: wordcraft_doc::Document =
                serde_json::from_value(source.clone()).unwrap();
            let Block::Table(table) = Arc::make_mut(&mut typed.body[9]) else {
                panic!("table missing")
            };
            let wrap = match location {
                "table" => &mut table.controls,
                "row" => &mut table.rows[0].controls,
                _ => &mut table.rows[0].cells[0].controls,
            };
            match kind {
                "plain" => {
                    wrap.open.push(ContentControl::default());
                    wrap.close = 1;
                }
                "close-only" => {
                    wrap.close = 1;
                }
                _ => {}
            }
            let native = serde_json::to_value(typed).unwrap();
            let ids = (0..native["body"].as_array().unwrap().len())
                .map(|i| format!("control_{i}"))
                .collect();
            let document = Document::import(native.clone(), ids).unwrap();
            let cp = document.checkpoint().unwrap();
            assert_eq!(
                WordCraftAdapter.describe_checkpoint(&cp).is_ok(),
                kind == "empty",
                "{location}/{kind}"
            );
            assert!(document.is_read_only());
            let restored = Document::restore(&cp).unwrap();
            assert_eq!(restored.native().unwrap(), native);
            assert_eq!(restored.checkpoint().unwrap(), cp);
        }
    }
}
