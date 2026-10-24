use mooring_protocol::{
    checkpoint::{self, Limits},
    Asset, Checkpoint, Counter,
};
use serde_json::Value;
#[test]
fn full_tuple_identity_matches_shared_encoding_unicode_counter_and_transplant_vectors() {
    let fixture: Value = serde_json::from_str(include_str!(
        "../../../protocol/checkpoint-identity-fixtures.json"
    ))
    .unwrap();
    for case in fixture["cases"].as_array().unwrap() {
        let cp: Checkpoint = serde_json::from_value(case["checkpoint"].clone()).unwrap();
        let limits = Limits {
            decoded_bytes: case["decoded_limit"].as_u64().unwrap() as usize,
            preimage_bytes: case["preimage_limit"].as_u64().unwrap() as usize,
        };
        let identity =
            checkpoint::compute(&cp, limits).unwrap_or_else(|e| panic!("{}:{e:?}", case["id"]));
        assert_eq!(
            identity.full_digest(),
            case["expected_digest"].as_str().unwrap(),
            "{}",
            case["id"]
        );
        assert_eq!(
            identity.content_digest(),
            case["expected_content_digest"].as_str().unwrap()
        );
        assert_eq!(
            mooring_protocol::boundary::encode_base64(identity.state(), limits.decoded_bytes)
                .unwrap(),
            case["expected_state_base64"]
        );
        assert_eq!(
            checkpoint::verify(&cp, limits).is_ok(),
            case["verify"].as_bool().unwrap()
        );
    }
    for case in fixture["rejected"].as_array().unwrap() {
        let cp: Checkpoint = serde_json::from_value(case["checkpoint"].clone()).unwrap();
        assert!(
            checkpoint::compute(
                &cp,
                Limits {
                    decoded_bytes: case["decoded_limit"].as_u64().unwrap() as usize,
                    preimage_bytes: case["preimage_limit"].as_u64().unwrap() as usize
                }
            )
            .is_err(),
            "{}",
            case["id"]
        );
    }
    assert_eq!(
        fixture["cases"][0]["expected_digest"],
        fixture["cases"][1]["expected_digest"]
    );
    assert_eq!(
        fixture["cases"][0]["expected_digest"],
        fixture["cases"][2]["expected_digest"]
    );
    assert_ne!(
        fixture["cases"][0]["expected_digest"],
        fixture["cases"][6]["expected_digest"]
    );
}
#[test]
fn generic_identity_supports_bounded_large_state_and_all_assets_without_frame_admission() {
    let fixture: Value = serde_json::from_str(include_str!(
        "../../../protocol/checkpoint-identity-fixtures.json"
    ))
    .unwrap();
    let mut cp: Checkpoint =
        serde_json::from_value(fixture["cases"][0]["checkpoint"].clone()).unwrap();
    let raw = vec![0u8; 1024 * 1024];
    cp.state = mooring_protocol::boundary::encode_base64(&raw, raw.len()).unwrap();
    cp.assets = (0..4096)
        .rev()
        .map(|i| Asset {
            asset_id: format!("asset-{i:04}"),
            digest: format!("sha256:{}", "a".repeat(64)),
            size_bytes: Counter(u64::MAX),
        })
        .collect();
    let identity = checkpoint::compute(
        &cp,
        Limits {
            decoded_bytes: 8 * 1024 * 1024,
            preimage_bytes: 16 * 1024 * 1024,
        },
    )
    .unwrap();
    assert_eq!(identity.state(), raw);
    assert!(checkpoint::compute(
        &cp,
        Limits {
            decoded_bytes: 8 * 1024 * 1024 + 1,
            preimage_bytes: 16 * 1024 * 1024
        }
    )
    .is_err());
    cp.assets.push(cp.assets[0].clone());
    assert!(checkpoint::compute(
        &cp,
        Limits {
            decoded_bytes: 8 * 1024 * 1024,
            preimage_bytes: 16 * 1024 * 1024
        }
    )
    .is_err());
}
