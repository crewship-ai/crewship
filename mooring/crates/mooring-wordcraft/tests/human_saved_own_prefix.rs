//! Host live slots below are SYNTHETIC authority inputs, never SQL execution.
use mooring_wordcraft::{
    command::Command,
    experimental_draft::{DraftAccess, DraftReplica, Proposal},
    experimental_human_basis::{
        self as basis, HostConfirmedBasis, HostSavedDelta, HostSavedOwnPrefix, HostSealedOriginal,
    },
    experimental_human_current::{prepare_current, Authority, HostWriter},
    Document,
};
use serde_json::{json, Value};
fn insert(text: &str) -> Command {
    serde_json::from_value(json!({"kind":"text.insert","target_id":"p_one","at":{"kind":"end"},"text":text,"properties":null})).unwrap()
}
fn prepared(base: &[u8], original: &Proposal) -> Vec<u8> {
    prepare_current(
        Authority::Writer(HostWriter::current(base, original.origin).unwrap()),
        &original.authored_frontier,
        &original.update,
        &original.targets,
    )
    .unwrap()
    .checkpoint()
    .unwrap()
}
fn saved<'a>(reference: &'a [u8], proposal: &'a Proposal) -> HostSavedDelta<'a> {
    HostSavedDelta::from_trusted_committed_store(
        reference,
        proposal.origin,
        &proposal.update,
        &proposal.targets,
    )
    .unwrap()
}
fn retained(
    base: &[u8],
    current: &[u8],
    original: &Proposal,
    history: &[HostSavedDelta<'_>],
    order: u64,
) -> Vec<u8> {
    let seal =
        HostSealedOriginal::from_trusted_original_store(original, b"synthetic-original", order)
            .unwrap();
    let context = HostConfirmedBasis::from_trusted_confirmed_store(
        base,
        current,
        original.origin,
        seal,
        order,
        history,
    )
    .unwrap();
    basis::prepare_confirmed_basis(basis::Authority::Writer(context))
        .unwrap()
        .checkpoint()
        .unwrap()
}
#[test]
fn original_b_survives_saved_own_a_and_peer_c_in_either_global_order() {
    for kind in ["insert", "format", "undo-insert"] {
        for peer_first in [false, true] {
            let mut native: Value =
                serde_json::from_str(include_str!("fixtures/d1-engine-canonical.json")).unwrap();
            native["body"] = json!([{"kind":"para","text":"aa","runs":[{"len":2}]}]);
            let base = Document::import(native, vec!["p_one".into()])
                .unwrap()
                .checkpoint()
                .unwrap();
            let mut alice = DraftReplica::restore(&base, 17, DraftAccess::Writer).unwrap();
            let base = if kind == "undo-insert" {
                let prior = alice.edit("prior", insert(" prior")).unwrap();
                prepared(&base, &prior)
            } else {
                base
            };
            let mut bob = DraftReplica::restore(&base, 18, DraftAccess::Writer).unwrap();
            let a_order = if kind == "undo-insert" { 2 } else { 1 };
            let original_b_order = a_order + 1;
            let a = match kind {
            "undo-insert" => alice.undo_e3("A").unwrap(),
            "format" => alice.edit("A", serde_json::from_value(json!({"kind":"text.format","target_id":"p_one","start":{"kind":"start"},"end":{"kind":"end"},"properties":{"bold":true}})).unwrap()).unwrap(),
            _ => alice.edit("A", insert(" A")).unwrap(),
        };
            let authored_a = alice.optimistic_checkpoint().unwrap(); // candidate only, NOT Saved
            let server_authored = prepared(&base, &a);
            eprintln!("own-prefix kind={kind} peer_first={peer_first} authored_bytes_equal={} full_native_equal={} frontier_equal={} client_bytes={} host_bytes={}",
            authored_a == server_authored,
            Document::restore(&authored_a).unwrap().native().unwrap() == Document::restore(&server_authored).unwrap().native().unwrap(),
            Document::restore(&authored_a).unwrap().state_vector() == Document::restore(&server_authored).unwrap().state_vector(),
            authored_a.len(), server_authored.len());
            let b = alice.edit("B", insert(" B😀")).unwrap();
            let c = bob.edit("C", insert(" C")).unwrap();
            let original_b = b.clone();
            let (global_cut_a, current) = if peer_first {
                let cut_c = prepared(&base, &c);
                let cut_a = retained(&base, &cut_c, &a, &[saved(b"saved-C", &c)], a_order);
                (cut_a.clone(), cut_a)
            } else {
                let cut_a = prepared(&base, &a);
                let current = retained(&base, &cut_a, &c, &[saved(b"saved-A", &a)], 1);
                (cut_a, current)
            };
            let history = if peer_first {
                vec![saved(b"saved-C", &c), saved(b"saved-A", &a)]
            } else {
                vec![saved(b"saved-A", &a), saved(b"saved-C", &c)]
            };
            // The unchanged old constructor must continue refusing own transcripts.
            let old = HostConfirmedBasis::from_trusted_confirmed_store(
                &base,
                &current,
                17,
                HostSealedOriginal::from_trusted_original_store(
                    &b,
                    b"original-B",
                    original_b_order,
                )
                .unwrap(),
                original_b_order,
                &history,
            );
            assert!(matches!(old, Err(basis::Error::UnqualifiedBasis)));
            let authored_digest = mooring_protocol::control::encode_checkpoint(&authored_a)
                .unwrap()
                .digest;
            let saved_digest = mooring_protocol::control::encode_checkpoint(&global_cut_a)
                .unwrap()
                .digest;
            let own = [HostSavedOwnPrefix::from_trusted_live_slot(
                HostSealedOriginal::from_trusted_original_store(&a, b"saved-A", a_order).unwrap(),
                &authored_digest,
                &saved_digest,
            )
            .unwrap()];
            let context = HostConfirmedBasis::from_trusted_live_store_with_own_prefix(
                &base,
                &current,
                17,
                HostSealedOriginal::from_trusted_original_store(
                    &b,
                    b"original-B",
                    original_b_order,
                )
                .unwrap(),
                original_b_order,
                &history,
                &own,
            )
            .unwrap();
            let candidate = basis::prepare_confirmed_basis(basis::Authority::Writer(context))
                .expect(
                "actual Saved own prefix must qualify original B without rewriting its frontier",
            );
            alice
                .apply_verified_host_delta(c.origin, &c.update, &c.targets)
                .unwrap();
            assert_eq!(
                candidate.native().unwrap(),
                alice.native().unwrap(),
                "FULL native global order peer_first={peer_first}"
            );
            let restored = Document::restore(&candidate.checkpoint().unwrap()).unwrap();
            assert_eq!(restored.block_ids().unwrap(), vec!["p_one"]);
            assert_eq!(restored.native().unwrap(), alice.native().unwrap());
            assert_eq!(b, original_b);
            for mode in [
                "missing-prefix",
                "reference",
                "origin",
                "raw",
                "targets",
                "order",
                "skip-order",
                "authored-digest",
                "saved-digest",
                "swapped-digests",
                "global-order",
                "frontier",
                "duplicate",
                "budget",
                "combined-history-budget",
                "invalid-digest",
            ] {
                if mode == "swapped-digests" && !peer_first {
                    continue;
                }
                let mut bad_a = a.clone();
                let mut bad_b = b.clone();
                if mode == "origin" {
                    bad_a.origin = 19;
                }
                if mode == "raw" {
                    bad_a.update[0] ^= 1;
                }
                if mode == "targets" {
                    bad_a.targets = vec!["other".into()];
                }
                if mode == "frontier" {
                    bad_b.authored_frontier = Document::restore(&current).unwrap().state_vector();
                }
                let mut transcript = if peer_first {
                    vec![saved(b"saved-C", &c), saved(b"saved-A", &a)]
                } else {
                    vec![saved(b"saved-A", &a), saved(b"saved-C", &c)]
                };
                if mode == "global-order" {
                    transcript.reverse();
                }
                if mode == "duplicate" {
                    transcript.push(saved(b"saved-A", &a));
                }
                if mode == "budget" {
                    transcript = (0..257).map(|_| saved(b"saved-A", &a)).collect();
                }
                let mut author_digest = authored_digest.clone();
                let mut actual_saved_digest = saved_digest.clone();
                if mode == "authored-digest" {
                    author_digest = mooring_protocol::control::encode_checkpoint(&base)
                        .unwrap()
                        .digest;
                }
                if mode == "saved-digest" {
                    actual_saved_digest = mooring_protocol::control::encode_checkpoint(&base)
                        .unwrap()
                        .digest;
                }
                if mode == "swapped-digests" {
                    std::mem::swap(&mut author_digest, &mut actual_saved_digest);
                }
                if mode == "invalid-digest" {
                    author_digest = "SHA256:BAD".into();
                }
                let reference: &[u8] = if mode == "reference" {
                    b"unknown-original"
                } else {
                    b"saved-A"
                };
                let order = if mode == "order" {
                    original_b_order
                } else {
                    a_order
                };
                let b_order = if mode == "skip-order" {
                    original_b_order + 1
                } else {
                    original_b_order
                };
                let prefix = HostSavedOwnPrefix::from_trusted_live_slot(
                    HostSealedOriginal::from_trusted_original_store(&bad_a, reference, order)
                        .unwrap(),
                    &author_digest,
                    &actual_saved_digest,
                );
                if mode == "invalid-digest" {
                    assert!(matches!(prefix, Err(basis::Error::InvalidHostContext)));
                    continue;
                }
                let own = if mode == "missing-prefix" {
                    vec![]
                } else {
                    vec![prefix.unwrap()]
                };
                let extra_history = (0..256).map(|_| {
                    mooring_wordcraft::experimental_human_current::HostCommittedOwnOperation::from_trusted_committed_host_store(17, &a.update)
                }).collect::<Vec<_>>();
                let result = HostConfirmedBasis::from_trusted_live_store_with_own_prefix(
                    &base,
                    &current,
                    17,
                    HostSealedOriginal::from_trusted_original_store(&bad_b, b"original-B", b_order)
                        .unwrap(),
                    b_order,
                    &transcript,
                    &own,
                )
                .and_then(|context| {
                    let context = if mode == "combined-history-budget" {
                        context.with_committed_own_history(&extra_history)?
                    } else {
                        context
                    };
                    basis::prepare_confirmed_basis(basis::Authority::Writer(context))
                });
                assert!(
                    result.is_err(),
                    "unqualified host witness accepted {mode}, peer_first={peer_first}"
                );
                assert_eq!(b, original_b);
            }
        }
    }
}
