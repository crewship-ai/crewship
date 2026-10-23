use mooring_client::{conformance_case, negotiate_json, TextClient};

#[test]
fn identifier_scalars_match_wire_without_constraining_text() {
    let mut client = TextClient::new();
    let id = "😀".repeat(200);
    client.insert(&id, &id, 0, "a\0b").unwrap();
    assert_eq!(client.text(&id), "a\0b");
    let drafts = client.unsaved_json();
    for invalid in ["😀".repeat(201), "a\0b".into(), String::new()] {
        assert_eq!(
            client.insert(&invalid, &id, 0, "bad"),
            Err("invalid_request".into())
        );
        assert_eq!(
            client.insert("other", &invalid, 0, "bad"),
            Err("invalid_request".into())
        );
    }
    assert_eq!(client.unsaved_json(), drafts);
}

#[test]
fn p30_shared_native_wasm_vectors() {
    let cases: Vec<serde_json::Value> =
        serde_json::from_str(include_str!("../../../fixtures/native-wasm.json")).unwrap();
    for case in cases {
        let result: serde_json::Value =
            serde_json::from_str(&conformance_case(&case.to_string()).unwrap()).unwrap();
        assert_eq!(result, case["expected"]);
    }
}
#[test]
fn p28_failed_recovery_keeps_optimistic_text_and_drafts() {
    let mut client = TextClient::new();
    client.insert("o1", "p1", 0, "Draft").unwrap();
    let before = client.unsaved_json();
    assert!(client.recover(&[255], "rejected").is_err());
    assert_eq!(client.text("p1"), "Draft");
    assert_eq!(client.unsaved_json(), before);
}
#[test]
fn p30_surrogate_split_and_duplicate_id_do_not_modify_document() {
    let mut client = TextClient::new();
    client.insert("o1", "p1", 0, "A😀B").unwrap();
    assert!(client.insert("o2", "p1", 2, "bad").is_err());
    assert!(client.insert("o1", "p1", 0, "duplicate").is_err());
    assert_eq!(client.text("p1"), "A😀B");
}
#[test]
fn p30_old_client_fails_before_document_exchange() {
    assert_eq!(
        negotiate_json(
            r#"{"type":"hello","request_id":"r","supported":[{"protocol_version":"0.3.0","model_version":"1.0.0","engine_version":"0.28.0"}]}"#
        ),
        Err("version_mismatch".into())
    );
}

#[test]
fn p21_draft_byte_quota_rejects_before_changing_text() {
    let mut client = TextClient::new();
    let chunk = "x".repeat(64 * 1024);
    let mut rejected = false;
    for index in 0..32 {
        let before_text = client.text("p1");
        let before_drafts = client.unsaved_json();
        let end = before_text.len() as u32; // ASCII fixture: bytes == UTF-16 units.
        if let Err(error) = client.insert(&format!("o{index}"), "p1", end, &chunk) {
            assert_eq!(error, "quota_exceeded");
            assert_eq!(client.text("p1"), before_text);
            assert_eq!(client.unsaved_json(), before_drafts);
            rejected = true;
            break;
        }
    }
    assert!(rejected, "full-text drafts must have a bounded byte budget");
}

#[test]
fn participant_sdk_cannot_offer_inherited_control_profile() {
    assert_eq!(
        negotiate_json(
            r#"{"type":"hello","request_id":"r","supported":[{"protocol_version":"0.4.0","model_version":"1.0.0","engine_version":"0.28.0"}],"control_profiles":["mooring.prepare.v1"]}"#
        ),
        Err("invalid_request".into())
    );
}
