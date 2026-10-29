use mooring_protocol::{boundary::*, Counter, Scope};
use serde_json::Value;

fn fixture() -> Value {
    serde_json::from_str(include_str!(
        "../../../protocol/boundary-semantics-fixtures.json"
    ))
    .unwrap()
}
fn error(label: &str) -> BoundaryError {
    match label {
        "Forbidden" => BoundaryError::Forbidden,
        "Binding" => BoundaryError::Binding,
        "Durability" => BoundaryError::Durability,
        "Receipt" => BoundaryError::Receipt,
        "Encoding" => BoundaryError::Encoding,
        "Capacity" => BoundaryError::Capacity,
        _ => panic!("unknown expected error {label}"),
    }
}
#[test]
fn shared_host_receipt_and_atomic_boundary_tampering() {
    let fixtures = fixture();
    let context = &fixtures["context"];
    let scope: Scope = serde_json::from_value(context["scope"].clone()).unwrap();
    let changes: Vec<_> = context["changes"]
        .as_array()
        .unwrap()
        .iter()
        .map(|change| PendingAtomic {
            change_id: change["change_id"].as_str().unwrap().into(),
            op_ids: change["op_ids"]
                .as_array()
                .unwrap()
                .iter()
                .map(|id| id.as_str().unwrap().into())
                .collect(),
        })
        .collect();
    let binding = CommitBinding {
        request_id: context["request_id"].as_str().unwrap(),
        scope: &scope,
        generation: Counter(context["generation"].as_str().unwrap().parse().unwrap()),
        changes: &changes,
    };
    let cases = fixtures["receipt_cases"].as_array().unwrap();
    assert_eq!(cases.len(), 26);
    for case in cases {
        let mut receipt = fixtures["base_receipt"].clone();
        for mutation in case["mutations"].as_array().unwrap() {
            *receipt
                .pointer_mut(mutation["path"].as_str().unwrap())
                .unwrap() = mutation["value"].clone();
        }
        let allowed = case
            .get("allowed_durabilities")
            .unwrap_or(&context["allowed_durabilities"]);
        let allowed: Vec<_> = allowed
            .as_array()
            .unwrap()
            .iter()
            .map(|mode| match mode.as_str().unwrap() {
                "node" => Durability::Node,
                "replicated" => Durability::Replicated,
                _ => panic!("unknown mode"),
            })
            .collect();
        let transport = match case["transport"].as_str().unwrap_or("host") {
            "host" => Transport::AuthenticatedHost {
                issuer: context["issuer"].as_str().unwrap(),
                durabilities: &allowed,
                required: match case
                    .get("required_durability")
                    .unwrap_or(&context["required_durability"])
                    .as_str()
                    .unwrap()
                {
                    "node" => Durability::Node,
                    "replicated" => Durability::Replicated,
                    _ => panic!("unknown required mode"),
                },
            },
            "participant" => Transport::Participant,
            "runtime" => Transport::Runtime,
            _ => panic!("unknown transport"),
        };
        let raw = serde_json::to_vec(&receipt).unwrap();
        let before = raw.clone();
        let actual = validate_commit_result(&raw, &transport, &binding);
        if let Some(expected) = case["expected_error"].as_str() {
            assert_eq!(actual.unwrap_err(), error(expected), "{}", case["id"]);
        } else {
            let checked = actual.unwrap_or_else(|error| panic!("{}: {error:?}", case["id"]));
            assert_eq!(checked.value(), &receipt, "{}", case["id"]);
            assert_eq!(
                checked.revision().0,
                receipt["revision"]
                    .as_str()
                    .unwrap()
                    .parse::<u64>()
                    .unwrap()
            );
        }
        assert_eq!(raw, before);
    }
    assert_eq!(
        validate_commit_result(b"not JSON", &Transport::Participant, &binding).unwrap_err(),
        BoundaryError::Forbidden
    );
}

#[test]
fn shared_canonical_base64_and_decoded_allocation_preflight() {
    let fixtures = fixture();
    let cases = fixtures["base64_cases"].as_array().unwrap();
    assert_eq!(cases.len(), 14);
    for case in cases {
        let actual = canonical_base64_decoded_len(
            case["text"].as_str().unwrap(),
            case["max_decoded_bytes"].as_u64().unwrap() as usize,
        );
        if let Some(expected) = case["expected_error"].as_str() {
            assert_eq!(actual.unwrap_err(), error(expected), "{}", case["id"]);
        } else {
            assert_eq!(
                actual.unwrap() as u64,
                case["decoded_bytes"].as_u64().unwrap(),
                "{}",
                case["id"]
            );
        }
    }
}
