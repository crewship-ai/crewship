use mooring_protocol::{
    canonical,
    control::{self, Peer},
    wire,
};
use serde_json::Value;

fn fixture() -> Value {
    serde_json::from_str(include_str!(
        "../../../protocol/causal-control-fixtures.json"
    ))
    .unwrap()
}

#[test]
fn old_strict_definitions_are_unchanged() {
    let schema: Value =
        serde_json::from_str(include_str!("../../../protocol/schema.json")).unwrap();
    for (name, expected) in fixture()["baseline_definition_hashes"].as_object().unwrap() {
        let bytes = serde_json::to_vec(&schema["$defs"][name]).unwrap();
        let normalized = canonical::normalize(&bytes).unwrap();
        assert_eq!(normalized.digest(), expected.as_str().unwrap(), "{name}");
    }
}

#[test]
fn causal_shapes_require_exact_profile_type_direction_and_private_channel() {
    let cases = fixture();
    let cases = cases["cases"].as_array().unwrap();
    assert_eq!(cases.len(), 25);
    for case in cases {
        let bytes = serde_json::to_vec(&case["message"]).unwrap();
        let host = case["sender"] == "host";
        let result = control::validate_causal(
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
            control::validate_causal(
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
        assert!(control::validate_causal(&bytes, Peer::Participant).is_err());
        assert!(control::validate(&bytes, Peer::InheritedHost).is_err());
        assert!(control::validate(&bytes, Peer::InheritedRuntime).is_err());
        assert!(wire::validate_bounded_envelope(&bytes).is_err());
    }
    let old: Value =
        serde_json::from_str(include_str!("../../../protocol/conformance-fixtures.json")).unwrap();
    for case in old["schema_cases"].as_array().unwrap() {
        if case["schema_ref"] == "#" && case["schema_valid"] == true {
            let bytes = serde_json::to_vec(&case["message"]).unwrap();
            assert!(control::validate_causal(&bytes, Peer::InheritedHost).is_err());
            assert!(control::validate_causal(&bytes, Peer::InheritedRuntime).is_err());
        }
    }
}

#[test]
fn required_nullable_entry_preserves_null_and_original_optional_absence() {
    let message = fixture()["cases"][0]["message"].clone();
    // Checkpoint validation carries no read attestations or newly invented authority.
    assert!(message.get("context").is_none());
    let fixture = fixture();
    let case = fixture["cases"]
        .as_array()
        .unwrap()
        .iter()
        .find(|case| case["id"] == "entry-null-allowed")
        .unwrap();
    let request: mooring_protocol::PrepareAtomicCausal =
        serde_json::from_value(case["message"].clone()).unwrap();
    let roundtrip = serde_json::to_value(request).unwrap();
    assert!(roundtrip["context"]["qualified_reads"][0]["entry"].is_null());
    let case = fixture["cases"]
        .as_array()
        .unwrap()
        .iter()
        .find(|case| case["id"] == "structure-absent-allowed")
        .unwrap();
    let request: mooring_protocol::PrepareAtomicCausal =
        serde_json::from_value(case["message"].clone()).unwrap();
    assert!(
        serde_json::to_value(request).unwrap()["context"]["qualified_reads"][0]
            .get("observed_structure_revision")
            .is_none()
    );
}

#[test]
fn session_native_fields_never_leak_into_standalone_causal_profile() {
    let cases = fixture();
    let mut message = cases["cases"]
        .as_array()
        .unwrap()
        .iter()
        .find(|case| case["id"] == "atomic_prepared-causal")
        .unwrap()["message"]
        .clone();
    message["native_delta"] = serde_json::json!({
        "writer_origin":"1", "allocation_origins":[], "update":"", "targets":["p"]
    });
    let bytes = serde_json::to_vec(&message).unwrap();
    assert!(control::validate_session_native_prepare(&bytes, Peer::InheritedRuntime).is_ok());
    assert!(control::validate_session_native_prepare(&bytes, Peer::InheritedHost).is_err());
    assert!(control::validate_session_native_prepare(&bytes, Peer::Participant).is_err());
    assert!(control::validate_causal(&bytes, Peer::InheritedRuntime).is_err());
    assert!(control::validate(&bytes, Peer::InheritedRuntime).is_err());
    assert!(wire::validate_bounded_envelope(&bytes).is_err());
    message["native_delta"] = Value::Null;
    let bytes = serde_json::to_vec(&message).unwrap();
    assert!(control::validate_session_native_prepare(&bytes, Peer::InheritedRuntime).is_err());
    assert!(control::validate_causal(&bytes, Peer::InheritedRuntime).is_err());

    let mut request = cases["cases"]
        .as_array()
        .unwrap()
        .iter()
        .find(|case| case["message"]["type"] == "prepare_atomic_causal" && case["valid"] == true)
        .unwrap()["message"]
        .clone();
    request["human_context"] = Value::Null;
    let bytes = serde_json::to_vec(&request).unwrap();
    assert!(control::validate_session_native_prepare(&bytes, Peer::InheritedHost).is_err());
    assert!(control::validate_causal(&bytes, Peer::InheritedHost).is_err());
}
