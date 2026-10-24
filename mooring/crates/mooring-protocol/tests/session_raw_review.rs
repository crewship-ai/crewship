use mooring_protocol::{control::Peer, session_control, wire::WireError};
use serde_json::{json, Value};
#[test]
fn wrapped_participant_original_raw_limit_survives_normalization() {
    let fixture: Value = serde_json::from_str(include_str!(
        "../../../protocol/session-control-fixtures.json"
    ))
    .unwrap();
    let mut message = fixture["cases"]
        .as_array()
        .unwrap()
        .iter()
        .find(|case| case["id"] == "valid-SessionParticipantInput")
        .unwrap()["message"]
        .clone();
    message["message"]["changes"][0]["operations"][0]["payload"]["escaped_data"] =
        json!("a".repeat(50_000));
    let normal = serde_json::to_string(&message).unwrap();
    let raw = normal
        .replace(
            &json!("a".repeat(50_000)).to_string(),
            &format!("\"{}\"", "\\u0061".repeat(50_000)),
        )
        .into_bytes();
    assert!(raw.len() > session_control::MAX_BATCH_BYTES);
    assert!(raw.len() < mooring_protocol::control::MAX_CONTROL_BYTES);
    assert!(normal.len() < session_control::MAX_BATCH_BYTES);
    assert!(
        matches!(
            session_control::validate(&raw, Peer::InheritedHost),
            Err(WireError::ResourceLimit)
        ),
        "oversized escaped original participant frame accepted after normalization"
    );
}
