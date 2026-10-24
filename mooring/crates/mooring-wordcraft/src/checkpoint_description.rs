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
        if !closed_self_contained(&document) {
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
    if document.is_read_only() || document.model != crate::format::Model::V2 {
        return false;
    }
    let Ok(native) = document.native() else {
        return false;
    };
    let Ok(typed) = serde_json::from_value::<wordcraft_doc::Document>(native.clone()) else {
        return false;
    };
    // This first descriptor proves ONLY the closed native paragraph subset.
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
        && [typed.last_section.headers, typed.last_section.footers]
            .iter()
            .all(|refs| refs.default.is_none() && refs.first.is_none() && refs.even.is_none())
        && no_unqualified_links(&native, 0, &mut 262144)
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
