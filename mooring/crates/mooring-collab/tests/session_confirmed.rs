#![cfg(unix)]
use mooring_collab::{
    session_confirmed::{OwnerController, PendingSettlement},
    session_lane::Lane,
    session_startup::{ConfirmedDocument, Startup},
};
use mooring_core::{
    actor::Actor,
    adapter::PrepareAdapter,
    batch::{Change, Tick},
};
use mooring_protocol::{
    boundary::{Durability, PendingAtomic, Transport},
    control, Counter, TargetVersion,
};
use mooring_wordcraft::{adapter::WordCraftAdapter, Document};
use serde_json::{json, Value};
use std::{num::NonZeroUsize, os::unix::net::UnixStream, time::Duration};
fn at(ms: u64) -> Tick {
    Tick(Duration::from_millis(ms))
}
fn host() -> Transport<'static> {
    Transport::AuthenticatedHost {
        issuer: "crewship",
        durabilities: &[Durability::Node],
        required: Durability::Node,
    }
}
fn open() -> Vec<u8> {
    serde_json::to_vec(&json!({"type":"session_open","required_capabilities":["checkpoint_identity_v1"],"request_id":"host-open","control_profile":"mooring.session-causal.v1","scope":{"workspace_id":"workspace","artifact_id":"artifact","application_id":"wordcraft"},"session_id":"host-session","runtime_ref":"fresh-authenticated-child","versions":mooring_collab::dispatch::wordcraft_versions()})).unwrap()
}
fn grant(acquire: &[u8]) -> Vec<u8> {
    let acquire: Value = serde_json::from_slice(acquire).unwrap();
    serde_json::to_vec(&json!({"type":"owner_grant","request_id":acquire["request_id"],"scope":acquire["scope"],"session_id":"host-session","runtime_ref":acquire["runtime_ref"],"fence":{"generation":"7","token":"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"},"lease_duration_ms":"60000","revision":"10","renewal_nonce":acquire["renewal_nonce"],"permission_version":"3"})).unwrap()
}
fn restore() -> (Vec<u8>, Value, Vec<String>) {
    let native: Value = serde_json::from_str(include_str!(
        "../../mooring-wordcraft/tests/fixtures/d1-engine-canonical.json"
    ))
    .unwrap();
    let ids: Vec<_> = (0..native["body"].as_array().unwrap().len())
        .map(|i| format!("stable-{i}"))
        .collect();
    let cp = Document::import(native.clone(), ids.clone())
        .unwrap()
        .checkpoint()
        .unwrap();
    let opaque = control::encode_checkpoint(&cp).unwrap();
    let targets: Vec<_> = WordCraftAdapter
        .validate_checkpoint(&cp)
        .unwrap()
        .into_iter()
        .map(|x| TargetVersion {
            target_id: x.target_id,
            deleted: x.deleted,
            revision: Counter(10),
            structure_revision: Counter(0),
        })
        .collect();
    let mut request = json!({"type":"session_restore","request_id":"host-restore","control_profile":"mooring.session-causal.v1","scope":{"workspace_id":"workspace","artifact_id":"artifact","application_id":"wordcraft"},"session_id":"host-session","runtime_ref":"fresh-authenticated-child","fence":{"generation":"7","token":"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"},"versions":mooring_collab::dispatch::wordcraft_versions(),"checkpoint":{"scope":{"workspace_id":"workspace","artifact_id":"artifact","application_id":"wordcraft"},"revision":"10","generation":"7","format_version":mooring_wordcraft::profile::CHECKPOINT_FORMAT,"versions":mooring_collab::dispatch::wordcraft_versions(),"encoding":"base64","state":opaque.state,"digest":opaque.digest,"assets":[]},"targets":targets});
    bind_checkpoint_identity(&mut request["checkpoint"]);
    (serde_json::to_vec(&request).unwrap(), native, ids)
}

