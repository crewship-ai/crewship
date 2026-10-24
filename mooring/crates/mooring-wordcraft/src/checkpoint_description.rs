//! Inactive application-owned checkpoint asset qualification, not authority.
use crate::{
    adapter::{PrepareError, WordCraftAdapter},
    Document,
};
use mooring_core::checkpoint_description::{CheckpointDescription, DescribeCheckpoint};

impl DescribeCheckpoint for WordCraftAdapter {
    fn describe_checkpoint(
        &self,
        checkpoint: &[u8],
    ) -> Result<CheckpointDescription, PrepareError> {
        // Restore EXACT provided bytes, including hidden CRDT/model integrity.
        // A description neither changes these bytes nor grants read/write access.
        let document = Document::restore(checkpoint).map_err(PrepareError::State)?;
        if !closed_self_contained(&document) && !crate::readonly_resources::qualified(&document) {
            return Err(PrepareError::UnqualifiedDescription);
        }
        Ok(CheckpointDescription {
            format_version: crate::profile::CHECKPOINT_FORMAT.into(),
            versions: crate::profile::qualified_versions(),
            assets: Vec::new(),
        })
    }
}
fn closed_self_contained(document: &Document) -> bool {
    if document.model != crate::format::Model::V2 {
        return false;
    }
    let Ok(native) = document.native() else {
        return false;
    };
    // Qualify the COMPLETE original tree before any disposable layout erasure.
    // This rejects serde loss, numeric drift, unknown fields and review modes.
    if !crate::native_qualification::lossless(&native) {
        return false;
    }
    let Ok(typed) = serde_json::from_value::<wordcraft_doc::Document>(native.clone()) else {
        return false;
    };
    if !typed.body.iter().all(|block| match &**block {
        wordcraft_doc::Block::Para(paragraph) => {
            paragraph.objects.is_empty()
                && paragraph
                    .section
                    .as_ref()
                    .is_none_or(|section| no_section_references(section))
        }
        _ => false,
    }) {
        return false;
    }
    // A disposable classifier input may omit ONLY the two qualified layout
    // fields. Original native/checkpoint/IDs are never overwritten or migrated.
    let mut layout_free = native.clone();
    for paragraph in layout_free["body"].as_array_mut().unwrap() {
        let object = paragraph.as_object_mut().unwrap();
        object.remove("section");
        if let Some(serde_json::Value::Object(props)) = object.get_mut("props") {
            props.remove("pageBreakBefore");
            if props.is_empty() {
                object.remove("props");
            }
        }
    }
    if crate::classify(&layout_free) != Ok(false) {
        return false;
    }
    // This descriptor proves only the known closed paragraph/section subset.
    // No absent/unknown graph is advertised as a complete empty manifest.
    typed.settings.attached_template.is_none()
        && !typed.settings.link_styles
        && typed.settings.page_picture.is_none()
        && typed.settings.watermark.is_none()
        && typed.styles.latent_styles.is_none()
        && typed.parts.is_empty()
        && typed.media.is_empty()
        && typed.passthrough.is_empty()
        && typed.glossary.is_empty()
        && typed.sources.is_empty()
        && typed.custom_props.is_empty()
        && no_section_references(&typed.last_section)
        && no_unqualified_links(&native, 0, &mut 262144)
}
pub(crate) fn no_section_references(section: &wordcraft_doc::SectionProps) -> bool {
    [&section.headers, &section.footers]
        .iter()
        .all(|refs| refs.default.is_none() && refs.first.is_none() && refs.even.is_none())
}
fn no_unqualified_links(value: &serde_json::Value, depth: usize, remaining: &mut usize) -> bool {
    if depth > 32 || *remaining == 0 {
        return false;
    }
    *remaining -= 1;
    match value {
        serde_json::Value::Object(fields) => fields.iter().all(|(key, value)| {
            (!matches!(key.as_str(), "link" | "latentStyles" | "attachedTemplate")
                || value.is_null())
                && no_unqualified_links(value, depth + 1, remaining)
        }),
        serde_json::Value::Array(values) => values
            .iter()
            .all(|value| no_unqualified_links(value, depth + 1, remaining)),
        _ => true,
    }
}
#[cfg(test)]
mod tests {
    use super::*;
    use wordcraft_doc::{Document as Native, PagePicture};
    fn native() -> Native {
        serde_json::from_str(include_str!("../tests/fixtures/d1-engine-canonical.json")).unwrap()
    }
    fn imported(native: Native) -> Document {
        let ids = (0..native.body.len())
            .map(|i| format!("description_{i}"))
            .collect();
        Document::import(serde_json::to_value(native).unwrap(), ids).unwrap()
    }
    #[test]
    fn actual_known_external_reference_fields_are_not_an_empty_manifest() {
        let mut native = native();
        native.settings.attached_template = Some("https://example.invalid/template.dotx".into());
        let document = imported(native);
        assert!(!document.is_read_only());
        assert!(
            !closed_self_contained(&document),
            "writable does not prove a complete empty external manifest"
        );
    }
    #[test]
    fn qualified_actual_d1_is_a_closed_self_contained_control() {
        assert!(closed_self_contained(&imported(native())));
    }
    #[test]
    fn known_page_picture_reference_is_not_proven_embedded_by_a_string_key() {
        let mut native = native();
        native.settings.page_picture = Some(PagePicture {
            media: "missing-media".into(),
            ..Default::default()
        });
        assert!(!closed_self_contained(&imported(native)));
    }
}

