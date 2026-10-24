//! Required concurrent-human qualification; no transport authority is fabricated.
use mooring_wordcraft::{
    experimental_draft::{DraftAccess, DraftReplica},
    experimental_human_basis::{
        self as basis, HostConfirmedBasis, HostSavedDelta, HostSealedOriginal,
    },
    experimental_human_current::{prepare_current, Authority, HostWriter},
    Document,
};
use serde_json::{json, Value};
#[test]
fn two_original_native_client_deltas_must_survive_a_newer_peer_confirmation() {
    let mut before: Value =
        serde_json::from_str(include_str!("fixtures/d1-engine-canonical.json")).unwrap();
    before["body"] = json!([{"kind":"para","text":"aa","runs":[{"len":2}]}]);
    let checkpoint = Document::import(before.clone(), vec!["p_one".into()])
        .unwrap()
        .checkpoint()
        .unwrap();
    let mut alice = DraftReplica::restore(&checkpoint, 17, DraftAccess::Writer).unwrap();
    let mut bob = DraftReplica::restore(&checkpoint, 18, DraftAccess::Writer).unwrap();
    let alice_selection = alice
        .observe_native_selection(&before, "p_one", 0, 0)
        .unwrap();
    let bob_selection = bob
        .observe_native_selection(&before, "p_one", 1, 1)
        .unwrap();
    let mut after_a = before.clone();
    after_a["body"][0]["text"] = json!("aaa");
    after_a["body"][0]["runs"] = json!([{"len":3}]);
    let mut after_b = before.clone();
    after_b["body"][0]["text"] = json!("aXa");
    after_b["body"][0]["runs"] = json!([{"len":3}]);
    let a = alice
        .native_transition_selected("alice_original", &before, &after_a, &alice_selection)
        .unwrap();
    let b = bob
        .native_transition_selected("bob_original", &before, &after_b, &bob_selection)
        .unwrap();
    let original_b = b.update.clone();
    let original_basis = b.authored_frontier.clone();
    let accepted_a = prepare_current(
        Authority::Writer(HostWriter::current(&checkpoint, 17).unwrap()),
        &a.authored_frontier,
        &a.update,
        &a.targets,
    )
    .unwrap();
    let current = accepted_a.checkpoint().unwrap();
    alice
        .apply_verified_host_delta(b.origin, &b.update, &b.targets)
        .unwrap();
    bob.apply_verified_host_delta(a.origin, &a.update, &a.targets)
        .unwrap();
    assert_eq!(alice.native().unwrap(), bob.native().unwrap());
    assert_eq!(alice.native().unwrap()["body"][0]["text"], json!("aaXa"));
    // Deliberately expose the missing retained-basis path. Replacing Bob's
    // immutable observed frontier with current would falsify his original edit.
    assert!(prepare_current(
        Authority::Writer(HostWriter::current(&current, 18).unwrap()),
        &b.authored_frontier,
        &b.update,
        &b.targets
    )
    .is_err());
    // Synthetic trusted-host qualification only: no production admission ledger
    // currently issues this origin/ordinal or binds the complete atomic packet.
    let saved = [HostSavedDelta::from_trusted_committed_store(
        b"synthetic-saved-alice",
        a.origin,
        &a.update,
        &a.targets,
    )
    .unwrap()];
    let sealed =
        HostSealedOriginal::from_trusted_original_store(&b, b"synthetic-original-bob", 1).unwrap();
    let host = HostConfirmedBasis::from_trusted_confirmed_store(
        &checkpoint,
        &current,
        18,
        sealed,
        1,
        &saved,
    )
    .unwrap();
    let accepted_b = basis::prepare_confirmed_basis(basis::Authority::Writer(host))
        .expect("retained confirmed basis qualifies original concurrent Bob bytes");
    assert_eq!(accepted_b.native().unwrap(), alice.native().unwrap());
    assert_eq!(b.update, original_b);
    assert_eq!(b.authored_frontier, original_basis);
}

