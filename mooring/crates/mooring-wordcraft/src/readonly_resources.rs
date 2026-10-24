//! Closed, read-only D2 resource graph. Never changes writer classification.
use crate::{checkpoint_description::no_section_references, Document};
use serde_json::Value;
use std::collections::{BTreeMap, BTreeSet};
use wordcraft_doc::{Block, Blocks, InlineObject, PartKind, RevisionKind};

// This proves a closed internal byte/reference graph, NOT codec validity or
// render safety. CRC/zlib payload validation belongs to the pinned renderer.
const MAX_MEDIA: usize = 64;
const MAX_MEDIA_BYTES: usize = 64 * 1024;
const MAX_TOTAL_MEDIA: usize = 1024 * 1024;

pub(crate) fn qualified(document: &Document) -> bool {
    if document.model != crate::format::Model::V2 || !document.is_read_only() {
        return false;
    }
    let Ok(native) = document.native() else {
        return false;
    };
    // Before serde's byte-store allocation or a clone: bound the entire tree,
    // reject unknown review forms and preflight every encoded media allocation.
    if !scan(&native, 0, &mut 262_144)
        || native
            .get("passthrough")
            .is_some_and(|v| v.as_object().is_none_or(|m| !m.is_empty()))
        || !media_preflight(&native)
    {
        return false;
    }
    let Ok(mut typed) = serde_json::from_value::<wordcraft_doc::Document>(native.clone()) else {
        return false;
    };
    typed.normalize_runs();
    typed.ensure_nonempty();
    if serde_json::to_value(&typed).ok().as_ref() != Some(&native) {
        return false; // No numeric tolerance, dropped fields or rewritten source.
    }
    if typed.settings.attached_template.is_some()
        || typed.settings.link_styles
        || typed.settings.page_picture.is_some()
        || typed.settings.watermark.is_some()
        || typed.settings.track_changes
        || typed.settings.protection.is_some()
        || typed.settings.protect_formatting
        || !typed.settings.protect_hash.is_empty()
        || typed.styles.latent_styles.is_some()
        || !typed.passthrough.is_empty()
        || !typed.glossary.is_empty()
        || !typed.sources.is_empty()
        || !typed.custom_props.is_empty()
        || !typed.footnote_separators.is_empty()
        || !typed.endnote_separators.is_empty()
        || !no_section_references(&typed.last_section)
        || typed.comments.len() > 128
        || typed.parts.len() != typed.comments.len()
        || typed.revisions.len() > 128
        || typed
            .revisions
            .iter()
            .any(|r| r.kind != RevisionKind::Insert || r.move_name.is_some())
    {
        return false;
    }
    // All insertion annotations throughout retained metadata resolve to the
    // actual insertion table. Authors/dates are content, never authentication.
    if !revision_refs(&native, typed.revisions.len()) {
        return false;
    }
    let mut graph = Graph::default();
    if !graph.blocks(&typed.body, 0, true) {
        return false;
    }
    let mut parts = BTreeSet::new();
    for (id, comment) in &typed.comments {
        let Some(part) = typed.parts.get(&comment.part) else {
            return false;
        };
        if comment.parent.is_some()
            || !parts.insert(comment.part)
            || part.kind != PartKind::Comment
            || !part.body.is_default()
            || graph.comments.get(id) != Some(&(1, 1))
            || !graph.blocks(&part.blocks, 0, false)
        {
            return false;
        }
    }
    graph.comments.len() == typed.comments.len()
        && graph.images.len() == typed.media.len()
        && graph.images.iter().all(|key| typed.media.contains_key(key))
}

