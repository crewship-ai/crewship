//! Synthetic trusted-host inputs, never SQL/admission execution evidence.
use mooring_wordcraft::{
    command::Command,
    document_update,
    experimental_draft::{DraftAccess, DraftReplica, Proposal},
    experimental_human_basis::{
        self as basis, HostBasisViews, HostConfirmedBasis, HostSavedDelta, HostSavedOwnPrefix,
        HostSealedOriginal,
    },
    experimental_human_current::{self as current, HostCommittedOwnOperation, HostWriter},
    Document,
};
use serde_json::{json, Value};
fn insert(target: &str, text: &str) -> Command {
    serde_json::from_value(json!({"kind":"text.insert","target_id":target,"at":{"kind":"end"},"text":text,"properties":null})).unwrap()
}
fn saved<'a>(reference: &'a [u8], p: &'a Proposal) -> HostSavedDelta<'a> {
    HostSavedDelta::from_trusted_committed_store(reference, p.origin, &p.update, &p.targets)
        .unwrap()
}
fn prepare(cp: &[u8], p: &Proposal) -> Vec<u8> {
    current::prepare_current(
        current::Authority::Writer(HostWriter::current(cp, p.origin).unwrap()),
        &p.authored_frontier,
        &p.update,
        &p.targets,
    )
    .unwrap()
    .checkpoint()
    .unwrap()
}
fn digest(cp: &[u8]) -> String {
    mooring_protocol::control::encode_checkpoint(cp)
        .unwrap()
        .digest
}
fn retained(
    base: &[u8],
    now: &[u8],
    p: &Proposal,
    history: &[HostSavedDelta<'_>],
    order: u64,
) -> Vec<u8> {
    let host = HostConfirmedBasis::from_trusted_confirmed_store(
        base,
        now,
        p.origin,
        HostSealedOriginal::from_trusted_original_store(p, b"synthetic-original", order).unwrap(),
        order,
        history,
    )
    .unwrap();
    basis::prepare_confirmed_basis(basis::Authority::Writer(host))
        .unwrap()
        .checkpoint()
        .unwrap()
}
#[test]
fn pending_own_undo_then_observed_delete_only_peer_then_b_excludes_unobserved_d() {
    for a_kind in ["insert", "format", "undo-insert"] {
        for peer_first in [true, false] {
            let mut native: Value =
                serde_json::from_str(include_str!("fixtures/d1-engine-canonical.json")).unwrap();
            native["body"] = json!([{"kind":"para","text":"alpha","runs":[{"len":5}]},{"kind":"para","text":"beta","runs":[{"len":4}]}]);
            let initial = Document::import(native, vec!["p0".into(), "p1".into()])
                .unwrap()
                .checkpoint()
                .unwrap();
            let mut own = DraftReplica::restore(&initial, 17, DraftAccess::Writer).unwrap();
            let h = own.edit("H", insert("p0", " own")).unwrap();
            let replay_basis_1 = prepare(&initial, &h); // actual SavedH, revision1 in host fixture
            let a=match a_kind {
        "undo-insert"=>own.undo_e3("A").unwrap(),
        "format"=>own.edit("A",serde_json::from_value(json!({"kind":"text.format","target_id":"p0","start":{"kind":"start"},"end":{"kind":"end"},"properties":{"bold":true}})).unwrap()).unwrap(),
        _=>own.edit("A",insert("p0"," A")).unwrap(),
    }; // original A.base_revision=1, order2
            let authored_a = own.optimistic_checkpoint().unwrap();
            let mut peer = DraftReplica::restore(&replay_basis_1, 18, DraftAccess::Writer).unwrap();
            let c=peer.edit("C",serde_json::from_value(json!({"kind":"text.delete","target_id":"p1","start":{"kind":"start"},"end":{"kind":"end"}})).unwrap()).unwrap();
            assert!(
                document_update::allocation_origins(&c.update)
                    .unwrap()
                    .is_empty(),
                "C must be an actual delete-only observation, not an SV-based proxy"
            );
            own.apply_verified_document_update(
                &document_update::encode(&c.update, &c.targets).unwrap(),
            )
            .unwrap();
            let b = own.edit("B", insert("p0", " B😀")).unwrap();
            let authored_b = own.optimistic_checkpoint().unwrap();
            let immutable_b = b.clone();
            let (a_revision, c_revision) = if peer_first { (3, 2) } else { (2, 3) };
            let (global_a, actual_b_basis) = if peer_first {
                let c_cut = prepare(&replay_basis_1, &c);
                let a_cut = retained(&replay_basis_1, &c_cut, &a, &[saved(b"saved-C", &c)], 2);
                (a_cut, c_cut)
            } else {
                let a_cut = prepare(&replay_basis_1, &a);
                let c_cut = retained(&replay_basis_1, &a_cut, &c, &[saved(b"saved-A", &a)], 1);
                (a_cut, c_cut)
            };
            let after_both = if peer_first {
                &global_a
            } else {
                &actual_b_basis
            };
            let mut unobserved =
                DraftReplica::restore(after_both, 19, DraftAccess::Writer).unwrap();
            let d = unobserved.edit("D", insert("p1", "unobserved D")).unwrap();
            let current_4 = prepare(after_both, &d);
            let mut transcript = vec![
                saved(b"saved-C", &c)
                    .with_saved_revision(c_revision)
                    .unwrap(),
                saved(b"saved-A", &a)
                    .with_saved_revision(a_revision)
                    .unwrap(),
            ];
            if !peer_first {
                transcript.swap(0, 1);
            }
            transcript.push(saved(b"saved-D", &d).with_saved_revision(4).unwrap());
            let authored_a_digest = digest(&authored_a);
            let saved_a_digest = digest(&global_a);
            let authored_b_digest = digest(&authored_b);
            let own_prefix = [HostSavedOwnPrefix::from_trusted_live_slot(
                HostSealedOriginal::from_trusted_original_store(&a, b"saved-A", 2)
                    .unwrap()
                    .with_original_base_revision(1),
                &authored_a_digest,
                &saved_a_digest,
            )
            .unwrap()];
            let views = HostBasisViews::from_trusted_live_store(&actual_b_basis, c_revision)
                .with_replay_basis(&replay_basis_1, 1)
                .unwrap();
            let prior_history =
                [HostCommittedOwnOperation::from_trusted_committed_host_store(17, &h.update)];
            let host = HostConfirmedBasis::from_trusted_live_store_with_basis_views(
                views,
                &current_4,
                17,
                HostSealedOriginal::from_trusted_original_store(&b, b"original-B", 3)
                    .unwrap()
                    .with_original_base_revision(c_revision),
                3,
                &transcript,
                &own_prefix,
            )
            .unwrap()
            .with_committed_own_history(&prior_history)
            .unwrap();
            let candidate = basis::prepare_confirmed_basis_with_authored_cut(
                basis::Authority::Writer(host),
                &authored_b_digest,
            )
            .expect("REQUIRED E3: exact authored A+C, not A or A+C+D");
            own.apply_verified_document_update(
                &document_update::encode(&d.update, &d.targets).unwrap(),
            )
            .unwrap();
            assert_eq!(
                candidate.native().unwrap(),
                own.native().unwrap(),
                "FULL native kind={a_kind} peer_first={peer_first}"
            );
            assert_eq!(
                Document::restore(&candidate.checkpoint().unwrap())
                    .unwrap()
                    .block_ids()
                    .unwrap(),
                vec!["p0", "p1"]
            );
            assert_eq!(b, immutable_b);
            // Exercise adversaries on the GC-sensitive C-before-A case, with no weakening
            // of the six positive full-native/digest/ID checks above.
            if a_kind == "undo-insert" && peer_first {
                for mode in [
                    "missing-replay",
                    "wrong-replay-state",
                    "wrong-original-basis",
                    "wrong-B-base",
                    "B-includes-unobserved-D",
                    "wrong-A-base",
                    "missing-C",
                    "missing-saved-label",
                    "missing-A-base",
                    "saved-order",
                    "swapped-digests",
                    "wrong-saved-cut",
                    "wrong-B-digest",
                    "prefix-order",
                ] {
                    let mut entries = vec![
                        saved(b"saved-C", &c).with_saved_revision(2).unwrap(),
                        saved(b"saved-A", &a).with_saved_revision(3).unwrap(),
                        saved(b"saved-D", &d).with_saved_revision(4).unwrap(),
                    ];
                    if mode == "missing-C" {
                        entries.remove(0);
                    }
                    if mode == "missing-saved-label" {
                        entries[0] = saved(b"saved-C", &c);
                    }
                    if mode == "saved-order" {
                        entries.swap(0, 1);
                    }
                    let actual_cp = if mode == "wrong-original-basis" {
                        &global_a
                    } else if mode == "B-includes-unobserved-D" {
                        &current_4
                    } else {
                        &actual_b_basis
                    };
                    let actual_rev = if mode == "B-includes-unobserved-D" {
                        4
                    } else {
                        2
                    };
                    let view = HostBasisViews::from_trusted_live_store(actual_cp, actual_rev);
                    let view = if mode == "missing-replay" {
                        view
                    } else {
                        view.with_replay_basis(
                            if mode == "wrong-replay-state" {
                                &initial
                            } else {
                                &replay_basis_1
                            },
                            1,
                        )
                        .unwrap()
                    };
                    let mut a_seal = HostSealedOriginal::from_trusted_original_store(
                        &a,
                        b"saved-A",
                        if mode == "prefix-order" { 3 } else { 2 },
                    )
                    .unwrap();
                    if mode != "missing-A-base" {
                        a_seal = a_seal.with_original_base_revision(if mode == "wrong-A-base" {
                            2
                        } else {
                            1
                        });
                    }
                    let (author_hash, saved_hash) = if mode == "swapped-digests" {
                        (&saved_a_digest, &authored_a_digest)
                    } else if mode == "wrong-saved-cut" {
                        (&authored_a_digest, &authored_b_digest)
                    } else {
                        (&authored_a_digest, &saved_a_digest)
                    };
                    let prefixes = [HostSavedOwnPrefix::from_trusted_live_slot(
                        a_seal,
                        author_hash,
                        saved_hash,
                    )
                    .unwrap()];
                    let b_base = if mode == "wrong-B-base" {
                        3
                    } else {
                        actual_rev
                    };
                    let context = HostConfirmedBasis::from_trusted_live_store_with_basis_views(
                        view,
                        &current_4,
                        17,
                        HostSealedOriginal::from_trusted_original_store(&b, b"original-B", 3)
                            .unwrap()
                            .with_original_base_revision(b_base),
                        3,
                        &entries,
                        &prefixes,
                    );
                    let result = context
                        .and_then(|host| host.with_committed_own_history(&prior_history))
                        .and_then(|host| {
                            basis::prepare_confirmed_basis_with_authored_cut(
                                basis::Authority::Writer(host),
                                if mode == "wrong-B-digest" {
                                    &authored_a_digest
                                } else {
                                    &authored_b_digest
                                },
                            )
                        });
                    assert!(
                        result.is_err(),
                        "unqualified observed-peer witness accepted {mode}"
                    );
                    assert_eq!(b, immutable_b);
                }
            }
        }
    }
}