fn fixture() -> (Value, Vec<u8>) {
    let mut native: Value =
        serde_json::from_str(include_str!("fixtures/d1-engine-canonical.json")).unwrap();
    native["body"] = json!([{"kind":"para","text":"aa","runs":[{"len":2}]}]);
    let cp = Document::import(native.clone(), vec!["p_one".into()])
        .unwrap()
        .checkpoint()
        .unwrap();
    (native, cp)
}
fn retained(
    base: &[u8],
    current: &[u8],
    proposal: &mooring_wordcraft::experimental_draft::Proposal,
    history: &[HostSavedDelta<'_>],
    ordinal: u64,
) -> Result<mooring_wordcraft::experimental_human_current::Candidate, basis::Error> {
    let sealed =
        HostSealedOriginal::from_trusted_original_store(proposal, b"synthetic-original", ordinal)?;
    let host = HostConfirmedBasis::from_trusted_confirmed_store(
        base,
        current,
        proposal.origin,
        sealed,
        ordinal,
        history,
    )?;
    basis::prepare_confirmed_basis(basis::Authority::Writer(host))
}
#[test]
fn actual_client_same_word_format_and_insert_merge_both_orders_with_peer_only_undo() {
    use mooring_wordcraft::command::{Anchor, Command};
    use std::collections::BTreeMap;
    for patch in [Some(true), Some(false), None] {
        let (_, cp) = fixture();
        let mut alice = DraftReplica::restore(&cp, 17, DraftAccess::Writer).unwrap();
        let mut bob = DraftReplica::restore(&cp, 18, DraftAccess::Writer).unwrap();
        let a = alice
            .edit(
                "alice_bold",
                Command::Format {
                    target_id: "p_one".into(),
                    start: Anchor::Start {},
                    end: Anchor::End {},
                    properties: BTreeMap::from([("bold".into(), Some(true))]),
                },
            )
            .unwrap();
        let b = bob
            .edit(
                "bob_format",
                Command::Format {
                    target_id: "p_one".into(),
                    start: Anchor::Start {},
                    end: Anchor::End {},
                    properties: BTreeMap::from([
                        ("bold".into(), patch),
                        ("italic".into(), Some(true)),
                    ]),
                },
            )
            .unwrap();
        let a_native = alice.native().unwrap();
        let b_native = bob.native().unwrap();
        alice
            .apply_verified_host_delta(b.origin, &b.update, &b.targets)
            .unwrap();
        bob.apply_verified_host_delta(a.origin, &a.update, &a.targets)
            .unwrap();
        let expected = alice.native().unwrap();
        assert_eq!(expected, bob.native().unwrap());
        for (first, second) in [(&a, &b), (&b, &a)] {
            let confirmed = prepare_current(
                Authority::Writer(HostWriter::current(&cp, first.origin).unwrap()),
                &first.authored_frontier,
                &first.update,
                &first.targets,
            )
            .unwrap()
            .checkpoint()
            .unwrap();
            let history = [HostSavedDelta::from_trusted_committed_store(
                b"synthetic-saved-first",
                first.origin,
                &first.update,
                &first.targets,
            )
            .unwrap()];
            let merged = retained(&cp, &confirmed, second, &history, 1).unwrap();
            assert_eq!(merged.native().unwrap(), expected);
            assert_eq!(
                Document::restore(&merged.checkpoint().unwrap())
                    .unwrap()
                    .block_ids()
                    .unwrap(),
                vec!["p_one"]
            );
        }
        alice.undo("alice_undo").unwrap();
        assert_eq!(alice.native().unwrap(), b_native);
        alice.redo("alice_redo").unwrap();
        assert_eq!(alice.native().unwrap(), expected);
        bob.undo("bob_undo").unwrap();
        assert_eq!(bob.native().unwrap(), a_native);
        bob.redo("bob_redo").unwrap();
        assert_eq!(bob.native().unwrap(), expected);
    }
}
#[test]
fn missing_lineage_same_frontier_state_swap_and_wrong_saved_origin_are_uncertain() {
    use mooring_wordcraft::command::{Anchor, Command};
    let (_, cp) = fixture();
    let mut alice = DraftReplica::restore(&cp, 17, DraftAccess::Writer).unwrap();
    let mut bob = DraftReplica::restore(&cp, 18, DraftAccess::Writer).unwrap();
    let edit = |text: &str| Command::Insert {
        target_id: "p_one".into(),
        at: Anchor::Start {},
        text: text.into(),
        properties: None,
    };
    let a = alice.edit("alice", edit("A")).unwrap();
    let b = bob.edit("bob", edit("B")).unwrap();
    let current = prepare_current(
        Authority::Writer(HostWriter::current(&cp, 17).unwrap()),
        &a.authored_frontier,
        &a.update,
        &a.targets,
    )
    .unwrap()
    .checkpoint()
    .unwrap();
    assert!(matches!(
        retained(&cp, &current, &b, &[], 1),
        Err(basis::Error::UnqualifiedBasis)
    ));
    // An independently imported document has a fresh random CRDT origin. Make
    // an actual valid state with THE SAME item IDs/clocks but different content
    // to prove that matching state vectors alone do not establish lineage.
    let mut swapped: Value = serde_json::from_slice(&cp).unwrap();
    let mut raw = mooring_protocol::boundary::decode_base64(
        swapped["update_base64"].as_str().unwrap(),
        16 * 1024 * 1024,
    )
    .unwrap();
    let positions: Vec<_> = raw
        .windows(3)
        .enumerate()
        .filter_map(|(i, b)| (b == [2, b'a', b'a']).then_some(i))
        .collect();
    assert_eq!(positions.len(), 1);
    let at = positions[0];
    raw[at + 1] = b'z';
    raw[at + 2] = b'z';
    swapped["native"]["body"][0]["text"] = json!("zz");
    swapped["update_base64"] =
        json!(mooring_protocol::boundary::encode_base64(&raw, 16 * 1024 * 1024).unwrap());
    let swapped = Document::restore(&serde_json::to_vec(&swapped).unwrap())
        .unwrap()
        .checkpoint()
        .unwrap();
    assert_eq!(
        mooring_wordcraft::experimental_human_current::checkpoint_frontier(&cp).unwrap(),
        mooring_wordcraft::experimental_human_current::checkpoint_frontier(&swapped).unwrap()
    );
    let history = [HostSavedDelta::from_trusted_committed_store(
        b"synthetic-a",
        a.origin,
        &a.update,
        &a.targets,
    )
    .unwrap()];
    assert!(matches!(
        retained(&swapped, &current, &b, &history, 1),
        Err(basis::Error::UnqualifiedBasis)
    ));
    let wrong =
        [
            HostSavedDelta::from_trusted_committed_store(b"synthetic-a", 19, &a.update, &a.targets)
                .unwrap(),
        ];
    assert!(matches!(
        retained(&cp, &current, &b, &wrong, 1),
        Err(basis::Error::UnqualifiedBasis)
    ));
    assert_eq!(Document::restore(&cp).unwrap().checkpoint().unwrap(), cp);
}
#[test]
fn host_original_order_origin_and_transcript_budget_cannot_be_replaced_by_frontier() {
    use mooring_wordcraft::command::{Anchor, Command};
    let (_, cp) = fixture();
    let mut local = DraftReplica::restore(&cp, 17, DraftAccess::Writer).unwrap();
    let p = local
        .edit(
            "first",
            Command::Insert {
                target_id: "p_one".into(),
                at: Anchor::Start {},
                text: "X".into(),
                properties: None,
            },
        )
        .unwrap();
    let sealed =
        || HostSealedOriginal::from_trusted_original_store(&p, b"synthetic-original", 2).unwrap();
    assert!(matches!(
        HostConfirmedBasis::from_trusted_confirmed_store(&cp, &cp, 17, sealed(), 1, &[]),
        Err(basis::Error::UnqualifiedBasis)
    ));
    assert!(matches!(
        HostConfirmedBasis::from_trusted_confirmed_store(&cp, &cp, 18, sealed(), 2, &[]),
        Err(basis::Error::InvalidHostContext)
    ));
    let history: Vec<_> = (0..257)
        .map(|_| {
            HostSavedDelta::from_trusted_committed_store(b"synthetic-a", 18, &p.update, &p.targets)
                .unwrap()
        })
        .collect();
    assert!(matches!(
        HostConfirmedBasis::from_trusted_confirmed_store(&cp, &cp, 17, sealed(), 2, &history),
        Err(basis::Error::UnqualifiedBasis)
    ));
    let bytes = vec![0; 1100];
    let history: Vec<_> = (0..256)
        .map(|_| {
            HostSavedDelta::from_trusted_committed_store(b"synthetic-a", 18, &bytes, &p.targets)
                .unwrap()
        })
        .collect();
    assert!(matches!(
        HostConfirmedBasis::from_trusted_confirmed_store(&cp, &cp, 17, sealed(), 2, &history),
        Err(basis::Error::UnqualifiedBasis)
    ));
    assert!(matches!(
        basis::prepare_confirmed_basis(basis::Authority::Reader),
        Err(basis::Error::Forbidden)
    ));
}

#[test]
fn native_selected_disjoint_deletions_merge_both_orders_without_reallocating_original_ids() {
    let (before, cp) = fixture();
    let mut a = DraftReplica::restore(&cp, 17, DraftAccess::Writer).unwrap();
    let mut b = DraftReplica::restore(&cp, 18, DraftAccess::Writer).unwrap();
    let sa = a.observe_native_selection(&before, "p_one", 0, 1).unwrap();
    let sb = b.observe_native_selection(&before, "p_one", 1, 2).unwrap();
    let mut after = before.clone();
    after["body"][0]["text"] = json!("a");
    after["body"][0]["runs"] = json!([{"len":1}]);
    let pa = a
        .native_transition_selected("delete_a", &before, &after, &sa)
        .unwrap();
    let pb = b
        .native_transition_selected("delete_b", &before, &after, &sb)
        .unwrap();
    let original_a = pa.update.clone();
    let original_b = pb.update.clone();
    let peer_only_b = b.native().unwrap();
    a.apply_verified_host_delta(pb.origin, &pb.update, &pb.targets)
        .unwrap();
    b.apply_verified_host_delta(pa.origin, &pa.update, &pa.targets)
        .unwrap();
    assert_eq!(a.native().unwrap(), b.native().unwrap());
    assert_eq!(a.native().unwrap()["body"][0]["text"], json!(""));
    for (first, second) in [(&pa, &pb), (&pb, &pa)] {
        let current = prepare_current(
            Authority::Writer(HostWriter::current(&cp, first.origin).unwrap()),
            &first.authored_frontier,
            &first.update,
            &first.targets,
        )
        .unwrap()
        .checkpoint()
        .unwrap();
        let saved = [HostSavedDelta::from_trusted_committed_store(
            b"synthetic-first-delete",
            first.origin,
            &first.update,
            &first.targets,
        )
        .unwrap()];
        let merged = retained(&cp, &current, second, &saved, 1).unwrap();
        assert_eq!(merged.native().unwrap(), a.native().unwrap());
    }
    a.undo("undo_delete_a").unwrap();
    assert_eq!(a.native().unwrap(), peer_only_b);
    assert_eq!(pa.update, original_a);
    assert_eq!(pb.update, original_b);
}
#[test]
fn saved_own_format_history_qualifies_redo_after_peer_without_optimistic_predecessors() {
    use mooring_wordcraft::{
        command::{Anchor, Command},
        experimental_human_current::HostCommittedOwnOperation,
    };
    use std::collections::BTreeMap;
    let (_, cp) = fixture();
    let mut a = DraftReplica::restore(&cp, 17, DraftAccess::Writer).unwrap();
    let mut b = DraftReplica::restore(&cp, 18, DraftAccess::Writer).unwrap();
    let format = |field: &str| Command::Format {
        target_id: "p_one".into(),
        start: Anchor::Start {},
        end: Anchor::End {},
        properties: BTreeMap::from([(field.into(), Some(true))]),
    };
    let pa = a.edit("a_bold", format("bold")).unwrap();
    let pb = b.edit("b_italic", format("italic")).unwrap();
    let peer_only = b.native().unwrap();
    a.apply_verified_host_delta(pb.origin, &pb.update, &pb.targets)
        .unwrap();
    b.apply_verified_host_delta(pa.origin, &pa.update, &pa.targets)
        .unwrap();
    let combined = a.native().unwrap();
    let current_a = prepare_current(
        Authority::Writer(HostWriter::current(&cp, 17).unwrap()),
        &pa.authored_frontier,
        &pa.update,
        &pa.targets,
    )
    .unwrap()
    .checkpoint()
    .unwrap();
    let saved = [HostSavedDelta::from_trusted_committed_store(
        b"synthetic-saved-a",
        17,
        &pa.update,
        &pa.targets,
    )
    .unwrap()];
    let combined_cp = retained(&cp, &current_a, &pb, &saved, 1)
        .unwrap()
        .checkpoint()
        .unwrap();
    let undo = a.undo("a_undo").unwrap();
    let undone = retained(&combined_cp, &combined_cp, &undo, &[], 2).unwrap();
    assert_eq!(undone.native().unwrap(), peer_only);
    let undone_cp = undone.checkpoint().unwrap();
    let redo = a.redo("a_redo").unwrap();
    let history = [
        HostCommittedOwnOperation::from_trusted_committed_host_store(17, &pa.update),
        HostCommittedOwnOperation::from_trusted_committed_host_store(17, &undo.update),
    ];
    let original =
        HostSealedOriginal::from_trusted_original_store(&redo, b"synthetic-saved-redo", 3).unwrap();
    let host = HostConfirmedBasis::from_trusted_confirmed_store(
        &undone_cp,
        &undone_cp,
        17,
        original,
        3,
        &[],
    )
    .unwrap()
    .with_committed_own_history(&history)
    .unwrap();
    let restored = basis::prepare_confirmed_basis(basis::Authority::Writer(host)).unwrap();
    assert_eq!(restored.native().unwrap(), combined);
    assert_eq!(a.native().unwrap(), combined);
}

#[test]
fn unknown_delete_clock_history_cannot_hide_inside_otherwise_real_saved_delta() {
    use mooring_wordcraft::command::{Anchor, Command};
    let (_, cp) = fixture();
    let mut a = DraftReplica::restore(&cp, 17, DraftAccess::Writer).unwrap();
    let mut b = DraftReplica::restore(&cp, 18, DraftAccess::Writer).unwrap();
    let edit = |text: &str| Command::Insert {
        target_id: "p_one".into(),
        at: Anchor::Start {},
        text: text.into(),
        properties: None,
    };
    let pa = a.edit("a", edit("A")).unwrap();
    let pb = b.edit("b", edit("B")).unwrap();
    let current = prepare_current(
        Authority::Writer(HostWriter::current(&cp, 17).unwrap()),
        &pa.authored_frontier,
        &pa.update,
        &pa.targets,
    )
    .unwrap()
    .checkpoint()
    .unwrap();
    let mut hidden_future = pa.update.clone();
    assert_eq!(hidden_future.pop(), Some(0));
    // Retain the REAL insertion, replace its empty DS with a deletion of an
    // unallocated foreign clock. Neither a pending DS nor absence of a missing
    // insertion flag is authority to silently ignore unavailable history.
    hidden_future.extend_from_slice(&[1, 19, 1, 0, 1]);
    let history = [HostSavedDelta::from_trusted_committed_store(
        b"synthetic-malformed-history",
        17,
        &hidden_future,
        &pa.targets,
    )
    .unwrap()];
    assert!(matches!(
        retained(&cp, &current, &pb, &history, 1),
        Err(basis::Error::UnqualifiedBasis)
    ));
}

#[test]
fn hidden_current_roots_and_unsealed_own_predecessors_never_become_confirmed_basis() {
    use mooring_text::yrs::{Map, ReadTxn, StateVector, Transact};
    use mooring_wordcraft::command::{Anchor, Command};
    let (_, cp) = fixture();
    let mut local = DraftReplica::restore(&cp, 17, DraftAccess::Writer).unwrap();
    let edit = |text: &str| Command::Insert {
        target_id: "p_one".into(),
        at: Anchor::Start {},
        text: text.into(),
        properties: None,
    };
    let first = local.edit("unsettled_a", edit("A")).unwrap();
    let second = local.edit("dependent_b", edit("B")).unwrap();
    assert!(matches!(
        retained(&cp, &cp, &second, &[], 2),
        Err(basis::Error::UnqualifiedBasis)
    ));
    let mut forged = second.clone();
    forged.authored_frontier =
        mooring_wordcraft::experimental_human_current::checkpoint_frontier(&cp).unwrap();
    assert!(matches!(
        retained(&cp, &cp, &forged, &[], 2),
        Err(basis::Error::UnqualifiedBasis)
    ));
    let mut envelope: Value = serde_json::from_slice(&cp).unwrap();
    let raw = mooring_protocol::boundary::decode_base64(
        envelope["update_base64"].as_str().unwrap(),
        16 * 1024 * 1024,
    )
    .unwrap();
    let doc = mooring_text::recover(&raw, vec![]).unwrap().document;
    doc.get_or_insert_map("hidden.unqualified")
        .insert(&mut doc.transact_mut(), "key", "opaque");
    let update = doc
        .transact()
        .encode_state_as_update_v1(&StateVector::default());
    envelope["update_base64"] =
        json!(mooring_protocol::boundary::encode_base64(&update, 16 * 1024 * 1024).unwrap());
    let hidden = serde_json::to_vec(&envelope).unwrap();
    assert!(matches!(
        retained(&cp, &hidden, &first, &[], 1),
        Err(basis::Error::UnqualifiedBasis)
    ));
    assert_eq!(second.update, forged.update);
}
