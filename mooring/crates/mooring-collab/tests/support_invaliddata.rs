#![cfg(unix)]
//! Diagnostic counterexamples: InvalidData identifies no unique failing subsystem.
use mooring_collab::session_driver::Driver;
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

fn renewing_driver_selected(
    duration: u64,
    native: bool,
) -> (
    Driver,
    UnixStream,
    std::sync::Arc<std::sync::atomic::AtomicU64>,
) {
    use std::sync::{
        atomic::{AtomicU64, Ordering},
        Arc,
    };
    let now = Arc::new(AtomicU64::new(0));
    let clock = now.clone();
    let (child, mut peer) = UnixStream::pair().unwrap();
    peer.set_read_timeout(Some(Duration::from_secs(2))).unwrap();
    let (restored, _, _) = restore();
    let worker = std::thread::spawn(move || {
        let opening = if native {
            let mut v: Value = serde_json::from_slice(&open()).unwrap();
            v["required_capabilities"] = json!([
                "checkpoint_identity_v1",
                "human_native_prepare_v1",
                "origin_inventory_v1"
            ]);
            serde_json::to_vec(&v).unwrap()
        } else {
            open()
        };
        mooring_collab::write_frame(&mut peer, &opening).unwrap();
        let acquire = mooring_collab::read_frame(&mut peer).unwrap().unwrap();
        let mut granted: Value = serde_json::from_slice(&grant(&acquire)).unwrap();
        granted["lease_duration_ms"] = json!(duration.to_string());
        mooring_collab::write_frame(&mut peer, &serde_json::to_vec(&granted).unwrap()).unwrap();
        mooring_collab::write_frame(&mut peer, &restored).unwrap();
        for _ in 0..2 {
            mooring_collab::read_frame(&mut peer).unwrap().unwrap();
        }
        peer
    });
    let constructor = if native {
        Driver::from_negotiated_inherited_stream_with_native_humans
    } else {
        Driver::from_negotiated_inherited_stream
    };
    let driver = constructor(child, &host(), move || Ok(at(clock.load(Ordering::SeqCst)))).unwrap();
    (driver, worker.join().unwrap(), now)
}
#[test]
fn support_expired_owner_returns_invaliddata_without_any_yrs_input() {
    let (driver, _peer, clock) = renewing_driver_selected(1000, false);
    let mut actor = driver.into_actor().unwrap();
    clock.store(1001, std::sync::atomic::Ordering::SeqCst);
    let error = actor.step(&host(), Duration::ZERO).unwrap_err();
    assert_eq!(error.kind(), std::io::ErrorKind::InvalidData);
    assert_eq!(error.to_string(), "running owner expired");
    eprintln!(
        "actor: expired-owner => {:?}: {} (no participant/Yrs update)",
        error.kind(),
        error
    );
}
#[test]
fn support_wrong_host_returns_same_invaliddata_without_any_yrs_input() {
    let (driver, _peer, _) = renewing_driver_selected(1000, false);
    let mut actor = driver.into_actor().unwrap();
    let error = actor
        .step(&Transport::Participant, Duration::ZERO)
        .unwrap_err();
    assert_eq!(error.kind(), std::io::ErrorKind::InvalidData);
    assert_eq!(error.to_string(), "actor needs original authenticated host");
    eprintln!(
        "actor: wrong-host => {:?}: {} (no participant/Yrs update)",
        error.kind(),
        error
    );
}
