//! Local candidate mechanics only. No operation admission, read-context
//! qualification, author identity, durable outcome or authoritative mutation.
use crate::{
    command::{self, Anchor, Association, Command},
    Document,
};
use mooring_text::yrs::{
    types::Attrs, Any, Assoc, ClientID, GetString, Map, Out, ReadTxn, StickyIndex, Text, TextRef,
    Transact, ID,
};

#[derive(Debug, PartialEq, Eq)]
pub(crate) enum CandidateError {
    InvalidCommand,
    ReadOnly,
    MissingTarget,
    UnresolvedAnchor,
    InvalidRange,
    InvalidState,
}

// Internal only until the host-context adapter can qualify every observation.
pub(crate) fn apply(document: &mut Document, command: &Command) -> Result<(), CandidateError> {
    command::validate(command).map_err(|_| CandidateError::InvalidCommand)?;
    if document.is_read_only() {
        return Err(CandidateError::ReadOnly);
    }
    let target = match command {
        Command::Insert { target_id, .. }
        | Command::Delete { target_id, .. }
        | Command::Format { target_id, .. } => target_id,
    };
    if !document
        .block_ids()
        .map_err(|_| CandidateError::InvalidState)?
        .contains(target)
    {
        return Err(CandidateError::MissingTarget);
    }
    let txn = document.doc.transact();
    let texts = txn
        .get_map("wordcraft.texts")
        .ok_or(CandidateError::InvalidState)?;
    let Some(Out::YText(text)) = texts.get(&txn, target) else {
        return Err(CandidateError::InvalidState);
    };
    let source = text.get_string(&txn);
    let resolve = |anchor| resolve(&txn, &text, &source, anchor);
    let (start, end) = match command {
        Command::Insert { at, .. } => {
            let at = resolve(at)?;
            (at, at)
        }
        Command::Delete { start, end, .. } | Command::Format { start, end, .. } => {
            (resolve(start)?, resolve(end)?)
        }
    };
    if end < start || (!matches!(command, Command::Insert { .. }) && start == end) {
        return Err(CandidateError::InvalidRange);
    }
    drop(txn);
    let mut txn = document.doc.transact_mut();
    match command {
        Command::Insert {
            text: value,
            properties,
            ..
        } => {
            if let Some(properties) = properties {
                let mut attrs = Attrs::new();
                attrs.insert(crate::PROPS_PRESENT.into(), Any::Bool(true));
                for (key, value) in properties {
                    attrs.insert(
                        format!("{}{key}", crate::CHARACTER_PREFIX).into(),
                        Any::Bool(*value),
                    );
                }
                text.insert_with_attributes(&mut txn, start, value, attrs);
            } else {
                text.insert(&mut txn, start, value);
            }
        }
        Command::Delete { .. } => text.remove_range(&mut txn, start, end - start),
        Command::Format { properties, .. } => {
            let mut attrs = Attrs::new();
            // Clearing a property on text without overrides must not invent an
            // explicit empty properties object that changes inheritance.
            if properties.values().any(Option::is_some) {
                attrs.insert(crate::PROPS_PRESENT.into(), Any::Bool(true));
            }
            for (key, value) in properties {
                attrs.insert(
                    format!("{}{key}", crate::CHARACTER_PREFIX).into(),
                    value.map(Any::Bool).unwrap_or(Any::Null),
                );
            }
            text.format(&mut txn, start, end - start, attrs);
        }
    }
    Ok(())
}

