use mooring_protocol::{
    control::{self, Peer},
    session_control, wire,
};
use serde_json::Value;

#[test]
fn origin_shapes_numeric_bounds_and_explicit_capabilities_are_closed() {
    let fixture: Value = serde_json::from_str(include_str!(
        "../../../protocol/origin-inventory-fixtures.json"
    ))
    .unwrap();
    for case in fixture["cases"].as_array().unwrap() {
        let raw = serde_json::to_vec(&case["message"]).unwrap();
        let peer = match case["peer"].as_str().unwrap() {
            "host" => Peer::InheritedHost,
            "runtime" => Peer::InheritedRuntime,
            "participant" => Peer::Participant,
            _ => unreachable!(),
        };
        assert_eq!(
            session_control::validate(&raw, peer).is_ok(),
            case["valid"] == true,
            "{}",
            case["id"]
        );
        assert!(control::validate(&raw, Peer::InheritedHost).is_err());
        assert!(control::validate_causal(&raw, Peer::InheritedHost).is_err());
        assert!(wire::validate_bounded_envelope(&raw).is_err());
    }
}

#[test]
fn full_inventory_capacity_does_not_allow_oversized_or_partial_order() {
    let fixture: Value = serde_json::from_str(include_str!(
        "../../../protocol/origin-inventory-fixtures.json"
    ))
    .unwrap();
    let mut message = fixture["cases"]
        .as_array()
        .unwrap()
        .iter()
        .find(|c| c["id"] == "session_origins_inspected-valid")
        .unwrap()["message"]
        .clone();
    message["allocated"] = serde_json::json!((0..4096)
        .map(|id| serde_json::json!({"origin":id.to_string(),"clock":"4294967295"}))
        .collect::<Vec<_>>());
    let raw = serde_json::to_vec(&message).unwrap();
    assert!(session_control::validate(&raw, Peer::InheritedRuntime).is_ok());
    message["allocated"]
        .as_array_mut()
        .unwrap()
        .push(serde_json::json!({"origin":"4096","clock":"1"}));
    assert!(session_control::validate(
        &serde_json::to_vec(&message).unwrap(),
        Peer::InheritedRuntime
    )
    .is_err());
}

#[test]
fn only_named_origin_controls_can_carry_large_private_checkpoint() {
    let fixture: Value = serde_json::from_str(include_str!(
        "../../../protocol/origin-inventory-fixtures.json"
    ))
    .unwrap();
    let mut message = fixture["cases"][0]["message"].clone();
    message["checkpoint"]["state"] = Value::String("a".repeat(400000));
    let raw = serde_json::to_vec(&message).unwrap();
    assert!(raw.len() > 262144 && raw.len() < control::MAX_CONTROL_BYTES);
    assert!(session_control::validate(&raw, Peer::InheritedHost).is_ok());
    assert!(wire::validate_bounded_envelope(&raw).is_err());
    message["checkpoint"]["state"] = Value::String("a".repeat(control::MAX_CONTROL_BYTES));
    assert_eq!(
        session_control::validate(&serde_json::to_vec(&message).unwrap(), Peer::InheritedHost)
            .unwrap_err(),
        wire::WireError::ResourceLimit
    );
}