fn media_preflight(native: &Value) -> bool {
    let Some(media) = native.get("media") else {
        return true;
    };
    let Some(media) = media.as_object() else {
        return false;
    };
    if media.len() > MAX_MEDIA {
        return false;
    }
    let mut total = 0_usize;
    let mut pixels = 0_u64;
    for (key, encoded) in media {
        let Some(encoded) = encoded.as_str() else {
            return false;
        };
        if key.is_empty()
            || key.len() > 128
            || !key
                .bytes()
                .all(|b| b.is_ascii_alphanumeric() || b"._-".contains(&b))
            || !key.ends_with(".png")
            || encoded.len() % 4 != 0
        {
            return false;
        }
        // Compute decoded length and aggregate budget BEFORE allocating bytes.
        // Canonical alphabet/padbits are checked by the bounded shared decoder.
        let padding = encoded
            .as_bytes()
            .iter()
            .rev()
            .take_while(|b| **b == b'=')
            .count();
        if padding > 2 {
            return false;
        }
        let Some(length) = (encoded.len() / 4)
            .checked_mul(3)
            .and_then(|n| n.checked_sub(padding))
        else {
            return false;
        };
        let Some(next) = total.checked_add(length) else {
            return false;
        };
        if length > MAX_MEDIA_BYTES || next > MAX_TOTAL_MEDIA {
            return false;
        }
        total = next;
        let Ok(bytes) = mooring_protocol::boundary::decode_base64(encoded, MAX_MEDIA_BYTES) else {
            return false;
        };
        let Some(area) = png_graph(&bytes) else {
            return false;
        };
        let Some(next) = pixels.checked_add(area) else {
            return false;
        };
        if next > 1024 * 1024 {
            return false;
        }
        pixels = next;
    }
    true
}

// A deliberately closed, reusable non-interlaced 8-bit PNG container profile.
// No ancillary chunks, external references, palette, animation or trailing data.
// All chunk bytes including CRC are retained; compressed payload is opaque.
fn png_graph(bytes: &[u8]) -> Option<u64> {
    if bytes.get(..8)? != b"\x89PNG\r\n\x1a\n" {
        return None;
    }
    let mut offset = 8_usize;
    let mut chunks = 0;
    let mut area = None;
    let mut data = false;
    while offset < bytes.len() {
        chunks += 1;
        if chunks > 256 {
            return None;
        }
        let length = u32::from_be_bytes(bytes.get(offset..offset.checked_add(4)?)?.try_into().ok()?)
            as usize;
        let end = offset.checked_add(12)?.checked_add(length)?;
        if end > bytes.len() {
            return None;
        }
        let kind = bytes.get(offset + 4..offset + 8)?;
        let payload = bytes.get(offset + 8..end - 4)?;
        match kind {
            b"IHDR" if chunks == 1 && length == 13 => {
                let width = u32::from_be_bytes(payload[..4].try_into().ok()?);
                let height = u32::from_be_bytes(payload[4..8].try_into().ok()?);
                if width == 0
                    || height == 0
                    || width > 1024
                    || height > 1024
                    || payload[8] != 8
                    || ![0, 2, 4, 6].contains(&payload[9])
                    || payload[10..13] != [0, 0, 0]
                {
                    return None;
                }
                area = Some(u64::from(width) * u64::from(height));
            }
            b"IDAT" if area.is_some() && length > 0 => {
                data = true;
            }
            b"IEND" if area.is_some() && data && length == 0 && end == bytes.len() => return area,
            _ => return None,
        }
        offset = end;
    }
    None
}