fn resolve(
    txn: &impl ReadTxn,
    text: &TextRef,
    source: &str,
    anchor: &Anchor,
) -> Result<u32, CandidateError> {
    let index = match anchor {
        Anchor::Start {} => 0,
        Anchor::End {} => text.len(txn),
        Anchor::Relative {
            client_id,
            clock,
            assoc,
        } => {
            let assoc = match assoc {
                Association::Before => Assoc::Before,
                Association::After => Assoc::After,
            };
            let sticky =
                StickyIndex::from_id(ID::new(ClientID::new(client_id.0), clock.0 as u32), assoc);
            let offset = sticky
                .get_offset(txn)
                .ok_or(CandidateError::UnresolvedAnchor)?;
            if offset.branch.id()
                != <TextRef as AsRef<mooring_text::yrs::branch::Branch>>::as_ref(text).id()
            {
                return Err(CandidateError::UnresolvedAnchor);
            }
            offset.index
        }
    };
    crate::utf16_to_byte(source, index).map_err(|_| CandidateError::UnresolvedAnchor)?;
    Ok(index)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::command::{decode, Anchor, Association};
    use mooring_text::yrs::{Assoc, IndexedSequence, Map, Out, ReadTxn, Transact};
    use serde_json::{json, Value};
    fn document() -> Document {
        let mut native: Value =
            serde_json::from_str(include_str!("../tests/fixtures/d1-native.json")).unwrap();
        native["body"] = json!([
            {"kind":"para","text":"Č😀e\u{301}","runs":[{"len":9}]},
            {"kind":"para","text":"other","runs":[{"len":5}]}
        ]);
        Document::import(native, vec!["p_one".into(), "p_two".into()]).unwrap()
    }
    fn command(value: Value) -> Command {
        decode(value.as_object().unwrap()).unwrap()
    }
    fn anchor(doc: &Document, target: &str, index: u32) -> Anchor {
        let txn = doc.doc.transact();
        let Some(Out::YText(text)) = txn.get_map("wordcraft.texts").unwrap().get(&txn, target)
        else {
            panic!("text missing")
        };
        let sticky = text.sticky_index(&txn, index, Assoc::Before).unwrap();
        let encoded = serde_json::to_value(sticky).unwrap();
        // Yrs uses the structural item ID; our wire grammar keeps both counters
        // as lossless strings rather than copying its numeric JSON encoding.
        let id = &encoded["item"];
        Anchor::Relative {
            client_id: mooring_protocol::Counter(id["client"].as_u64().unwrap()),
            clock: mooring_protocol::Counter(id["clock"].as_u64().unwrap()),
            assoc: Association::Before,
        }
    }
    #[test]
    fn confirmed_relative_command_survives_checkpoint_and_preserves_full_native() {
        let original = document();
        let at = anchor(&original, "p_one", 3);
        let checkpoint = original.checkpoint().unwrap();
        let mut candidate = Document::restore(&checkpoint).unwrap();
        apply(
            &mut candidate,
            &Command::Insert {
                target_id: "p_one".into(),
                at,
                text: "X".into(),
                properties: Some(Default::default()),
            },
        )
        .unwrap();
        let result = candidate.native().unwrap();
        assert_eq!(result["body"][0]["text"], "Č😀Xe\u{301}");
        let mut expected = original.native().unwrap();
        expected["body"][0]["text"] = json!("Č😀Xe\u{301}");
        expected["body"][0]["runs"] = json!([
            {"len":6}, {"len":1,"props":{}}, {"len":3}
        ]);
        assert_eq!(result, expected);
        assert_eq!(
            Document::restore(&candidate.checkpoint().unwrap())
                .unwrap()
                .native()
                .unwrap(),
            result
        );
        assert_eq!(original.checkpoint().unwrap(), checkpoint);
    }
    #[test]
    fn format_flags_false_and_clear_are_distinct_and_preserve_other_fields() {
        let mut candidate = document();
        apply(&mut candidate,&command(json!({"kind":"text.format","target_id":"p_one","start":{"kind":"start"},"end":{"kind":"end"},"properties":{"bold":false,"italicCs":true}}))).unwrap();
        assert_eq!(
            candidate.native().unwrap()["body"][0]["runs"],
            json!([{"len":9,"props":{"bold":false,"italicCs":true}}])
        );
        apply(&mut candidate,&command(json!({"kind":"text.format","target_id":"p_one","start":{"kind":"start"},"end":{"kind":"end"},"properties":{"bold":null}}))).unwrap();
        assert_eq!(
            candidate.native().unwrap()["body"][0]["runs"],
            json!([{"len":9,"props":{"italicCs":true}}])
        );
        apply(&mut candidate,&command(json!({"kind":"text.delete","target_id":"p_one","start":{"kind":"start"},"end":{"kind":"end"}}))).unwrap();
        assert_eq!(candidate.native().unwrap()["body"][0]["text"], "");
        assert_eq!(candidate.native().unwrap()["body"][0]["runs"], json!([]));
    }
    #[test]
    fn wrong_paragraph_anchor_and_surrogate_interior_are_rejected_before_edit() {
        let mut doc = document();
        let checkpoint = doc.checkpoint().unwrap();
        for at in [anchor(&doc, "p_two", 1), anchor(&doc, "p_one", 2)] {
            assert_eq!(
                apply(
                    &mut doc,
                    &Command::Insert {
                        target_id: "p_one".into(),
                        at,
                        text: "x".into(),
                        properties: None
                    }
                ),
                Err(CandidateError::UnresolvedAnchor)
            );
            assert_eq!(doc.checkpoint().unwrap(), checkpoint);
        }
    }
    #[test]
    fn local_candidate_errors_leave_the_document_unchanged() {
        let mut doc = document();
        let original = doc.checkpoint().unwrap();
        for (cmd, expected) in [
            (
                Command::Insert {
                    target_id: "missing".into(),
                    at: Anchor::Start {},
                    text: "x".into(),
                    properties: None,
                },
                CandidateError::MissingTarget,
            ),
            (
                Command::Insert {
                    target_id: "p_one".into(),
                    at: Anchor::Relative {
                        client_id: mooring_protocol::Counter(1 << 53),
                        clock: mooring_protocol::Counter(0),
                        assoc: Association::After,
                    },
                    text: "x".into(),
                    properties: None,
                },
                CandidateError::InvalidCommand,
            ),
            (
                Command::Delete {
                    target_id: "p_one".into(),
                    start: Anchor::End {},
                    end: Anchor::Start {},
                },
                CandidateError::InvalidRange,
            ),
            (
                Command::Delete {
                    target_id: "p_one".into(),
                    start: Anchor::Start {},
                    end: Anchor::Start {},
                },
                CandidateError::InvalidRange,
            ),
            (
                Command::Insert {
                    target_id: "p_one".into(),
                    at: Anchor::Relative {
                        client_id: mooring_protocol::Counter(0),
                        clock: mooring_protocol::Counter(u32::MAX.into()),
                        assoc: Association::After,
                    },
                    text: "x".into(),
                    properties: None,
                },
                CandidateError::UnresolvedAnchor,
            ),
        ] {
            assert_eq!(apply(&mut doc, &cmd), Err(expected));
            assert_eq!(doc.checkpoint().unwrap(), original);
        }
        let mut native = doc.native().unwrap();
        native["body"][0]["inlineObject"] = json!({"kind":"unsupported"});
        let mut readonly = Document::import(native, vec!["p_one".into(), "p_two".into()]).unwrap();
        let before = readonly.checkpoint().unwrap();
        assert_eq!(
            apply(
                &mut readonly,
                &command(
                    json!({"kind":"text.insert","target_id":"p_one","at":{"kind":"start"},"text":"x"})
                )
            ),
            Err(CandidateError::ReadOnly)
        );
        assert_eq!(readonly.checkpoint().unwrap(), before);
    }
    #[test]
    fn clearing_an_absent_override_does_not_invent_explicit_empty_properties() {
        let mut doc = document();
        let original = doc.native().unwrap();
        apply(&mut doc,&command(json!({"kind":"text.format","target_id":"p_one","start":{"kind":"start"},"end":{"kind":"end"},"properties":{"bold":null}}))).unwrap();
        assert_eq!(doc.native().unwrap(), original);
    }
    #[test]
    fn relative_agent_anchor_tracks_confirmed_prefix_instead_of_replaying_absolute_offset() {
        let mut doc = document();
        let at = anchor(&doc, "p_one", 3);
        apply(&mut doc,&command(json!({"kind":"text.insert","target_id":"p_one","at":{"kind":"start"},"text":"prefix "}))).unwrap();
        apply(
            &mut doc,
            &Command::Insert {
                target_id: "p_one".into(),
                at,
                text: "X".into(),
                properties: None,
            },
        )
        .unwrap();
        assert_eq!(
            doc.native().unwrap()["body"][0]["text"],
            "prefix Č😀Xe\u{301}"
        );
    }
}
