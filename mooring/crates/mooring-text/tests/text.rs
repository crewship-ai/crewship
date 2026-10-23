use mooring_text::{decode_input, new_document, recover, Access, InputError, UnsavedDraft};
use yrs::sync::{Message, SyncMessage};
use yrs::updates::encoder::Encode;
use yrs::{GetString, ReadTxn, StateVector, Text, Transact};

fn update() -> Vec<u8> {
    let doc = new_document();
    let text = doc.get_or_insert_text("p1");
    let mut tx = doc.transact_mut();
    text.push(&mut tx, "Reader must not write");
    tx.encode_state_as_update_v1(&StateVector::default())
}

#[test]
fn p16_reader_cannot_write_with_either_update_or_sync_step2() {
    for msg in [
        SyncMessage::Update(update()),
        SyncMessage::SyncStep2(update()),
    ] {
        let bytes = Message::Sync(msg).encode_v1();
        assert_eq!(
            decode_input(&bytes, Access::Reader, 4096),
            Err(InputError::Forbidden)
        );
        assert!(decode_input(&bytes, Access::Writer, 4096).is_ok());
    }
}
#[test]
fn p16_reader_sync_step1_is_read_only_but_response_still_needs_delivery_gate() {
    let bytes = Message::Sync(SyncMessage::SyncStep1(StateVector::default())).encode_v1();
    assert!(decode_input(&bytes, Access::Reader, 4096).is_ok());
}
#[test]
fn p16_auth_custom_and_concatenated_frames_fail_closed() {
    for msg in [
        Message::Auth(None),
        Message::AwarenessQuery,
        Message::Custom(17, vec![1]),
    ] {
        assert_eq!(
            decode_input(&msg.encode_v1(), Access::Writer, 4096),
            Err(InputError::Unsupported)
        );
    }
    let mut bytes = Message::Sync(SyncMessage::SyncStep1(StateVector::default())).encode_v1();
    bytes.extend(Message::Sync(SyncMessage::Update(update())).encode_v1());
    assert_eq!(
        decode_input(&bytes, Access::Reader, 4096),
        Err(InputError::TrailingData)
    );
    assert_eq!(
        decode_input(&[0, 1, 10], Access::Writer, 4096),
        Err(InputError::Malformed)
    );
    assert_eq!(
        decode_input(&[0, 1, 10], Access::Writer, 2),
        Err(InputError::TooLarge)
    );
}
#[test]
fn p28_a_to_b_recovers_authoritative_text_and_preserves_dependent_draft() {
    let server = new_document();
    let canonical = server.get_or_insert_text("p1");
    canonical.push(&mut server.transact_mut(), "Confirmed");
    let snapshot = server
        .transact()
        .encode_state_as_update_v1(&StateVector::default());
    // Local A creates a new object; B edits its text. Both are rejected, so
    // neither rejected object IDs nor B's text may be blindly replayed.
    let local = new_document();
    let optimistic = local.get_or_insert_text("rejected-A");
    optimistic.push(&mut local.transact_mut(), "Draft B — čeština 👩‍💻");
    let draft = UnsavedDraft {
        op_id: "B".into(),
        text: optimistic.get_string(&local.transact()),
        reason: "dependency_rejected:A:permission_revoked".into(),
    };
    let recovery =
        recover(&snapshot, vec![draft.clone()]).unwrap_or_else(|e| panic!("{}", e.reason));
    assert_eq!(recovery.unsaved, vec![draft]);
    assert!(recovery
        .document
        .transact()
        .get_text("rejected-A")
        .is_none());
    assert_eq!(
        recovery
            .document
            .transact()
            .get_text("p1")
            .unwrap()
            .get_string(&recovery.document.transact()),
        "Confirmed"
    );
    assert_eq!(
        recovery
            .document
            .transact()
            .encode_state_as_update_v1(&StateVector::default()),
        snapshot
    );
}
#[test]
fn p28_bad_snapshot_returns_all_drafts_intact() {
    let draft = UnsavedDraft {
        op_id: "B".into(),
        text: "Unconfirmed".into(),
        reason: "target_deleted".into(),
    };
    let error = match recover(&[255], vec![draft.clone()]) {
        Ok(_) => panic!("invalid snapshot accepted"),
        Err(e) => e,
    };
    assert_eq!(error.unsaved, vec![draft]);
}
#[test]
fn p30_browser_utf16_offsets_match_astral_text() {
    let doc = new_document();
    let text = doc.get_or_insert_text("p1");
    text.push(&mut doc.transact_mut(), "A😀B");
    assert_eq!(text.len(&doc.transact()), 4);
    text.remove_range(&mut doc.transact_mut(), 1, 2);
    assert_eq!(text.get_string(&doc.transact()), "AB");
}

#[test]
fn p21_tiny_state_vector_allocation_bomb_is_rejected_before_yrs_decode() {
    // SyncStep1, 5-byte vector declaring 2^32-1 entries and no entry data.
    assert_eq!(
        decode_input(&[0, 0, 5, 255, 255, 255, 255, 15], Access::Reader, 1024),
        Err(InputError::Malformed)
    );
}

#[test]
fn p28_incomplete_causal_snapshot_is_not_presented_as_recovered() {
    let doc = new_document();
    let text = doc.get_or_insert_text("p1");
    text.push(&mut doc.transact_mut(), "A");
    let before_b = doc.transact().state_vector();
    text.push(&mut doc.transact_mut(), "B");
    let only_b = doc.transact().encode_state_as_update_v1(&before_b);
    let draft = UnsavedDraft {
        op_id: "B".into(),
        text: "B".into(),
        reason: "dependency_rejected".into(),
    };
    let error = match recover(&only_b, vec![draft.clone()]) {
        Ok(_) => panic!("partial snapshot accepted"),
        Err(e) => e,
    };
    assert_eq!(error.unsaved, vec![draft]);
    assert!(error.reason.contains("dependencies"));
}
