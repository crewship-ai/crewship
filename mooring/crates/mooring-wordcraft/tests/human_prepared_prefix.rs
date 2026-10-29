//! SYNTHETIC same-writer candidate context. Prepared is NEVER durable Saved.
use mooring_wordcraft::{
    command::Command,
    experimental_draft::{DraftAccess, DraftReplica, Proposal},
    experimental_human_basis::{
        self as basis, HostBasisViews, HostConfirmedBasis, HostPreparedDelta,
        HostPreparedOwnPrefix, HostPreparedPrefix, HostSavedDelta, HostSealedOriginal,
    },
    experimental_human_current::{prepare_current, Authority, HostWriter},
    Document,
};
use serde_json::{json, Value};
fn insert(text: &str) -> Command {
    serde_json::from_value(json!({"kind":"text.insert","target_id":"p_one","at":{"kind":"end"},"text":text,"properties":null})).unwrap()
}
fn hash(raw: &[u8]) -> String {
    mooring_protocol::control::encode_checkpoint(raw)
        .unwrap()
        .digest
}
fn prepared(base: &[u8], p: &Proposal) -> Vec<u8> {
    prepare_current(
        Authority::Writer(HostWriter::current(base, p.origin).unwrap()),
        &p.authored_frontier,
        &p.update,
        &p.targets,
    )
    .unwrap()
    .checkpoint()
    .unwrap()
}
#[test]
fn earlier_same_transaction_candidate_a_is_not_saved_but_original_b_requires_it() {
    for kind in ["insert", "format", "undo-insert"] {
        for peer_first in [false, true] {
            let mut native: Value =
                serde_json::from_str(include_str!("fixtures/d1-engine-canonical.json")).unwrap();
            native["body"] = json!([{"kind":"para","text":"aa","runs":[{"len":2}]}]);
            let initial = Document::import(native, vec!["p_one".into()])
                .unwrap()
                .checkpoint()
                .unwrap();
            let mut alice = DraftReplica::restore(&initial, 17, DraftAccess::Writer).unwrap();
            let (base, rev, order) = if kind == "undo-insert" {
                let h = alice.edit("H", insert(" prior")).unwrap();
                (prepared(&initial, &h), 1, 2)
            } else {
                (initial, 0, 1)
            };
            let mut bob = DraftReplica::restore(&base, 18, DraftAccess::Writer).unwrap();
            let a=match kind {"format"=>alice.edit("A",serde_json::from_value(json!({"kind":"text.format","target_id":"p_one","start":{"kind":"start"},"end":{"kind":"end"},"properties":{"bold":true}})).unwrap()).unwrap(),"undo-insert"=>alice.undo_e3("A").unwrap(),_=>alice.edit("A",insert(" A")).unwrap()};
            let authored_a = alice.optimistic_checkpoint().unwrap();
            let b = alice.edit("B", insert(" B😀")).unwrap();
            let original = b.clone();
            let authored_b = hash(&alice.optimistic_checkpoint().unwrap());
            let c = bob.edit("C", insert(" peer")).unwrap();
            let (txstart, history) = if peer_first {
                let cut = prepared(&base, &c);
                let saved = HostSavedDelta::from_trusted_committed_store(
                    b"actual-Saved-C",
                    18,
                    &c.update,
                    &c.targets,
                )
                .unwrap()
                .with_saved_revision(rev + 1)
                .unwrap();
                (cut, vec![saved])
            } else {
                (base.clone(), vec![])
            };
            let txrev = rev + u64::from(peer_first);
            let staged_a = if peer_first {
                let h = HostConfirmedBasis::from_trusted_confirmed_store(
                    &base,
                    &txstart,
                    17,
                    HostSealedOriginal::from_trusted_original_store(&a, b"original-A", order)
                        .unwrap(),
                    order,
                    &history,
                )
                .unwrap();
                basis::prepare_confirmed_basis(basis::Authority::Writer(h))
                    .unwrap()
                    .checkpoint()
                    .unwrap()
            } else {
                prepared(&base, &a)
            };
            let txhash = hash(&txstart);
            let prepared_hash = hash(&staged_a);
            let authored_hash = hash(&authored_a);
            for mode in [
                "good",
                "missing-prefix",
                "tx-hash",
                "tx-revision",
                "candidate-hash",
                "raw",
                "reference",
                "origin",
                "targets",
                "prospective-revision",
                "authored-hash",
                "original-base",
                "ordinal",
                "b-hash",
                "quota",
                "duplicate",
            ] {
                let result = (|| {
                    let bad_hash = format!("sha256:{}", "0".repeat(64));
                    let badtargets = vec!["p_other".to_string()];
                    let mk = || {
                        HostPreparedDelta::from_trusted_writer_candidate(
                            if mode == "reference" {
                                b"different"
                            } else {
                                b"original-A"
                            },
                            if mode == "origin" { 18 } else { 17 },
                            if mode == "raw" { &b.update } else { &a.update },
                            if mode == "targets" {
                                &badtargets
                            } else {
                                &a.targets
                            },
                            if mode == "prospective-revision" {
                                txrev
                            } else {
                                txrev + 1
                            },
                            if mode == "candidate-hash" {
                                &bad_hash
                            } else {
                                &prepared_hash
                            },
                        )
                    };
                    let entries = if mode == "quota" {
                        (0..257).map(|_| mk()).collect::<Result<Vec<_>, _>>()?
                    } else if mode == "duplicate" {
                        vec![mk()?, mk()?]
                    } else {
                        vec![mk()?]
                    };
                    let own = [HostPreparedOwnPrefix::from_trusted_writer_original(
                        HostSealedOriginal::from_trusted_original_store(
                            &a,
                            b"original-A",
                            if mode == "ordinal" { order + 1 } else { order },
                        )?
                        .with_original_base_revision(
                            if mode == "original-base" {
                                txrev + 2
                            } else {
                                rev
                            },
                        ),
                        if mode == "authored-hash" {
                            &bad_hash
                        } else {
                            &authored_hash
                        },
                    )?];
                    let pending = HostPreparedPrefix::from_trusted_writer_candidates(
                        if mode == "tx-revision" {
                            txrev + 1
                        } else {
                            txrev
                        },
                        if mode == "tx-hash" {
                            &bad_hash
                        } else {
                            &txhash
                        },
                        &entries,
                        &own,
                    )?;
                    let views = HostBasisViews::from_trusted_live_store(&base, rev);
                    let views = if mode == "missing-prefix" {
                        views
                    } else {
                        views.with_prepared_prefix(&pending)?
                    };
                    let host = HostConfirmedBasis::from_trusted_live_store_with_basis_views(
                        views,
                        &staged_a,
                        17,
                        HostSealedOriginal::from_trusted_original_store(
                            &b,
                            b"original-B",
                            order + 1,
                        )?
                        .with_original_base_revision(rev),
                        order + 1,
                        &history,
                        &[],
                    )?;
                    basis::prepare_confirmed_basis_with_authored_cut(
                        basis::Authority::Writer(host),
                        if mode == "b-hash" {
                            &bad_hash
                        } else {
                            &authored_b
                        },
                    )
                })();
                if mode == "good" {
                    let candidate = result
                        .expect("same SQL batch uses DISTINCT prepared evidence, no fake Saved");
                    if peer_first {
                        alice
                            .apply_verified_host_delta(18, &c.update, &c.targets)
                            .unwrap();
                    }
                    assert_eq!(
                        candidate.native().unwrap(),
                        alice.native().unwrap(),
                        "{kind} peer={peer_first}"
                    );
                    assert_eq!(
                        Document::restore(&candidate.checkpoint().unwrap())
                            .unwrap()
                            .block_ids()
                            .unwrap(),
                        vec!["p_one"]
                    );
                } else {
                    assert!(result.is_err(), "{kind} peer={peer_first} hostile={mode}");
                }
                assert_eq!(b, original);
            }
        }
    }
}
