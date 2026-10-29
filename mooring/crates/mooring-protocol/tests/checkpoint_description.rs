use mooring_protocol::{
    control::{self, Peer},
    session_control, wire,
};
use serde_json::Value;

#[test]
fn descriptor_shapes_require_closed_capability_and_private_direction() {
    let fixture: Value = serde_json::from_str(include_str!(
        "../../../protocol/checkpoint-description-fixtures.json"
    ))
    .unwrap();
    for case in fixture["cases"].as_array().unwrap() {
        let raw = serde_json::to_vec(&case["message"]).unwrap();
        let peer = || match case["peer"].as_str().unwrap() {
            "host" => Peer::InheritedHost,
            "runtime" => Peer::InheritedRuntime,
            "participant" => Peer::Participant,
            other => panic!("unexpected peer {other}"),
        };
        assert_eq!(
            session_control::validate(&raw, peer()).is_ok(),
            case["valid"] == true,
            "{}",
            case["id"]
        );
        assert!(control::validate(&raw, peer()).is_err());
        assert!(control::validate_causal(&raw, peer()).is_err());
        assert!(wire::validate_bounded_envelope(&raw).is_err());
    }
}

#[test]
fn only_named_descriptor_controls_get_bounded_large_frame_budget() {
    let fixture: Value = serde_json::from_str(include_str!(
        "../../../protocol/checkpoint-description-fixtures.json"
    ))
    .unwrap();
    let mut message = fixture["cases"]
        .as_array()
        .unwrap()
        .iter()
        .find(|c| c["id"] == "session_checkpoint_described-valid")
        .unwrap()["message"]
        .clone();
    let assets = |count| {
        (0..count).map(|i| serde_json::json!({"asset_id":format!("{i}{}", "😀".repeat(195)),"digest":format!("sha256:{}", "0".repeat(64)),"size_bytes":"18446744073709551615"})).collect::<Vec<_>>()
    };
    message["description"]["assets"] = serde_json::json!(assets(1000));
    let raw = serde_json::to_vec(&message).unwrap();
    assert!(raw.len() > 262144 && raw.len() < control::MAX_CONTROL_BYTES);
    assert!(session_control::validate(&raw, Peer::InheritedRuntime).is_ok());
    assert!(wire::validate_bounded_envelope(&raw).is_err());
    message["description"]["assets"] = serde_json::json!(assets(2800));
    let raw = serde_json::to_vec(&message).unwrap();
    assert!(raw.len() > control::MAX_CONTROL_BYTES);
    assert_eq!(
        session_control::validate(&raw, Peer::InheritedRuntime).unwrap_err(),
        wire::WireError::ResourceLimit
    );
}