fn scan(value: &Value, depth: usize, remaining: &mut usize) -> bool {
    if depth > 32 || *remaining == 0 {
        return false;
    }
    *remaining -= 1;
    match value {
        Value::Object(fields) => fields.iter().all(|(key, value)| {
            (!matches!(
                key.as_str(),
                "link" | "latentStyles" | "attachedTemplate" | "fmtChange" | "numChange" | "del"
            ) || value.is_null())
                && scan(value, depth + 1, remaining)
        }),
        Value::Array(values) => values.iter().all(|v| scan(v, depth + 1, remaining)),
        _ => true,
    }
}
fn revision_refs(value: &Value, revisions: usize) -> bool {
    // Depth/work already checked before this traversal.
    match value {
        Value::Object(fields) => fields.iter().all(|(key, value)| {
            (key != "ins"
                || value.is_null()
                || value.as_u64().is_some_and(|i| i < revisions as u64))
                && revision_refs(value, revisions)
        }),
        Value::Array(values) => values.iter().all(|v| revision_refs(v, revisions)),
        _ => true,
    }
}
#[derive(Default)]
struct Graph {
    work: usize,
    cells: usize,
    edges: usize,
    images: BTreeSet<String>,
    comments: BTreeMap<u32, (usize, usize)>,
}
impl Graph {
    fn blocks(&mut self, blocks: &Blocks, depth: usize, body: bool) -> bool {
        if depth > 8 || blocks.is_empty() {
            return false;
        }
        for block in blocks {
            self.work += 1;
            if self.work > 4096 {
                return false;
            }
            match &**block {
                Block::Table(table) if body => {
                    if table.rows.is_empty() || table.rows.len() > 256 {
                        return false;
                    }
                    for row in &table.rows {
                        if row.cells.is_empty() || row.cells.len() > 256 {
                            return false;
                        }
                        for cell in &row.cells {
                            self.cells += 1;
                            if self.cells > 4096 || !self.blocks(&cell.blocks, depth + 1, true) {
                                return false;
                            }
                        }
                    }
                }
                Block::Para(paragraph) => {
                    if paragraph
                        .section
                        .as_ref()
                        .is_some_and(|s| !no_section_references(s))
                        || paragraph.text.chars().filter(|c| *c == '\u{fffc}').count()
                            != paragraph.objects.len()
                        || (!body && !paragraph.objects.is_empty())
                    {
                        return false;
                    }
                    for object in &paragraph.objects {
                        self.edges += 1;
                        if self.edges > 1024 {
                            return false;
                        }
                        match object {
                            InlineObject::Image {
                                media, ole: None, ..
                            } => {
                                self.images.insert(media.clone());
                            }
                            InlineObject::CommentStart { id } => {
                                self.comments.entry(*id).or_default().0 += 1;
                            }
                            InlineObject::CommentEnd { id } => {
                                self.comments.entry(*id).or_default().1 += 1;
                            }
                            _ => return false,
                        }
                    }
                }
                _ => return false,
            }
        }
        true
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::adapter::{PrepareError, WordCraftAdapter};
    use mooring_core::checkpoint_description::DescribeCheckpoint;
    use serde_json::json;
    fn fixture() -> Value {
        serde_json::from_str(include_str!("../tests/fixtures/d2-engine-canonical.json")).unwrap()
    }
    fn document(native: Value) -> Document {
        let ids = (0..native["body"].as_array().unwrap().len())
            .map(|i| format!("d2_graph_{i}"))
            .collect();
        Document::import(native, ids).unwrap()
    }
    fn denied(native: Value) {
        let document = document(native.clone());
        let checkpoint = document.checkpoint().unwrap();
        assert!(matches!(
            WordCraftAdapter.describe_checkpoint(&checkpoint),
            Err(PrepareError::UnqualifiedDescription)
        ));
        let restored = Document::restore(&checkpoint).unwrap();
        assert_eq!(restored.native().unwrap(), native);
        assert_eq!(restored.checkpoint().unwrap(), checkpoint);
        assert!(restored.is_read_only());
    }
    #[test]
    fn every_internal_edge_and_all_original_metadata_must_qualify() {
        let changes: [fn(&mut Value); 14] = [
            |v| v["body"][8]["objects"][0]["media"] = json!("https://example.invalid/image.png"),
            |v| {
                v["media"].as_object_mut().unwrap().clear();
            },
            |v| v["media"]["unused.png"] = v["media"]["image1.png"].clone(),
            |v| v["comments"]["0"]["part"] = json!(999),
            |v| v["body"][1]["objects"][0]["id"] = json!(999),
            |v| v["comments"]["0"]["parent"] = json!(999),
            |v| v["parts"]["1"]["body"] = json!({"next":999}),
            |v| v["parts"]["1"]["kind"] = json!("header"),
            |v| v["body"][11]["runs"][0]["props"]["ins"] = json!(999),
            |v| v["revisions"][0]["move_name"] = json!("unqualified-move"),
            |v| v["lastSection"]["headers"]["default"] = json!(999),
            |v| v["styles"]["defaultChr"]["link"] = json!("https://example.invalid/"),
            |v| v["core"]["unknownResource"] = json!("external"),
            |v| v["styles"]["defaultPara"]["lineSpacing"]["value"] = json!(1.15),
        ];
        for change in changes {
            let mut v = fixture();
            change(&mut v);
            denied(v);
        }
    }
    #[test]
    fn typed_actual_extra_parts_ole_and_review_forms_are_not_hidden_by_projection() {
        let original = fixture();
        let mut native = original.clone();
        native["parts"]["99"] = native["parts"]["1"].clone();
        denied(native);
        let mut native = original.clone();
        native["body"][11]["runs"][0]["props"]["del"] = json!(0);
        denied(native);
        let mut native = original;
        // Valid raw OLE structure need not be decoded: any nonnull opaque form
        // fails full typed qualification or the closed object-kind graph.
        native["body"][8]["objects"][0]["ole"] = json!({"url":"https://example.invalid"});
        denied(native);
    }
    #[test]
    fn canonical_base64_and_aggregate_media_limits_precede_typed_allocation() {
        let mut v = fixture();
        v["media"]["image1.png"] = json!("A".repeat(4 * (MAX_MEDIA_BYTES / 3 + 2)));
        assert!(!media_preflight(&v));
        v["media"]["image1.png"] = json!("AB=="); // nonzero padbits
        assert!(!media_preflight(&v));
        let mut v = fixture();
        let pixel = v["media"]["image1.png"].clone();
        for i in 0..=MAX_MEDIA {
            v["media"][format!("image{i}.png")] = pixel.clone();
        }
        assert!(!media_preflight(&v));
        let mut v = fixture();
        v["media"]["../image.png"] = pixel;
        assert!(!media_preflight(&v));
    }
    #[test]
    fn png_profile_is_generic_bounded_and_does_not_claim_crc_or_codec_safety() {
        let native = fixture();
        let bytes = mooring_protocol::boundary::decode_base64(
            native["media"]["image1.png"].as_str().unwrap(),
            MAX_MEDIA_BYTES,
        )
        .unwrap();
        assert_eq!(png_graph(&bytes), Some(1));
        let mut other = bytes.clone();
        other[19] = 2; // width=2, different PNG bytes; CRC deliberately unqualified
        assert_eq!(png_graph(&other), Some(2));
        let mut v = fixture();
        v["media"]["image1.png"] =
            json!(mooring_protocol::boundary::encode_base64(&other, MAX_MEDIA_BYTES).unwrap());
        assert!(qualified(&document(v)));
        let mut oversized = bytes.clone();
        oversized[16..20].copy_from_slice(&1025_u32.to_be_bytes());
        assert_eq!(png_graph(&oversized), None);
        for end in 0..bytes.len() {
            assert_eq!(png_graph(&bytes[..end]), None);
        }
        for chunk in [b"acTL", b"tEXt", b"iTXt", b"eXIf", b"zzZZ"] {
            let mut unknown = bytes.clone();
            unknown[37..41].copy_from_slice(chunk);
            assert_eq!(png_graph(&unknown), None);
        }
        let mut trailing = bytes.clone();
        trailing.push(0);
        assert_eq!(png_graph(&trailing), None);
        let mut huge = bytes.clone();
        huge[33..37].copy_from_slice(&u32::MAX.to_be_bytes());
        assert_eq!(png_graph(&huge), None);
    }
    #[test]
    fn work_depth_and_annotation_bounds_fail_closed() {
        let mut deep = Value::Null;
        for _ in 0..34 {
            deep = json!([deep]);
        }
        assert!(!scan(&deep, 0, &mut 100));
        assert!(!scan(&json!([0, 1, 2]), 0, &mut 3));
        assert!(!scan(&json!({"fmtChange":{}}), 0, &mut 100));
        let mut native = fixture();
        native["body"][1]["text"] = json!(native["body"][1]["text"]
            .as_str()
            .unwrap()
            .replace('\u{fffc}', "abc"));
        // Classification itself may refuse malformed run lengths; this pure
        // graph probe checks that lost object positioning is never qualified.
        assert!(!qualified(&document(native)));
    }
}
