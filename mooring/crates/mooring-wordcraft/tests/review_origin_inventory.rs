use mooring_wordcraft::{
    origin_inventory::{inspect, Error, MAX_ORIGIN},
    Document,
};
use serde_json::Value;
fn checkpoint() -> Vec<u8> {
    let native: Value =
        serde_json::from_str(include_str!("fixtures/d1-engine-canonical.json")).unwrap();
    Document::import(
        native,
        (0..8).map(|i| format!("review_origin_{i}")).collect(),
    )
    .unwrap()
    .checkpoint()
    .unwrap()
}
#[test]
fn independent_origin_candidates_never_alias_and_leave_source_bytes_unchanged() {
    let cp = checkpoint();
    let inventory = inspect(&cp).unwrap();
    let before = inventory.checkpoint().to_vec();
    for candidate in [0, 1 << 53, (1 << 53) + 1, (1 << 63) + 17, u64::MAX] {
        let result = std::panic::catch_unwind(|| inventory.check_unused(candidate));
        assert_eq!(result.unwrap(), Err(Error::InvalidOrigin));
    }
    for (origin, _) in inventory.allocated() {
        if *origin != 0 {
            assert_eq!(inventory.check_unused(*origin), Err(Error::OriginInUse));
        }
    }
    assert!(matches!(
        inventory.check_unused(MAX_ORIGIN),
        Ok(()) | Err(Error::OriginInUse)
    ));
    assert_eq!(inventory.checkpoint(), before);
    assert_eq!(Document::restore(&cp).unwrap().checkpoint().unwrap(), cp);
}
#[test]
fn independent_unknown_future_delete_cannot_qualify_a_checkpoint_origin() {
    let cp = checkpoint();
    let mut envelope: Value = serde_json::from_slice(&cp).unwrap();
    let update = mooring_protocol::boundary::decode_base64(
        envelope["update_base64"].as_str().unwrap(),
        16 * 1024 * 1024,
    )
    .unwrap();
    // Closed Yrs-v1 update with no structs and a delete of as-yet unknown origin17.
    let orphan = [0, 1, 17, 1, 0, 1];
    let merged =
        mooring_text::yrs::merge_updates_v1([update.as_slice(), orphan.as_slice()]).unwrap();
    envelope["update_base64"] = Value::String(
        mooring_protocol::boundary::encode_base64(&merged, 16 * 1024 * 1024).unwrap(),
    );
    let bytes = serde_json::to_vec(&envelope).unwrap();
    assert!(matches!(inspect(&bytes), Err(Error::InvalidCheckpoint)));
    assert_eq!(inspect(&cp).unwrap().checkpoint(), cp);
}
