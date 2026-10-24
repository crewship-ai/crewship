use mooring_protocol::{
    canonical,
    control::{self, Peer},
    session_control, wire,
};
use serde_json::Value;
fn fixture() -> Value {
    serde_json::from_str(include_str!(
        "../../../protocol/session-control-fixtures.json"
    ))
    .unwrap()
}
#[test]
fn preexisting_definitions_only_add_session_native_optional_fields() {
    let schema: Value =
        serde_json::from_str(include_str!("../../../protocol/schema.json")).unwrap();
    for (name, expected) in fixture()["baseline_definition_hashes"].as_object().unwrap() {
        let mut definition = schema["$defs"][name].clone();
        // These fields are accepted only by the explicitly opted-in session
        // validator. Removing them must recover every byte of the old shape;
        // required fields and all standalone semantics remain unchanged.
        let optional = match name.as_str() {
            "PrepareAtomicCausal" => Some("human_context"),
            "AtomicPreparedCausal" => Some("native_delta"),
            "SessionCommitResult" => Some("native_deltas"),
            _ => None,
        };
        if let Some(field) = optional {
            assert!(!definition["required"]
                .as_array()
                .unwrap()
                .iter()
                .any(|v| v == field));
            assert!(definition["properties"]
                .as_object_mut()
                .unwrap()
                .remove(field)
                .is_some());
        }
        let bytes = serde_json::to_vec(&definition).unwrap();
        assert_eq!(
            canonical::normalize(&bytes).unwrap().digest(),
            expected.as_str().unwrap(),
            "{name}"
        );
    }
}
#[test]
fn session_shapes_are_closed_private_and_directional() {
    let fixture = fixture();
    for case in fixture["cases"].as_array().unwrap() {
        let bytes = serde_json::to_vec(&case["message"]).unwrap();
        let host = case["sender"] == "host";
        let result = session_control::validate(
            &bytes,
            if host {
                Peer::InheritedHost
            } else {
                Peer::InheritedRuntime
            },
        );
        assert_eq!(
            result.is_ok(),
            case["valid"] == true,
            "{}: {result:?}",
            case["id"]
        );
        assert!(
            session_control::validate(
                &bytes,
                if host {
                    Peer::InheritedRuntime
                } else {
                    Peer::InheritedHost
                }
            )
            .is_err(),
            "{}",
            case["id"]
        );
        assert!(session_control::validate(&bytes, Peer::Participant).is_err());
        assert!(control::validate(&bytes, Peer::InheritedHost).is_err());
        assert!(control::validate(&bytes, Peer::InheritedRuntime).is_err());
        assert!(control::validate_causal(&bytes, Peer::InheritedHost).is_err());
        assert!(control::validate_causal(&bytes, Peer::InheritedRuntime).is_err());
        assert!(wire::validate_bounded_envelope(&bytes).is_err());
    }
}

#[test]
fn private_wrapping_never_widens_batch_or_participant_input() {
    let f = fixture();
    let valid = |name: &str| {
        f["cases"]
            .as_array()
            .unwrap()
            .iter()
            .find(|case| case["id"] == name)
            .unwrap()["message"]
            .clone()
    };
    let mut batch = valid("valid-SessionSubmitBatch");
    batch["changes"][0]["operations"][0]["payload"]["large"] =
        Value::String("x".repeat(session_control::MAX_BATCH_BYTES));
    let raw = serde_json::to_vec(&batch).unwrap();
    assert!(raw.len() < control::MAX_CONTROL_BYTES);
    assert_eq!(
        session_control::validate(&raw, Peer::InheritedRuntime).unwrap_err(),
        wire::WireError::ResourceLimit
    );
    let mut participant = valid("valid-SessionParticipantInput");
    participant["message"]["changes"][0]["operations"][0]["payload"]["large"] =
        Value::String("x".repeat(session_control::MAX_BATCH_BYTES));
    let raw = serde_json::to_vec(&participant).unwrap();
    assert!(raw.len() < control::MAX_CONTROL_BYTES);
    assert_eq!(
        session_control::validate(&raw, Peer::InheritedHost).unwrap_err(),
        wire::WireError::ResourceLimit
    );
}

#[test]
fn only_explicit_full_checkpoint_messages_keep_large_session_budget() {
    let f = fixture();
    for (name, peer) in [
        ("valid-SessionRestore", Peer::InheritedHost),
        ("valid-SessionCommitResult", Peer::InheritedHost),
    ] {
        let mut message = f["cases"]
            .as_array()
            .unwrap()
            .iter()
            .find(|c| c["id"] == name)
            .unwrap()["message"]
            .clone();
        message["checkpoint"]["state"] = Value::String("x".repeat(300_000));
        let bytes = serde_json::to_vec(&message).unwrap();
        assert!(bytes.len() > session_control::MAX_BATCH_BYTES);
        assert!(session_control::validate(&bytes, peer).is_ok(), "{name}");
    }
}

#[test]
fn human_prepare_capability_never_substitutes_for_checkpoint_identity() {
    let fixture = fixture();
    for (kind, field, host) in [
        ("session_open", "required_capabilities", true),
        ("session_opened", "capabilities", false),
    ] {
        let template = fixture["cases"]
            .as_array()
            .unwrap()
            .iter()
            .find(|case| case["valid"] == true && case["message"]["type"] == kind)
            .unwrap()["message"]
            .clone();
        for (caps, valid) in [
            (serde_json::json!(["checkpoint_identity_v1"]), true),
            (
                serde_json::json!(["checkpoint_identity_v1", "origin_inventory_v1"]),
                true,
            ),
            (
                serde_json::json!(["checkpoint_identity_v1", "human_native_prepare_v1"]),
                true,
            ),
            (
                serde_json::json!([
                    "human_native_prepare_v1",
                    "origin_inventory_v1",
                    "checkpoint_identity_v1"
                ]),
                true,
            ),
            (serde_json::json!(["human_native_prepare_v1"]), false),
            (
                serde_json::json!(["origin_inventory_v1", "human_native_prepare_v1"]),
                false,
            ),
            (
                serde_json::json!([
                    "checkpoint_identity_v1",
                    "human_native_prepare_v1",
                    "human_native_prepare_v1"
                ]),
                false,
            ),
            (
                serde_json::json!(["checkpoint_identity_v1", "future_human"]),
                false,
            ),
        ] {
            let mut message = template.clone();
            message[field] = caps.clone();
            let raw = serde_json::to_vec(&message).unwrap();
            assert_eq!(
                session_control::validate(
                    &raw,
                    if host {
                        Peer::InheritedHost
                    } else {
                        Peer::InheritedRuntime
                    }
                )
                .is_ok(),
                valid,
                "{kind}: {caps}"
            );
            assert!(control::validate_causal(
                &raw,
                if host {
                    Peer::InheritedHost
                } else {
                    Peer::InheritedRuntime
                }
            )
            .is_err());
            assert!(wire::validate_bounded_envelope(&raw).is_err());
        }
    }
}
