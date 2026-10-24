use mooring_protocol::{
    canonical::{fingerprint_atomic, normalize, CanonicalError, MAX_CANONICAL_BYTES},
    Scope,
};
use serde_json::{json, Value};
fn vectors() -> Value {
    serde_json::from_str(include_str!("../../../protocol/fingerprint-fixtures.json")).unwrap()
}
fn wire_change(input: &Value) -> Value {
    let mut operations = input["operations_without_fingerprints"].clone();
    for operation in operations.as_array_mut().unwrap() {
        operation["fingerprint"] = json!("untrusted-placeholder");
    }
    let mut change = json!({"change_id":input["change_id"],"operations":operations,"membership_hash":"untrusted-placeholder"});
    if let Some(task) = input.get("task_id") {
        change["task_id"] = task.clone();
    }
    change
}
#[test]
fn jcs_shared_numeric_unicode_escape_and_normalized_adapter_goldens() {
    let vectors = vectors();
    for case in vectors["canonicalization_vectors"].as_array().unwrap() {
        let normalized = normalize(case["input_json"].as_str().unwrap().as_bytes()).unwrap();
        assert_eq!(
            std::str::from_utf8(normalized.bytes()).unwrap(),
            case["canonical_json"].as_str().unwrap(),
            "{}",
            case["id"]
        );
        assert_eq!(normalized.digest(), case["sha256"], "{}", case["id"]);
    }
    for case in vectors["adapter_semantics_vectors"].as_array().unwrap() {
        let original = vectors["canonicalization_vectors"]
            .as_array()
            .unwrap()
            .iter()
            .find(|v| v["id"] == case["canonicalization_vector"])
            .unwrap();
        let normalized = normalize(original["input_json"].as_str().unwrap().as_bytes()).unwrap();
        let expected: Value =
            serde_json::from_str(case["adapter_input_canonical"].as_str().unwrap()).unwrap();
        assert_eq!(normalized.value(), &expected, "{}", case["id"]);
    }
    let normalized =
        normalize(br#"{"payload":{"value":9007199254740993},"exact":"9007199254740993"}"#).unwrap();
    assert_eq!(
        normalized.value()["payload"]["value"].as_u64(),
        Some(9007199254740992)
    );
    assert_eq!(normalized.value()["exact"], "9007199254740993");
}
#[test]
fn strict_raw_decode_rejects_duplicates_surrogates_nonfinite_trailing_and_depth() {
    for case in vectors()["invalid_inputs"].as_array().unwrap() {
        assert!(
            normalize(case["input_json"].as_str().unwrap().as_bytes()).is_err(),
            "{}",
            case["id"]
        );
    }
    for raw in [
        br#"{"x":1,"\u0078":2}"#.as_slice(),
        br#"{"x":[{"y":1,"y":2}]}"#,
        b"{} []",
        &[0xff],
    ] {
        assert!(normalize(raw).is_err());
    }
    let nested = format!("{}0{}", "[".repeat(200), "]".repeat(200));
    assert!(normalize(nested.as_bytes()).is_err());
    assert_eq!(
        normalize(&vec![b' '; MAX_CANONICAL_BYTES + 1]).unwrap_err(),
        CanonicalError::Size
    );
    // JCS can expand short exponent notation. Bound normalized output too.
    let expands = format!("[{}]", vec!["1e20"; 15000].join(","));
    assert!(expands.len() < MAX_CANONICAL_BYTES);
    assert_eq!(
        normalize(expands.as_bytes()).unwrap_err(),
        CanonicalError::Size
    );
}
#[test]
fn membership_and_operation_fingerprints_match_shared_order_task_author_goldens() {
    let vectors = vectors();
    for (input, expected) in
        std::iter::once((&vectors["atomic_input"], &vectors["membership_hash"])).chain(
            vectors["membership_vectors"]
                .as_array()
                .unwrap()
                .iter()
                .map(|case| (&case["input"], &case["membership_hash"])),
        )
    {
        let scope: Scope = serde_json::from_value(input["scope"].clone()).unwrap();
        let wire = serde_json::to_vec(&wire_change(input)).unwrap();
        let computed = fingerprint_atomic(&scope, &wire).unwrap();
        assert_eq!(computed.membership_hash(), expected.as_str().unwrap());
        assert!(!computed.matches_wire());
        let signed = computed.with_fingerprints();
        let verified = fingerprint_atomic(&scope, &serde_json::to_vec(&signed).unwrap()).unwrap();
        assert!(verified.matches_wire());
    }
    let input = &vectors["atomic_input"];
    let scope: Scope = serde_json::from_value(input["scope"].clone()).unwrap();
    let computed =
        fingerprint_atomic(&scope, &serde_json::to_vec(&wire_change(input)).unwrap()).unwrap();
    assert_eq!(
        computed.operation_fingerprints()[0],
        vectors["operation_fingerprint"]
    );
    for case in vectors["retry_vectors"].as_array().unwrap() {
        let input = &case["atomic_input"];
        let scope: Scope = serde_json::from_value(input["scope"].clone()).unwrap();
        let computed =
            fingerprint_atomic(&scope, &serde_json::to_vec(&wire_change(input)).unwrap()).unwrap();
        assert_eq!(computed.membership_hash(), case["membership_hash"]);
        for (actual, expected) in computed
            .operation_fingerprints()
            .iter()
            .zip(case["operation_fingerprints"].as_array().unwrap())
        {
            assert_eq!(actual, &expected["fingerprint"]);
        }
    }
    // Only verifies the checkpoint PREIMAGE golden; decoding JSON/base64 state
    // and authenticating checkpoint authority is a separate host boundary.
    for case in vectors["checkpoint_vectors"].as_array().unwrap() {
        let normalized = normalize(&serde_json::to_vec(&case["preimage"]).unwrap()).unwrap();
        assert_eq!(normalized.digest(), case["digest"]);
        assert_eq!(
            std::str::from_utf8(normalized.bytes()).unwrap(),
            case["canonical_json"].as_str().unwrap()
        );
    }
}
#[test]
fn adapter_receives_normalized_atomic_payload_and_wire_tampering_fails_identity() {
    let input = &vectors()["atomic_input"];
    let scope: Scope = serde_json::from_value(input["scope"].clone()).unwrap();
    let mut change = wire_change(input);
    change["operations"][0]["payload"] = json!({"value":9007199254740993u64});
    let computed = fingerprint_atomic(&scope, &serde_json::to_vec(&change).unwrap()).unwrap();
    assert_eq!(
        computed.change().operations[0].payload["value"].as_u64(),
        Some(9007199254740992)
    );
    let mut signed = computed.with_fingerprints();
    assert!(
        fingerprint_atomic(&scope, &serde_json::to_vec(&signed).unwrap())
            .unwrap()
            .matches_wire()
    );
    signed.operations[0].payload["value"] = json!(42);
    assert!(
        !fingerprint_atomic(&scope, &serde_json::to_vec(&signed).unwrap())
            .unwrap()
            .matches_wire()
    );
    signed.operations.push(signed.operations[0].clone());
    assert!(matches!(
        fingerprint_atomic(&scope, &serde_json::to_vec(&signed).unwrap()),
        Err(CanonicalError::InvalidAtomic)
    ));
}