#[cfg(test)]
mod graph_tests {
    use super::*;
    use serde_json::json;
    fn fixture() -> wordcraft_doc::Document {
        serde_json::from_str(include_str!("../tests/fixtures/d1-engine-canonical.json")).unwrap()
    }
    #[test]
    fn actual_typed_retained_link_and_graph_metadata_is_unqualified_without_rewrite() {
        let changes: [fn(&mut wordcraft_doc::Document); 5] = [
            |d| d.settings.link_styles = true,
            |d| d.styles.latent_styles = Some("<opaqueRelationship/>".into()),
            |d| d.styles.default_chr.link = Some("https://example.invalid/default-link".into()),
            |d| {
                d.numbering.abstracts[0].levels[0].chr.link =
                    Some("https://example.invalid/numbering-link".into())
            },
            |d| d.last_section.headers.default = Some(9),
        ];
        for change in changes {
            let mut typed = fixture();
            change(&mut typed);
            let ids = (0..typed.body.len())
                .map(|i| format!("graph_{i}"))
                .collect();
            let native = serde_json::to_value(typed).unwrap();
            let document = Document::import(native.clone(), ids).unwrap();
            let checkpoint = document.checkpoint().unwrap();
            assert!(!closed_self_contained(&document));
            assert!(matches!(
                WordCraftAdapter.describe_checkpoint(&document.checkpoint().unwrap()),
                Err(PrepareError::UnqualifiedDescription)
            ));
            assert_eq!(document.native().unwrap(), native);
            assert_eq!(document.checkpoint().unwrap(), checkpoint);
        }
    }
    #[test]
    fn unknown_opaque_fields_and_unqualified_embedded_media_are_not_guessed_empty() {
        let mut typed = fixture();
        typed.add_media(vec![137, 80, 78, 71, 13, 10, 26, 10], "png");
        let mut native = serde_json::to_value(typed).unwrap();
        for media in [true, false] {
            if !media {
                native.as_object_mut().unwrap().remove("media");
                native["core"]["opaqueExternalReference"] =
                    json!("https://example.invalid/unknown");
            }
            let ids = (0..native["body"].as_array().unwrap().len())
                .map(|i| format!("graph_{i}"))
                .collect();
            let document = Document::import(native.clone(), ids).unwrap();
            let checkpoint = document.checkpoint().unwrap();
            assert!(!closed_self_contained(&document));
            assert!(matches!(
                WordCraftAdapter.describe_checkpoint(&document.checkpoint().unwrap()),
                Err(PrepareError::UnqualifiedDescription)
            ));
            assert_eq!(
                Document::restore(&checkpoint).unwrap().native().unwrap(),
                native
            );
            assert_eq!(document.checkpoint().unwrap(), checkpoint);
        }
    }
    #[test]
    fn manifest_scan_depth_and_work_are_explicitly_bounded() {
        let mut value = json!(null);
        for _ in 0..34 {
            value = json!([value]);
        }
        assert!(!no_unqualified_links(&value, 0, &mut 100));
        assert!(!no_unqualified_links(&json!([null, null, null]), 0, &mut 3));
        assert!(!no_unqualified_links(&json!({"link":""}), 0, &mut 100));
        assert!(no_unqualified_links(&json!({"link":null}), 0, &mut 100));
    }
}

