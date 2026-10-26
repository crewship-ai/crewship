use mooring_wordcraft::{
    command::Command,
    experimental_draft::{ClientError, DraftAccess, DraftReplica},
    Document,
};
use serde_json::{json, Value};
#[test]
fn foreign_delete_of_own_insert_is_refused_before_undo_history_pop() {
    let mut native: Value =
        serde_json::from_str(include_str!("fixtures/d1-engine-canonical.json")).unwrap();
    native["body"] = json!([{"kind":"para","text":"aa","runs":[{"len":2}]}]);
    let cp = Document::import(native, vec!["p_one".into()])
        .unwrap()
        .checkpoint()
        .unwrap();
    let mut own = DraftReplica::restore(&cp, 17, DraftAccess::Writer).unwrap();
    let mut peer = DraftReplica::restore(&cp, 18, DraftAccess::Writer).unwrap();
    let ins:Command=serde_json::from_value(json!({"kind":"text.insert","target_id":"p_one","at":{"kind":"start"},"text":"X","properties":{"bold":true,"italic":false}})).unwrap();
    let a = own.edit("A", ins).unwrap();
    peer.apply_verified_host_delta(17, &a.update, &a.targets)
        .unwrap();
    let before = peer.native().unwrap();
    let selection = peer
        .observe_native_selection(&before, "p_one", 0, 1)
        .unwrap();
    let after = Document::restore(&cp).unwrap().native().unwrap();
    let c = peer
        .native_transition_selected("C", &before, &after, &selection)
        .unwrap();
    own.apply_verified_host_delta(18, &c.update, &c.targets)
        .unwrap();
    let cut = own.optimistic_checkpoint().unwrap();
    assert_eq!(own.undo_e3("undo-A"), Err(ClientError::UnsupportedUndo));
    assert_eq!(
        own.undo_e3("undo-A-again"),
        Err(ClientError::UnsupportedUndo)
    );
    assert_eq!(own.optimistic_checkpoint().unwrap(), cut);
}

#[test]
fn foreign_delete_already_locally_deleted_by_pending_undo_refuses_redo() {
    let mut native: Value =
        serde_json::from_str(include_str!("fixtures/d1-engine-canonical.json")).unwrap();
    native["body"] = json!([{"kind":"para","text":"aa","runs":[{"len":2}]}]);
    let cp = Document::import(native, vec!["p_one".into()])
        .unwrap()
        .checkpoint()
        .unwrap();
    let mut own = DraftReplica::restore(&cp, 17, DraftAccess::Writer).unwrap();
    let mut peer = DraftReplica::restore(&cp, 18, DraftAccess::Writer).unwrap();
    let ins:Command=serde_json::from_value(json!({"kind":"text.insert","target_id":"p_one","at":{"kind":"start"},"text":"X","properties":{"bold":true,"italic":false}})).unwrap();
    let a = own.edit("A", ins).unwrap();
    peer.apply_verified_host_delta(17, &a.update, &a.targets)
        .unwrap();
    let own_undo = own.undo_e3("own-pending-undo").unwrap();
    let before = peer.native().unwrap();
    let selection = peer
        .observe_native_selection(&before, "p_one", 0, 1)
        .unwrap();
    let after = Document::restore(&cp).unwrap().native().unwrap();
    let c = peer
        .native_transition_selected("C", &before, &after, &selection)
        .unwrap();
    own.apply_verified_host_delta(18, &c.update, &c.targets)
        .unwrap();
    let cut = own.optimistic_checkpoint().unwrap();
    let visible = own.native().unwrap();
    assert_eq!(own.redo_e3("redo-A"), Err(ClientError::UnsupportedUndo));
    assert_eq!(
        own.redo_e3("redo-A-again"),
        Err(ClientError::UnsupportedUndo)
    );
    assert_eq!(own.optimistic_checkpoint().unwrap(), cut);
    assert_eq!(own.native().unwrap(), visible);
    assert!(!own_undo.update.is_empty());
}

#[test]
fn matched_own_undo_echo_keeps_redo_full_format_and_unknown_original_is_denied() {
    let mut native: Value =
        serde_json::from_str(include_str!("fixtures/d1-engine-canonical.json")).unwrap();
    native["body"] = json!([{"kind":"para","text":"aa","runs":[{"len":2}]}]);
    let cp = Document::import(native, vec!["p_one".into()])
        .unwrap()
        .checkpoint()
        .unwrap();
    let mut own = DraftReplica::restore(&cp, 17, DraftAccess::Writer).unwrap();
    let ins:Command=serde_json::from_value(json!({"kind":"text.insert","target_id":"p_one","at":{"kind":"start"},"text":"X😀","properties":{"bold":true,"italic":false}})).unwrap();
    own.edit("A", ins).unwrap();
    let full_insert = own.native().unwrap();
    let u = own.undo_e3("own-undo").unwrap();
    let data = mooring_wordcraft::document_update::encode(&u.update, &u.targets).unwrap();
    let cut = own.optimistic_checkpoint().unwrap();
    assert_eq!(
        own.apply_verified_own_document_update(&data, "unknown"),
        Err(ClientError::InvalidOperation)
    );
    assert_eq!(own.optimistic_checkpoint().unwrap(), cut);
    own.apply_verified_own_document_update(&data, "own-undo")
        .unwrap();
    assert_eq!(own.optimistic_checkpoint().unwrap(), cut);
    own.redo_e3("redo-own").unwrap();
    assert_eq!(own.native().unwrap(), full_insert);
}
#[test]
fn cumulative_foreign_set_conservatively_refuses_after_unrelated_peer_insert() {
    let mut native: Value =
        serde_json::from_str(include_str!("fixtures/d1-engine-canonical.json")).unwrap();
    native["body"] = json!([{"kind":"para","text":"aa","runs":[{"len":2}]}]);
    let cp = Document::import(native, vec!["p_one".into()])
        .unwrap()
        .checkpoint()
        .unwrap();
    let mut own = DraftReplica::restore(&cp, 17, DraftAccess::Writer).unwrap();
    let mut peer = DraftReplica::restore(&cp, 18, DraftAccess::Writer).unwrap();
    let insert = |text: &str| {
        serde_json::from_value(json!({"kind":"text.insert","target_id":"p_one","at":{"kind":"start"},"text":text,"properties":null})).unwrap()
    };
    let a = own.edit("A", insert("X")).unwrap();
    peer.apply_verified_host_delta(17, &a.update, &a.targets)
        .unwrap();
    let u = own.undo_e3("own-undo").unwrap();
    peer.apply_verified_host_delta(17, &u.update, &u.targets)
        .unwrap();
    peer.edit("peer-unrelated-insert", insert("P")).unwrap();
    // Actual state delta carries historical cumulative tombstones, although P
    // itself did not delete X. This is a documented conservative UX limitation.
    let peer_cp = peer.optimistic_checkpoint().unwrap();
    let delta = Document::restore(&peer_cp)
        .unwrap()
        .update_since(&cp)
        .unwrap();
    own.apply_verified_host_delta(18, &delta, &["p_one".into()])
        .unwrap();
    let cut = own.optimistic_checkpoint().unwrap();
    assert_eq!(own.redo_e3("redo-A"), Err(ClientError::UnsupportedUndo));
    assert_eq!(own.optimistic_checkpoint().unwrap(), cut);
}
