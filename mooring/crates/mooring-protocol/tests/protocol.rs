use mooring_protocol::{negotiate, Counter, Hello, True, Versions};

#[test]
fn p30_counters_preserve_values_above_javascript_precision() {
    for value in [0, 9_007_199_254_740_993, u64::MAX] {
        let encoded = serde_json::to_string(&Counter(value)).unwrap();
        assert_eq!(encoded, format!("\"{value}\""));
        assert_eq!(
            serde_json::from_str::<Counter>(&encoded).unwrap(),
            Counter(value)
        );
    }
    for invalid in [
        "0",
        "\"01\"",
        "\"+1\"",
        "\"-1\"",
        "\"\"",
        "\"18446744073709551616\"",
        "\"1e1\"",
        "\"１\"",
    ] {
        assert!(
            serde_json::from_str::<Counter>(invalid).is_err(),
            "{invalid}"
        );
    }
}
#[test]
fn p30_const_tags_and_unknown_fields_cannot_select_wrong_message() {
    assert!(serde_json::from_str::<Hello>(
        r#"{"type":"commit_batch","request_id":"r","supported":[]}"#
    )
    .is_err());
    assert!(serde_json::from_str::<Hello>(
        r#"{"type":"hello","request_id":"r","supported":[],"forged":true}"#
    )
    .is_err());
    assert!(serde_json::from_str::<True>("false").is_err());
    assert!(serde_json::from_str::<True>("true").is_ok());
}
#[test]
fn p30_old_client_and_model_engine_mismatch_require_upgrade() {
    let versions = Versions {
        protocol_version: "0.4.0".into(),
        model_version: "1.0.0".into(),
        engine_version: "0.28.0".into(),
    };
    let mut hello: Hello = serde_json::from_str(r#"{"type":"hello","request_id":"r","supported":[{"protocol_version":"0.4.0","model_version":"1.0.0","engine_version":"0.28.0"}]}"#).unwrap();
    assert_eq!(negotiate(&hello, std::slice::from_ref(&versions)), Ok(&versions));
    for axis in 0..3 {
        let mut old = versions.clone();
        match axis {
            0 => old.protocol_version = "0.3.0".into(),
            1 => old.model_version = "0.0.1".into(),
            _ => old.engine_version = "0.24.0".into(),
        }
        hello.supported = vec![old];
        assert_eq!(
            negotiate(&hello, std::slice::from_ref(&versions)),
            Err("version_mismatch")
        );
    }
    hello.supported.clear();
    assert_eq!(negotiate(&hello, &[versions]), Err("invalid_request"));
}

#[test]
fn generated_shapes_decode_and_roundtrip_every_positive_envelope_fixture() {
    let fixtures: serde_json::Value = serde_json::from_str(include_str!("../../../protocol/conformance-fixtures.json")).unwrap();
    let mut count = 0;
    for case in fixtures["schema_cases"].as_array().unwrap() {
        if case["schema_ref"] == "#" && case["schema_valid"] == true {
            let message: mooring_protocol::Message = serde_json::from_value(case["message"].clone()).unwrap_or_else(|error| panic!("{}: {error}",case["id"]));
            assert_eq!(serde_json::to_value(message).unwrap(), case["message"], "{}",case["id"]);
            count += 1;
        }
    }
    assert!(count >= 46, "every envelope needs a positive decoding fixture");
}
