//! Inactive local connector mechanics. Never a server command or admission API.
//! Full native equality and one immutable value per action/property are required.
use crate::{Document, CHARACTER_FLAGS};
use mooring_text::yrs::{
    types::Attrs, updates::decoder::Decode, Any, Doc, Map, Options, Out, ReadTxn, Text, Transact,
    Update,
};
use serde_json::Value;

const MAX_CHARS: usize = 65_536;
type Properties = [Option<bool>; 5];
type Patch = [Option<Option<bool>>; 5];
#[derive(Clone, Copy)]
struct Cell {
    ch: char,
    props: Properties,
}
#[derive(Debug, PartialEq, Eq)]
pub enum TransitionError {
    Unsupported,
    MismatchedBefore,
    NoChange,
    Limit,
    InvalidState,
}
struct Span {
    start: u32,
    len: u32,
    patch: Patch,
}
pub(crate) struct Plan {
    target: String,
    start: u32,
    removed: u32,
    inserted: String,
    insert_props: Properties,
    spans: Vec<Span>,
    after: Value,
}
/// Hash the complete typed request without binary64 normalization, integer
/// rounding or allocating a second full serialized document. Bound the writer.
pub(crate) fn identity(before: &Value, after: &Value) -> Result<[u8; 32], TransitionError> {
    use sha2::{Digest, Sha256};
    struct Writer {
        hash: Sha256,
        remaining: usize,
    }
    impl std::io::Write for Writer {
        fn write(&mut self, bytes: &[u8]) -> std::io::Result<usize> {
            self.remaining = self
                .remaining
                .checked_sub(bytes.len())
                .ok_or_else(|| std::io::Error::other("native request bound"))?;
            self.hash.update(bytes);
            Ok(bytes.len())
        }
        fn flush(&mut self) -> std::io::Result<()> {
            Ok(())
        }
    }
    let mut writer = Writer {
        hash: Sha256::new(),
        remaining: crate::MAX_NATIVE_BYTES * 2 + 3,
    };
    writer
        .hash
        .update(b"mooring.wordcraft.native-transition.v1\0");
    serde_json::to_writer(&mut writer, &[before, after]).map_err(|_| TransitionError::Limit)?;
    Ok(writer.hash.finalize().into())
}
fn cells(para: &Value) -> Result<Vec<Cell>, TransitionError> {
    let text = para["text"].as_str().ok_or(TransitionError::Unsupported)?;
    if text.chars().count() > MAX_CHARS {
        return Err(TransitionError::Limit);
    }
    let mut cells = Vec::new();
    let mut offset = 0_usize;
    for run in para["runs"]
        .as_array()
        .ok_or(TransitionError::Unsupported)?
    {
        let len = usize::try_from(run["len"].as_u64().ok_or(TransitionError::Unsupported)?)
            .map_err(|_| TransitionError::Limit)?;
        let end = offset.checked_add(len).ok_or(TransitionError::Limit)?;
        let value = text.get(offset..end).ok_or(TransitionError::Unsupported)?;
        let mut props = [None; 5];
        props[4] = Some(
            run.get("props")
                .is_some_and(|p| p.as_object().is_some_and(|p| p.is_empty())),
        );
        for (i, field) in CHARACTER_FLAGS.iter().enumerate() {
            props[i] = run
                .get("props")
                .and_then(|p| p.get(*field))
                .map(|v| v.as_bool().ok_or(TransitionError::Unsupported))
                .transpose()?;
        }
        cells.extend(value.chars().map(|ch| Cell { ch, props }));
        offset = end;
    }
    if offset != text.len() {
        return Err(TransitionError::Unsupported);
    }
    Ok(cells)
}
fn assign(writes: &mut Patch, patch: &Patch) -> Result<(), TransitionError> {
    for (prior, next) in writes.iter_mut().zip(patch) {
        if let Some(next) = next {
            if prior.is_some_and(|prior| prior != *next) {
                return Err(TransitionError::Unsupported);
            }
            *prior = Some(*next);
        }
    }
    Ok(())
}
impl Plan {
    pub(crate) fn e3_undoable(&self) -> bool {
        self.removed == 0
    }

