//! Exact qualified native CoreProps vocabulary; unknown encodings stay opaque.
use serde_json::Value;
// WordCraft668d7ce doc::CoreProps: missing fields acquire native defaults,
// unknown fields are dropped, and revision is u32. Neither roundtrip is exact.
pub(crate) fn supported(value: &Value) -> bool {
    const STRINGS: [&str; 9] = [
        "title",
        "subject",
        "creator",
        "keywords",
        "description",
        "category",
        "lastModifiedBy",
        "created",
        "modified",
    ];
    let Some(core) = value.as_object() else {
        return false;
    };
    core.len() == 10
        && STRINGS
            .iter()
            .all(|field| core.get(*field).is_some_and(Value::is_string))
        && core
            .get("revision")
            .and_then(Value::as_u64)
            .is_some_and(|v| v <= u32::MAX as u64)
}
#[cfg(test)]
mod tests {
    use crate::{
        experimental_draft::{ClientError, DraftAccess, DraftReplica},
        Document,
    };
    use mooring_text::yrs::{Any, Map, ReadTxn, StateVector, Transact};
    use serde_json::{json, Value};
    fn source() -> Value {
        serde_json::from_str(include_str!("../tests/fixtures/d1-engine-canonical.json")).unwrap()
    }
    fn ids(v: &Value) -> Vec<String> {
        (0..v["body"].as_array().unwrap().len())
            .map(|i| format!("core_{i}"))
            .collect()
    }
    #[test]
    fn unsupported_native_core_fields_preserve_opaque_source_readonly() {
        for edit in [0, 1, 2, 3] {
            let mut native = source();
            match edit {
                0 => native["core"]["opaqueConnectorInteger"] = json!(9007199254740993_u64),
                1 => {
                    native["core"].as_object_mut().unwrap().remove("title");
                }
                2 => native["core"]["title"] = json!(true),
                _ => native["core"]["revision"] = json!(4294967296_u64),
            }
            let doc = Document::import(native.clone(), ids(&native)).unwrap();
            assert!(
                doc.is_read_only(),
                "unqualified core case {edit} was writable"
            );
            assert_eq!(doc.native().unwrap(), native);
            let cp = doc.checkpoint().unwrap();
            let restored = Document::restore(&cp).unwrap();
            assert!(restored.is_read_only());
            assert_eq!(restored.native().unwrap(), native);
            let client = DraftReplica::restore(&cp, 17, DraftAccess::Writer).unwrap();
            assert_eq!(client.require_native_access(), Err(ClientError::Forbidden));
        }
    }
    #[test]
    fn old_projected_core_checkpoint_restores_readonly_without_rewrite() {
        // Construct the exact earlier projected encoding: all paragraph text
        // remains in its actual Y.Text identities, not copied/reimported text.
        let mut native = source();
        let stable = ids(&native);
        let doc = Document::import(native.clone(), stable.clone()).unwrap();
        native["core"]["opaqueConnectorInteger"] = json!(9007199254740993_u64);
        let mut header = native.clone();
        header.as_object_mut().unwrap().remove("body");
        let metadata = doc.doc.get_or_insert_map("wordcraft.metadata");
        metadata.insert(&mut doc.doc.transact_mut(), "document", header.to_string());
        let frontier = doc.state_vector();
        let cp = doc.checkpoint().unwrap();
        let restored = Document::restore(&cp).unwrap();
        assert!(
            restored.is_read_only(),
            "earlier core encoding remained writable"
        );
        assert_eq!(restored.native().unwrap(), native);
        assert_eq!(restored.block_ids().unwrap(), stable);
        assert_eq!(restored.state_vector(), frontier);
        assert_eq!(restored.checkpoint().unwrap(), cp);
        let writer = crate::experimental_human_current::HostWriter::current(&cp, 17).unwrap();
        assert!(matches!(
            crate::experimental_human_current::prepare_current(
                crate::experimental_human_current::Authority::Writer(writer),
                &frontier,
                &[255],
                &[]
            ),
            Err(crate::experimental_human_current::Error::UnsupportedMutation)
        ));
    }
    #[test]
    fn readonly_compatibility_never_invents_missing_partial_or_foreign_roots() {
        for case in 0..4 {
            let mut native = source();
            let stable = ids(&native);
            let doc = Document::import(native.clone(), stable.clone()).unwrap();
            native["core"]["unknown"] = json!(true);
            let mut header = native.clone();
            header.as_object_mut().unwrap().remove("body");
            doc.doc.get_or_insert_map("wordcraft.metadata").insert(
                &mut doc.doc.transact_mut(),
                "document",
                header.to_string(),
            );
            let mut stored: Value = serde_json::from_slice(&doc.checkpoint().unwrap()).unwrap();
            let texts = doc.doc.get_or_insert_map("wordcraft.texts");
            {
                let mut txn = doc.doc.transact_mut();
                match case {
                    0 => {
                        for id in &stable {
                            texts.remove(&mut txn, id);
                        }
                    }
                    1 => {
                        texts.remove(&mut txn, &stable[0]);
                    }
                    2 => {
                        texts.insert(
                            &mut txn,
                            stable[0].as_str(),
                            Any::String("not a shared text".into()),
                        );
                    }
                    _ => {}
                }
            }
            if case == 3 {
                doc.doc.get_or_insert_map("foreign.hidden").insert(
                    &mut doc.doc.transact_mut(),
                    "hidden",
                    "not projected",
                );
            }
            let update = doc
                .doc
                .transact()
                .encode_state_as_update_v1(&StateVector::default());
            stored["update_base64"] = json!(mooring_protocol::boundary::encode_base64(
                &update,
                crate::MAX_CRDT_UPDATE_BYTES
            )
            .unwrap());
            assert!(
                Document::restore(&serde_json::to_vec(&stored).unwrap()).is_err(),
                "malformed projected case {case} accepted"
            );
        }
    }
}