fn running() -> (OwnerController, ConfirmedDocument, Lane, UnixStream) {
    let (child, mut peer) = UnixStream::pair().unwrap();
    let lane = Lane::from_negotiated_inherited_stream(child).unwrap();
    let (startup, acquire) = Startup::from_open(&open(), at(0)).unwrap();
    let accepted = startup
        .accept_owner(&grant(&acquire), &host(), at(1))
        .unwrap();
    let mut ready = accepted.restore(&restore().0, || Ok(at(2))).unwrap();
    ready.acknowledge(&lane.output(), || Ok(at(3))).unwrap();
    for _ in 0..2 {
        mooring_collab::read_frame(&mut peer).unwrap().unwrap();
    }
    let (owner, confirmed) = ready.into_running(at(4)).unwrap();
    (owner, confirmed, lane, peer)
}
fn pending() -> Vec<PendingAtomic> {
    vec![PendingAtomic {
        change_id: "change".into(),
        op_ids: vec!["op".into()],
    }]
}
fn reply(owner: &OwnerController, current: &ConfirmedDocument, saved: bool) -> Value {
    let mut checkpoint = serde_json::to_value(current.checkpoint()).unwrap();
    let mut targets = serde_json::to_value(current.targets()).unwrap();
    let revision = if saved { "11" } else { "10" };
    let changes = if saved {
        checkpoint["revision"] = json!("11");
        targets[0]["revision"] = json!("11");
        json!([{"change_id":"change","status":"saved","first_revision":"11","last_revision":"11","operations":[{"op_id":"op","status":"saved","revision":"11","issuer":"crewship","durability":"node"}]}])
    } else {
        json!([{"change_id":"change","status":"rejected","operations":[{"op_id":"op","status":"rejected","reason":{"code":"forbidden","message":"denied","retry":"new_operation_after_refresh"}}]}])
    };
    bind_checkpoint_identity(&mut checkpoint);
    json!({"type":"session_commit_result","request_id":"submit","control_profile":"mooring.session-causal.v1","scope":owner.scope(),"session_id":owner.session_id(),"runtime_ref":owner.runtime_ref(),"fence":owner.initial_owner().fence,"versions":owner.versions(),"receipt":{"type":"commit_result","request_id":"submit","scope":owner.scope(),"generation":owner.initial_owner().fence.generation,"revision":revision,"issuer":"crewship","durability":"node","changes":changes},"checkpoint":checkpoint,"targets":targets})
}
fn verify(
    owner: &OwnerController,
    current: &ConfirmedDocument,
    value: &Value,
) -> std::io::Result<Option<ConfirmedDocument>> {
    owner.verify_committed(
        current,
        &serde_json::to_vec(value).unwrap(),
        PendingSettlement {
            request_id: "submit",
            changes: &pending(),
        },
        &host(),
        || Ok(at(5)),
    )
}
#[test]
fn rejected_receipt_keeps_actual_owned_document_and_checkpoint_identity() {
    let (owner, current, _lane, _peer) = running();
    let native = current.document().native().unwrap();
    let ids = current.document().block_ids().unwrap();
    let bytes = serde_json::to_vec(current.checkpoint()).unwrap();
    assert!(verify(&owner, &current, &reply(&owner, &current, false))
        .unwrap()
        .is_none());
    assert_eq!(current.document().native().unwrap(), native);
    assert_eq!(current.document().block_ids().unwrap(), ids);
    assert_eq!(serde_json::to_vec(current.checkpoint()).unwrap(), bytes);
    assert!(owner.may_own(at(6)));
}
#[test]
fn complete_saved_host_checkpoint_restores_one_replacement_doc() {
    let (owner, current, _lane, _peer) = running();
    let native = current.document().native().unwrap();
    let ids = current.document().block_ids().unwrap();
    let next = verify(&owner, &current, &reply(&owner, &current, true))
        .unwrap()
        .unwrap();
    assert_eq!(next.checkpoint().revision, Counter(11));
    assert_eq!(current.checkpoint().revision, Counter(10));
    assert_eq!(next.document().native().unwrap(), native);
    assert_eq!(next.document().block_ids().unwrap(), ids);
}
#[test]
fn invalid_receipt_or_application_state_closes_owner_without_touching_confirmed_doc() {
    for mode in [
        "late",
        "partial",
        "unknown",
        "corrupt-app",
        "omitted-inventory",
        "counter-regression",
        "wrong-provenance",
        "gap",
        "sql-failed",
    ] {
        let (owner, current, _lane, _peer) = running();
        let native = current.document().native().unwrap();
        let ids = current.document().block_ids().unwrap();
        let mut value = reply(&owner, &current, true);
        match mode {
            "late" => value["request_id"] = json!("late"),
            "partial" => value["receipt"]["changes"][0]["operations"] = json!([]),
            "unknown" => {
                value = reply(&owner, &current, false);
                value["receipt"]["changes"][0]["operations"][0]["reason"]["code"] =
                    json!("unknown_outcome");
            }
            "corrupt-app" => {
                let opaque = control::encode_checkpoint(b"{}").unwrap();
                value["checkpoint"]["state"] = json!(opaque.state);
                bind_checkpoint_identity(&mut value["checkpoint"]);
            }
            "omitted-inventory" => value["targets"] = json!([]),
            "counter-regression" => value["targets"][0]["revision"] = json!("9"),
            "wrong-provenance" => value["checkpoint"]["generation"] = json!("6"),
            "gap" => {
                value["receipt"]["revision"] = json!("12");
                value["receipt"]["changes"][0]["first_revision"] = json!("12");
                value["receipt"]["changes"][0]["last_revision"] = json!("12");
                value["receipt"]["changes"][0]["operations"][0]["revision"] = json!("12");
                value["checkpoint"]["revision"] = json!("12");
            }
            "sql-failed" => value = json!({"type":"protocol_error","code":"internal_error"}),
            _ => unreachable!(),
        }
        if matches!(mode, "wrong-provenance" | "gap") {
            // Keep full identity valid so these test the settlement boundary,
            // not an earlier generic digest mismatch.
            bind_checkpoint_identity(&mut value["checkpoint"]);
        }
        assert!(verify(&owner, &current, &value).is_err(), "accepted {mode}");
        assert!(!owner.may_own(at(6)), "owner reopened after {mode}");
        assert_eq!(current.document().native().unwrap(), native);
        assert_eq!(current.document().block_ids().unwrap(), ids);
    }
}
#[test]
fn actual_actor_sends_b_while_a_pending_and_duplicate_receipt_retains_b_for_recovery() {
    let (owner, current, _lane, _peer) = running();
    let response = reply(&owner, &current, true);
    let mut actor = Actor::new(current, 10, NonZeroUsize::new(4096).unwrap()).unwrap();
    for value in [1u8, 2] {
        actor
            .enqueue(Change {
                value,
                operations: NonZeroUsize::new(1).unwrap(),
                bytes: NonZeroUsize::new(32).unwrap(),
            })
            .unwrap();
        let sent = actor.dispatch(Duration::from_millis(500)).unwrap().unwrap();
        assert_eq!(sent.sequence, u64::from(value));
        assert_eq!(sent.batch.body[0].value, value);
    }
    assert_eq!(actor.confirmed().1, 10); // A has no locally invented durable outcome.
    let next = verify(&owner, actor.confirmed().0, &response)
        .unwrap()
        .unwrap();
    let completed = actor.settle_host_response(1, Some((next, 11))).unwrap();
    assert_eq!(completed[0].value, 1);
    assert_eq!(actor.confirmed().1, 11);
    assert!(actor.settle_host_response(1, None).is_err());
    let (confirmed, revision, originals) = actor.into_recovery();
    assert_eq!(revision, 11);
    assert_eq!(confirmed.checkpoint().revision, Counter(11));
    assert_eq!(originals.len(), 1);
    assert_eq!(originals[0].value, 2);
}