    pub(crate) fn prepare(
        document: &Document,
        before: &Value,
        after: &Value,
    ) -> Result<Self, TransitionError> {
        Self::prepare_inner(document, before, after, None)
    }
    pub(crate) fn prepare_selected(
        document: &Document,
        before: &Value,
        after: &Value,
        target: &str,
        start_byte: usize,
        end_byte: usize,
    ) -> Result<Self, TransitionError> {
        Self::prepare_inner(
            document,
            before,
            after,
            Some((target, start_byte, end_byte)),
        )
    }
    fn prepare_inner(
        document: &Document,
        before: &Value,
        after: &Value,
        selection: Option<(&str, usize, usize)>,
    ) -> Result<Self, TransitionError> {
        if document.is_read_only() {
            return Err(TransitionError::Unsupported);
        }
        if document
            .native()
            .map_err(|_| TransitionError::InvalidState)?
            != *before
        {
            return Err(TransitionError::MismatchedBefore);
        }
        let ids = document
            .block_ids()
            .map_err(|_| TransitionError::InvalidState)?;
        let checked = Document::import(after.clone(), ids.clone())
            .map_err(|_| TransitionError::Unsupported)?;
        if checked.is_read_only() {
            return Err(TransitionError::Unsupported);
        }
        let mut before_meta = before.clone();
        let mut after_meta = after.clone();
        for native in [&mut before_meta, &mut after_meta] {
            for para in native["body"]
                .as_array_mut()
                .ok_or(TransitionError::Unsupported)?
            {
                let object = para.as_object_mut().ok_or(TransitionError::Unsupported)?;
                object.remove("text");
                object.remove("runs");
            }
        }
        if before_meta != after_meta {
            return Err(TransitionError::Unsupported);
        }
        let changed: Vec<_> = before["body"]
            .as_array()
            .unwrap()
            .iter()
            .zip(after["body"].as_array().unwrap())
            .enumerate()
            .filter(|(_, (a, b))| a != b)
            .collect();
        if changed.is_empty() {
            return Err(TransitionError::NoChange);
        }
        if changed.len() != 1 {
            return Err(TransitionError::Unsupported);
        }
        let (index, (before_para, after_para)) = changed[0];
        let old = cells(before_para)?;
        let new = cells(after_para)?;
        let selected = if let Some((target, start, end)) = selection {
            if target != ids[index] || end < start {
                return Err(TransitionError::Unsupported);
            }
            let text = before_para["text"]
                .as_str()
                .ok_or(TransitionError::Unsupported)?;
            let prefix = text.get(..start).ok_or(TransitionError::Unsupported)?;
            let suffix = text.get(end..).ok_or(TransitionError::Unsupported)?;
            let after_text = after_para["text"]
                .as_str()
                .ok_or(TransitionError::Unsupported)?;
            let middle_end = after_text
                .len()
                .checked_sub(suffix.len())
                .filter(|end| *end >= start)
                .ok_or(TransitionError::Unsupported)?;
            if !after_text.starts_with(prefix)
                || !after_text.ends_with(suffix)
                || after_text.get(start..middle_end).is_none()
            {
                return Err(TransitionError::Unsupported);
            }
            Some((
                prefix.chars().count(),
                text.get(..end).unwrap().chars().count(),
                after_text[..middle_end].chars().count(),
                crate::byte_to_utf16(text, start).map_err(|_| TransitionError::Unsupported)?,
                crate::byte_to_utf16(text, end).map_err(|_| TransitionError::Unsupported)?,
            ))
        } else {
            None
        };
        let text_changed = before_para["text"] != after_para["text"];
        let (prefix, old_end, new_end) =
            if let Some((prefix, old_end, new_end, _, _)) = selected.filter(|_| text_changed) {
                (prefix, old_end, new_end)
            } else {
                let prefix = old
                    .iter()
                    .zip(&new)
                    .take_while(|(a, b)| a.ch == b.ch)
                    .count();
                let full_suffix = old
                    .iter()
                    .rev()
                    .zip(new.iter().rev())
                    .take_while(|(a, b)| a.ch == b.ch)
                    .count();
                if old.len() != new.len() && prefix + full_suffix > old.len().min(new.len()) {
                    return Err(TransitionError::Unsupported);
                }
                let suffix = old[prefix..]
                    .iter()
                    .rev()
                    .zip(new[prefix..].iter().rev())
                    .take_while(|(a, b)| a.ch == b.ch)
                    .count();
                (prefix, old.len() - suffix, new.len() - suffix)
            };
        let start = old[..prefix].iter().map(|c| c.ch.len_utf16() as u32).sum();
        let removed = old[prefix..old_end]
            .iter()
            .map(|c| c.ch.len_utf16() as u32)
            .sum();
        let inserted: String = new[prefix..new_end].iter().map(|c| c.ch).collect();
        if inserted.len() > 65_536 {
            return Err(TransitionError::Limit);
        }
        let insert_props = new.get(prefix).map(|c| c.props).unwrap_or([None; 5]);
        if new[prefix..new_end].iter().any(|c| c.props != insert_props) {
            return Err(TransitionError::Unsupported);
        }
        let mut writes = [None; 5];
        if !inserted.is_empty() {
            assign(&mut writes, &insert_props.map(Some))?;
        }
        let mut spans: Vec<Span> = Vec::new();
        let mut offset = 0_u32;
        for (i, cell) in new.iter().enumerate() {
            let old_cell = if i < prefix {
                Some(old[i])
            } else if i >= new_end {
                Some(old[old_end + i - new_end])
            } else {
                None
            };
            let len = cell.ch.len_utf16() as u32;
            if let Some(old_cell) = old_cell {
                let patch = std::array::from_fn(|f| {
                    (old_cell.props[f] != cell.props[f]).then_some(cell.props[f])
                });
                if patch.iter().any(Option::is_some) {
                    if let Some((_, _, _, range_start, range_end)) = selected {
                        // A known native splice cannot silently format retained
                        // neighbors. Pure formatting is confined to selection.
                        if text_changed || offset < range_start || offset + len > range_end {
                            return Err(TransitionError::Unsupported);
                        }
                    }
                    assign(&mut writes, &patch)?;
                    if let Some(last) = spans
                        .last_mut()
                        .filter(|last| last.start + last.len == offset && last.patch == patch)
                    {
                        last.len += len;
                    } else {
                        spans.push(Span {
                            start: offset,
                            len,
                            patch,
                        });
                    }
                }
            }
            offset += len;
        }
        if inserted.is_empty() && removed == 0 && spans.is_empty() {
            return Err(TransitionError::Unsupported);
        }
        let plan = Self {
            target: ids[index].clone(),
            start,
            removed,
            inserted,
            insert_props,
            spans,
            after: after.clone(),
        };
        // Dry run on a disposable fork with the SAME local client identity.
        // This verifies complete native segmentation before any live mutation.
        let doc = Doc::with_options(Options {
            client_id: document.doc.client_id(),
            offset_kind: mooring_text::yrs::OffsetKind::Utf16,
            skip_gc: true,
            ..Options::default()
        });
        let bytes = document
            .doc
            .transact()
            .encode_state_as_update_v1(&mooring_text::yrs::StateVector::default());
        doc.transact_mut()
            .apply_update(Update::decode_v1(&bytes).map_err(|_| TransitionError::InvalidState)?)
            .map_err(|_| TransitionError::InvalidState)?;
        let mut fork = Document {
            doc,
            read_only: document.read_only,
            project_text: document.project_text,
            model: document.model,
        };
        let stamp = crate::format::current_agent_stamp(&document.doc)
            .map_err(|_| TransitionError::Limit)?;
        plan.apply(
            &mut fork,
            mooring_text::yrs::Origin::from(document.doc.client_id()),
            stamp,
        )?;
        Ok(plan)
    }
    pub(crate) fn apply(
        &self,
        document: &mut Document,
        origin: mooring_text::yrs::Origin,
        stamp: (u64, u64),
    ) -> Result<(), TransitionError> {
        let txn = document.doc.transact();
        let Some(Out::YText(text)) = txn
            .get_map("wordcraft.texts")
            .and_then(|map| map.get(&txn, &self.target))
        else {
            return Err(TransitionError::InvalidState);
        };
        drop(txn);
        let attrs = |patch: Patch| -> Attrs {
            patch
                .into_iter()
                .enumerate()
                .filter_map(|(i, value)| {
                    value.map(|value| {
                        let field = if i == 4 { "empty" } else { CHARACTER_FLAGS[i] };
                        (
                            format!("wc2:{field}:{}:{}", stamp.0, stamp.1).into(),
                            value
                                .map(Any::Bool)
                                .unwrap_or(Any::String("inherit".into())),
                        )
                    })
                })
                .collect()
        };
        {
            let mut txn = document.doc.transact_mut_with(origin);
            if self.removed > 0 {
                text.remove_range(&mut txn, self.start, self.removed);
            }
            if !self.inserted.is_empty() {
                text.insert_with_attributes(
                    &mut txn,
                    self.start,
                    &self.inserted,
                    attrs(self.insert_props.map(Some)),
                );
            }
            for span in &self.spans {
                text.format(&mut txn, span.start, span.len, attrs(span.patch));
            }
        }
        if document
            .native()
            .map_err(|_| TransitionError::InvalidState)?
            != self.after
        {
            return Err(TransitionError::Unsupported);
        }
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;
    fn native() -> Value {
        let mut value: Value =
            serde_json::from_str(include_str!("../tests/fixtures/d1-engine-canonical.json"))
                .unwrap();
        value["body"] = json!([{ "kind":"para", "text":"Č😀e\u{301}", "runs":[{"len":9}] }]);
        value
    }
    fn prepare_apply(before: Value, after: Value) -> Result<Value, TransitionError> {
        let mut doc = Document::import(before.clone(), vec!["p_one".into()]).unwrap();
        let plan = Plan::prepare(&doc, &before, &after)?;
        let stamp = crate::format::current_agent_stamp(&doc.doc).unwrap();
        plan.apply(&mut doc, mooring_text::yrs::Origin::from("test"), stamp)?;
        Ok(doc.native().unwrap())
    }
    #[test]
    fn actual_one_splice_handles_nonbmp_combining_and_explicit_false() {
        let before = native();
        let mut after = before.clone();
        after["body"][0]["text"] = json!("Č😀Xe\u{301}");
        after["body"][0]["runs"] =
            json!([{"len":6},{"len":1,"props":{"bold":false,"italicCs":true}},{"len":3}]);
        assert_eq!(prepare_apply(before, after.clone()).unwrap(), after);
    }
    #[test]
    fn formatting_and_deletion_preserve_full_native() {
        let before = native();
        let mut after = before.clone();
        after["body"][0]["runs"] =
            json!([{"len":2},{"len":4,"props":{"bold":true,"boldCs":true}},{"len":3}]);
        assert_eq!(prepare_apply(before.clone(), after.clone()).unwrap(), after);
        after = before.clone();
        after["body"][0]["text"] = json!("Če\u{301}");
        after["body"][0]["runs"] = json!([{"len":5}]);
        assert_eq!(prepare_apply(before, after.clone()).unwrap(), after);
    }
    #[test]
    fn repeated_glyph_splice_is_not_guessed_from_full_native_equality() {
        let mut before = native();
        before["body"][0]["text"] = json!("aa");
        before["body"][0]["runs"] = json!([{"len":2}]);
        let mut after = before.clone();
        after["body"][0]["text"] = json!("aaa");
        after["body"][0]["runs"] = json!([{"len":3}]);
        let doc = Document::import(before.clone(), vec!["p_one".into()]).unwrap();
        assert!(matches!(
            Plan::prepare(&doc, &before, &after),
            Err(TransitionError::Unsupported)
        ));
    }
    #[test]
    fn unrepresentable_native_run_segmentation_refuses_during_pure_preflight() {
        let before = native();
        let doc = Document::import(before.clone(), vec!["p_one".into()]).unwrap();
        let frontier = doc.state_vector();
        let mut after = before.clone();
        after["body"][0]["text"] = json!("Č😀e\u{301}X");
        after["body"][0]["runs"] = json!([{"len":2},{"len":8}]);
        assert!(matches!(
            Plan::prepare(&doc, &before, &after),
            Err(TransitionError::Unsupported)
        ));
        assert_eq!(doc.state_vector(), frontier);
        assert_eq!(doc.native().unwrap(), before);
    }
    #[test]
    fn structural_and_conflicting_property_intents_refuse_before_mutation() {
        let before = native();
        let mut after = before.clone();
        after["body"][0]["props"] = json!({"numbering":{"id":1}});
        assert!(prepare_apply(before.clone(), after).is_err());
        let mut after = before.clone();
        after["body"][0]["runs"] =
            json!([{"len":2,"props":{"bold":true}},{"len":7,"props":{"bold":false}}]);
        assert_eq!(
            prepare_apply(before, after),
            Err(TransitionError::Unsupported)
        );
    }
}