#[cfg(test)]
mod trait_tests {
    use super::*;
    use mooring_text::yrs::{ReadTxn, StateVector, Transact};
    fn source() -> Document {
        let native: serde_json::Value =
            serde_json::from_str(include_str!("../tests/fixtures/d1-engine-canonical.json"))
                .unwrap();
        let ids = (0..native["body"].as_array().unwrap().len())
            .map(|i| format!("description_{i}"))
            .collect();
        Document::import(native, ids).unwrap()
    }
    #[test]
    fn actual_qualified_checkpoint_description_is_complete_and_does_not_rewrite_bytes() {
        let document = source();
        let bytes = document.checkpoint().unwrap();
        let description = WordCraftAdapter.describe_checkpoint(&bytes).unwrap();
        assert_eq!(
            description.format_version,
            crate::profile::CHECKPOINT_FORMAT
        );
        assert_eq!(description.versions, crate::profile::qualified_versions());
        assert!(description.assets.is_empty());
        assert_eq!(document.checkpoint().unwrap(), bytes);
        assert_eq!(
            Document::restore(&bytes).unwrap().checkpoint().unwrap(),
            bytes
        );
    }
    #[test]
    fn legacy_identity_is_not_inferred_from_current_qualified_profile() {
        let document = source();
        let legacy = crate::Checkpoint {
            format: crate::profile::LEGACY_CHECKPOINT_FORMAT.into(),
            native: document.native().unwrap(),
            block_ids: document.block_ids().unwrap(),
            update: document
                .doc
                .transact()
                .encode_state_as_update_v1(&StateVector::default()),
        };
        let bytes = serde_json::to_vec(&legacy).unwrap();
        assert!(Document::restore(&bytes).unwrap().is_read_only());
        assert!(matches!(
            WordCraftAdapter.describe_checkpoint(&bytes),
            Err(PrepareError::UnqualifiedDescription)
        ));
        assert_eq!(
            Document::restore(&bytes).unwrap().checkpoint().unwrap(),
            bytes
        );
    }
    #[test]
    fn unknown_format_and_mismatched_opaque_native_are_errors_not_empty_manifest() {
        let bytes = source().checkpoint().unwrap();
        for replacement in ["unknown-format", "native-substitution"] {
            let mut envelope: serde_json::Value = serde_json::from_slice(&bytes).unwrap();
            if replacement == "unknown-format" {
                envelope["format"] = serde_json::json!(replacement);
            } else {
                envelope["native"]["core"]["title"] = serde_json::json!(replacement);
            }
            let altered = serde_json::to_vec(&envelope).unwrap();
            assert!(matches!(
                WordCraftAdapter.describe_checkpoint(&altered),
                Err(PrepareError::State(_))
            ));
        }
    }
}

#[cfg(test)]
mod readonly_layout_tests {
    use super::*;
    use serde_json::{json, Value};
    #[test]
    fn qualified_known_section_layout_describes_without_granting_writes() {
        let mut native: Value =
            serde_json::from_str(include_str!("../tests/fixtures/d1-engine-canonical.json"))
                .unwrap();
        native["body"][0]["props"]["pageBreakBefore"] = json!(true);
        native["body"][0]["section"] = native["lastSection"].clone();
        let ids: Vec<_> = (0..native["body"].as_array().unwrap().len())
            .map(|i| format!("layout_{i}"))
            .collect();
        let document = Document::import(native.clone(), ids.clone()).unwrap();
        assert!(document.is_read_only());
        let cp = document.checkpoint().unwrap();
        let description = WordCraftAdapter.describe_checkpoint(&cp).unwrap();
        assert!(description.assets.is_empty());
        assert_eq!(
            description.format_version,
            crate::profile::CHECKPOINT_FORMAT
        );
        let restored = Document::restore(&cp).unwrap();
        assert!(restored.is_read_only());
        assert_eq!(restored.native().unwrap(), native);
        assert_eq!(restored.block_ids().unwrap(), ids);
        assert_eq!(restored.checkpoint().unwrap(), cp);
    }
}

