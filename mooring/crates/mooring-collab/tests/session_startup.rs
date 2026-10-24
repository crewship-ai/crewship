#![cfg(unix)]
use mooring_collab::{session_lane::Lane, session_startup::Startup};
use mooring_core::{adapter::PrepareAdapter, batch::Tick};
use mooring_protocol::{
    boundary::{Durability, Transport},
    control, Counter, TargetVersion,
};
use mooring_wordcraft::{adapter::WordCraftAdapter, Document};
use serde_json::{json, Value};
use std::{os::unix::net::UnixStream, time::Duration};
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
#[test]
fn actual_owned_document_restores_and_acknowledges_original_requests_on_one_fd() {
    let (mut child, mut peer) = UnixStream::pair().unwrap();
    peer.set_read_timeout(Some(Duration::from_secs(2))).unwrap();
    let (startup, acquire) = Startup::from_open(&open(), at(0)).unwrap();
    assert_eq!(startup.retry(), acquire);
    mooring_collab::write_frame(&mut child, &acquire).unwrap();
    assert_eq!(
        mooring_collab::read_frame(&mut peer).unwrap().unwrap(),
        acquire
    );
    let owner = startup
        .accept_owner(&grant(&acquire), &host(), at(1))
        .unwrap();
    let (raw, native, ids) = restore();
    let mut ready = owner.restore(&raw, || Ok(at(2))).unwrap();
    assert_eq!(ready.confirmed().document().native().unwrap(), native);
    assert_eq!(ready.confirmed().document().block_ids().unwrap(), ids);
    assert_eq!(ready.host_issuer(), "crewship");
    assert_eq!(ready.basis().revision, Counter(10));
    let lane = Lane::from_negotiated_inherited_stream(child).unwrap();
    ready.acknowledge(&lane.output(), || Ok(at(3))).unwrap();
    for (typ, request) in [
        ("session_restored", "host-restore"),
        ("session_opened", "host-open"),
    ] {
        let frame = mooring_collab::read_frame(&mut peer).unwrap().unwrap();
        let value: Value = serde_json::from_slice(&frame).unwrap();
        assert_eq!(value["type"], typ);
        assert_eq!(value["request_id"], request);
    }
    assert!(ready.acknowledge(&lane.output(), || Ok(at(4))).is_err());
}
#[test]
fn wrong_session_or_late_owner_reply_never_opens() {
    let (startup, acquire) = Startup::from_open(&open(), at(0)).unwrap();
    let mut bad: Value = serde_json::from_slice(&grant(&acquire)).unwrap();
    bad["session_id"] = json!("other-session");
    assert!(startup
        .accept_owner(&serde_json::to_vec(&bad).unwrap(), &host(), at(1))
        .is_err());
    let (startup, acquire) = Startup::from_open(&open(), at(0)).unwrap();
    assert!(startup
        .accept_owner(&grant(&acquire), &host(), at(60000))
        .is_err());
}
#[test]
fn restore_binds_full_inventory_digest_fence_versions_and_counters() {
    for field in [
        "inventory",
        "digest",
        "fence",
        "versions",
        "counters",
        "head",
        "format",
    ] {
        let (startup, acquire) = Startup::from_open(&open(), at(0)).unwrap();
        let owner = startup
            .accept_owner(&grant(&acquire), &host(), at(1))
            .unwrap();
        let (raw, _, _) = restore();
        let mut value: Value = serde_json::from_slice(&raw).unwrap();
        match field {
            "inventory" => {
                value["targets"].as_array_mut().unwrap().pop();
            }
            "digest" => value["checkpoint"]["digest"] = json!(format!("sha256:{}", "0".repeat(64))),
            "fence" => value["fence"]["generation"] = json!("8"),
            "versions" => value["checkpoint"]["versions"]["model_version"] = json!("0.1.0"),
            "counters" => value["targets"][0]["structure_revision"] = json!("11"),
            "head" => value["checkpoint"]["revision"] = json!("9"),
            "format" => value["checkpoint"]["format_version"] = json!("other-format"),
            _ => unreachable!(),
        };
        assert!(
            owner
                .restore(&serde_json::to_vec(&value).unwrap(), || Ok(at(2)))
                .is_err(),
            "{field}"
        );
    }
}
#[test]
fn expiry_or_clock_reversal_after_restore_cannot_acknowledge() {
    for final_tick in [at(0), at(60000)] {
        let (startup, acquire) = Startup::from_open(&open(), at(0)).unwrap();
        let owner = startup
            .accept_owner(&grant(&acquire), &host(), at(1))
            .unwrap();
        let (raw, _, _) = restore();
        let mut calls = 0;
        assert!(owner
            .restore(&raw, || {
                calls += 1;
                Ok(if calls == 1 { at(2) } else { final_tick })
            })
            .is_err());
        assert_eq!(calls, 2);
    }
    let (startup, acquire) = Startup::from_open(&open(), at(0)).unwrap();
    let owner = startup
        .accept_owner(&grant(&acquire), &host(), at(1))
        .unwrap();
    let (raw, _, _) = restore();
    let mut ready = owner.restore(&raw, || Ok(at(2))).unwrap();
    let (child, _peer) = UnixStream::pair().unwrap();
    let lane = Lane::from_negotiated_inherited_stream(child).unwrap();
    assert!(ready.acknowledge(&lane.output(), || Ok(at(60000))).is_err());
    assert!(!ready.may_own(at(3)));
}

