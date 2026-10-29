//! Bounded diagnostic probes; success does not qualify a host session or UI.
use mooring_text::{decode_input, new_document, Access, InputError};
use yrs::sync::{Message, SyncMessage};
use yrs::updates::{decoder::Decode, encoder::Encode};
use yrs::{Doc, GetString, ReadTxn, StateVector, Text, Transact, Update};

fn apply(doc: &Doc, update: &[u8]) {
    let frame = Message::Sync(SyncMessage::Update(update.to_vec())).encode_v1();
    let decoded = decode_input(&frame, Access::Writer, 2 << 20).unwrap();
    let Message::Sync(SyncMessage::Update(bytes)) = decoded else {
        panic!("wrong message")
    };
    doc.transact_mut()
        .apply_update(Update::decode_v1(&bytes).unwrap())
        .unwrap();
}
fn snapshot(doc: &Doc) -> Vec<u8> {
    doc.transact()
        .encode_state_as_update_v1(&StateVector::default())
}
fn contents(doc: &Doc) -> String {
    doc.get_or_insert_text("p1").get_string(&doc.transact())
}
#[test]
fn support_rapid_unicode_5000_updates_roundtrip() {
    let author = new_document();
    let replica = new_document();
    let text = author.get_or_insert_text("p1");
    let samples = ["ř", "ě", "A", "😀", "\u{301}", "\n"];
    let mut expected = String::new();
    let mut bytes = 0;
    for i in 0..5000 {
        let before = author.transact().state_vector();
        let value = samples[i % samples.len()];
        text.push(&mut author.transact_mut(), value);
        expected.push_str(value);
        let update = author.transact().encode_state_as_update_v1(&before);
        bytes += update.len();
        apply(&replica, &update);
        assert_eq!(contents(&replica), expected, "update {i}");
    }
    assert_eq!(contents(&author), contents(&replica));
    eprintln!(
        "rapid: updates=5000 incremental_bytes={bytes} final_utf8_bytes={}",
        expected.len()
    );
}
#[test]
fn support_concurrent_updates_reverse_delivery_and_reconnect() {
    let left = new_document();
    let right = new_document();
    let left_text = left.get_or_insert_text("p1");
    let right_text = right.get_or_insert_text("p1");
    let mut left_updates = Vec::new();
    let mut right_updates = Vec::new();
    for _ in 0..300 {
        let before = left.transact().state_vector();
        left_text.push(&mut left.transact_mut(), "ž");
        left_updates.push(left.transact().encode_state_as_update_v1(&before));
        let before = right.transact().state_vector();
        right_text.push(&mut right.transact_mut(), "ř");
        right_updates.push(right.transact().encode_state_as_update_v1(&before));
    }
    for update in right_updates.iter().rev() {
        apply(&left, update);
    }
    for update in left_updates.iter().rev() {
        apply(&right, update);
    }
    assert_eq!(contents(&left), contents(&right));
    assert_eq!(contents(&left).chars().filter(|c| *c == 'ž').count(), 300);
    assert_eq!(contents(&left).chars().filter(|c| *c == 'ř').count(), 300);
    let reopened = new_document();
    apply(&reopened, &snapshot(&right));
    let frontier = reopened.transact().state_vector();
    left_text.push(&mut left.transact_mut(), " reconnect ✅");
    let missing = left.transact().encode_state_as_update_v1(&frontier);
    apply(&reopened, &missing);
    apply(&reopened, &missing); // lost acknowledgment retry is idempotent in Yrs.
    assert_eq!(contents(&left), contents(&reopened));
    assert!(!reopened.transact_mut().has_missing_updates());
    eprintln!("concurrent: authors=2 updates=600 reversed=true reconnect=true duplicate=true");
}
#[test]
fn support_large_update_has_explicit_boundary_limit() {
    let author = new_document();
    let value = "Příliš žluťoučký kůň 😀\n".repeat(20000);
    author
        .get_or_insert_text("p1")
        .push(&mut author.transact_mut(), &value);
    let update = snapshot(&author);
    let frame = Message::Sync(SyncMessage::Update(update.clone())).encode_v1();
    assert_eq!(
        decode_input(&frame, Access::Writer, frame.len() - 1),
        Err(InputError::TooLarge)
    );
    let replica = new_document();
    apply(&replica, &update);
    assert_eq!(contents(&replica), value);
    eprintln!(
        "large: utf8_bytes={} update_bytes={} frame_bytes={}",
        value.len(),
        update.len(),
        frame.len()
    );
}