#[cfg(test)]
mod d3_tests {
    use super::*;
    use serde_json::{json, Value};
    fn fixture() -> Value {
        serde_json::from_str(include_str!("../tests/fixtures/d3-engine-canonical.json")).unwrap()
    }
    fn imported(native: Value) -> Document {
        let ids = (0..native["body"].as_array().unwrap().len())
            .map(|i| format!("d3_section_{i}"))
            .collect();
        Document::import(native, ids).unwrap()
    }
    #[test]
    fn actual_engine_canonical_d3_complete_graph_paging_and_original_bytes_remain_readonly() {
        let native = fixture();
        let mut document = imported(native.clone());
        assert!(document.is_read_only());
        assert_eq!(native["body"].as_array().unwrap().len(), 43);
        let ids = document.block_ids().unwrap();
        let cp = document.checkpoint().unwrap();
        let description = WordCraftAdapter.describe_checkpoint(&cp).unwrap();
        assert!(description.assets.is_empty());
        assert_eq!(description.versions, crate::profile::qualified_versions());
        let mut after = None;
        let mut all = Vec::new();
        let mut pages = 0;
        let mut largest = 0;
        loop {
            let page = crate::read::page(&document, after.as_deref(), 5, 16384).unwrap();
            let bytes = serde_json::to_vec(&page).unwrap().len();
            assert!(bytes <= 16384);
            largest = largest.max(bytes);
            for paragraph in &page.paragraphs {
                assert_eq!(paragraph.target_id, ids[all.len()]);
                all.push(paragraph.native.clone());
            }
            pages += 1;
            match page.next_after {
                Some(cursor) => after = Some(cursor),
                None => break,
            }
        }
        assert_eq!(Value::Array(all), native["body"]);
        assert!(pages > 1);
        let header = crate::read::header(&document, 262144).unwrap();
        let mut expected_header = native.clone();
        expected_header.as_object_mut().unwrap().remove("body");
        assert_eq!(header, expected_header);
        let edit = crate::command::Command::Insert {
            target_id: ids[0].clone(),
            at: crate::command::Anchor::Start {},
            text: "forbidden".into(),
            properties: None,
        };
        assert_eq!(
            crate::candidate::apply(&mut document, &edit),
            Err(crate::candidate::CandidateError::ReadOnly)
        );
        let restored = Document::restore(&cp).unwrap();
        assert!(restored.is_read_only());
        assert_eq!(restored.native().unwrap(), native);
        assert_eq!(restored.block_ids().unwrap(), ids);
        assert_eq!(restored.checkpoint().unwrap(), cp);
        assert_eq!(document.checkpoint().unwrap(), cp);
        println!(
            "d3_descriptor_metrics native={} checkpoint={} pages={} largest_page={} header={}",
            serde_json::to_vec(&native).unwrap().len(),
            cp.len(),
            pages,
            largest,
            serde_json::to_vec(&header).unwrap().len()
        );
    }
    #[test]
    fn original_tree_unknown_section_references_and_modes_cannot_hide_in_layout_erasure() {
        let source = fixture();
        let section = source["body"]
            .as_array()
            .unwrap()
            .iter()
            .position(|p| p.get("section").is_some())
            .unwrap();
        for case in [
            "middle-header",
            "middle-footer",
            "unknown-section",
            "page-picture",
            "style-link",
            "review-mode",
            "numeric-drift",
        ] {
            let mut native = source.clone();
            match case {
                "middle-header" => {
                    native["body"][section]["section"]["headers"]["default"] =
                        json!("missing-header-part")
                }
                "middle-footer" => {
                    native["body"][section]["section"]["footers"]["even"] =
                        json!("missing-footer-part")
                }
                "unknown-section" => {
                    native["body"][section]["section"]["unqualifiedResource"] =
                        json!("https://example.invalid/resource")
                }
                "page-picture" => {
                    let mut typed: wordcraft_doc::Document =
                        serde_json::from_value(native).unwrap();
                    typed.settings.page_picture = Some(wordcraft_doc::PagePicture {
                        media: "unqualified-picture".into(),
                        ..Default::default()
                    });
                    native = serde_json::to_value(typed).unwrap();
                }
                "style-link" => {
                    native["styles"]["defaultChr"]["link"] =
                        json!("https://example.invalid/default-link")
                }
                "review-mode" => native["settings"]["trackChanges"] = json!(true),
                "numeric-drift" => {
                    native["styles"]["defaultPara"]["lineSpacing"]["value"] = json!(1.15)
                }
                _ => unreachable!(),
            }
            let document = imported(native.clone());
            let cp = document.checkpoint().unwrap();
            assert!(
                matches!(
                    WordCraftAdapter.describe_checkpoint(&cp),
                    Err(PrepareError::UnqualifiedDescription)
                ),
                "{case}"
            );
            let restored = Document::restore(&cp).unwrap();
            assert_eq!(restored.native().unwrap(), native, "{case}");
            assert_eq!(restored.checkpoint().unwrap(), cp, "{case}");
        }
    }
}

#[cfg(test)]
mod d2_tests {
    use super::*;
    #[test]
    fn known_actual_d2_resources_are_complete_readonly_without_rewriting() {
        let native: serde_json::Value =
            serde_json::from_str(include_str!("../tests/fixtures/d2-engine-canonical.json"))
                .unwrap();
        let ids: Vec<String> = (0..native["body"].as_array().unwrap().len())
            .map(|i| format!("d2_{i}"))
            .collect();
        let mut document = Document::import(native.clone(), ids.clone()).unwrap();
        assert!(document.is_read_only());
        let checkpoint = document.checkpoint().unwrap();
        let description = WordCraftAdapter.describe_checkpoint(&checkpoint).unwrap();
        assert!(description.assets.is_empty());
        let command = crate::command::Command::Insert {
            target_id: ids[0].clone(),
            at: crate::command::Anchor::Start {},
            text: "must remain unsaved".into(),
            properties: None,
        };
        assert_eq!(
            crate::candidate::apply(&mut document, &command),
            Err(crate::candidate::CandidateError::ReadOnly)
        );
        assert_eq!(document.checkpoint().unwrap(), checkpoint);
        let restored = Document::restore(&checkpoint).unwrap();
        assert!(restored.is_read_only());
        assert_eq!(restored.native().unwrap(), native);
        assert_eq!(restored.block_ids().unwrap(), ids);
        assert_eq!(restored.checkpoint().unwrap(), checkpoint);
    }
}