#[test]
fn owner_expiry_between_restore_ack_and_open_ack_never_publishes_running() {
    let (startup, acquire) = Startup::from_open(&open(), at(0)).unwrap();
    let owner = startup
        .accept_owner(&grant(&acquire), &host(), at(1))
        .unwrap();
    let (raw, _, _) = restore();
    let mut ready = owner.restore(&raw, || Ok(at(2))).unwrap();
    let (child, mut peer) = UnixStream::pair().unwrap();
    peer.set_read_timeout(Some(Duration::from_secs(2))).unwrap();
    let lane = Lane::from_negotiated_inherited_stream(child).unwrap();
    let mut calls = 0;
    assert!(ready
        .acknowledge(&lane.output(), || {
            calls += 1;
            Ok(if calls == 1 { at(3) } else { at(60000) })
        })
        .is_err());
    let first = mooring_collab::read_frame(&mut peer).unwrap().unwrap();
    assert_eq!(
        serde_json::from_slice::<Value>(&first).unwrap()["type"],
        "session_restored"
    );
    assert!(mooring_collab::read_frame(&mut peer).unwrap().is_none());
    assert!(!ready.may_own(at(4)));
}

#[test]
fn owner_epoch_advances_without_mutating_confirmed_document_or_granting_old_gate() {
    let (startup, acquire) = Startup::from_open(&open(), at(0)).unwrap();
    let owner = startup
        .accept_owner(&grant(&acquire), &host(), at(1))
        .unwrap();
    let (raw, native, ids) = restore();
    let ready = owner.restore(&raw, || Ok(at(2))).unwrap();
    let renewal = ready.begin_owner_renewal(at(3)).unwrap();
    assert_eq!(ready.retry_owner_renewal(at(4)).unwrap(), renewal);
    let request: Value = serde_json::from_slice(&renewal).unwrap();
    let response = json!({"type":"owner_grant","request_id":request["request_id"],"scope":request["scope"],"session_id":request["session_id"],"runtime_ref":"fresh-authenticated-child","fence":request["fence"],"lease_duration_ms":"60000","revision":"999","renewal_nonce":request["renewal_nonce"],"permission_version":"4"});
    let advance = ready
        .accept_owner_renewal(&serde_json::to_vec(&response).unwrap(), &host(), at(5))
        .unwrap();
    assert_eq!(advance.previous, Counter(3));
    assert_eq!(advance.current, Counter(4));
    assert!(!ready.matches_owner_permission(Counter(3), at(6)));
    assert!(ready.matches_owner_permission(Counter(4), at(6))); // prerequisite only, not participant read/write authority.
    assert_eq!(ready.confirmed().document().native().unwrap(), native);
    assert_eq!(ready.confirmed().document().block_ids().unwrap(), ids);
    assert_eq!(ready.basis().revision, Counter(10)); // informational grant revision999 never installs state or Saved.
    ready.close_owner();
    assert!(!ready.may_own(at(7)));
}

#[test]
fn new_owner_restores_unchanged_prior_generation_checkpoint() {
    let (startup, acquire) = Startup::from_open(&open(), at(0)).unwrap();
    let owner = startup
        .accept_owner(&grant(&acquire), &host(), at(1))
        .unwrap();
    let (raw, native, ids) = restore();
    let mut value: Value = serde_json::from_slice(&raw).unwrap();
    value["checkpoint"]["generation"] = json!("1");
    bind_checkpoint_identity(&mut value["checkpoint"]);
    let raw = serde_json::to_vec(&value).unwrap();
    let ready = owner.restore(&raw, || Ok(at(2))).unwrap();
    assert_eq!(ready.owner().fence.generation, Counter(7));
    assert_eq!(ready.confirmed().checkpoint().generation, Counter(1));
    assert_eq!(ready.confirmed().document().native().unwrap(), native);
    assert_eq!(ready.confirmed().document().block_ids().unwrap(), ids);
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

#[test]
fn legacy_capability_and_content_only_identity_cannot_activate() {
    let mut old: Value = serde_json::from_slice(&open()).unwrap();
    old.as_object_mut().unwrap().remove("required_capabilities");
    assert!(Startup::from_open(&serde_json::to_vec(&old).unwrap(), at(0)).is_err());
    for mode in ["content-only", "relabelled-creator", "unqualified-assets"] {
        let (startup, acquire) = Startup::from_open(&open(), at(0)).unwrap();
        let owner = startup
            .accept_owner(&grant(&acquire), &host(), at(1))
            .unwrap();
        let mut restore: Value = serde_json::from_slice(&restore().0).unwrap();
        match mode {
            "content-only" => {
                let cp = serde_json::from_value(restore["checkpoint"].clone()).unwrap();
                let identity = mooring_protocol::checkpoint::compute(
                    &cp,
                    mooring_protocol::checkpoint::Limits {
                        decoded_bytes: control::MAX_CHECKPOINT_BYTES,
                        preimage_bytes: control::MAX_CONTROL_BYTES,
                    },
                )
                .unwrap();
                restore["checkpoint"]["digest"] = json!(identity.content_digest());
            }
            "relabelled-creator" => restore["checkpoint"]["generation"] = json!("1"),
            "unqualified-assets" => {
                restore["checkpoint"]["assets"] = json!([{"asset_id":"claimed-external","digest":format!("sha256:{}", "0".repeat(64)),"size_bytes":"1"}]);
                bind_checkpoint_identity(&mut restore["checkpoint"]);
            }
            _ => unreachable!(),
        }
        assert!(
            owner
                .restore(&serde_json::to_vec(&restore).unwrap(), || Ok(at(2)))
                .is_err(),
            "{mode}"
        );
    }
}
