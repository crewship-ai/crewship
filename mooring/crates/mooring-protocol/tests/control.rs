use mooring_protocol::{
    control::{self, Peer},
    wire::{self, WireError},
};
use serde_json::{json, Value};

fn messages() -> Vec<Value> {
    let fixture: Value =
        serde_json::from_str(include_str!("../../../protocol/conformance-fixtures.json")).unwrap();
    fixture["schema_cases"]
        .as_array()
        .unwrap()
        .iter()
        .filter(|case| case["schema_ref"] == "#" && case["schema_valid"] == true)
        .map(|case| case["message"].clone())
        .collect()
}

#[test]
fn four_private_shapes_require_explicit_channel_and_direction() {
    let mut seen = std::collections::BTreeSet::new();
    for message in messages() {
        let kind = message["type"].as_str().unwrap();
        if !control::is_control_type(kind) {
            continue;
        }
        seen.insert(kind.to_owned());
        let bytes = serde_json::to_vec(&message).unwrap();
        let request = matches!(kind, "validate_checkpoint" | "prepare_atomic");
        assert!(control::validate(
            &bytes,
            if request {
                Peer::InheritedHost
            } else {
                Peer::InheritedRuntime
            }
        )
        .is_ok());
        assert_eq!(
            control::validate(
                &bytes,
                if request {
                    Peer::InheritedRuntime
                } else {
                    Peer::InheritedHost
                }
            )
            .unwrap_err(),
            WireError::InvalidShape
        );
        assert_eq!(
            control::validate(&bytes, Peer::Participant).unwrap_err(),
            WireError::InvalidShape
        );
        assert_eq!(
            wire::validate_bounded_envelope(&bytes).unwrap_err(),
            WireError::InvalidShape
        );
    }
    assert_eq!(seen.len(), 4);
    let commit = messages()
        .into_iter()
        .find(|message| message["type"] == "commit_batch")
        .unwrap();
    assert_eq!(
        control::validate(&serde_json::to_vec(&commit).unwrap(), Peer::InheritedHost).unwrap_err(),
        WireError::InvalidShape
    );
}

#[test]
fn private_bootstrap_remains_small_and_participant_cannot_select_control() {
    let hello = json!({"type":"hello","request_id":"r","supported":[{"protocol_version":"0.4.0","model_version":"1.0.0","engine_version":"0.1.0"}],"control_profiles":[control::PROFILE]});
    let bytes = serde_json::to_vec(&hello).unwrap();
    assert!(control::validate_bootstrap(&bytes, Peer::InheritedHost).is_ok());
    assert!(control::validate_bootstrap(&bytes, Peer::Participant).is_err());
    assert!(wire::validate_bounded_envelope(&bytes).is_err());
    assert!(control::validate(&bytes, Peer::InheritedHost).is_err());
    let mut oversized = bytes;
    oversized.resize(wire::MAX_FRAME_BYTES + 1, b' ');
    assert_eq!(
        control::validate_bootstrap(&oversized, Peer::InheritedHost).unwrap_err(),
        WireError::ResourceLimit
    );
}

#[test]
fn private_raw_and_canonical_limits_do_not_relax_public_values() {
    let fixture: Value = serde_json::from_str(include_str!(
        "../../../protocol/private-control-fixtures.json"
    ))
    .unwrap();
    assert_eq!(fixture["limits"]["raw_bytes"], control::MAX_CONTROL_BYTES);
    assert_eq!(
        fixture["limits"]["canonical_bytes"],
        control::MAX_CONTROL_BYTES
    );
    assert_eq!(
        fixture["limits"]["decoded_checkpoint_bytes"],
        control::MAX_CHECKPOINT_BYTES
    );
    assert_eq!(
        fixture["public_profile_unchanged_bytes"],
        wire::MAX_FRAME_BYTES
    );
    let message = messages()
        .into_iter()
        .find(|message| message["type"] == "validate_checkpoint")
        .unwrap();
    let mut bytes = serde_json::to_vec(&message).unwrap();
    bytes.resize(control::MAX_CONTROL_BYTES, b' ');
    assert!(control::validate(&bytes, Peer::InheritedHost).is_ok());
    assert_eq!(
        wire::normalize_bounded(&bytes).unwrap_err(),
        WireError::ResourceLimit
    );
    bytes.push(b' ');
    assert_eq!(
        control::validate(&bytes, Peer::InheritedHost).unwrap_err(),
        WireError::ResourceLimit
    );
    // JCS expands 1e21 -> 1e+21. The raw message fits but its canonical form does not.
    let mut message = messages()
        .into_iter()
        .find(|message| message["type"] == "prepare_atomic")
        .unwrap();
    message["change"]["operations"][0]["payload"] = json!({"values":[1e21,1e21,1e21],"padding":""});
    let empty = serde_json::to_string(&message).unwrap();
    let short = empty.replace("1e+21", "1e21");
    let padding = "x".repeat(control::MAX_CONTROL_BYTES - short.len());
    let raw = short.replace("\"padding\":\"\"", &format!("\"padding\":\"{padding}\""));
    assert_eq!(raw.len(), control::MAX_CONTROL_BYTES);
    assert_eq!(
        control::validate(raw.as_bytes(), Peer::InheritedHost).unwrap_err(),
        WireError::ResourceLimit
    );
}

#[test]
fn checkpoint_bytes_padding_digest_and_component_bound_are_verified_before_restore() {
    for length in 0..=257 {
        let bytes: Vec<u8> = (0..length).map(|value| value as u8).collect();
        let checkpoint = control::encode_checkpoint(&bytes).unwrap();
        assert_eq!(control::decode_checkpoint(&checkpoint).unwrap(), bytes);
    }
    let mut checkpoint = control::encode_checkpoint(b"{}").unwrap();
    checkpoint.state = "e31=".into(); // Same decoded bytes if nonzero pad bits were ignored.
    assert!(control::decode_checkpoint(&checkpoint).is_err());
    let mut checkpoint = control::encode_checkpoint(b"{}").unwrap();
    checkpoint.digest = format!("sha256:{}", "0".repeat(64));
    assert!(control::decode_checkpoint(&checkpoint).is_err());
    let maximum = vec![0; control::MAX_CHECKPOINT_BYTES];
    let checkpoint = control::encode_checkpoint(&maximum).unwrap();
    assert_eq!(control::decode_checkpoint(&checkpoint).unwrap(), maximum);
    assert!(control::encode_checkpoint(&vec![0; control::MAX_CHECKPOINT_BYTES + 1]).is_err());
    // The maximum component may not fit a complete frame: never truncate it.
    let mut message = messages()
        .into_iter()
        .find(|message| message["type"] == "validate_checkpoint")
        .unwrap();
    message["checkpoint"] = serde_json::to_value(checkpoint).unwrap();
    assert_eq!(
        control::validate(&serde_json::to_vec(&message).unwrap(), Peer::InheritedHost).unwrap_err(),
        WireError::ResourceLimit
    );
}