#[test]
fn expiry_or_clock_failure_after_restore_discards_replacement_and_preserves_original() {
    for clock_error in [false, true] {
        let (owner, current, _lane, _peer) = running();
        let value = reply(&owner, &current, true);
        let native = current.document().native().unwrap();
        let mut calls = 0;
        let result = owner.verify_committed(
            &current,
            &serde_json::to_vec(&value).unwrap(),
            PendingSettlement {
                request_id: "submit",
                changes: &pending(),
            },
            &host(),
            || {
                calls += 1;
                if calls == 1 {
                    Ok(at(5))
                } else if clock_error {
                    Err(std::io::Error::other("clock unavailable"))
                } else {
                    Ok(at(60000))
                }
            },
        );
        assert!(result.is_err());
        assert_eq!(calls, 2);
        assert!(!owner.may_own(at(7)));
        assert_eq!(current.document().native().unwrap(), native);
        assert_eq!(current.checkpoint().revision, Counter(10));
    }
}

fn bind_checkpoint_identity(value: &mut Value) {
    let cp = serde_json::from_value(value.clone()).unwrap();
    let identity = mooring_protocol::checkpoint::compute(
        &cp,
        mooring_protocol::checkpoint::Limits {
            decoded_bytes: control::MAX_CHECKPOINT_BYTES,
            preimage_bytes: control::MAX_CONTROL_BYTES,
        },
    )
    .unwrap();
    value["digest"] = json!(identity.full_digest());
}
