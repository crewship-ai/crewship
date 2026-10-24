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
fn preexisting_definitions_are_unchanged() {
    let schema: Value =
        serde_json::from_str(include_str!("../../../protocol/schema.json")).unwrap();
    for (name, expected) in fixture()["baseline_definition_hashes"].as_object().unwrap() {
        let bytes = serde_json::to_vec(&schema["$defs"][name]).unwrap();
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
