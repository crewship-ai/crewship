use mooring_protocol::{wire, Hello, Versions};
use serde_json::{json, Value};

fn expand(case: &Value) -> Vec<u8> {
    if let Some(hex) = case.get("utf8_hex").and_then(Value::as_str) {
        assert_eq!(hex.len() % 2, 0);
        return (0..hex.len())
            .step_by(2)
            .map(|i| u8::from_str_radix(&hex[i..i + 2], 16).unwrap())
            .collect();
    }
    let mut result = String::new();
    for segment in case["segments"].as_array().unwrap() {
        if let Some(text) = segment.as_str() {
            result.push_str(text);
        } else {
            let text = segment["text"].as_str().unwrap();
            let count = segment["repeat"].as_u64().unwrap() as usize;
            assert!(text.len().saturating_mul(count) <= 300_000);
            result.push_str(&text.repeat(count));
        }
    }
    result.into_bytes()
}

// Independent tree-based accounting verifies fixture metadata. Production
// preflight counts raw bytes without allocating this parsed tree.
fn counts(value: &Value) -> (usize, usize) {
    let children: Vec<_> = match value {
        Value::Array(items) => items.iter().collect(),
        Value::Object(items) => items.values().collect(),
        _ => return (0, 0),
    };
    let mut depth = 1;
    let mut tokens = 1 + children.len().saturating_sub(1);
    if value.is_object() {
        tokens += children.len();
    }
    for child in children {
        let (child_depth, child_tokens) = counts(child);
        depth = depth.max(1 + child_depth);
        tokens += child_tokens;
    }
    (depth, tokens)
}

#[test]
fn shared_resource_identifier_and_version_boundaries() {
    let fixtures: Value =
        serde_json::from_str(include_str!("../../../protocol/wire-profile-fixtures.json")).unwrap();
    assert_eq!(fixtures["profile"], "mooring-e3-bounded-json-v1");
    assert_eq!(
        fixtures["limits"],
        json!({"raw_bytes":wire::MAX_FRAME_BYTES,"canonical_bytes":wire::MAX_FRAME_BYTES,"container_depth":wire::MAX_JSON_DEPTH,"structural_tokens":wire::MAX_STRUCTURAL_TOKENS,"identifier_scalars":200})
    );
    for case in fixtures["resource_cases"].as_array().unwrap() {
        let raw = expand(case);
        if case.get("depth").is_some() || case.get("structural_tokens").is_some() {
            let parsed: Value = serde_json::from_slice(&raw).unwrap();
            let (depth, tokens) = counts(&parsed);
            if let Some(expected) = case["depth"].as_u64() {
                assert_eq!(depth as u64, expected, "{}", case["id"]);
            }
            if let Some(expected) = case["structural_tokens"].as_u64() {
                assert_eq!(tokens as u64, expected, "{}", case["id"]);
            }
        }
        if let Some(bytes) = case["raw_bytes"].as_u64() {
            assert_eq!(raw.len() as u64, bytes, "{}", case["id"]);
        }
        // Independent strict JCS bytes can exceed the bounded profile. Verify
        // the fixture's expansion count without imposing the helper's cap.
        if let Some(bytes) = case["canonical_bytes"].as_u64() {
            let parsed: Value = serde_json::from_slice(&raw).unwrap();
            assert_eq!(
                serde_json_canonicalizer::to_vec(&parsed).unwrap().len() as u64,
                bytes
            );
        }
        assert_eq!(
            wire::normalize_bounded(&raw).is_ok(),
            case["valid"].as_bool().unwrap(),
            "{}",
            case["id"]
        );
    }
    for case in fixtures["identifier_cases"].as_array().unwrap() {
        let value = String::from_utf8(expand(case)).unwrap();
        let expected = case["valid"].as_bool().unwrap();
        assert_eq!(wire::valid_identifier(&value), expected, "{}", case["id"]);
        let raw = serde_json::to_vec(&json!({"type":"hello","request_id":value,"supported":[{"protocol_version":"0.4.0","model_version":"1.0.0","engine_version":"1.0.0"}]})).unwrap();
        assert_eq!(
            wire::validate_bounded_envelope(&raw).is_ok(),
            expected,
            "{}",
            case["id"]
        );
    }
    for case in fixtures["version_cases"].as_array().unwrap() {
        let version = Versions {
            protocol_version: case["value"].as_str().unwrap().into(),
            model_version: "1.0.0".into(),
            engine_version: "1.0.0".into(),
        };
        let supported = [version.clone()];
        let hello = Hello {
            request_id: "request".into(),
            supported: vec![version],
            r#type: mooring_protocol::HelloType::Hello,
        };
        let expected = case["valid"].as_bool().unwrap();
        assert_eq!(
            mooring_protocol::negotiate(&hello, &supported).is_ok(),
            expected,
            "{}",
            case["value"]
        );
        let raw = serde_json::to_vec(&hello).unwrap();
        assert_eq!(
            wire::validate_bounded_envelope(&raw).is_ok(),
            expected,
            "{}",
            case["value"]
        );
    }
    assert_eq!(fixtures["resource_cases"].as_array().unwrap().len(), 14);
    assert_eq!(fixtures["identifier_cases"].as_array().unwrap().len(), 9);
    assert_eq!(fixtures["version_cases"].as_array().unwrap().len(), 8);
}

#[test]
fn opaque_application_keys_do_not_become_identifiers() {
    let value = json!({"op_id":"\0".repeat(300),"request_id":"😀".repeat(201)});
    assert!(wire::normalize_bounded(&serde_json::to_vec(&value).unwrap()).is_ok());
}
